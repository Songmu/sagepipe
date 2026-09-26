package copilot

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Songmu/sagepipe/internal/agent"
)

func TestCopilotACPNoticeHelperProcess(t *testing.T) {
	if os.Getenv("SAGEPIPE_COPILOT_NOTICE_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			t.Fatal(err)
		}
		respond := func(result any) {
			if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
				t.Fatal(err)
			}
		}
		switch request.Method {
		case "initialize":
			respond(map[string]any{"protocolVersion": 1})
		case "session/new":
			respond(map[string]any{"sessionId": "notice-session"})
		case "session/prompt":
			for _, text := range []string{"Info: Disabled tools: bash, edit", `{"items":`, `["ping"]}`} {
				if err := encoder.Encode(map[string]any{
					"jsonrpc": "2.0", "method": "session/update",
					"params": map[string]any{
						"sessionId": "notice-session",
						"update": map[string]any{
							"sessionUpdate": "agent_message_chunk",
							"content":       map[string]any{"type": "text", "text": text},
						},
					},
				}); err != nil {
					t.Fatal(err)
				}
			}
			respond(map[string]any{"stopReason": "end_turn"})
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestNewACPIgnoresCopilotToolNotice(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mock Copilot launcher requires a POSIX shell")
	}
	bin := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nexec %q -test.run=^TestCopilotACPNoticeHelperProcess$ -- \"$@\"\n", os.Args[0])
	if err := os.WriteFile(filepath.Join(bin, "copilot"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("SAGEPIPE_COPILOT_NOTICE_HELPER", "1")

	r, err := NewACP("", t.TempDir(), "view", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	res, err := r.Run(context.Background(), agent.Request{Prompt: "ping", MaxResponseBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != `{"items":["ping"]}` {
		t.Fatalf("response = %q", res.Text)
	}
}

func TestNewACPLaunchArguments(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mock Copilot launcher requires a POSIX shell")
	}
	bin := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$SAGEPIPE_COPILOT_ARGS_FILE\"\n"
	if err := os.WriteFile(filepath.Join(bin, "copilot"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("SAGEPIPE_COPILOT_ARGS_FILE", argsFile)

	for _, tt := range []struct {
		name, model, tools string
		extra              []string
		want               []string
	}{
		{"defaults", "", "", nil, []string{"--acp", "--stdio"}},
		{"extra arguments", "", "", []string{"--disable-builtin-mcps", "--disable-mcp-server=workiq"}, []string{
			"--disable-builtin-mcps", "--disable-mcp-server=workiq", "--acp", "--stdio",
		}},
		{"model and tools", "test-model", "view   grep\tglob", nil, []string{
			"--acp", "--stdio", "--model", "test-model", "--available-tools=view,grep,glob",
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.Remove(argsFile); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			r, err := NewACP(tt.model, t.TempDir(), tt.tools, tt.extra)
			if r == nil || err != nil {
				t.Fatalf("NewACP() = (%v, %v), expected an unused runner", r, err)
			}
			if _, err := os.Stat(argsFile); !os.IsNotExist(err) {
				t.Fatalf("NewACP started Copilot before Run: %v", err)
			}
			_, err = r.Run(context.Background(), agent.Request{Prompt: "not sent to model"})
			if err == nil || !strings.Contains(err.Error(), "initialize ACP agent") {
				t.Fatalf("Run error = %v, expected initialization failure", err)
			}
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Split(strings.TrimSpace(string(data)), "\n")
			if !slices.Equal(got, tt.want) {
				t.Fatalf("args = %q, want %q", got, tt.want)
			}
		})
	}

	for _, tt := range []struct{ name, tools string }{
		{"whitespace", "  \t"},
		{"comma separated", "Read,Grep"},
		{"lowercase", "read"},
		{"unknown", "Read"},
	} {
		t.Run("invalid tools/"+tt.name, func(t *testing.T) {
			if err := os.Remove(argsFile); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			r, err := NewACP("", t.TempDir(), tt.tools, nil)
			if r != nil || err == nil || !strings.Contains(err.Error(), "allowed-tools") {
				t.Fatalf("NewACP(tools=%q) = (%v, %v)", tt.tools, r, err)
			}
			if _, err := os.Stat(argsFile); !os.IsNotExist(err) {
				t.Fatalf("invalid tools launched Copilot: %v", err)
			}
		})
	}
	r, err := NewACP("", t.TempDir(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(argsFile); !os.IsNotExist(err) {
		t.Fatalf("closing unused Copilot runner launched a process: %v", err)
	}
}

func TestFilterToolNotice(t *testing.T) {
	for _, tt := range []struct {
		name, text, wantError string
		skip                  bool
	}{
		{"disabled tools", "Info: Disabled tools: asset-generator-create_chart_bar, bash, write_agent", "", true},
		{"unknown tool", `Info: Unknown tool name in the tool allowlist: "read"`, "rejected", false},
		{"mixed notice and answer", `Info: Disabled tools: bash{"items":["ping"]}`, "unrecognized", false},
		{"malformed tool list", "Info: Disabled tools: bash,edit", "unrecognized", false},
		{"answer", `{"items":["ping"]}`, "", false},
		{"model text", "Info: This is ordinary text", "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			skip, err := filterToolNotice(tt.text)
			if skip != tt.skip || (tt.wantError == "" && err != nil) ||
				(tt.wantError != "" && (err == nil || !strings.Contains(err.Error(), tt.wantError))) {
				t.Fatalf("filterToolNotice() = (%t, %v), want skip=%t, error=%q", skip, err, tt.skip, tt.wantError)
			}
		})
	}
}
