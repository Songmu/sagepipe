package pipeline

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
)

type mapResult struct {
	index     int
	line      int
	transform transformResult
}

func (p *processor) mapMode(first *record) int {
	if p.cfg.Concurrency <= 1 {
		return p.runMap(first)
	}
	return p.runMapConcurrent(first)
}

func (p *processor) runMapConcurrent(first *record) int {
	ctx, cancel := context.WithCancel(p.ctx)
	var workers sync.WaitGroup
	defer func() {
		cancel()
		workers.Wait()
	}()

	results := make(chan mapResult, p.cfg.Concurrency)
	ready := make(map[int]mapResult)
	pending := make(map[int]int)
	var nativeSchema []byte
	schemaReady := false
	nextIndex, emitIndex := 0, 0

	dispatch := func(rec record) bool {
		data, err := marshalInput(rec.value)
		if err != nil {
			p.diag.log(slog.LevelError, "input_encoding_failed", "input", "Could not encode input record", rec.line)
			return false
		}
		if !schemaReady {
			nativeSchema = p.nativeItemsSchema()
			schemaReady = true
		}
		index := nextIndex
		nextIndex++
		pending[index] = rec.line
		prompt := p.transformPrompt(string(data))
		workers.Add(1)
		go func() {
			defer workers.Done()
			result := p.transformWithRetry(ctx, prompt, nativeSchema, rec.line)
			results <- mapResult{index: index, line: rec.line, transform: result}
		}()
		return true
	}

	if first != nil && !dispatch(*first) {
		return 2
	}
	var input <-chan readResult
	eof := false
	inputFailed := false
	for {
		if !eof && input == nil && nextIndex-emitIndex < p.cfg.Concurrency {
			result := make(chan readResult, 1)
			select {
			case p.readRequests <- readRequest{p.cfg.MaxLineBytes, result}:
				input = result
			case <-p.ctx.Done():
				p.reportInputFailure(p.ctx.Err())
				return 2
			}
		}
		if eof && nextIndex == emitIndex {
			if inputFailed {
				return 2
			}
			return 0
		}
		select {
		case read := <-input:
			input = nil
			if err := p.ctx.Err(); err != nil {
				p.reportInputFailure(err)
				return 2
			}
			if errors.Is(read.err, io.EOF) {
				eof = true
				continue
			}
			if read.err != nil {
				p.reportInputFailure(read.err)
				eof = true
				inputFailed = true
				continue
			}
			p.lineNo++
			p.rawBytes += read.line.rawBytes
			if rec, ok := p.parse(read.line); ok && !dispatch(rec) {
				return 2
			}
		case result := <-results:
			ready[result.index] = result
			for {
				current, ok := ready[emitIndex]
				if !ok {
					break
				}
				delete(ready, emitIndex)
				delete(pending, emitIndex)
				emitIndex++
				if current.transform.agentErr != nil {
					p.failures++
					p.reportAgentFailure(current.transform.agentErr, current.line, "agent")
					if p.ctx.Err() != nil {
						return 2
					}
					continue
				}
				if current.transform.outputErr != nil {
					p.failures++
					p.reportInvalidResponse(current.transform.outputErr, current.transform.response, current.line)
					continue
				}
				if err := p.emit(current.transform.payload, current.transform.count); err != nil {
					return 2
				}
			}
		case <-p.ctx.Done():
			if nextIndex != emitIndex {
				p.reportAgentFailure(p.ctx.Err(), pending[emitIndex], "agent")
			} else {
				p.reportInputFailure(p.ctx.Err())
			}
			return 2
		}
	}
}
