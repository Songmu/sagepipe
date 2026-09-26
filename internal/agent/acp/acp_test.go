package acp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Songmu/sagepipe/internal/agent"
	sdk "github.com/coder/acp-go-sdk"
)

func TestACPHelperProcess(t *testing.T) {
	if os.Getenv("SAGEPIPE_ACP_TEST_HELPER") != "1" {
		return
	}
	if marker := os.Getenv("SAGEPIPE_ACP_TEST_STARTED_FILE"); marker != "" {
		if err := os.WriteFile(marker, []byte("started"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if path := os.Getenv("SAGEPIPE_ACP_TEST_START_LOG"); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintln(f, os.Getpid()); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if marker := os.Getenv("SAGEPIPE_ACP_TEST_DESCENDANT_FILE"); marker != "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestACPDescendantHelperProcess$")
		cmd.Env = append(os.Environ(), "SAGEPIPE_ACP_TEST_DESCENDANT_MARKER="+marker)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker+".pid", []byte(strconv.Itoa(cmd.Process.Pid)), 0600); err != nil {
			cmd.Process.Kill()
			cmd.Wait()
			t.Fatal(err)
		}
		if err := cmd.Process.Release(); err != nil {
			t.Fatal(err)
		}
	}
	if os.Getenv("SAGEPIPE_ACP_TEST_MALFORMED") == "1" {
		fmt.Fprintln(os.Stdout, "secret from malformed peer output")
		return
	}
	mock := &mockAgent{}
	mock.conn = sdk.NewAgentSideConnection(mock, os.Stdout, os.Stdin)
	<-mock.conn.Done()
}

func TestACPDescendantHelperProcess(t *testing.T) {
	marker := os.Getenv("SAGEPIPE_ACP_TEST_DESCENDANT_MARKER")
	if marker == "" {
		return
	}
	for n := 0; ; n++ {
		if err := os.WriteFile(marker, []byte(strconv.Itoa(n)), 0600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type mockAgent struct {
	conn     *sdk.AgentSideConnection
	sessions int
	closed   int
	model    string
	closeErr bool
}

func (*mockAgent) Authenticate(context.Context, sdk.AuthenticateRequest) (sdk.AuthenticateResponse, error) {
	return sdk.AuthenticateResponse{}, nil
}

func (*mockAgent) Initialize(ctx context.Context, request sdk.InitializeRequest) (sdk.InitializeResponse, error) {
	if os.Getenv("SAGEPIPE_ACP_TEST_HANG_INIT") == "1" {
		<-ctx.Done()
		return sdk.InitializeResponse{}, ctx.Err()
	}
	if request.ProtocolVersion != sdk.ProtocolVersionNumber || request.ClientCapabilities.Fs.ReadTextFile || request.ClientCapabilities.Terminal {
		return sdk.InitializeResponse{}, errors.New("unexpected client capabilities")
	}
	caps := sdk.AgentCapabilities{}
	if os.Getenv("SAGEPIPE_ACP_TEST_NO_CLOSE") != "1" {
		caps.SessionCapabilities.Close = &sdk.SessionCloseCapabilities{}
	}
	return sdk.InitializeResponse{ProtocolVersion: sdk.ProtocolVersionNumber, AgentCapabilities: caps}, nil
}

func (*mockAgent) Logout(context.Context, sdk.LogoutRequest) (sdk.LogoutResponse, error) {
	return sdk.LogoutResponse{}, nil
}

func (*mockAgent) Cancel(context.Context, sdk.CancelNotification) error {
	return os.WriteFile("cancelled", []byte("yes"), 0600)
}

func (m *mockAgent) CloseSession(_ context.Context, p sdk.CloseSessionRequest) (sdk.CloseSessionResponse, error) {
	if os.Getenv("SAGEPIPE_ACP_TEST_NO_CLOSE") == "1" {
		return sdk.CloseSessionResponse{}, errors.New("session/close was not advertised")
	}
	if os.Getenv("SAGEPIPE_ACP_TEST_HANG_CLOSE") == "1" {
		if err := os.WriteFile("closing", []byte("yes"), 0600); err != nil {
			return sdk.CloseSessionResponse{}, err
		}
		<-m.conn.Done()
		return sdk.CloseSessionResponse{}, errors.New("connection closed during session/close")
	}
	if m.closeErr {
		return sdk.CloseSessionResponse{}, errors.New("sensitive close error")
	}
	if p.SessionId != sdk.SessionId(fmt.Sprintf("session-%d", m.sessions)) {
		return sdk.CloseSessionResponse{}, errors.New("unexpected session close")
	}
	m.closed++
	return sdk.CloseSessionResponse{}, nil
}

func (*mockAgent) ListSessions(context.Context, sdk.ListSessionsRequest) (sdk.ListSessionsResponse, error) {
	return sdk.ListSessionsResponse{}, nil
}

func (m *mockAgent) NewSession(_ context.Context, p sdk.NewSessionRequest) (sdk.NewSessionResponse, error) {
	cwd, err := os.Getwd()
	if err != nil || p.Cwd != cwd || p.McpServers == nil || m.closed != m.sessions {
		return sdk.NewSessionResponse{}, errors.New("session was not isolated or working directory differs")
	}
	m.sessions++
	values := sdk.SessionConfigSelectOptionsUngrouped{{Name: "test-model", Value: "test-model"}}
	category := sdk.SessionConfigOptionCategoryModel
	return sdk.NewSessionResponse{
		SessionId: sdk.SessionId(fmt.Sprintf("session-%d", m.sessions)),
		ConfigOptions: []sdk.SessionConfigOption{{Select: &sdk.SessionConfigOptionSelect{
			Category: &category, Id: "model", Name: "Model", CurrentValue: "default",
			Options: sdk.SessionConfigSelectOptions{Ungrouped: &values},
		}}},
	}, nil
}

func (m *mockAgent) Prompt(ctx context.Context, p sdk.PromptRequest) (sdk.PromptResponse, error) {
	if len(p.Prompt) != 1 || p.Prompt[0].Text == nil {
		return sdk.PromptResponse{}, errors.New("missing prompt text")
	}
	prompt := p.Prompt[0].Text.Text
	if prompt == "disconnect" {
		os.Exit(0)
	}
	if prompt == "block" {
		if err := os.WriteFile("started", []byte("yes"), 0600); err != nil {
			return sdk.PromptResponse{}, err
		}
		<-ctx.Done()
		return sdk.PromptResponse{StopReason: sdk.StopReasonCancelled}, nil
	}
	if prompt == "protocol" {
		if err := os.WriteFile("started", []byte("yes"), 0600); err != nil {
			return sdk.PromptResponse{}, err
		}
		for {
			if _, err := os.Stat("continue"); err == nil {
				break
			} else if !os.IsNotExist(err) {
				return sdk.PromptResponse{}, err
			}
			select {
			case <-ctx.Done():
				return sdk.PromptResponse{}, ctx.Err()
			case <-time.After(10 * time.Millisecond):
			}
		}
		m.conn.SessionUpdate(ctx, sdk.SessionNotification{
			SessionId: p.SessionId, Update: sdk.UpdateAgentThoughtText(strings.Repeat("x", 1024)),
		})
		<-ctx.Done()
		return sdk.PromptResponse{}, ctx.Err()
	}
	if prompt == "secret" {
		return sdk.PromptResponse{}, errors.New("sensitive server error secret")
	}
	if prompt == "incomplete" {
		return sdk.PromptResponse{StopReason: sdk.StopReasonMaxTokens}, nil
	}
	if prompt == "closefail" {
		m.closeErr = true
	}
	send := func(id sdk.SessionId, update sdk.SessionUpdate) error {
		return m.conn.SessionUpdate(ctx, sdk.SessionNotification{SessionId: id, Update: update})
	}
	if err := send(p.SessionId, sdk.UpdateUserMessageText("unrelated user text")); err != nil {
		return sdk.PromptResponse{}, err
	}
	if err := send(p.SessionId, sdk.UpdateAgentThoughtText("unrelated thought")); err != nil {
		return sdk.PromptResponse{}, err
	}
	if err := send("another-session", sdk.UpdateAgentMessageText("unrelated session")); err != nil {
		return sdk.PromptResponse{}, err
	}
	if prompt == "large" {
		send(p.SessionId, sdk.UpdateAgentMessageText("abc"))
		send(p.SessionId, sdk.UpdateAgentMessageText("def"))
		return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn}, nil
	}
	if prompt == "notice" {
		if err := send(p.SessionId, sdk.UpdateAgentMessageText("Info: Disabled tools: bash, edit")); err != nil {
			return sdk.PromptResponse{}, err
		}
		if err := send(p.SessionId, sdk.UpdateAgentMessageText(`{"items":["ping"]}`)); err != nil {
			return sdk.PromptResponse{}, err
		}
		return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn}, nil
	}
	interim := sdk.UpdateAgentMessageText("not the final answer")
	interim.AgentMessageChunk.MessageId = new("00000000-0000-4000-8000-000000000001")
	if err := send(p.SessionId, interim); err != nil {
		return sdk.PromptResponse{}, err
	}
	if err := send(p.SessionId, sdk.StartToolCall("call-1", "mock tool")); err != nil {
		return sdk.PromptResponse{}, err
	}
	reply, err := m.conn.RequestPermission(ctx, sdk.RequestPermissionRequest{
		SessionId: p.SessionId, Options: []sdk.PermissionOption{{
			Kind: sdk.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: "allow",
		}},
		ToolCall: sdk.ToolCallUpdate{ToolCallId: "call-1"},
	})
	if err != nil || reply.Outcome.Cancelled == nil || reply.Outcome.Selected != nil {
		return sdk.PromptResponse{}, errors.New("client did not deny permission")
	}
	text := fmt.Sprintf(`{"items":["%s","denied","%s"]}`, p.SessionId, m.model)
	mid := len(text) / 2
	first := sdk.UpdateAgentMessageText(text[:mid])
	first.AgentMessageChunk.MessageId = new("00000000-0000-4000-8000-000000000002")
	if err := send(p.SessionId, first); err != nil {
		return sdk.PromptResponse{}, err
	}
	second := sdk.UpdateAgentMessageText(text[mid:])
	second.AgentMessageChunk.MessageId = first.AgentMessageChunk.MessageId
	if err := send(p.SessionId, second); err != nil {
		return sdk.PromptResponse{}, err
	}
	return sdk.PromptResponse{
		StopReason: sdk.StopReasonEndTurn,
		Usage:      &sdk.Usage{InputTokens: 11, OutputTokens: 7, CachedReadTokens: new(3)},
	}, nil
}

func (*mockAgent) ResumeSession(context.Context, sdk.ResumeSessionRequest) (sdk.ResumeSessionResponse, error) {
	return sdk.ResumeSessionResponse{}, nil
}

func (m *mockAgent) SetSessionConfigOption(_ context.Context, p sdk.SetSessionConfigOptionRequest) (sdk.SetSessionConfigOptionResponse, error) {
	if p.ValueId == nil || p.ValueId.ConfigId != "model" || p.ValueId.Value != "test-model" {
		return sdk.SetSessionConfigOptionResponse{}, errors.New("wrong model selection")
	}
	m.model = string(p.ValueId.Value)
	return sdk.SetSessionConfigOptionResponse{ConfigOptions: []sdk.SessionConfigOption{}}, nil
}

func (*mockAgent) SetSessionMode(context.Context, sdk.SetSessionModeRequest) (sdk.SetSessionModeResponse, error) {
	return sdk.SetSessionModeResponse{}, nil
}

func mockOptions(t *testing.T) Options {
	t.Helper()
	t.Setenv("SAGEPIPE_ACP_TEST_HELPER", "1")
	return Options{Command: os.Args[0], Args: []string{"-test.run=^TestACPHelperProcess$"}, CWD: t.TempDir()}
}

func TestNewAndCloseWithoutRunDoNotLaunchAgent(t *testing.T) {
	opts := mockOptions(t)
	marker := filepath.Join(opts.CWD, "launched")
	t.Setenv("SAGEPIPE_ACP_TEST_STARTED_FILE", marker)

	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if r.(*runner).cmd != nil {
		t.Fatal("New started an agent")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("unused runner started an agent: %v", err)
	}
	if _, err := r.Run(context.Background(), agent.Request{Prompt: "hello"}); err == nil {
		t.Fatal("Run succeeded after Close")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("closed runner started an agent: %v", err)
	}
}

func TestNewPreservesRelativeCWD(t *testing.T) {
	startupCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(startupCWD); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	}()

	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "relative"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(base); err != nil {
		t.Fatal(err)
	}
	r, err := New(Options{Command: os.Args[0], CWD: "relative"})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.(*runner).cwd; got != "relative" {
		t.Fatalf("cwd = %q, want relative", got)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCancelledFirstRunDoesNotLaunchAgent(t *testing.T) {
	opts := mockOptions(t)
	marker := filepath.Join(opts.CWD, "launched")
	t.Setenv("SAGEPIPE_ACP_TEST_STARTED_FILE", marker)
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Run(ctx, agent.Request{Prompt: "hello"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("cancelled Run started an agent: %v", err)
	}
}

func TestCancelDuringInitializationReapsAgent(t *testing.T) {
	opts := mockOptions(t)
	marker := filepath.Join(opts.CWD, "launched")
	startLog := filepath.Join(opts.CWD, "starts")
	t.Setenv("SAGEPIPE_ACP_TEST_STARTED_FILE", marker)
	t.Setenv("SAGEPIPE_ACP_TEST_START_LOG", startLog)
	t.Setenv("SAGEPIPE_ACP_TEST_HANG_INIT", "1")
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := r.Run(ctx, agent.Request{Prompt: "not sent"})
		result <- err
	}()
	awaitFile(t, marker)
	state := r.(*runner)
	state.procMu.Lock()
	cmd := state.cmd
	state.procMu.Unlock()
	if cmd == nil {
		t.Fatal("agent did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("initialization did not stop after cancellation")
	}
	if cmd.ProcessState == nil || state.cmd != nil || state.conn != nil {
		t.Fatal("agent was not reaped")
	}
	t.Setenv("SAGEPIPE_ACP_TEST_HANG_INIT", "0")
	res, err := r.Run(context.Background(), agent.Request{Prompt: "hello"})
	if err != nil || !strings.Contains(res.Text, `"session-1"`) || processStarts(t, startLog) != 2 {
		t.Fatalf("Run after cancelled initialization = %+v, %v", res, err)
	}
}

func TestRunUsesIndependentSessionsAndDeniesPermission(t *testing.T) {
	opts := mockOptions(t)
	marker := filepath.Join(opts.CWD, "launched")
	startLog := filepath.Join(opts.CWD, "starts")
	t.Setenv("SAGEPIPE_ACP_TEST_STARTED_FILE", marker)
	t.Setenv("SAGEPIPE_ACP_TEST_START_LOG", startLog)
	opts.Model = "test-model"
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("New started an agent: %v", err)
	}

	for n := 1; n <= 2; n++ {
		res, err := r.Run(context.Background(), agent.Request{
			Prompt: "hello", NativeSchema: []byte(`{"type":"object"}`), MaxResponseBytes: 100,
		})
		if err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf(`{"items":["session-%d","denied","test-model"]}`, n)
		if res.Text != want {
			t.Fatalf("response = %q, want %q", res.Text, want)
		}
		if res.Usage == nil || *res.Usage != (agent.Usage{InputTokens: 11, OutputTokens: 7, CachedInputTokens: 3}) {
			t.Fatalf("usage = %+v", res.Usage)
		}
		if !reflect.DeepEqual(res.Warnings, []string{"ACP does not support native output schemas; validate the response locally"}) {
			t.Fatalf("warnings = %v", res.Warnings)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("Run did not start the agent: %v", err)
		}
		if count := processStarts(t, startLog); count != 1 {
			t.Fatalf("close-capable agent started %d times, want 1", count)
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if r.(*runner).cmd.ProcessState == nil {
		t.Fatal("agent was not reaped")
	}
	if _, err := r.Run(context.Background(), agent.Request{Prompt: "hello"}); err == nil {
		t.Fatal("Run succeeded after Close")
	}
}

func TestRunRecyclesAgentWithoutSessionClose(t *testing.T) {
	opts := mockOptions(t)
	startLog := filepath.Join(opts.CWD, "starts")
	t.Setenv("SAGEPIPE_ACP_TEST_START_LOG", startLog)
	t.Setenv("SAGEPIPE_ACP_TEST_NO_CLOSE", "1")
	opts.Model = "test-model"
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	for n := 1; n <= 3; n++ {
		res, err := r.Run(context.Background(), agent.Request{Prompt: "hello", MaxResponseBytes: 100})
		if err != nil {
			t.Fatal(err)
		}
		if want := `{"items":["session-1","denied","test-model"]}`; res.Text != want {
			t.Fatalf("response %d = %q, want %q", n, res.Text, want)
		}
		if processStarts(t, startLog) != n {
			t.Fatalf("record %d did not start a fresh agent", n)
		}
		if state := r.(*runner); state.cmd != nil || state.conn != nil || !state.waited {
			t.Fatalf("record %d retained a live agent", n)
		}
	}
}

func TestRunFiltersOnlyLeadingTransportNotice(t *testing.T) {
	opts := mockOptions(t)
	opts.FilterAgentChunk = func(chunk string) (bool, error) {
		return chunk == "Info: Disabled tools: bash, edit", nil
	}
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	res, err := r.Run(context.Background(), agent.Request{Prompt: "notice", MaxResponseBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != `{"items":["ping"]}` {
		t.Fatalf("filtered response = %q", res.Text)
	}

	withoutFilter := mockOptions(t)
	unfiltered, err := New(withoutFilter)
	if err != nil {
		t.Fatal(err)
	}
	defer unfiltered.Close()
	res, err = unfiltered.Run(context.Background(), agent.Request{Prompt: "notice", MaxResponseBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Info: Disabled tools: bash, edit"+`{"items":["ping"]}` {
		t.Fatalf("unfiltered response = %q", res.Text)
	}
}

func processStarts(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "\n")
}

func TestRunLimitsAndSanitizedErrors(t *testing.T) {
	for _, tt := range []struct {
		prompt, want string
		limit        int64
	}{
		{"large", "response byte limit", 4},
		{"incomplete", "did not complete", 100},
		{"secret", "prompt ACP agent: failed", 100},
		{"closefail", "close ACP session: failed", 100},
	} {
		t.Run(tt.prompt, func(t *testing.T) {
			r, err := New(mockOptions(t))
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			_, err = r.Run(context.Background(), agent.Request{Prompt: tt.prompt, MaxResponseBytes: tt.limit})
			if err == nil || !strings.Contains(err.Error(), tt.want) || strings.Contains(err.Error(), "sensitive") {
				t.Fatalf("Run error = %v", err)
			}
		})
	}
}

func TestRunRecyclesAfterRequestFailure(t *testing.T) {
	for _, tt := range []struct {
		prompt, want string
		limit        int64
	}{
		{"large", "response byte limit exceeded", 4},
		{"closefail", "close ACP session: failed", 100},
		{"disconnect", "ACP connection was lost", 100},
	} {
		t.Run(tt.prompt, func(t *testing.T) {
			opts := mockOptions(t)
			startLog := filepath.Join(opts.CWD, "starts")
			t.Setenv("SAGEPIPE_ACP_TEST_START_LOG", startLog)
			r, err := New(opts)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()

			_, err = r.Run(context.Background(), agent.Request{Prompt: tt.prompt, MaxResponseBytes: tt.limit})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("first Run error = %v, want %q", err, tt.want)
			}
			state := r.(*runner)
			if state.cmd != nil || state.conn != nil || !state.waited {
				t.Fatal("failed request did not reap and recycle its agent")
			}
			res, err := r.Run(context.Background(), agent.Request{Prompt: "hello"})
			if err != nil || !strings.Contains(res.Text, `"session-1"`) || processStarts(t, startLog) != 2 {
				t.Fatalf("Run after %s = %+v, %v", tt.prompt, res, err)
			}
		})
	}
}

func TestRunRecyclesAfterProtocolLimit(t *testing.T) {
	opts := mockOptions(t)
	startLog := filepath.Join(opts.CWD, "starts")
	t.Setenv("SAGEPIPE_ACP_TEST_START_LOG", startLog)
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	result := make(chan error, 1)
	go func() {
		_, err := r.Run(context.Background(), agent.Request{Prompt: "protocol"})
		result <- err
	}()
	awaitFile(t, filepath.Join(opts.CWD, "started"))
	state := r.(*runner)
	state.procMu.Lock()
	budget := state.budget
	state.procMu.Unlock()
	if budget == nil {
		t.Fatal("agent did not start")
	}
	budget.remaining.Store(64)
	if err := os.WriteFile(filepath.Join(opts.CWD, "continue"), []byte("yes"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "protocol byte limit exceeded") {
			t.Fatalf("Run error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("protocol limit did not interrupt the agent")
	}
	if state.cmd != nil || state.conn != nil || !state.waited {
		t.Fatal("protocol limit did not reap and recycle the agent")
	}
	res, err := r.Run(context.Background(), agent.Request{Prompt: "hello"})
	if err != nil || !strings.Contains(res.Text, `"session-1"`) || processStarts(t, startLog) != 2 {
		t.Fatalf("Run after protocol limit = %+v, %v", res, err)
	}
}

func TestModelAndToolsMustBeSupported(t *testing.T) {
	opts := mockOptions(t)
	opts.AllowedTools = "Read"
	if _, err := New(opts); err == nil || !strings.Contains(err.Error(), "allowed-tools") {
		t.Fatalf("unsupported tools = %v", err)
	}
	opts.AllowedTools = ""
	opts.Model = "missing"
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := r.Run(context.Background(), agent.Request{Prompt: "hello"}); err == nil || !strings.Contains(err.Error(), "requested model") {
		t.Fatalf("unsupported model = %v", err)
	}
}

func TestPeerOutputIsNotLogged(t *testing.T) {
	opts := mockOptions(t)
	t.Setenv("SAGEPIPE_ACP_TEST_MALFORMED", "1")
	var logs lockedBuffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)

	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Run(context.Background(), agent.Request{Prompt: "secret"})
	if r == nil || err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(logs.String(), "secret") {
		t.Fatalf("malformed peer output leaked: runner = %v, error = %v, logs = %q", r, err, logs.String())
	}
	if state := r.(*runner); state.cmd != nil || state.conn != nil || !state.waited {
		t.Fatal("initialization failure did not reap and recycle the agent")
	}
	t.Setenv("SAGEPIPE_ACP_TEST_MALFORMED", "0")
	res, err := r.Run(context.Background(), agent.Request{Prompt: "hello"})
	if err != nil || !strings.Contains(res.Text, `"session-1"`) {
		t.Fatalf("Run after initialization failure = %+v, %v", res, err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

type lockedBuffer struct {
	mu   sync.Mutex
	text strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.String()
}

func TestCancelTerminatesAgent(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		name := "cancel"
		want := context.Canceled
		if timeout {
			name = "deadline"
			want = context.DeadlineExceeded
		}
		t.Run(name, func(t *testing.T) {
			opts := mockOptions(t)
			startLog := filepath.Join(opts.CWD, "starts")
			t.Setenv("SAGEPIPE_ACP_TEST_START_LOG", startLog)
			r, err := New(opts)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			ctx, cancel := context.WithCancel(context.Background())
			if timeout {
				ctx, cancel = context.WithTimeout(context.Background(), 500*time.Millisecond)
			}
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, err := r.Run(ctx, agent.Request{Prompt: "block"})
				result <- err
			}()
			awaitFile(t, filepath.Join(opts.CWD, "started"))
			state := r.(*runner)
			state.procMu.Lock()
			cmd := state.cmd
			state.procMu.Unlock()
			if cmd == nil {
				t.Fatal("agent did not start")
			}
			if !timeout {
				cancel()
			}
			select {
			case err := <-result:
				if !errors.Is(err, want) {
					t.Fatalf("Run error = %v, want %v", err, want)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Run did not return after cancellation")
			}
			awaitFile(t, filepath.Join(opts.CWD, "cancelled"))
			if cmd.ProcessState == nil || state.cmd != nil || state.conn != nil {
				t.Fatal("agent not reaped and recycled after cancellation")
			}
			res, err := r.Run(context.Background(), agent.Request{Prompt: "hello"})
			if err != nil || !strings.Contains(res.Text, `"session-1"`) || processStarts(t, startLog) != 2 {
				t.Fatalf("Run after cancellation = %+v, %v", res, err)
			}
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Run(context.Background(), agent.Request{Prompt: "hello"}); err == nil {
				t.Fatal("Run succeeded after explicit Close")
			}
		})
	}
}

func TestCancelTerminatesACPDescendants(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows only terminates the direct agent process")
	}
	for _, method := range []string{"cancel", "close"} {
		t.Run(method, func(t *testing.T) {
			opts := mockOptions(t)
			heartbeat := filepath.Join(opts.CWD, "descendant")
			t.Setenv("SAGEPIPE_ACP_TEST_DESCENDANT_FILE", heartbeat)
			r, err := New(opts)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, err := r.Run(ctx, agent.Request{Prompt: "block"})
				result <- err
			}()
			awaitFile(t, filepath.Join(opts.CWD, "started"))
			awaitFile(t, heartbeat)
			pidFile := heartbeat + ".pid"
			awaitFile(t, pidFile)
			pidBytes, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(string(pidBytes))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				before, beforeErr := os.ReadFile(heartbeat)
				time.Sleep(60 * time.Millisecond)
				after, afterErr := os.ReadFile(heartbeat)
				if beforeErr == nil && afterErr == nil && string(before) != string(after) {
					if child, err := os.FindProcess(pid); err == nil {
						child.Kill()
						child.Release()
					}
				}
			})
			if method == "cancel" {
				cancel()
			} else {
				closed := make(chan error, 1)
				go func() { closed <- r.Close() }()
				select {
				case err := <-closed:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("Close did not stop the agent")
				}
			}
			select {
			case err := <-result:
				if method == "cancel" && !errors.Is(err, context.Canceled) || method == "close" && err == nil {
					t.Fatalf("Run after %s = %v", method, err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Run did not stop after " + method)
			}
			before, err := os.ReadFile(heartbeat)
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(120 * time.Millisecond)
			after, err := os.ReadFile(heartbeat)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Errorf("ACP descendant %d continued running after %s", pid, method)
			}
		})
	}
}

func TestCloseInterruptsPrompt(t *testing.T) {
	opts := mockOptions(t)
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := r.Run(context.Background(), agent.Request{Prompt: "block"})
		result <- err
	}()
	awaitFile(t, filepath.Join(opts.CWD, "started"))
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("Run succeeded after Close")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after Close")
	}
}

func TestCloseDuringCancellation(t *testing.T) {
	opts := mockOptions(t)
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := r.Run(ctx, agent.Request{Prompt: "block"})
		result <- err
	}()
	awaitFile(t, filepath.Join(opts.CWD, "started"))
	state := r.(*runner)
	state.procMu.Lock()
	cmd := state.cmd
	state.procMu.Unlock()
	if cmd == nil {
		t.Fatal("agent did not start")
	}
	closed := make(chan error, 1)
	go func() { closed <- r.Close() }()
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("Run succeeded during Close and cancellation")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run hung during Close and cancellation")
	}
	select {
	case err := <-closed:
		if err != nil || cmd.ProcessState == nil {
			t.Fatalf("Close did not reap the agent: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close hung during cancellation")
	}
	if _, err := r.Run(context.Background(), agent.Request{Prompt: "hello"}); err == nil {
		t.Fatal("Run succeeded after Close")
	}
}

func TestCloseInterruptsSessionClose(t *testing.T) {
	opts := mockOptions(t)
	t.Setenv("SAGEPIPE_ACP_TEST_HANG_CLOSE", "1")
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := r.Run(context.Background(), agent.Request{Prompt: "hello"})
		result <- err
	}()
	awaitFile(t, filepath.Join(opts.CWD, "closing"))
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("Run succeeded after Close interrupted session/close")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run hung during session/close")
	}
}

func TestSessionUpdateLimit(t *testing.T) {
	c := &client{}
	ctx, cancel := context.WithCancelCause(context.Background())
	a := &answer{session: "session", limit: 100, cancel: cancel}
	c.setAnswer(a)
	update := sdk.SessionNotification{SessionId: "another-session", Update: sdk.UpdateAgentThoughtText("ignored")}
	for range maxSessionUpdates + 1 {
		if err := c.SessionUpdate(ctx, update); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := c.result(a)
	if err == nil || !strings.Contains(err.Error(), "update limit") || context.Cause(ctx) != err {
		t.Fatalf("update overflow error = %v", err)
	}
}

func TestProtocolByteLimit(t *testing.T) {
	b := &budgetReader{reader: strings.NewReader("abcdef")}
	b.remaining.Store(4)
	data, err := io.ReadAll(b)
	if err == nil || !strings.Contains(err.Error(), "protocol byte limit") || string(data) != "abcd" || !b.exceeded.Load() {
		t.Fatalf("protocol bytes = %q, error = %v, exceeded = %t", data, err, b.exceeded.Load())
	}
}

func awaitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("agent did not write expected marker %q", filepath.Base(path))
}
