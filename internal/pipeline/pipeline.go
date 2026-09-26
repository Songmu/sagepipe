package pipeline

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/Songmu/sagepipe/internal/agent"
	"github.com/Songmu/sagepipe/internal/config"
	"github.com/Songmu/sagepipe/internal/schema"
)

type processor struct {
	ctx          context.Context
	cfg          config.Config
	input        *bufio.Reader
	readRequests chan readRequest
	readDone     chan struct{}
	output       io.Writer
	diag         *diagnostics
	runner       agent.Runner
	inSchema     *schema.Document
	outSchema    *schema.Document
	lineNo       int
	failures     int
	outputs      int
	rawBytes     int64
	warnedNative bool
}

type record struct {
	value any
	line  int
}

type readRequest struct {
	max    int64
	result chan readResult
}

type readResult struct {
	line inputLine
	err  error
}

// Run processes a stream and returns 0 for success, 1 for completed runs with
// rejected input records, or 2 for failures that prevent completing the run.
func Run(ctx context.Context, cfg config.Config, in io.Reader, out, errOut io.Writer, runner agent.Runner) (status int) {
	p := &processor{
		ctx:          ctx,
		cfg:          cfg,
		input:        bufio.NewReader(in),
		readRequests: make(chan readRequest),
		readDone:     make(chan struct{}),
		output:       out,
		diag:         newDiagnostics(errOut, cfg.Verbosity),
		runner:       runner,
	}
	defer func() {
		if err := runner.Close(); err != nil {
			p.diag.log(slog.LevelError, "agent_close_failed", "agent", "Could not close agent connection", 0)
			status = 2
		}
		status = p.finish(status)
	}()
	go p.readInput()
	defer close(p.readDone)
	inSchema, err := compileSchema(cfg.InputSchema)
	if err != nil {
		p.diag.log(slog.LevelError, "invalid_input_schema", "config", err.Error(), 0)
		return 2
	}
	p.inSchema = inSchema
	outSchema, err := compileSchema(cfg.OutputSchema)
	if err != nil {
		p.diag.log(slog.LevelError, "invalid_output_schema", "config", err.Error(), 0)
		return 2
	}
	p.outSchema = outSchema
	p.diag.log(slog.LevelInfo, "agent_selected", "config", "Agent selected", 0,
		"agent", cfg.Agent.Provider, "protocol", cfg.Agent.Protocol)
	if cfg.AllowedTools != "" {
		p.diag.log(slog.LevelInfo, "tools_selected", "config", "Agent tool settings supplied", 0)
	}

	switch cfg.Mode {
	case "map":
		status = p.mapMode(nil)
	case "reduce":
		status = p.runReduce(nil)
	case "auto":
		status = p.runAuto()
	default:
		p.diag.log(slog.LevelError, "invalid_mode", "config", "Invalid processing mode", 0)
		status = 2
	}
	return status
}

func compileSchema(spec config.SchemaSpec) (*schema.Document, error) {
	if !spec.Present {
		return nil, nil
	}
	if spec.Path != "" {
		return schema.Load(spec.Path)
	}
	return schema.Inline(spec.JSON, spec.BaseURI)
}

func (p *processor) finish(status int) int {
	if status == 0 && p.failures != 0 {
		status = 1
	}
	p.diag.log(slog.LevelInfo, "summary", "complete", "Processing finished", 0,
		"failures", p.failures, "outputs", p.outputs, "exit_code", status)
	return status
}

func (p *processor) next(max int64) (inputLine, error) {
	if err := p.ctx.Err(); err != nil {
		return inputLine{}, err
	}
	result := make(chan readResult, 1)
	select {
	case p.readRequests <- readRequest{max, result}:
	case <-p.ctx.Done():
		return inputLine{}, p.ctx.Err()
	}
	var read readResult
	select {
	case read = <-result:
	case <-p.ctx.Done():
		return inputLine{}, p.ctx.Err()
	}
	if ctxErr := p.ctx.Err(); ctxErr != nil {
		return inputLine{}, ctxErr
	}
	if read.err == nil {
		p.lineNo++
		p.rawBytes += read.line.rawBytes
	}
	return read.line, read.err
}

func (p *processor) readInput() {
	for {
		select {
		case request := <-p.readRequests:
			line, err := nextLine(p.input, request.max)
			select {
			case request.result <- readResult{line, err}:
			case <-p.readDone:
				return
			}
		case <-p.readDone:
			return
		}
	}
}

func (p *processor) parse(line inputLine) (record, bool) {
	if p.inSchema != nil && line.blank {
		return record{}, false
	}
	if line.tooLong {
		p.reject("line_too_long", "input", "Input line exceeds the size limit")
		return record{}, false
	}
	if !utf8.Valid(line.data) {
		p.reject("invalid_utf8", "input", "Input record is not UTF-8")
		return record{}, false
	}
	if p.inSchema == nil {
		return record{value: string(line.data), line: p.lineNo}, true
	}
	value, err := schema.Decode(line.data)
	if err != nil {
		p.reject("invalid_json", "input", "Input record is not a single JSON value")
		return record{}, false
	}
	if err := p.inSchema.Validate(value); err != nil {
		p.reject("input_schema_failed", "input", "Input record does not match input_schema")
		return record{}, false
	}
	return record{value: value, line: p.lineNo}, true
}

func (p *processor) reject(code, stage, message string) {
	p.failures++
	p.diag.log(slog.LevelError, code, stage, message, p.lineNo)
}

func (p *processor) runMap(first *record) int {
	if first != nil {
		if err := p.processMap(*first); err != nil {
			return 2
		}
	}
	for {
		line, err := p.next(p.cfg.MaxLineBytes)
		if errors.Is(err, io.EOF) {
			return 0
		}
		if err != nil {
			p.reportInputFailure(err)
			return 2
		}
		rec, ok := p.parse(line)
		if !ok {
			continue
		}
		if err := p.processMap(rec); err != nil {
			return 2
		}
	}
}

func (p *processor) processMap(rec record) error {
	data, err := marshalInput(rec.value)
	if err != nil {
		p.diag.log(slog.LevelError, "input_encoding_failed", "input", "Could not encode input record", rec.line)
		return err
	}
	resp, err := p.invoke(p.transformPrompt(string(data)), p.nativeItemsSchema())
	if err != nil {
		p.failures++
		p.reportAgentFailure(err, rec.line, "agent")
		if p.ctx.Err() != nil {
			return err
		}
		return nil
	}
	payload, count, err := p.parseOutput(resp, rec.line)
	if err != nil {
		p.failures++
		p.reportInvalidResponse(err, resp, rec.line)
		return nil
	}
	if err := p.emit(payload, count); err != nil {
		return err
	}
	return nil
}

func (p *processor) emit(payload []byte, count int) error {
	if err := writeOutput(p.output, payload); err != nil {
		p.diag.log(slog.LevelError, "output_write_failed", "output", "Could not write standard output", 0)
		return err
	}
	p.outputs += count
	return nil
}

func (p *processor) runReduce(first *record) int {
	var values []any
	if first != nil {
		values = append(values, first.value)
	}
	if p.rawBytes > p.cfg.MaxInputBytes {
		p.diag.log(slog.LevelError, "input_too_large", "input", "Input exceeds max_input_bytes", 0)
		return 2
	}
	for {
		line, err := p.next(p.cfg.MaxInputBytes)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			p.reportInputFailure(err)
			return 2
		}
		if p.rawBytes > p.cfg.MaxInputBytes {
			p.diag.log(slog.LevelError, "input_too_large", "input", "Input exceeds max_input_bytes", 0)
			return 2
		}
		rec, ok := p.parse(line)
		if ok {
			values = append(values, rec.value)
		}
	}
	if len(values) == 0 {
		return 0
	}
	data, err := marshalInput(values)
	if err != nil {
		p.diag.log(slog.LevelError, "input_encoding_failed", "input", "Could not encode input records", 0)
		return 2
	}
	resp, err := p.invoke(p.transformPrompt(string(data)), p.nativeItemsSchema())
	if err != nil {
		p.reportAgentFailure(err, 0, "agent")
		return 2
	}
	payload, count, err := p.parseOutput(resp, 0)
	if err != nil {
		p.reportInvalidResponse(err, resp, 0)
		return 2
	}
	if err := p.emit(payload, count); err != nil {
		return 2
	}
	return 0
}

func (p *processor) runAuto() int {
	preLimit := max(p.cfg.MaxLineBytes, p.cfg.MaxInputBytes)
	for {
		line, err := p.next(preLimit)
		if errors.Is(err, io.EOF) {
			return 0
		}
		if err != nil {
			p.reportInputFailure(err)
			return 2
		}
		rec, ok := p.parse(line)
		if !ok {
			continue
		}
		mode := "map"
		reason := "The prompt is empty"
		called := false
		if strings.TrimSpace(p.cfg.Prompt) != "" {
			called = true
			response, err := p.invoke(modePrompt(p.cfg.Prompt), nil)
			if err != nil {
				p.reportAgentFailure(err, 0, "mode")
				return 2
			}
			var agentReason string
			modeResponse, unwrapped := unwrapJSONCodeFence(response.Text)
			if unwrapped {
				p.diag.log(slog.LevelWarn, "markdown_fence_removed", "mode",
					"Removed Markdown code fence from agent response", 0)
			}
			mode, agentReason, err = parseModeResponse(modeResponse)
			if err != nil {
				p.diag.log(slog.LevelError, "invalid_mode_response", "mode", "Agent returned an invalid mode decision", 0)
				text, truncated := diagnosticResponse(response.Text)
				p.diag.log(slog.LevelDebug, "invalid_mode_response_detail", "mode",
					"Agent mode response validation failed", 0, "reason", err.Error(), "response", text,
					"response_bytes", len(response.Text), "response_truncated", truncated)
				return 2
			}
			p.diag.log(slog.LevelDebug, "mode_reason", "mode", "Agent processing mode rationale", 0,
				"agent_mode", mode, "reason", agentReason)
			switch mode {
			case "reduce":
				reason = "The agent determined cross-record context is required"
			case "uncertain":
				mode = "map"
				reason = "The agent could not determine whether cross-record context is required"
				p.diag.log(slog.LevelWarn, "mode_uncertain", "mode", "Agent was uncertain; selected map", 0)
			default:
				reason = "The agent determined records can be processed independently"
			}
		}
		p.diag.log(slog.LevelInfo, "mode_selected", "mode", "Processing mode selected", 0,
			"mode", mode, "reason", reason, "agent_called", called)
		if mode == "reduce" {
			return p.runReduce(&rec)
		}
		if int64(len(line.data)) > p.cfg.MaxLineBytes {
			p.reject("line_too_long", "input", "Input line exceeds the size limit")
			return p.mapMode(nil)
		}
		return p.mapMode(&rec)
	}
}

func (p *processor) invoke(prompt string, nativeSchema []byte) (agent.Response, error) {
	return p.invokeWithContext(p.ctx, prompt, nativeSchema)
}

func (p *processor) invokeWithContext(ctx context.Context, prompt string, nativeSchema []byte) (agent.Response, error) {
	if p.cfg.Timeout != 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.cfg.Timeout)
		defer cancel()
	}
	resp, err := p.runner.Run(ctx, agent.Request{
		Prompt: prompt, NativeSchema: nativeSchema, MaxResponseBytes: p.cfg.MaxResponseBytes,
		OnLaunch: func(launch agent.Launch) {
			p.diag.log(slog.LevelDebug, "agent_process_starting", "agent", "Starting agent process", 0,
				"command", launch.Command(), "args", launch.Args(), "cwd", launch.CWD())
		},
	})
	if err != nil {
		return agent.Response{}, err
	}
	if !utf8.ValidString(resp.Text) || int64(len(resp.Text)) > p.cfg.MaxResponseBytes {
		return agent.Response{}, fmt.Errorf("agent response exceeds size limit or is not UTF-8")
	}
	if resp.Usage != nil {
		p.diag.log(slog.LevelInfo, "agent_usage", "agent", "Agent usage reported", 0,
			"input_tokens", resp.Usage.InputTokens, "output_tokens", resp.Usage.OutputTokens,
			"cached_input_tokens", resp.Usage.CachedInputTokens)
	}
	for _, warning := range resp.Warnings {
		p.diag.log(slog.LevelWarn, "agent_warning", "agent", warning, 0)
	}
	return resp, nil
}

func (p *processor) reportAgentFailure(err error, line int, stage string) {
	code, message := "agent_call_failed", "Agent request failed"
	if stage == "mode" {
		code, message = "mode_detection_failed", "Agent could not classify processing mode"
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		code, message = "agent_timeout", "Agent request timed out"
	case errors.Is(err, context.Canceled):
		code, message = "agent_cancelled", "Agent request was cancelled"
	}
	p.diag.log(slog.LevelError, code, stage, message, line)
	p.diag.log(slog.LevelDebug, code+"_detail", stage, "Agent request failure detail", line, "reason", err.Error())
}

func (p *processor) reportInputFailure(err error) {
	code, message := "input_read_failed", "Could not read standard input"
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		code, message = "input_timeout", "Input read timed out"
	case errors.Is(err, context.Canceled):
		code, message = "input_cancelled", "Input read was cancelled"
	}
	p.diag.log(slog.LevelError, code, "input", message, 0)
}

func writeOutput(w io.Writer, payload []byte) error {
	for len(payload) != 0 {
		n, err := w.Write(payload)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(payload) {
			return io.ErrShortWrite
		}
		payload = payload[n:]
	}
	return nil
}
