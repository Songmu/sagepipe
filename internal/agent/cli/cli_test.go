package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Songmu/sagepipe/internal/agent"
	"github.com/Songmu/sagepipe/internal/agent/claude"
	"github.com/Songmu/sagepipe/internal/agent/cli"
	"github.com/Songmu/sagepipe/internal/agent/codex"
	"github.com/Songmu/sagepipe/internal/agent/copilot"
)

type processCapture struct {
	Args       []string `json:"args"`
	Dir        string   `json:"dir"`
	Prompt     string   `json:"prompt"`
	Schema     string   `json:"schema"`
	SchemaPath string   `json:"schema_path"`
	SchemaMode uint32   `json:"schema_mode"`
	Marker     string   `json:"marker"`
	PID        int      `json:"pid"`
}

func TestMain(m *testing.M) {
	if os.Getenv("SAGEPIPE_HELPER") == "1" {
		helperProcess()
		return
	}
	os.Exit(m.Run())
}

func helperProcess() {
	args := os.Args[1:]
	input, _ := io.ReadAll(os.Stdin)
	cwd, _ := os.Getwd()
	capture := processCapture{Args: args, Dir: cwd, Prompt: string(input), Marker: os.Getenv("SAGEPIPE_MARKER"), PID: os.Getpid()}
	if index := slices.Index(args, "--output-schema"); index >= 0 && index+1 < len(args) {
		capture.SchemaPath = args[index+1]
		schema, err := os.ReadFile(capture.SchemaPath)
		if err != nil {
			os.Exit(19)
		}
		capture.Schema = string(schema)
		info, err := os.Stat(capture.SchemaPath)
		if err != nil {
			os.Exit(21)
		}
		capture.SchemaMode = uint32(info.Mode().Perm())
	}
	if path := os.Getenv("SAGEPIPE_CAPTURE"); path != "" {
		data, _ := json.Marshal(capture)
		if err := os.WriteFile(path, data, 0600); err != nil {
			os.Exit(20)
		}
	}
	switch os.Getenv("SAGEPIPE_MODE") {
	case "sleep":
		time.Sleep(30 * time.Second)
	case "stderr":
		_, _ = fmt.Fprint(os.Stderr, strings.Repeat("x", 70<<10))
	case "big":
		fmt.Println(strings.Repeat("x", 128<<10))
	case "bytes":
		n, _ := strconv.Atoi(os.Getenv("SAGEPIPE_BYTES"))
		_, _ = io.WriteString(os.Stdout, strings.Repeat("x", n))
	case "escaped":
		n, _ := strconv.Atoi(os.Getenv("SAGEPIPE_BYTES"))
		_ = json.NewEncoder(os.Stdout).Encode(struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
			Result  string `json:"result"`
		}{"result", "success", strings.Repeat("\x00", n)})
	default:
		_, _ = fmt.Fprint(os.Stderr, os.Getenv("SAGEPIPE_STDERR"))
		fmt.Print(os.Getenv("SAGEPIPE_STDOUT"))
	}
	if status, _ := strconv.Atoi(os.Getenv("SAGEPIPE_STATUS")); status != 0 {
		os.Exit(status)
	}
	os.Exit(0)
}

func runnerOptions(t *testing.T) (cli.Options, string) {
	t.Helper()
	t.Setenv("SAGEPIPE_HELPER", "1")
	t.Setenv("SAGEPIPE_MODE", "")
	t.Setenv("SAGEPIPE_BYTES", "")
	t.Setenv("SAGEPIPE_STATUS", "")
	t.Setenv("SAGEPIPE_STDERR", "")
	t.Setenv("SAGEPIPE_STDOUT", "")
	t.Setenv("SAGEPIPE_MARKER", "inherited")
	path := filepath.Join(t.TempDir(), "capture.json")
	t.Setenv("SAGEPIPE_CAPTURE", path)
	return cli.Options{Program: os.Args[0], Dir: t.TempDir()}, path
}

func captured(t *testing.T, path string) processCapture {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var capture processCapture
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	return capture
}

func argValue(args []string, name string) string {
	idx := slices.Index(args, name)
	if idx < 0 || idx+1 >= len(args) {
		return ""
	}
	return args[idx+1]
}

const safeSchema = `{"type":"object","properties":{"items":{"type":"array","items":{"type":"string"}}},"required":["items"],"additionalProperties":false}`

func TestCopilotCLI(t *testing.T) {
	opts, capturePath := runnerOptions(t)
	opts.Model = "test-model"
	opts.AllowedTools = []string{"shell(git status:*)", "read"}
	runner, err := copilot.NewCLI(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SAGEPIPE_STDOUT", "{\"type\":\"assistant.message\",\"data\":{\"phase\":\"analysis\",\"content\":\"not final\"}}\n"+
		"{\"type\":\"assistant.message\",\"data\":{\"phase\":\"final_answer\",\"content\":\"answer\"}}\n"+
		"{\"type\":\"result\",\"exitCode\":0,\"usage\":{\"premiumRequests\":1}}\n")
	resp, err := runner.Run(context.Background(), agent.Request{Prompt: "short", MaxResponseBytes: 6})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "answer" || resp.Usage != nil {
		t.Fatalf("unexpected response: %+v", resp)
	}
	first := captured(t, capturePath)
	if first.Dir != opts.Dir || first.Marker != "inherited" || first.Prompt != "" ||
		argValue(first.Args, "-p") != "short" || argValue(first.Args, "--model") != "test-model" ||
		!slices.Contains(first.Args, "--allow-tool=shell(git status:*),read") ||
		!slices.Contains(first.Args, "--output-format=json") || !slices.Contains(first.Args, "--no-ask-user") {
		t.Fatalf("unexpected process options: %+v", first)
	}

	shortPrompt := "Output schema:\n" + safeSchema + "\nInput:\nshort"
	resp, err = runner.Run(context.Background(), agent.Request{Prompt: shortPrompt, NativeSchema: []byte(safeSchema)})
	if err != nil || len(resp.Warnings) != 1 || argValue(captured(t, capturePath).Args, "-p") != shortPrompt {
		t.Fatalf("short schema fallback changed the prompt: %+v, %v", resp, err)
	}

	longPrompt := "Output schema:\n" + safeSchema + "\nInput:\n" + strings.Repeat("long prompt ", 20000)
	resp, err = runner.Run(context.Background(), agent.Request{Prompt: longPrompt, NativeSchema: []byte(safeSchema)})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Warnings) != 1 || resp.Text != "answer" {
		t.Fatalf("missing schema fallback: %+v", resp)
	}
	second := captured(t, capturePath)
	if slices.Contains(second.Args, "-p") || second.Prompt != longPrompt || first.PID == second.PID {
		t.Fatalf("original prompt was not piped unchanged to a fresh process")
	}
	if err := runner.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeCLI(t *testing.T) {
	opts, capturePath := runnerOptions(t)
	opts.Model = "model"
	opts.AllowedTools = []string{"Bash(git status:*)", "Read"}
	runner, err := claude.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SAGEPIPE_STDOUT", `{"type":"result","subtype":"success","is_error":false,"result":"ignored","structured_output":{"items":["answer"]},"usage":{"input_tokens":3,"output_tokens":7,"cache_read_input_tokens":2}}`+"\n")
	resp, err := runner.Run(context.Background(), agent.Request{Prompt: strings.Repeat("prompt ", 10000), NativeSchema: []byte(safeSchema)})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != `{"items":["answer"]}` || len(resp.Warnings) != 0 || resp.Usage == nil || resp.Usage.CachedInputTokens != 2 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	capture := captured(t, capturePath)
	if capture.Dir != opts.Dir || argValue(capture.Args, "--json-schema") != safeSchema ||
		argValue(capture.Args, "--model") != "model" || argValue(capture.Args, "--allowedTools") != "Bash(git status:*)" ||
		!slices.Contains(capture.Args, "Read") || !slices.Contains(capture.Args, "-p") ||
		!strings.Contains(capture.Prompt, "prompt ") {
		t.Fatalf("unexpected Claude arguments: %+v", capture)
	}
	t.Setenv("SAGEPIPE_STDOUT", `{"type":"result","subtype":"success","result":"plain"}`+"\n")
	unsupportedSchema := `{"$ref":"#/$defs/root"}`
	fallbackPrompt := "Output schema:\n" + unsupportedSchema + "\nInput:\nvalue"
	resp, err = runner.Run(context.Background(), agent.Request{Prompt: fallbackPrompt, NativeSchema: []byte(unsupportedSchema)})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "plain" || len(resp.Warnings) != 1 || captured(t, capturePath).Prompt != fallbackPrompt {
		t.Fatalf("schema fallback changed the prompt: %+v", resp)
	}
}

func TestCodexCLIAndSchemaCleanup(t *testing.T) {
	opts, capturePath := runnerOptions(t)
	opts.Model = "model"
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	runner, err := codex.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SAGEPIPE_STDOUT", "{\"type\":\"item.completed\",\"item\":{\"type\":\"command_execution\",\"text\":\"not final\"}}\n"+
		"{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"answer\"}}\n"+
		"{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":10,\"output_tokens\":4,\"cached_input_tokens\":2}}\n")
	resp, err := runner.Run(context.Background(), agent.Request{Prompt: strings.Repeat("long ", 30000), NativeSchema: []byte(safeSchema)})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "answer" || resp.Usage == nil || resp.Usage.CachedInputTokens != 2 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	capture := captured(t, capturePath)
	if capture.Schema != safeSchema || capture.SchemaPath == "" || argValue(capture.Args, "--model") != "model" ||
		!slices.Contains(capture.Args, "exec") || !slices.Contains(capture.Args, "--json") ||
		capture.Args[len(capture.Args)-1] != "-" || !strings.Contains(capture.Prompt, "long ") {
		t.Fatalf("unexpected Codex process: %+v", capture)
	}
	if _, err := os.Stat(capture.SchemaPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("schema file remains: %v", err)
	}
	if runtime.GOOS != "windows" && capture.SchemaMode != 0600 {
		t.Fatalf("insecure schema file mode: %o", capture.SchemaMode)
	}
	t.Setenv("SAGEPIPE_STATUS", "9")
	if _, err := runner.Run(context.Background(), agent.Request{Prompt: "prompt", NativeSchema: []byte(safeSchema)}); err == nil {
		t.Fatal("expected CLI failure")
	}
	capture = captured(t, capturePath)
	if _, err := os.Stat(capture.SchemaPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("schema file remains after failure: %v", err)
	}
	if runtime.GOOS != "windows" {
		files, err := os.ReadDir(tmp)
		if err != nil || len(files) != 0 {
			t.Fatalf("temporary files remain: %v, %v", files, err)
		}
	}
	t.Setenv("SAGEPIPE_STATUS", "")
	unsupportedSchema := `{"$ref":"#/$defs/root"}`
	fallbackPrompt := "Output schema:\n" + unsupportedSchema + "\nInput:\nvalue"
	resp, err = runner.Run(context.Background(), agent.Request{Prompt: fallbackPrompt, NativeSchema: []byte(unsupportedSchema)})
	if err != nil || len(resp.Warnings) != 1 || captured(t, capturePath).Prompt != fallbackPrompt {
		t.Fatalf("unsupported schema changed the prompt: %+v, %v", resp, err)
	}
}

func TestSubprocessFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		build  func(cli.Options) (agent.Runner, error)
		output string
	}{
		{"Copilot", func(o cli.Options) (agent.Runner, error) { return copilot.NewCLI(o) },
			"{\"type\":\"assistant.message\",\"data\":{\"phase\":\"final_answer\",\"content\":\"answer\"}}\n{\"type\":\"result\",\"exitCode\":0}\n"},
		{"Claude", func(o cli.Options) (agent.Runner, error) { return claude.New(o) },
			`{"type":"result","subtype":"success","result":"answer"}` + "\n"},
		{"Codex", func(o cli.Options) (agent.Runner, error) { return codex.New(o) },
			"{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"answer\"}}\n{\"type\":\"turn.completed\"}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, _ := runnerOptions(t)
			runner, err := tc.build(opts)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("SAGEPIPE_STDOUT", tc.output)
			req := agent.Request{Prompt: "secret prompt", MaxResponseBytes: 5}
			if _, err := runner.Run(context.Background(), req); err == nil || !strings.Contains(err.Error(), "limit") {
				t.Fatalf("final answer limit: %v", err)
			}
			req.MaxResponseBytes = 1000
			t.Setenv("SAGEPIPE_STATUS", "7")
			t.Setenv("SAGEPIPE_STDERR", "secret stderr")
			if _, err := runner.Run(context.Background(), req); err == nil ||
				!strings.Contains(err.Error(), "status 7") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unsanitized exit status: %v", err)
			}
			t.Setenv("SAGEPIPE_STATUS", "")
			t.Setenv("SAGEPIPE_STDOUT", "not JSON\n")
			if _, err := runner.Run(context.Background(), req); err == nil ||
				strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "not JSON") {
				t.Fatalf("unsanitized invalid output: %v", err)
			}
			t.Setenv("SAGEPIPE_STDOUT", "")
			if _, err := runner.Run(context.Background(), req); err == nil {
				t.Fatal("expected missing final answer error")
			}
			t.Setenv("SAGEPIPE_MODE", "big")
			limited := opts
			limited.MaxOutputBytes = 512
			smallRunner, err := tc.build(limited)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := smallRunner.Run(context.Background(), req); err == nil || !strings.Contains(err.Error(), "stdout limit") {
				t.Fatalf("stdout limit: %v", err)
			}
			t.Setenv("SAGEPIPE_MODE", "stderr")
			if _, err := runner.Run(context.Background(), req); err == nil || !strings.Contains(err.Error(), "stderr limit") {
				t.Fatalf("stderr limit: %v", err)
			}
			t.Setenv("SAGEPIPE_MODE", "sleep")
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			started := time.Now()
			if _, err := runner.Run(ctx, req); !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 5*time.Second {
				t.Fatalf("cancellation failed: %v", err)
			}
		})
	}
}

func TestUnsupportedAndInvalidConfiguration(t *testing.T) {
	opts, path := runnerOptions(t)
	opts.AllowedTools = []string{"Read"}
	runner, err := codex.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), agent.Request{Prompt: "secret"}); err == nil ||
		!strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("Codex accepted unsupported tools: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected subprocess: %v", err)
	}
	opts.AllowedTools = nil
	runner, err = codex.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), agent.Request{NativeSchema: []byte("{")}); err == nil {
		t.Fatal("invalid schema was silently dropped")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected subprocess: %v", err)
	}
}

func TestNativeSchemaSafety(t *testing.T) {
	for _, tc := range []struct {
		name            string
		schema          string
		allRequired     bool
		wantNative      bool
		wantInvalidJSON bool
	}{
		{"simple", safeSchema, true, true, false},
		{"optional Claude field", `{"type":"object","properties":{"x":{"type":"string"}},"required":[],"additionalProperties":false}`, false, true, false},
		{"optional Codex field", `{"type":"object","properties":{"x":{"type":"string"}},"required":[],"additionalProperties":false}`, true, false, false},
		{"reference", `{"$ref":"#/$defs/x"}`, false, false, false},
		{"constraint", `{"type":"object","properties":{"x":{"type":"string","pattern":"a"}},"required":["x"],"additionalProperties":false}`, true, false, false},
		{"open object", `{"type":"object","properties":{},"required":[]}`, false, false, false},
		{"trailing JSON", `{} {}`, false, false, true},
		{"invalid UTF-8", string([]byte{0xff}), false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			native, err := cli.SafeNativeSchema([]byte(tc.schema), tc.allRequired)
			if native != tc.wantNative || (err != nil) != tc.wantInvalidJSON {
				t.Fatalf("native=%v error=%v", native, err)
			}
		})
	}
}

func TestProviderFailureEvents(t *testing.T) {
	for _, tc := range []struct {
		name   string
		build  func(cli.Options) (agent.Runner, error)
		output string
	}{
		{"Copilot", func(o cli.Options) (agent.Runner, error) { return copilot.NewCLI(o) },
			"{\"type\":\"assistant.message\",\"data\":{\"phase\":\"final_answer\",\"content\":\"answer\"}}\n{\"type\":\"result\",\"exitCode\":2}\n"},
		{"Claude", func(o cli.Options) (agent.Runner, error) { return claude.New(o) },
			`{"type":"result","subtype":"error","is_error":true,"result":"secret diagnostic"}` + "\n"},
		{"Codex", func(o cli.Options) (agent.Runner, error) { return codex.New(o) },
			"{\"type\":\"error\",\"message\":\"secret diagnostic\"}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, _ := runnerOptions(t)
			t.Setenv("SAGEPIPE_STDOUT", tc.output)
			runner, err := tc.build(opts)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runner.Run(context.Background(), agent.Request{Prompt: "secret prompt"}); err == nil ||
				strings.Contains(err.Error(), "secret") {
				t.Fatalf("failure event leaked data: %v", err)
			}
		})
	}
}

func TestClaudeLargeSchemaFallback(t *testing.T) {
	opts, capturePath := runnerOptions(t)
	runner, err := claude.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SAGEPIPE_STDOUT", `{"type":"result","subtype":"success","result":"answer"}`+"\n")
	schema := `{"type":"object","description":"` + strings.Repeat("x", 9000) +
		`","properties":{},"required":[],"additionalProperties":false}`
	prompt := "Output schema:\n" + schema + "\nInput:\nvalue"
	resp, err := runner.Run(context.Background(), agent.Request{Prompt: prompt, NativeSchema: []byte(schema)})
	if err != nil {
		t.Fatal(err)
	}
	capture := captured(t, capturePath)
	if resp.Text != "answer" || len(resp.Warnings) != 1 ||
		slices.Contains(capture.Args, "--json-schema") || capture.Prompt != prompt {
		t.Fatal("oversized native schema fallback changed the prompt")
	}
	if _, err := runner.Run(context.Background(), agent.Request{Prompt: strings.Repeat("x", 10_000_001)}); err == nil ||
		!strings.Contains(err.Error(), "stdin limit") {
		t.Fatalf("expected explicit Claude stdin limit error: %v", err)
	}
}

func TestEventOutputLimitOptions(t *testing.T) {
	defaults, err := (cli.Options{}).Prepare("helper")
	if err != nil || defaults.MaxOutputBytes != 16<<20 {
		t.Fatalf("unexpected direct-use default: %+v, %v", defaults, err)
	}
	maxInt := int64(^uint(0) >> 1)
	for _, limit := range []int64{33 << 20, maxInt - 1} {
		opts, err := (cli.Options{MaxOutputBytes: limit}).Prepare("helper")
		if err != nil || opts.MaxOutputBytes != limit {
			t.Fatalf("valid output limit %d: %v", limit, err)
		}
	}
	for _, limit := range []int64{-1, maxInt} {
		if _, err := (cli.Options{MaxOutputBytes: limit}).Prepare("helper"); err == nil {
			t.Fatalf("accepted unrepresentable output limit %d", limit)
		}
	}
}

func TestExactEventStreamBoundary(t *testing.T) {
	opts, _ := runnerOptions(t)
	opts.MaxOutputBytes = 512
	prepared, err := opts.Prepare("helper")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SAGEPIPE_MODE", "bytes")
	for _, n := range []int{512, 513} {
		t.Setenv("SAGEPIPE_BYTES", strconv.Itoa(n))
		var got int
		err := cli.Execute(context.Background(), "test", prepared, nil, nil, func(line []byte) error {
			got += len(line)
			return nil
		})
		if n == 512 && (err != nil || got != n) {
			t.Fatalf("exact cap %d: got %d bytes, %v", n, got, err)
		}
		if n == 513 && (err == nil || !strings.Contains(err.Error(), "stdout limit")) {
			t.Fatalf("over cap %d: got %d bytes, %v", n, got, err)
		}
	}
}

func TestEscapedAnswerExceedsOldEventCap(t *testing.T) {
	const responseBytes = 8 << 20
	opts, _ := runnerOptions(t)
	opts.MaxOutputBytes = 6*responseBytes + (1 << 20)
	t.Setenv("SAGEPIPE_MODE", "escaped")
	t.Setenv("SAGEPIPE_BYTES", strconv.Itoa(responseBytes))
	runner, err := claude.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), agent.Request{Prompt: "prompt", MaxResponseBytes: responseBytes})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Text) != responseBytes || strings.Trim(resp.Text, "\x00") != "" {
		t.Fatalf("unexpected extracted answer size/content: %d", len(resp.Text))
	}
}
