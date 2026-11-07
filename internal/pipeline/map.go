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

// concurrentMap keeps scheduling state on the coordinator goroutine.
// The window includes completed results until they are emitted in input order.
type concurrentMap struct {
	p            *processor
	ctx          context.Context
	workers      sync.WaitGroup
	results      chan mapResult
	ready        map[int]mapResult
	pending      map[int]int
	nativeSchema []byte
	schemaReady  bool
	nextIndex    int
	emitIndex    int
	input        <-chan readResult
	eof          bool
	inputFailed  bool
}

func (p *processor) mapMode(first *record) int {
	if p.cfg.Concurrency <= 1 {
		return p.runMap(first)
	}
	return p.runMapConcurrent(first)
}

func (p *processor) runMapConcurrent(first *record) int {
	ctx, cancel := context.WithCancel(p.ctx)
	m := &concurrentMap{
		p:       p,
		ctx:     ctx,
		results: make(chan mapResult, p.cfg.Concurrency),
		ready:   make(map[int]mapResult),
		pending: make(map[int]int),
	}
	defer func() {
		cancel()
		m.workers.Wait()
	}()

	if first != nil && m.dispatch(*first) != nil {
		return 2
	}
	for {
		if m.requestInput() != nil {
			return 2
		}
		if m.eof && m.nextIndex == m.emitIndex {
			if m.inputFailed {
				return 2
			}
			return 0
		}
		select {
		case read := <-m.input:
			if m.consumeInput(read) != nil {
				return 2
			}
		case result := <-m.results:
			m.ready[result.index] = result
			if m.emitReady() != nil {
				return 2
			}
		case <-p.ctx.Done():
			m.reportCancellation()
			return 2
		}
	}
}

func (m *concurrentMap) dispatch(rec record) error {
	data, err := marshalInput(rec.value)
	if err != nil {
		m.p.diag.log(slog.LevelError, "input_encoding_failed", "input", "Could not encode input record", rec.line)
		return err
	}
	if !m.schemaReady {
		m.nativeSchema = m.p.nativeItemsSchema()
		m.schemaReady = true
	}
	index := m.nextIndex
	m.nextIndex++
	m.pending[index] = rec.line
	prompt := m.p.transformPrompt(string(data))
	nativeSchema := m.nativeSchema
	m.workers.Add(1)
	go func() {
		defer m.workers.Done()
		result := m.p.transformWithRetry(m.ctx, prompt, nativeSchema, rec.line)
		m.results <- mapResult{index: index, line: rec.line, transform: result}
	}()
	return nil
}

func (m *concurrentMap) requestInput() error {
	if m.eof || m.input != nil || m.nextIndex-m.emitIndex >= m.p.cfg.Concurrency {
		return nil
	}
	result := make(chan readResult, 1)
	select {
	case m.p.readRequests <- readRequest{m.p.cfg.MaxLineBytes, result}:
		m.input = result
		return nil
	case <-m.p.ctx.Done():
		m.p.reportInputFailure(m.p.ctx.Err())
		return m.p.ctx.Err()
	}
}

func (m *concurrentMap) consumeInput(read readResult) error {
	m.input = nil
	if err := m.p.ctx.Err(); err != nil {
		m.p.reportInputFailure(err)
		return err
	}
	if errors.Is(read.err, io.EOF) {
		m.eof = true
		return nil
	}
	if read.err != nil {
		m.p.reportInputFailure(read.err)
		m.eof = true
		m.inputFailed = true
		return nil
	}
	m.p.lineNo++
	m.p.rawBytes += read.line.rawBytes
	if rec, ok := m.p.parse(read.line); ok {
		return m.dispatch(rec)
	}
	return nil
}

func (m *concurrentMap) emitReady() error {
	for {
		current, ok := m.ready[m.emitIndex]
		if !ok {
			return nil
		}
		delete(m.ready, m.emitIndex)
		delete(m.pending, m.emitIndex)
		m.emitIndex++
		if err := m.p.emitMapResult(current.transform, current.line); err != nil {
			return err
		}
	}
}

func (m *concurrentMap) reportCancellation() {
	if m.nextIndex != m.emitIndex {
		m.p.reportAgentFailure(m.p.ctx.Err(), m.pending[m.emitIndex], "agent")
	} else {
		m.p.reportInputFailure(m.p.ctx.Err())
	}
}
