// Package acp runs stdio ACP agents with one independent session per request.
package acp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Songmu/sagepipe/internal/agent"
	"github.com/Songmu/sagepipe/internal/agent/process"
	sdk "github.com/coder/acp-go-sdk"
)

const (
	defaultResponseBytes = 8 << 20
	maxSessionUpdates    = 10000
	minProtocolBytes     = 64 << 20
	initializeTimeout    = 30 * time.Second
	cancelGracePeriod    = 100 * time.Millisecond
)

// Options configures a stdio ACP process. Command is passed directly to
// os/exec (no shell); Args and CWD apply to the entire connection.
type Options struct {
	Command      string
	Args         []string
	CWD          string
	Model        string
	AllowedTools string
	// FilterAgentChunk may discard recognized leading transport notices;
	// its errors must not contain prompt, response, or environment data.
	FilterAgentChunk func(string) (bool, error)
}

type runner struct {
	command       string
	args          []string
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	stdout        io.ReadCloser
	conn          *sdk.ClientSideConnection
	client        *client
	budget        *budgetReader
	cwd           string
	model         string
	filter        func(string) (bool, error)
	closeSessions bool

	runSlot chan struct{}
	closed  chan struct{}
	once    sync.Once
	procMu  sync.Mutex
	waited  bool
}

var _ agent.Runner = (*runner)(nil)

// New validates the options without starting the agent. The first Run starts
// and initializes it; the caller must Close the runner even if Run is never used.
// If the agent does not support session/close, each Run reaps its process and
// the next Run starts a new one.
// Generic ACP has no portable tool-filtering setting; product adapters must
// translate it to their own launch arguments instead.
func New(opts Options) (agent.Runner, error) {
	if opts.Command == "" {
		return nil, errors.New("ACP command is required")
	}
	if opts.AllowedTools != "" {
		return nil, errors.New("generic ACP does not support allowed-tools")
	}
	cwd := opts.CWD
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return nil, errors.New("determine ACP working directory: failed")
		}
	}
	info, err := os.Stat(cwd)
	if err != nil || !info.IsDir() {
		return nil, errors.New("ACP working directory is not a directory")
	}

	return &runner{
		command: opts.Command, args: append([]string(nil), opts.Args...),
		cwd: cwd, model: opts.Model, filter: opts.FilterAgentChunk,
		runSlot: make(chan struct{}, 1), closed: make(chan struct{}),
	}, nil
}

func (r *runner) start(ctx context.Context, onLaunch func(agent.Launch)) error {
	r.procMu.Lock()
	defer r.procMu.Unlock()
	select {
	case <-r.closed:
		return errors.New("ACP connection is closed")
	default:
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	cmd := exec.Command(r.command, r.args...)
	cmd.Dir = r.cwd
	cmd.Stderr = io.Discard
	process.Configure(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return errors.New("open ACP input pipe: failed")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return errors.New("open ACP output pipe: failed")
	}
	if onLaunch != nil {
		onLaunch(agent.NewLaunch(r.command, r.args, r.cwd))
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return errors.New("start ACP agent: failed")
	}

	r.cmd, r.stdin, r.stdout = cmd, stdin, stdout
	r.client = &client{filter: r.filter}
	r.waited = false
	r.budget = &budgetReader{reader: stdout, ready: make(chan struct{})}
	r.budget.remaining.Store(minProtocolBytes)
	r.conn = sdk.NewClientSideConnection(r.client, stdin, r.budget)
	// The SDK otherwise logs malformed peer messages (including their raw
	// contents) to the process's default logger.
	r.conn.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	close(r.budget.ready)
	return nil
}

func (r *runner) initialize(ctx context.Context) error {
	initCtx, cancel := context.WithTimeout(ctx, initializeTimeout)
	defer cancel()
	finished := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		r.watch(initCtx, finished, nil)
	}()
	init, err := r.conn.Initialize(initCtx, sdk.InitializeRequest{ProtocolVersion: sdk.ProtocolVersionNumber})
	close(finished)
	<-watchDone
	if err != nil || initCtx.Err() != nil {
		if initCtx.Err() != nil {
			return initCtx.Err()
		}
		return errors.New("initialize ACP agent: failed")
	}
	if init.ProtocolVersion != sdk.ProtocolVersionNumber {
		return errors.New("ACP agent selected an unsupported protocol version")
	}
	r.closeSessions = init.AgentCapabilities.SessionCapabilities.Close != nil
	return nil
}

func (r *runner) Run(ctx context.Context, request agent.Request) (response agent.Response, runErr error) {
	if err := ctx.Err(); err != nil {
		return agent.Response{}, err
	}
	if request.MaxResponseBytes < 0 {
		return agent.Response{}, errors.New("ACP max response bytes must not be negative")
	}
	limit := request.MaxResponseBytes
	if limit == 0 {
		limit = defaultResponseBytes
	}
	select {
	case r.runSlot <- struct{}{}:
	case <-r.closed:
		return agent.Response{}, errors.New("ACP connection is closed")
	case <-ctx.Done():
		return agent.Response{}, ctx.Err()
	}
	defer func() { <-r.runSlot }()
	select {
	case <-r.closed:
		return agent.Response{}, errors.New("ACP connection is closed")
	default:
	}
	recycle := false
	// Keep the connection stable until session cleanup and the watcher finish.
	defer func() {
		if r.conn != nil {
			select {
			case <-r.conn.Done():
				recycle = true
			default:
			}
			if r.budget.exceeded.Load() {
				recycle = true
			}
		}
		if ctx.Err() != nil || recycle {
			r.stopProcess(true)
		}
	}()
	if r.conn != nil {
		select {
		case <-r.conn.Done():
			r.stopProcess(true)
		default:
		}
	}
	if r.conn == nil {
		if err := r.start(ctx, request.OnLaunch); err != nil {
			return agent.Response{}, err
		}
		if err := r.initialize(ctx); err != nil {
			recycle = true
			return agent.Response{}, err
		}
	}
	select {
	case <-r.closed:
		return agent.Response{}, errors.New("ACP connection is closed")
	case <-r.conn.Done():
		recycle = true
		return agent.Response{}, errors.New("ACP connection was lost")
	default:
	}
	if !r.closeSessions {
		// Without session/close, release the entire process after this turn.
		// The next Run starts a fresh process rather than accumulating sessions.
		recycle = true
	}
	protocolLimit := int64(minProtocolBytes)
	if limit > protocolLimit/4 {
		if limit > (1<<63-1)/4 {
			protocolLimit = 1<<63 - 1
		} else {
			protocolLimit = limit * 4
		}
	}
	r.budget.exceeded.Store(false)
	r.budget.remaining.Store(protocolLimit)

	runCtx, cancel := context.WithCancelCause(ctx)
	finished := make(chan struct{})
	sessionReady := make(chan sdk.SessionId, 1)
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		r.watch(runCtx, finished, sessionReady)
	}()
	defer func() {
		close(finished)
		<-watchDone
		if runCtx.Err() != nil {
			recycle = true
		}
		cancel(nil)
		if ctx.Err() != nil && runErr == nil {
			response = agent.Response{}
			runErr = ctx.Err()
		}
	}()

	session, err := r.conn.NewSession(runCtx, sdk.NewSessionRequest{
		Cwd: r.cwd, McpServers: []sdk.McpServer{},
	})
	if err != nil {
		recycle = true
		return agent.Response{}, r.rpcError(ctx, runCtx, "create ACP session")
	}
	if session.SessionId == "" {
		recycle = true
		return agent.Response{}, errors.New("ACP agent returned an empty session ID")
	}
	sessionReady <- session.SessionId
	response.Model = currentModel(session.ConfigOptions)
	if r.closeSessions {
		defer func() {
			// A cancelled connection is being torn down by watch; there is no
			// safe way to wait for a session/close response on it.
			if runCtx.Err() == nil {
				if _, closeErr := r.conn.CloseSession(runCtx, sdk.CloseSessionRequest{SessionId: session.SessionId}); closeErr != nil {
					recycle = true
					if runErr == nil {
						response = agent.Response{}
						runErr = r.rpcError(ctx, runCtx, "close ACP session")
					}
				}
			}
		}()
	}

	if r.model != "" {
		id, choice, ok := modelOption(session.ConfigOptions, r.model)
		if !ok {
			return agent.Response{}, errors.New("ACP agent does not offer the requested model")
		}
		_, err = r.conn.SetSessionConfigOption(runCtx, sdk.SetSessionConfigOptionRequest{
			ValueId: &sdk.SetSessionConfigOptionValueId{
				SessionId: session.SessionId, ConfigId: id, Value: choice.Value,
			},
		})
		if err != nil {
			return agent.Response{}, r.rpcError(ctx, runCtx, "select ACP model")
		}
		response.Model = &agent.Model{
			ID: string(choice.Value), Name: choice.Name, Source: agent.ModelSourceExplicit,
		}
	}

	answer := &answer{session: session.SessionId, limit: limit, cancel: cancel}
	r.client.setAnswer(answer)
	defer r.client.setAnswer(nil)

	result, err := r.conn.Prompt(runCtx, sdk.PromptRequest{
		SessionId: session.SessionId, Prompt: []sdk.ContentBlock{sdk.TextBlock(request.Prompt)},
	})
	text, chunks, answerErr := r.client.result(answer)
	if answerErr != nil {
		return agent.Response{}, answerErr
	}
	if err != nil {
		return agent.Response{}, r.rpcError(ctx, runCtx, "prompt ACP agent")
	}
	if err := ctx.Err(); err != nil {
		return agent.Response{}, err
	}
	if result.StopReason != sdk.StopReasonEndTurn {
		return agent.Response{}, errors.New("ACP agent did not complete the turn")
	}
	if chunks == 0 || text == "" {
		return agent.Response{}, fmt.Errorf("ACP: %w", agent.ErrNoTextResponse)
	}
	response.Text = text
	if result.Usage != nil {
		response.Usage = &agent.Usage{
			InputTokens: int64(result.Usage.InputTokens), OutputTokens: int64(result.Usage.OutputTokens),
		}
		if result.Usage.CachedReadTokens != nil {
			response.Usage.CachedInputTokens = int64(*result.Usage.CachedReadTokens)
		}
	}
	if len(request.NativeSchema) > 0 {
		response.Warnings = []string{"ACP does not support native output schemas; validate the response locally"}
	}
	return response, nil
}

func modelOption(
	options []sdk.SessionConfigOption, model string,
) (sdk.SessionConfigId, sdk.SessionConfigSelectOption, bool) {
	for _, option := range options {
		selectOption := option.Select
		if selectOption == nil {
			continue
		}
		isModel := selectOption.Category != nil && *selectOption.Category == sdk.SessionConfigOptionCategoryModel
		if !isModel && selectOption.Id != "model" {
			continue
		}
		if selectOption.Options.Ungrouped != nil {
			if choice, ok := modelChoice(*selectOption.Options.Ungrouped, model); ok {
				return selectOption.Id, choice, true
			}
		}
		if selectOption.Options.Grouped != nil {
			for _, group := range *selectOption.Options.Grouped {
				if choice, ok := modelChoice(group.Options, model); ok {
					return selectOption.Id, choice, true
				}
			}
		}
	}
	return "", sdk.SessionConfigSelectOption{}, false
}

func modelChoice(choices []sdk.SessionConfigSelectOption, model string) (sdk.SessionConfigSelectOption, bool) {
	for _, choice := range choices {
		if string(choice.Value) == model {
			return choice, true
		}
	}
	return sdk.SessionConfigSelectOption{}, false
}

func currentModel(options []sdk.SessionConfigOption) *agent.Model {
	for _, option := range options {
		selectOption := option.Select
		if selectOption == nil {
			continue
		}
		isModel := selectOption.Category != nil && *selectOption.Category == sdk.SessionConfigOptionCategoryModel
		if !isModel && selectOption.Id != "model" {
			continue
		}
		current := string(selectOption.CurrentValue)
		if selectOption.Options.Ungrouped != nil {
			if choice, ok := modelChoice(*selectOption.Options.Ungrouped, current); ok {
				return &agent.Model{
					ID: current, Name: choice.Name, Source: agent.ModelSourceSessionConfig,
				}
			}
		}
		if selectOption.Options.Grouped != nil {
			for _, group := range *selectOption.Options.Grouped {
				if choice, ok := modelChoice(group.Options, current); ok {
					return &agent.Model{
						ID: current, Name: choice.Name, Source: agent.ModelSourceSessionConfig,
					}
				}
			}
		}
		if current != "" {
			return &agent.Model{ID: current, Source: agent.ModelSourceSessionConfig}
		}
	}
	return nil
}

func (r *runner) rpcError(ctx, runCtx context.Context, operation string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := context.Cause(runCtx); err != nil {
		return err
	}
	if r.budget.exceeded.Load() {
		return errors.New("ACP protocol byte limit exceeded")
	}
	select {
	case <-r.closed:
		return errors.New("ACP connection is closed")
	case <-r.conn.Done():
		return errors.New("ACP connection was lost")
	default:
	}
	return errors.New(operation + ": failed")
}

func (r *runner) watch(ctx context.Context, finished <-chan struct{}, sessionReady <-chan sdk.SessionId) {
	if ctx.Err() == nil {
		select {
		case <-finished:
		case <-ctx.Done():
		}
	}
	if ctx.Err() == nil {
		return
	}
	if sessionReady != nil {
		select {
		case id := <-sessionReady:
			sent := make(chan struct{})
			conn := r.conn
			go func() {
				conn.Cancel(context.Background(), sdk.CancelNotification{SessionId: id})
				close(sent)
			}()
			select {
			case <-sent:
			case <-time.After(cancelGracePeriod):
			}
			// session/cancel is a notification (no acknowledgement). Give the
			// peer a short chance to process it before terminating the process.
			time.Sleep(cancelGracePeriod)
		default:
		}
	}
	r.stopProcess(false)
}

// Close terminates and reaps the agent even if it is stuck on an ACP request.
func (r *runner) Close() error {
	r.once.Do(func() {
		close(r.closed)
		r.stopProcess(false)
	})
	return nil
}

func (r *runner) stopProcess(recycle bool) {
	r.procMu.Lock()
	defer r.procMu.Unlock()
	if r.cmd != nil && !r.waited {
		r.stdin.Close()
		r.stdout.Close()
		process.Terminate(r.cmd)
		r.cmd.Wait()
		r.waited = true
	}
	if recycle {
		select {
		case <-r.closed:
			return
		default:
		}
		r.cmd, r.stdin, r.stdout = nil, nil, nil
		r.conn, r.client, r.budget = nil, nil, nil
		r.closeSessions = false
	}
}

type budgetReader struct {
	reader    io.Reader
	ready     chan struct{}
	remaining atomic.Int64
	exceeded  atomic.Bool
}

func (b *budgetReader) Read(p []byte) (int, error) {
	if b.ready != nil {
		<-b.ready
	}
	remaining := b.remaining.Load()
	if remaining <= 0 {
		b.exceeded.Store(true)
		return 0, errors.New("ACP protocol byte limit exceeded")
	}
	if int64(len(p)) > remaining {
		p = p[:int(remaining)]
	}
	n, err := b.reader.Read(p)
	b.remaining.Add(-int64(n))
	return n, err
}

type client struct {
	mu     sync.Mutex
	answer *answer
	filter func(string) (bool, error)
}

type answer struct {
	session   sdk.SessionId
	limit     int64
	cancel    context.CancelCauseFunc
	text      strings.Builder
	messageID string
	hasID     bool
	afterTool bool
	chunks    int
	events    int
	err       error
}

func (c *client) setAnswer(a *answer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.answer = a
}

func (c *client) result(a *answer) (string, int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return a.text.String(), a.chunks, a.err
}

func (c *client) SessionUpdate(_ context.Context, n sdk.SessionNotification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.answer
	if a == nil || a.err != nil {
		return nil
	}
	a.events++
	if a.events > maxSessionUpdates {
		a.err = errors.New("ACP session update limit exceeded")
		a.cancel(a.err)
		return nil
	}
	if n.SessionId != a.session {
		return nil
	}
	chunk := n.Update.AgentMessageChunk
	if chunk == nil {
		if n.Update.ToolCall != nil || n.Update.ToolCallUpdate != nil {
			a.afterTool = true
		}
		return nil
	}
	if chunk.Content.Text == nil {
		a.err = errors.New("ACP agent returned a non-text message")
		a.cancel(a.err)
		return nil
	}
	text := chunk.Content.Text.Text
	if a.chunks == 0 && c.filter != nil {
		skip, err := c.filter(text)
		if err != nil {
			a.err = err
			a.cancel(err)
			return nil
		}
		if skip {
			return nil
		}
	}
	hasID := chunk.MessageId != nil
	if a.chunks > 0 && (a.afterTool || hasID != a.hasID || (hasID && *chunk.MessageId != a.messageID)) {
		a.text.Reset()
		a.chunks = 0
	}
	a.afterTool = false
	a.hasID = hasID
	if hasID {
		a.messageID = *chunk.MessageId
	}
	if int64(len(text)) > a.limit-int64(a.text.Len()) {
		a.err = errors.New("ACP response byte limit exceeded")
		a.cancel(a.err)
		return nil
	}
	a.chunks++
	a.text.WriteString(text)
	return nil
}

func (*client) RequestPermission(context.Context, sdk.RequestPermissionRequest) (sdk.RequestPermissionResponse, error) {
	return sdk.RequestPermissionResponse{Outcome: sdk.NewRequestPermissionOutcomeCancelled()}, nil
}

func (*client) ReadTextFile(context.Context, sdk.ReadTextFileRequest) (sdk.ReadTextFileResponse, error) {
	return sdk.ReadTextFileResponse{}, errors.New("ACP client filesystem access is disabled")
}

func (*client) WriteTextFile(context.Context, sdk.WriteTextFileRequest) (sdk.WriteTextFileResponse, error) {
	return sdk.WriteTextFileResponse{}, errors.New("ACP client filesystem access is disabled")
}

func (*client) CreateTerminal(context.Context, sdk.CreateTerminalRequest) (sdk.CreateTerminalResponse, error) {
	return sdk.CreateTerminalResponse{}, errors.New("ACP client terminal access is disabled")
}

func (*client) KillTerminal(context.Context, sdk.KillTerminalRequest) (sdk.KillTerminalResponse, error) {
	return sdk.KillTerminalResponse{}, errors.New("ACP client terminal access is disabled")
}

func (*client) TerminalOutput(context.Context, sdk.TerminalOutputRequest) (sdk.TerminalOutputResponse, error) {
	return sdk.TerminalOutputResponse{}, errors.New("ACP client terminal access is disabled")
}

func (*client) ReleaseTerminal(context.Context, sdk.ReleaseTerminalRequest) (sdk.ReleaseTerminalResponse, error) {
	return sdk.ReleaseTerminalResponse{}, errors.New("ACP client terminal access is disabled")
}

func (*client) WaitForTerminalExit(context.Context, sdk.WaitForTerminalExitRequest) (sdk.WaitForTerminalExitResponse, error) {
	return sdk.WaitForTerminalExitResponse{}, errors.New("ACP client terminal access is disabled")
}
