package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Songmu/sagepipe/internal/agent"
	"github.com/Songmu/sagepipe/internal/config"
	"github.com/Songmu/sagepipe/internal/schema"
)

type fakeRunner struct {
	run    func(agent.Request) (agent.Response, error)
	calls  []agent.Request
	closed int
}

func (f *fakeRunner) Run(_ context.Context, req agent.Request) (agent.Response, error) {
	f.calls = append(f.calls, req)
	return f.run(req)
}

func (f *fakeRunner) Close() error {
	f.closed++
	return nil
}

func testConfig(mode string) config.Config {
	return config.Config{
		Agent:            config.AgentConfig{Provider: "test", Protocol: "cli"},
		Mode:             mode,
		MaxLineBytes:     64,
		MaxInputBytes:    256,
		MaxResponseBytes: 1024,
		Timeout:          time.Second,
	}
}

func TestMapRejectsWholeInvalidAnswerAndContinues(t *testing.T) {
	cfg := testConfig("map")
	in := strings.NewReader("one\ntwo\nthree\n")
	var out, diag strings.Builder
	f := &fakeRunner{run: func(req agent.Request) (agent.Response, error) {
		switch {
		case strings.HasSuffix(req.Prompt, `"one"`):
			return agent.Response{Text: `{"items":["a","b"]}`}, nil
		case strings.HasSuffix(req.Prompt, `"two"`):
			return agent.Response{Text: `{"items":["partial",4]}`}, nil
		default:
			return agent.Response{Text: `{"items":[]}`}, nil
		}
	}}
	if code := Run(context.Background(), cfg, in, &out, &diag, f); code != 1 {
		t.Fatalf("exit code = %d, want 1; diagnostics: %s", code, diag.String())
	}
	if got := out.String(); got != "a\nb\n" {
		t.Errorf("stdout = %q, want only complete valid answers", got)
	}
	if len(f.calls) != 3 || f.closed != 1 {
		t.Errorf("calls=%d closed=%d, want 3 and 1", len(f.calls), f.closed)
	}
	checkDiagnostic(t, diag.String(), "invalid_response", 2)
}

func TestDiagnosticsDoNotEchoToolRules(t *testing.T) {
	cfg := testConfig("map")
	cfg.Verbosity = 1
	cfg.AllowedTools = "Bash(echo private-pattern)"
	var out, diag strings.Builder
	f := &fakeRunner{run: func(agent.Request) (agent.Response, error) {
		return agent.Response{Text: `{"items":["ok"]}`}, nil
	}}
	if code := Run(context.Background(), cfg, strings.NewReader("input\n"), &out, &diag, f); code != 0 {
		t.Fatalf("exit code = %d, diagnostics: %s", code, diag.String())
	}
	checkDiagnostic(t, diag.String(), "tools_selected", 0)
	if strings.Contains(diag.String(), "private-pattern") {
		t.Fatalf("diagnostics exposed tool rule: %s", diag.String())
	}
}

func TestDiagnosticsVerbosityLevels(t *testing.T) {
	for _, tt := range []struct {
		name      string
		verbosity int
		wantInfo  bool
		wantDebug bool
	}{
		{"default", 0, false, false},
		{"info", 1, true, false},
		{"debug", 2, true, true},
		{"higher verbosity", 3, true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig("map")
			cfg.Verbosity = tt.verbosity
			var out, diag strings.Builder
			f := &fakeRunner{run: func(req agent.Request) (agent.Response, error) {
				req.OnLaunch(agent.NewLaunch("agent", []string{"private-token"}, "/tmp"))
				return agent.Response{
					Text:     `{"items":["ok"]}`,
					Usage:    &agent.Usage{InputTokens: 2},
					Warnings: []string{"agent warning"},
				}, nil
			}}
			if code := Run(context.Background(), cfg, strings.NewReader("input\n"), &out, &diag, f); code != 0 {
				t.Fatalf("exit code = %d, diagnostics: %s", code, diag.String())
			}
			if out.String() != "ok\n" {
				t.Fatalf("stdout = %q", out.String())
			}
			for _, code := range []string{"agent_selected", "agent_usage", "summary"} {
				if got := strings.Contains(diag.String(), `"code":"`+code+`"`); got != tt.wantInfo {
					t.Errorf("%s present = %t, want %t: %s", code, got, tt.wantInfo, diag.String())
				}
			}
			if got := strings.Contains(diag.String(), `"code":"agent_process_starting"`); got != tt.wantDebug {
				t.Errorf("DEBUG present = %t, want %t: %s", got, tt.wantDebug, diag.String())
			}
			if strings.Contains(diag.String(), "private-token") != tt.wantDebug {
				t.Errorf("launch arguments leaked or missing: %s", diag.String())
			}
			checkDiagnostic(t, diag.String(), "agent_warning", 0)
			if !strings.Contains(diag.String(), `"level":"WARN"`) {
				t.Errorf("WARN missing: %s", diag.String())
			}
		})
	}
}

func TestDiagnosticsHideLaunchArgumentsByDefault(t *testing.T) {
	cfg := testConfig("map")
	var out, diag strings.Builder
	f := &fakeRunner{run: func(req agent.Request) (agent.Response, error) {
		req.OnLaunch(agent.NewLaunch(
			"copilot",
			[]string{
				"--disable-builtin-mcps",
				"--disable-mcp-server=workiq",
				"-p", "private prompt",
				"--token=private-token",
				"--api_key=private-api-key",
				"--allowedTools", "private-tool", "other-tool",
				"--allow-tool=private-policy",
				"--model", "test-model",
			},
			"/tmp/project",
		))
		return agent.Response{Text: `{"items":["ok"]}`}, nil
	}}
	if code := Run(context.Background(), cfg, strings.NewReader("input\n"), &out, &diag, f); code != 0 {
		t.Fatalf("exit code = %d, diagnostics: %s", code, diag.String())
	}
	if strings.Contains(diag.String(), "agent_process_starting") ||
		strings.Contains(diag.String(), "private prompt") {
		t.Fatalf("default diagnostics exposed launch arguments: %s", diag.String())
	}
}

func TestVerboseDiagnosticsLogRawLaunchArguments(t *testing.T) {
	cfg := testConfig("map")
	cfg.Verbosity = 2
	var out, diag strings.Builder
	f := &fakeRunner{run: func(req agent.Request) (agent.Response, error) {
		req.OnLaunch(agent.NewLaunch(
			"copilot",
			[]string{"--additional-mcp-config", `{"env":{"API_TOKEN":"private-token"}}`, "-p", "private prompt"},
			"/tmp/project",
		))
		return agent.Response{Text: `{"items":["ok"]}`}, nil
	}}
	if code := Run(context.Background(), cfg, strings.NewReader("input\n"), &out, &diag, f); code != 0 {
		t.Fatalf("exit code = %d, diagnostics: %s", code, diag.String())
	}
	for _, want := range []string{
		`"level":"DEBUG"`,
		`"code":"agent_process_starting"`,
		`"command":"copilot"`,
		`"--additional-mcp-config"`,
		`API_TOKEN`,
		`private-token`,
		`private prompt`,
		`"cwd":"/tmp/project"`,
	} {
		if !strings.Contains(diag.String(), want) {
			t.Fatalf("verbose diagnostics missing %q: %s", want, diag.String())
		}
	}
}

func TestMapRejectsMultilineTextWithoutPartialOutput(t *testing.T) {
	cfg := testConfig("map")
	var out, diag strings.Builder
	f := &fakeRunner{run: func(agent.Request) (agent.Response, error) {
		return agent.Response{Text: `{"items":["first","two\nlines"]}`}, nil
	}}
	if code := Run(context.Background(), cfg, strings.NewReader("input\n"), &out, &diag, f); code != 1 {
		t.Fatalf("exit code = %d, want 1; diagnostics: %s", code, diag.String())
	}
	if out.Len() != 0 {
		t.Errorf("part of a rejected answer was written: %q", out.String())
	}
}

func TestReduceDropsInvalidInputButFailsRun(t *testing.T) {
	cfg := testConfig("reduce")
	cfg.InputSchema = config.SchemaSpec{Present: true, JSON: []byte(`{"type":"integer"}`), BaseURI: "file:///tmp/sagepipe-input.json"}
	cfg.OutputSchema = config.SchemaSpec{Present: true, JSON: []byte(`{"type":"integer"}`), BaseURI: "file:///tmp/sagepipe-output.json"}
	var out, diag strings.Builder
	f := &fakeRunner{run: func(req agent.Request) (agent.Response, error) {
		if !strings.HasSuffix(req.Prompt, "[1,2]") {
			t.Errorf("expected only valid records in prompt, got %q", req.Prompt)
		}
		return agent.Response{Text: `{"items":[3]}`}, nil
	}}
	if code := Run(context.Background(), cfg, strings.NewReader("1\nnot-json\n \t\n2\n"), &out, &diag, f); code != 1 {
		t.Fatalf("exit code = %d, want 1; diagnostics: %s", code, diag.String())
	}
	if out.String() != "3\n" || len(f.calls) != 1 {
		t.Errorf("stdout=%q calls=%d", out.String(), len(f.calls))
	}
	checkDiagnostic(t, diag.String(), "invalid_json", 2)
}

func TestReduceWithNoValidRecordsDoesNotCallAgent(t *testing.T) {
	cfg := testConfig("reduce")
	cfg.InputSchema = config.SchemaSpec{Present: true, JSON: []byte(`{"type":"integer"}`), BaseURI: "file:///tmp/sagepipe-input.json"}
	var out, diag strings.Builder
	f := &fakeRunner{run: func(agent.Request) (agent.Response, error) {
		t.Error("agent called without valid input")
		return agent.Response{}, nil
	}}
	if code := Run(context.Background(), cfg, strings.NewReader("invalid\n \t\n"), &out, &diag, f); code != 1 {
		t.Fatalf("exit code = %d, want 1; diagnostics: %s", code, diag.String())
	}
	if out.Len() != 0 || len(f.calls) != 0 {
		t.Errorf("stdout=%q calls=%d", out.String(), len(f.calls))
	}
}

func TestMapInvalidUTF8SkipsLineAndKeepsPhysicalLineNumber(t *testing.T) {
	cfg := testConfig("map")
	var out, diag strings.Builder
	f := &fakeRunner{run: func(agent.Request) (agent.Response, error) {
		return agent.Response{Text: `{"items":["ok"]}`}, nil
	}}
	in := strings.NewReader(string([]byte{0xff, '\n'}) + "valid\n")
	if code := Run(context.Background(), cfg, in, &out, &diag, f); code != 1 {
		t.Fatalf("exit code = %d, want 1; diagnostics: %s", code, diag.String())
	}
	if out.String() != "ok\n" || len(f.calls) != 1 {
		t.Errorf("stdout=%q calls=%d", out.String(), len(f.calls))
	}
	checkDiagnostic(t, diag.String(), "invalid_utf8", 1)
}

func TestMapOversizedLineSkipsToNextLine(t *testing.T) {
	cfg := testConfig("map")
	cfg.MaxLineBytes = 3
	var out, diag strings.Builder
	f := &fakeRunner{run: func(agent.Request) (agent.Response, error) {
		return agent.Response{Text: `{"items":["ok"]}`}, nil
	}}
	if code := Run(context.Background(), cfg, strings.NewReader("four\nok\r\n"), &out, &diag, f); code != 1 {
		t.Fatalf("exit code = %d, want 1; diagnostics: %s", code, diag.String())
	}
	if out.String() != "ok\n" || len(f.calls) != 1 {
		t.Errorf("stdout=%q calls=%d", out.String(), len(f.calls))
	}
	checkDiagnostic(t, diag.String(), "line_too_long", 1)
}

func TestInputAndOutputFormatsAreIndependent(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		inputSchema  bool
		outputSchema bool
		answer       string
		want         string
		promptSuffix string
	}{
		{"text to JSONL", "hello\n", false, true, `{"items":[{"value":"hello"}]}`, "{\"value\":\"hello\"}\n", `"hello"`},
		{"JSONL to text", `{"value":"hello"}` + "\n", true, false, `{"items":["ok"]}`, "ok\n", `{"value":"hello"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig("map")
			if tt.inputSchema {
				cfg.InputSchema = config.SchemaSpec{Present: true, JSON: []byte("true"), BaseURI: "file:///tmp/sagepipe-input.json"}
			}
			if tt.outputSchema {
				cfg.OutputSchema = config.SchemaSpec{Present: true, JSON: []byte("true"), BaseURI: "file:///tmp/sagepipe-output.json"}
			}
			var out, diag strings.Builder
			f := &fakeRunner{run: func(req agent.Request) (agent.Response, error) {
				if !strings.HasSuffix(req.Prompt, tt.promptSuffix) {
					t.Errorf("input prompt suffix=%q, want %q", req.Prompt, tt.promptSuffix)
				}
				return agent.Response{Text: tt.answer}, nil
			}}
			if code := Run(context.Background(), cfg, strings.NewReader(tt.input), &out, &diag, f); code != 0 {
				t.Fatalf("exit code = %d, want 0; diagnostics: %s", code, diag.String())
			}
			if out.String() != tt.want {
				t.Errorf("stdout=%q, want %q", out.String(), tt.want)
			}
		})
	}
}

func TestJSONLBlankLineOverMapLimitIsIgnored(t *testing.T) {
	cfg := testConfig("map")
	cfg.MaxLineBytes = 3
	cfg.InputSchema = config.SchemaSpec{Present: true, JSON: []byte("true"), BaseURI: "file:///tmp/sagepipe-input.json"}
	var out, diag strings.Builder
	f := &fakeRunner{run: func(agent.Request) (agent.Response, error) {
		return agent.Response{Text: `{"items":[]}`}, nil
	}}
	if code := Run(context.Background(), cfg, strings.NewReader(strings.Repeat(" ", 10000)+"\n1\n"), &out, &diag, f); code != 0 {
		t.Fatalf("exit code = %d, want 0; diagnostics: %s", code, diag.String())
	}
	if len(f.calls) != 1 || strings.Contains(diag.String(), "line_too_long") {
		t.Errorf("calls=%d diagnostics=%s", len(f.calls), diag.String())
	}
}

func TestAutoClassifiesBeforeTransform(t *testing.T) {
	cfg := testConfig("auto")
	cfg.Verbosity = 1
	cfg.Prompt = "Summarize all lines"
	var out, diag strings.Builder
	f := &fakeRunner{}
	f.run = func(req agent.Request) (agent.Response, error) {
		if len(f.calls) == 1 {
			if strings.Contains(req.Prompt, `"first"`) {
				t.Error("mode classifier received input")
			}
			return agent.Response{Text: `{"mode":"reduce","reason":"cross-record context"}`}, nil
		}
		return agent.Response{Text: `{"items":["summary"]}`}, nil
	}
	if code := Run(context.Background(), cfg, strings.NewReader("first\nsecond\n"), &out, &diag, f); code != 0 {
		t.Fatalf("exit code = %d, want 0; diagnostics: %s", code, diag.String())
	}
	if out.String() != "summary\n" || len(f.calls) != 2 {
		t.Errorf("stdout=%q calls=%d", out.String(), len(f.calls))
	}
	checkDiagnostic(t, diag.String(), "mode_selected", 0)
}

func TestAutoDoesNotLogAgentReasonContainingPrompt(t *testing.T) {
	cfg := testConfig("auto")
	cfg.Prompt = "private transformation instruction"
	var out, diag strings.Builder
	f := &fakeRunner{}
	f.run = func(agent.Request) (agent.Response, error) {
		if len(f.calls) == 1 {
			return agent.Response{Text: `{"mode":"map","reason":"private transformation instruction"}`}, nil
		}
		return agent.Response{Text: `{"items":[]}`}, nil
	}
	if code := Run(context.Background(), cfg, strings.NewReader("hello\n"), &out, &diag, f); code != 0 {
		t.Fatalf("exit code = %d, want 0; diagnostics: %s", code, diag.String())
	}
	if strings.Contains(diag.String(), cfg.Prompt) {
		t.Errorf("prompt leaked into diagnostics: %s", diag.String())
	}
}

func TestAutoUncertainDefaultsToMap(t *testing.T) {
	cfg := testConfig("auto")
	cfg.Prompt = "Classify each row"
	var out, diag strings.Builder
	f := &fakeRunner{}
	f.run = func(agent.Request) (agent.Response, error) {
		if len(f.calls) == 1 {
			return agent.Response{Text: `{"mode":"uncertain","reason":"cannot tell"}`}, nil
		}
		return agent.Response{Text: `{"items":["ok"]}`}, nil
	}
	if code := Run(context.Background(), cfg, strings.NewReader("first\nsecond\n"), &out, &diag, f); code != 0 {
		t.Fatalf("exit code = %d, want 0; diagnostics: %s", code, diag.String())
	}
	if out.String() != "ok\nok\n" || len(f.calls) != 3 {
		t.Errorf("stdout=%q calls=%d", out.String(), len(f.calls))
	}
	checkDiagnostic(t, diag.String(), "mode_uncertain", 0)
}

func TestEmptyJSONLDoesNotCallAgent(t *testing.T) {
	cfg := testConfig("auto")
	cfg.InputSchema = config.SchemaSpec{Present: true, JSON: []byte("true"), BaseURI: "file:///tmp/sagepipe-input.json"}
	cfg.Prompt = "Summarize"
	var out, diag strings.Builder
	f := &fakeRunner{run: func(agent.Request) (agent.Response, error) {
		t.Fatal("agent called for blank JSONL input")
		return agent.Response{}, nil
	}}
	if code := Run(context.Background(), cfg, strings.NewReader(" \t\n\n"), &out, &diag, f); code != 0 {
		t.Fatalf("exit code = %d, want 0; diagnostics: %s", code, diag.String())
	}
	if out.Len() != 0 || f.closed != 1 {
		t.Errorf("stdout=%q closed=%d", out.String(), f.closed)
	}
}

func TestReduceCountsIgnoredBlankLinesTowardTotalLimit(t *testing.T) {
	cfg := testConfig("reduce")
	cfg.MaxInputBytes = 3
	cfg.InputSchema = config.SchemaSpec{Present: true, JSON: []byte("true"), BaseURI: "file:///tmp/sagepipe-input.json"}
	var out, diag strings.Builder
	f := &fakeRunner{run: func(agent.Request) (agent.Response, error) {
		t.Error("oversized reduce input called agent")
		return agent.Response{}, nil
	}}
	if code := Run(context.Background(), cfg, strings.NewReader(" \t\n1\n"), &out, &diag, f); code != 2 {
		t.Fatalf("exit code = %d, want 2; diagnostics: %s", code, diag.String())
	}
	checkDiagnostic(t, diag.String(), "input_too_large", 0)
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestInputReadFailureIsFatal(t *testing.T) {
	cfg := testConfig("map")
	var out, diag strings.Builder
	f := &fakeRunner{run: func(agent.Request) (agent.Response, error) {
		t.Error("agent called after input read failure")
		return agent.Response{}, nil
	}}
	if code := Run(context.Background(), cfg, brokenReader{}, &out, &diag, f); code != 2 {
		t.Fatalf("exit code = %d, want 2; diagnostics: %s", code, diag.String())
	}
	checkDiagnostic(t, diag.String(), "input_read_failed", 0)
}

func TestInputCancellationIsNotReadFailure(t *testing.T) {
	for _, mode := range []string{"map", "reduce", "auto"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var out, diag strings.Builder
			f := &fakeRunner{run: func(agent.Request) (agent.Response, error) {
				t.Fatal("agent called after input cancellation")
				return agent.Response{}, nil
			}}
			if code := Run(ctx, testConfig(mode), strings.NewReader("input\n"), &out, &diag, f); code != 2 {
				t.Fatalf("exit code = %d, want 2; diagnostics: %s", code, diag.String())
			}
			if f.closed != 1 || out.Len() != 0 || strings.Contains(diag.String(), `"code":"input_read_failed"`) {
				t.Errorf("closed=%d stdout=%q diagnostics=%s", f.closed, out.String(), diag.String())
			}
			checkDiagnostic(t, diag.String(), "input_cancelled", 0)
		})
	}
}

type stalledReader struct {
	reading chan struct{}
	release chan struct{}
	err     error
}

func (r stalledReader) Read([]byte) (int, error) {
	close(r.reading)
	<-r.release
	return 0, r.err
}

func TestInputCancellationDuringReadOverridesEOFAndReadError(t *testing.T) {
	for _, readErr := range []error{io.EOF, io.ErrClosedPipe} {
		t.Run(readErr.Error(), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reader := stalledReader{reading: make(chan struct{}), release: make(chan struct{}), err: readErr}
			var out, diag strings.Builder
			f := &fakeRunner{run: func(agent.Request) (agent.Response, error) {
				t.Fatal("agent called after input cancellation")
				return agent.Response{}, nil
			}}
			done := make(chan int, 1)
			go func() { done <- Run(ctx, testConfig("map"), reader, &out, &diag, f) }()
			select {
			case <-reader.reading:
			case <-time.After(2 * time.Second):
				cancel()
				close(reader.release)
				t.Fatal("input read did not start")
			}
			cancel()
			close(reader.release)
			select {
			case code := <-done:
				if code != 2 || f.closed != 1 || strings.Count(diag.String(), `"code":"input_cancelled"`) != 1 ||
					strings.Contains(diag.String(), `"code":"input_read_failed"`) {
					t.Errorf("code=%d closed=%d diagnostics=%s", code, f.closed, diag.String())
				}
			case <-time.After(2 * time.Second):
				t.Fatal("input read did not stop")
			}
		})
	}
}

func TestInputDeadlineIsNotReadFailure(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	var out, diag strings.Builder
	f := &fakeRunner{run: func(agent.Request) (agent.Response, error) {
		t.Fatal("agent called after input deadline")
		return agent.Response{}, nil
	}}
	if code := Run(ctx, testConfig("reduce"), strings.NewReader("input\n"), &out, &diag, f); code != 2 {
		t.Fatalf("exit code = %d, want 2; diagnostics: %s", code, diag.String())
	}
	checkDiagnostic(t, diag.String(), "input_timeout", 0)
}

func TestOutputWriteFailureIsFatal(t *testing.T) {
	cfg := testConfig("map")
	var diag strings.Builder
	f := &fakeRunner{run: func(agent.Request) (agent.Response, error) {
		return agent.Response{Text: `{"items":["result"]}`}, nil
	}}
	if code := Run(context.Background(), cfg, strings.NewReader("hello\n"), brokenWriter{}, &diag, f); code != 2 {
		t.Fatalf("exit code = %d, want 2; diagnostics: %s", code, diag.String())
	}
	checkDiagnostic(t, diag.String(), "output_write_failed", 0)
}

func TestEmptyAnswerDoesNotWriteToOutput(t *testing.T) {
	cfg := testConfig("map")
	var diag strings.Builder
	f := &fakeRunner{run: func(agent.Request) (agent.Response, error) {
		return agent.Response{Text: `{"items":[]}`}, nil
	}}
	if code := Run(context.Background(), cfg, strings.NewReader("hello\n"), brokenWriter{}, &diag, f); code != 0 {
		t.Fatalf("exit code = %d, want 0; diagnostics: %s", code, diag.String())
	}
}

func TestResponseLimitIncludesFinalAnswer(t *testing.T) {
	answer := `{"items":["ok"]}`
	for _, tt := range []struct {
		name  string
		limit int64
		code  int
	}{
		{"at limit", int64(len(answer)), 0},
		{"over limit", int64(len(answer) - 1), 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig("map")
			cfg.MaxResponseBytes = tt.limit
			var out, diag strings.Builder
			f := &fakeRunner{run: func(agent.Request) (agent.Response, error) {
				return agent.Response{Text: answer}, nil
			}}
			if code := Run(context.Background(), cfg, strings.NewReader("hello\n"), &out, &diag, f); code != tt.code {
				t.Fatalf("exit code = %d, want %d; diagnostics: %s", code, tt.code, diag.String())
			}
			if tt.code == 0 && out.String() != "ok\n" || tt.code != 0 && out.Len() != 0 {
				t.Errorf("stdout = %q", out.String())
			}
		})
	}
}

func TestSchemaNumericValidationPreservesPrecision(t *testing.T) {
	cfg := testConfig("map")
	cfg.OutputSchema = config.SchemaSpec{
		Present: true, JSON: []byte(`{"const":0.10000000000000001}`), BaseURI: "file:///tmp/sagepipe-output.json",
	}
	var out, diag strings.Builder
	f := &fakeRunner{run: func(agent.Request) (agent.Response, error) {
		return agent.Response{Text: `{"items":[0.1]}`}, nil
	}}
	if code := Run(context.Background(), cfg, strings.NewReader("x\n"), &out, &diag, f); code != 1 {
		t.Fatalf("exit code = %d, want 1; diagnostics: %s", code, diag.String())
	}
	if out.Len() != 0 {
		t.Errorf("invalid result written: %q", out.String())
	}
}

func TestNumericInputEncodingPreservesJSONNumber(t *testing.T) {
	value, err := schema.Decode([]byte(`0.10000000000000001`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := marshalInput(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `0.10000000000000001` {
		t.Errorf("encoded input = %s, want original JSON number", encoded)
	}
}

func TestUnsafeNativeSchemaFallsBackToLocalValidation(t *testing.T) {
	cfg := testConfig("map")
	cfg.OutputSchema = config.SchemaSpec{
		Present: true,
		JSON:    []byte(`{"$defs":{"word":{"type":"string"}},"$ref":"#/$defs/word"}`),
		BaseURI: "file:///tmp/sagepipe-output.json",
	}
	var out, diag strings.Builder
	f := &fakeRunner{run: func(req agent.Request) (agent.Response, error) {
		if req.NativeSchema != nil {
			t.Errorf("unsafe native schema passed to adapter: %s", req.NativeSchema)
		}
		return agent.Response{Text: `{"items":["ok"]}`}, nil
	}}
	if code := Run(context.Background(), cfg, strings.NewReader("a\nb\n"), &out, &diag, f); code != 0 {
		t.Fatalf("exit code = %d, want 0; diagnostics: %s", code, diag.String())
	}
	if out.String() != "\"ok\"\n\"ok\"\n" {
		t.Errorf("stdout = %q", out.String())
	}
	if n := strings.Count(diag.String(), `"code":"native_schema_unavailable"`); n != 1 {
		t.Errorf("native schema warning count = %d, want 1; %s", n, diag.String())
	}
}

func TestCopilotACPUsesPromptAndLocalValidationWithoutNativeHint(t *testing.T) {
	cfg := testConfig("map")
	cfg.Agent = config.AgentConfig{Provider: "copilot", Protocol: "acp"}
	cfg.OutputSchema = config.SchemaSpec{Present: true, JSON: []byte(`{"type":"string"}`), BaseURI: "file:///tmp/sagepipe-output.json"}
	var out, diag strings.Builder
	f := &fakeRunner{run: func(req agent.Request) (agent.Response, error) {
		if len(req.NativeSchema) != 0 || !strings.Contains(req.Prompt, `"type":"string"`) {
			t.Errorf("unexpected schema hint or missing prompt schema: %+v", req)
		}
		return agent.Response{Text: `{"items":["ok"]}`}, nil
	}}
	if code := Run(context.Background(), cfg, strings.NewReader("a\n"), &out, &diag, f); code != 0 {
		t.Fatalf("exit code = %d, want 0; diagnostics: %s", code, diag.String())
	}
	if out.String() != "\"ok\"\n" || strings.Contains(diag.String(), `"code":"agent_warning"`) {
		t.Errorf("stdout=%q diagnostics=%s", out.String(), diag.String())
	}
}

func TestAgentFailureSkipsOnlyMapRecord(t *testing.T) {
	cfg := testConfig("map")
	var out, diag strings.Builder
	f := &fakeRunner{run: func(req agent.Request) (agent.Response, error) {
		if strings.HasSuffix(req.Prompt, `"first"`) {
			return agent.Response{}, errors.New("input text should not appear in diagnostics")
		}
		return agent.Response{Text: `{"items":["second"]}`}, nil
	}}
	if code := Run(context.Background(), cfg, strings.NewReader("first\nsecond\n"), &out, &diag, f); code != 1 {
		t.Fatalf("exit code = %d, want 1; diagnostics: %s", code, diag.String())
	}
	if out.String() != "second\n" || strings.Contains(diag.String(), "input text should not appear") {
		t.Errorf("stdout=%q diagnostics=%s", out.String(), diag.String())
	}
}

func TestAgentTimeoutIsReportedWithoutLeakingError(t *testing.T) {
	cfg := testConfig("map")
	var out, diag strings.Builder
	f := &fakeRunner{run: func(agent.Request) (agent.Response, error) {
		return agent.Response{}, context.DeadlineExceeded
	}}
	if code := Run(context.Background(), cfg, strings.NewReader("input\n"), &out, &diag, f); code != 1 {
		t.Fatalf("exit code = %d, want 1; diagnostics: %s", code, diag.String())
	}
	checkDiagnostic(t, diag.String(), "agent_timeout", 1)
}

func TestGlobalCancellationStopsMapAfterActiveRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out, diag strings.Builder
	f := &fakeRunner{run: func(agent.Request) (agent.Response, error) {
		cancel()
		return agent.Response{}, context.Canceled
	}}
	if code := Run(ctx, testConfig("map"), strings.NewReader("first\nsecond\n"), &out, &diag, f); code != 2 {
		t.Fatalf("exit code = %d, want 2; diagnostics: %s", code, diag.String())
	}
	if len(f.calls) != 1 || f.closed != 1 || out.Len() != 0 ||
		strings.Count(diag.String(), `"code":"agent_cancelled"`) != 1 ||
		strings.Contains(diag.String(), `"code":"input_read_failed"`) ||
		strings.Contains(diag.String(), `"code":"input_cancelled"`) {
		t.Errorf("calls=%d closed=%d stdout=%q diagnostics=%s", len(f.calls), f.closed, out.String(), diag.String())
	}
}

func TestMapContinuesAfterPerRequestTimeout(t *testing.T) {
	var out, diag strings.Builder
	calls := 0
	f := &fakeRunner{run: func(agent.Request) (agent.Response, error) {
		calls++
		if calls == 1 {
			return agent.Response{}, context.DeadlineExceeded
		}
		return agent.Response{Text: `{"items":["second"]}`}, nil
	}}
	if code := Run(context.Background(), testConfig("map"), strings.NewReader("first\nsecond\n"), &out, &diag, f); code != 1 {
		t.Fatalf("exit code = %d, want 1; diagnostics: %s", code, diag.String())
	}
	if len(f.calls) != 2 || out.String() != "second\n" {
		t.Errorf("calls=%d stdout=%q", len(f.calls), out.String())
	}
	checkDiagnostic(t, diag.String(), "agent_timeout", 1)
}

func checkDiagnostic(t *testing.T, raw, code string, line int) {
	t.Helper()
	for part := range strings.SplitSeq(strings.TrimSpace(raw), "\n") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(part), &obj); err != nil {
			t.Fatalf("diagnostic is not JSON: %q: %v", part, err)
		}
		if obj["code"] == code {
			if obj["stage"] == nil || obj["message"] == nil || obj["level"] == nil {
				t.Errorf("diagnostic lacks required fields: %v", obj)
			}
			if line > 0 && obj["line"] != float64(line) {
				t.Errorf("line = %v, want %d", obj["line"], line)
			}
			return
		}
	}
	t.Errorf("missing diagnostic code %q in %s", code, raw)
}
