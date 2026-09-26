package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
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
	case "descendant":
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), "SAGEPIPE_MODE=descendant-sleep", "SAGEPIPE_CAPTURE=")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(22)
		}
		if err := os.WriteFile(os.Getenv("SAGEPIPE_DESCENDANT_PID"), []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
			child.Process.Kill()
			os.Exit(23)
		}
		child.Process.Release()
		switch os.Getenv("SAGEPIPE_CHILD_OUTPUT") {
		case "stdout-limit":
			fmt.Print(strings.Repeat("x", 1024))
		case "stderr-limit":
			fmt.Println("ready")
			_, _ = fmt.Fprint(os.Stderr, strings.Repeat("x", 70<<10))
		default:
			fmt.Println("ready")
			_, _ = fmt.Fprint(os.Stderr, os.Getenv("SAGEPIPE_STDERR"))
		}
	case "descendant-sleep":
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
	t.Setenv("SAGEPIPE_CHILD_OUTPUT", "")
	t.Setenv("SAGEPIPE_DESCENDANT_PID", "")
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
	opts.Args = []string{"--disable-builtin-mcps", "--disable-mcp-server=workiq"}
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
	if resp.Text != "answer" || resp.Usage != nil ||
		resp.Model == nil ||
		*resp.Model != (agent.Model{ID: "test-model", Source: agent.ModelSourceExplicit}) {
		t.Fatalf("unexpected response: %+v", resp)
	}
	first := captured(t, capturePath)
	if first.Dir != opts.Dir || first.Marker != "inherited" || first.Prompt != "" ||
		argValue(first.Args, "-p") != "short" || argValue(first.Args, "--model") != "test-model" ||
		!slices.Contains(first.Args, "--allow-tool=shell(git status:*),read") ||
		!slices.Contains(first.Args, "--disable-builtin-mcps") ||
		!slices.Contains(first.Args, "--disable-mcp-server=workiq") ||
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
	opts.Args = []string{"--verbose"}
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
	if resp.Text != `{"items":["answer"]}` || len(resp.Warnings) != 0 || resp.Usage == nil ||
		resp.Usage.CachedInputTokens != 2 ||
		resp.Model == nil || *resp.Model != (agent.Model{ID: "model", Source: agent.ModelSourceExplicit}) {
		t.Fatalf("unexpected response: %+v", resp)
	}
	capture := captured(t, capturePath)
	if capture.Dir != opts.Dir || argValue(capture.Args, "--json-schema") != safeSchema ||
		argValue(capture.Args, "--model") != "model" || argValue(capture.Args, "--allowedTools") != "Bash(git status:*)" ||
		!slices.Contains(capture.Args, "--verbose") ||
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
	opts.Args = []string{"--sandbox=read-only"}
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
	if resp.Text != "answer" || resp.Usage == nil || resp.Usage.CachedInputTokens != 2 ||
		resp.Model == nil || *resp.Model != (agent.Model{ID: "model", Source: agent.ModelSourceExplicit}) {
		t.Fatalf("unexpected response: %+v", resp)
	}
	capture := captured(t, capturePath)
	if capture.Schema != safeSchema || capture.SchemaPath == "" || argValue(capture.Args, "--model") != "model" ||
		!slices.Contains(capture.Args, "exec") || !slices.Contains(capture.Args, "--json") ||
		!slices.Contains(capture.Args, "--sandbox=read-only") ||
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
	copilotAnswer := `{"type":"assistant.message","data":{"phase":"final_answer","content":"answer"}}` + "\n"
	for _, tc := range []struct {
		name          string
		build         func(cli.Options) (agent.Runner, error)
		output        string
		failureOutput string
	}{
		{"Copilot", func(o cli.Options) (agent.Runner, error) { return copilot.NewCLI(o) },
			copilotAnswer + `{"type":"result","exitCode":0}` + "\n",
			copilotAnswer + `{"type":"result","exitCode":2}` + "\n"},
		{"Claude", func(o cli.Options) (agent.Runner, error) { return claude.New(o) },
			`{"type":"result","subtype":"success","result":"answer"}` + "\n",
			`{"type":"result","subtype":"error","is_error":true,"result":"secret diagnostic"}` + "\n"},
		{"Codex", func(o cli.Options) (agent.Runner, error) { return codex.New(o) },
			"{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"answer\"}}\n{\"type\":\"turn.completed\"}\n",
			"{\"type\":\"error\",\"message\":\"secret diagnostic\"}\n"},
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
			t.Setenv("SAGEPIPE_STDOUT", tc.failureOutput)
			if _, err := runner.Run(context.Background(), agent.Request{Prompt: "secret prompt"}); err == nil ||
				strings.Contains(err.Error(), "secret") {
				t.Fatalf("failure event leaked data: %v", err)
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
	for _, tc := range []struct {
		name, wantError string
		tools           []string
		request         agent.Request
	}{
		{"unsupported tools", "unsupported", []string{"Read"}, agent.Request{Prompt: "secret"}},
		{"invalid schema", "", nil, agent.Request{NativeSchema: []byte("{")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, path := runnerOptions(t)
			opts.AllowedTools = tc.tools
			runner, err := codex.New(opts)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runner.Run(context.Background(), tc.request); err == nil ||
				(tc.wantError != "" && !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("Run error = %v, want %q", err, tc.wantError)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unexpected subprocess: %v", err)
			}
		})
	}
}

func TestNativeSchemaSafety(t *testing.T) {
	const optionalField = `{"type":"object","properties":{"x":{"type":"string"}},"required":[],"additionalProperties":false}`
	for _, tc := range []struct {
		name            string
		schema          string
		allRequired     bool
		wantNative      bool
		wantInvalidJSON bool
	}{
		{"simple", safeSchema, true, true, false},
		{"optional Claude field", optionalField, false, true, false},
		{"optional Codex field", optionalField, true, false, false},
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
	relative, err := (cli.Options{Dir: "./relative"}).Prepare("helper")
	if err != nil || relative.Dir != "./relative" {
		t.Fatalf("relative directory was rewritten: %+v, %v", relative, err)
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
		err := cli.Execute(context.Background(), "test", prepared, nil, nil, nil, func(line []byte) error {
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

func TestExecuteInheritedPipes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		outputMode string
		maxBytes   int64
		wantError  string
	}{
		{"cancel", "", 0, "canceled"},
		{"parent exit", "", 0, "output pipes remained open"},
		{"parent exit status", "", 0, "status 7"},
		{"stdout limit", "stdout-limit", 512, "stdout limit"},
		{"stderr limit", "stderr-limit", 0, "stderr limit"},
		{"callback error", "", 0, "callback failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, _ := runnerOptions(t)
			opts.MaxOutputBytes = tc.maxBytes
			prepared, err := opts.Prepare("helper")
			if err != nil {
				t.Fatal(err)
			}
			pidPath := filepath.Join(t.TempDir(), "descendant.pid")
			t.Setenv("SAGEPIPE_DESCENDANT_PID", pidPath)
			t.Setenv("SAGEPIPE_MODE", "descendant")
			t.Setenv("SAGEPIPE_CHILD_OUTPUT", tc.outputMode)
			t.Setenv("SAGEPIPE_STDERR", "secret stderr")
			if tc.name == "parent exit status" {
				t.Setenv("SAGEPIPE_STATUS", "7")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			var cancelTimer *time.Timer
			defer func() {
				if cancelTimer != nil {
					cancelTimer.Stop()
				}
			}()
			started := time.Now()
			err = cli.Execute(ctx, "test", prepared, nil, nil, nil, func(line []byte) error {
				if tc.name == "cancel" {
					cancelTimer = time.AfterFunc(100*time.Millisecond, cancel)
				}
				if tc.name == "callback error" {
					return errors.New("callback failed")
				}
				return nil
			})
			if time.Since(started) > 3*time.Second {
				t.Errorf("inherited pipes delayed %s: %v", tc.name, err)
			}
			if tc.name == "cancel" {
				if !errors.Is(err, context.Canceled) {
					t.Errorf("cancellation error: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) || strings.Contains(err.Error(), "secret") {
				t.Errorf("unexpected %s error: %v", tc.name, err)
			}
			pidBytes, err := os.ReadFile(pidPath)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(string(pidBytes))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if child, err := os.FindProcess(pid); err == nil {
					child.Kill()
					child.Release()
				}
			})
			if runtime.GOOS == "windows" {
				return
			}
			deadline := time.Now().Add(2 * time.Second)
			for descendantRunning(pid) && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if descendantRunning(pid) {
				t.Errorf("descendant %d remains running after %s", pid, tc.name)
			}
		})
	}
}

func descendantRunning(pid int) bool {
	if runtime.GOOS == "linux" {
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if errors.Is(err, os.ErrNotExist) {
			return false
		}
		if i := strings.LastIndexByte(string(stat), ')'); err == nil && i >= 0 && i+2 < len(stat) && stat[i+2] == 'Z' {
			return false
		}
	}
	child, err := os.FindProcess(pid)
	return err == nil && child.Signal(syscall.Signal(0)) == nil
}

func TestExecuteSuccessfulPipes(t *testing.T) {
	opts, _ := runnerOptions(t)
	prepared, err := opts.Prepare("helper")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SAGEPIPE_STDOUT", "first\nsecond\n")
	t.Setenv("SAGEPIPE_STDERR", "secret stderr")
	var lines []string
	err = cli.Execute(context.Background(), "test", prepared, nil, nil, nil, func(line []byte) error {
		lines = append(lines, string(line))
		return nil
	})
	if err != nil || !slices.Equal(lines, []string{"first", "second"}) {
		t.Fatalf("ordinary subprocess: %q, %v", lines, err)
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
