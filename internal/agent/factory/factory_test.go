package factory

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Songmu/sagepipe/internal/agent"
	"github.com/Songmu/sagepipe/internal/config"
)

func TestSplitAllowedTools(t *testing.T) {
	for _, tt := range []struct {
		name, input string
		want        []string
		wantErr     bool
	}{
		{"tool with spaces", "Read Bash(git status:*) Grep", []string{"Read", "Bash(git status:*)", "Grep"}, false},
		{"unclosed pattern", "Bash(git status:*", nil, true},
		{"unexpected closing parenthesis", "Read )", nil, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := splitAllowedTools(tt.input)
			if (err != nil) != tt.wantErr || (!tt.wantErr && !slices.Equal(got, tt.want)) {
				t.Fatalf("splitAllowedTools(%q) = (%q, %v), want (%q, error=%t)",
					tt.input, got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestEventLimit(t *testing.T) {
	for _, tt := range []struct {
		name    string
		value   int64
		minimum int64
		wantErr bool
	}{
		{"valid limit", 8388608, 8388608 * 6, false},
		{"zero", 0, 0, true},
		{"negative", -1, 0, true},
		{"overflow", 1<<63 - 1, 0, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			limit, err := eventLimit(tt.value)
			if (err != nil) != tt.wantErr || (!tt.wantErr && limit < tt.minimum) {
				t.Fatalf("eventLimit(%d) = (%d, %v), want at least %d, error=%t",
					tt.value, limit, err, tt.minimum, tt.wantErr)
			}
		})
	}
}

func TestMissingProgramFailsBeforeReadingInput(t *testing.T) {
	if err := requireProgram("sagepipe-nonexistent-command-344422"); err == nil ||
		!strings.Contains(err.Error(), "not available") {
		t.Fatalf("missing command returned %v", err)
	}
}

func TestUnsupportedToolsFailBeforeStartingAgent(t *testing.T) {
	cfg := config.Config{
		Agent:            config.AgentConfig{Provider: "codex", Protocol: "cli", CWD: t.TempDir()},
		AllowedTools:     "Read",
		MaxResponseBytes: 8388608,
	}
	if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "does not support allowed-tools") {
		t.Fatalf("unsupported tools returned %v", err)
	}
}

func TestConcurrentACPHasIndependentRunners(t *testing.T) {
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Agent:       config.AgentConfig{Protocol: "acp", Command: program, CWD: t.TempDir()},
		Concurrency: 3,
	}
	runner, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	pool, ok := runner.(*runnerPool)
	if !ok || len(pool.runners) != 3 ||
		pool.runners[0] == pool.runners[1] || pool.runners[1] == pool.runners[2] {
		t.Fatalf("expected three independent ACP runners, got %T", runner)
	}
	if err := runner.Close(); err != nil {
		t.Fatal(err)
	}
}

type poolTestRunner struct {
	run    func(context.Context) (agent.Response, error)
	closed int
	err    error
}

func (r *poolTestRunner) Run(ctx context.Context, _ agent.Request) (agent.Response, error) {
	return r.run(ctx)
}

func (r *poolTestRunner) Close() error {
	r.closed++
	return r.err
}

func TestRunnerPoolAcquisitionAndCleanup(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	first := &poolTestRunner{run: func(ctx context.Context) (agent.Response, error) {
		close(started)
		select {
		case <-release:
			return agent.Response{Text: "first"}, nil
		case <-ctx.Done():
			return agent.Response{}, ctx.Err()
		}
	}, err: errors.New("close first")}
	second := &poolTestRunner{run: func(context.Context) (agent.Response, error) {
		return agent.Response{Text: "second"}, nil
	}}
	pool := &runnerPool{
		runners:   []agent.Runner{first, second},
		available: make(chan agent.Runner, 2),
	}
	pool.available <- first
	pool.available <- second
	done := make(chan error, 1)
	go func() {
		_, err := pool.Run(context.Background(), agent.Request{})
		done <- err
	}()
	<-started
	response, err := pool.Run(context.Background(), agent.Request{})
	if err != nil || response.Text != "second" {
		t.Fatalf("second request = (%q, %v)", response.Text, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := pool.Run(ctx, agent.Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled request: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := pool.Close(); err == nil || err.Error() != "close first" ||
		first.closed != 1 || second.closed != 1 {
		t.Fatalf("close error=%v, counts=%d,%d", err, first.closed, second.closed)
	}
}
