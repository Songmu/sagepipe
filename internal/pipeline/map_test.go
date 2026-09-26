package pipeline

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Songmu/sagepipe/internal/agent"
)

type parallelRunner struct {
	run    func(context.Context, agent.Request) (agent.Response, error)
	closed atomic.Int32
}

func (r *parallelRunner) Run(ctx context.Context, req agent.Request) (agent.Response, error) {
	return r.run(ctx, req)
}

func (r *parallelRunner) Close() error {
	r.closed.Add(1)
	return nil
}

func TestConcurrentMapOrderedAndBounded(t *testing.T) {
	cfg := testConfig("map")
	cfg.Concurrency = 2
	started := make(chan string, 4)
	release := make(chan struct{})
	runner := &parallelRunner{run: func(ctx context.Context, req agent.Request) (agent.Response, error) {
		for _, value := range []string{"one", "two", "three", "four"} {
			if strings.HasSuffix(req.Prompt, `"`+value+`"`) {
				started <- value
				if value == "one" {
					select {
					case <-release:
					case <-ctx.Done():
						return agent.Response{}, ctx.Err()
					}
				}
				return agent.Response{Text: `{"items":["` + value + `","done"]}`}, nil
			}
		}
		return agent.Response{}, errors.New("unexpected prompt")
	}}
	var out, diag strings.Builder
	done := make(chan int, 1)
	go func() {
		done <- Run(context.Background(), cfg, strings.NewReader("one\ntwo\nthree\nfour\n"), &out, &diag, runner)
	}()
	for range 2 {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatal("two requests did not overlap")
		}
	}
	select {
	case value := <-started:
		close(release)
		t.Fatalf("started %q before the earliest result was emitted", value)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case code := <-done:
		if code != 0 || out.String() != "one\ndone\ntwo\ndone\nthree\ndone\nfour\ndone\n" ||
			runner.closed.Load() != 1 {
			t.Errorf("code=%d output=%q closed=%d diagnostics=%s", code, out.String(), runner.closed.Load(), diag.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent map did not finish")
	}
}

func TestConcurrentMapContinuesAfterBadRecords(t *testing.T) {
	cfg := testConfig("map")
	cfg.Concurrency = 2
	runner := &parallelRunner{run: func(_ context.Context, req agent.Request) (agent.Response, error) {
		switch {
		case strings.Contains(req.Prompt, "bad"):
			return agent.Response{Text: `{"items":["partial",3]}`}, nil
		case strings.Contains(req.Prompt, "error"):
			return agent.Response{}, errors.New("failed")
		default:
			return agent.Response{Text: `{"items":["ok"]}`}, nil
		}
	}}
	var out, diag strings.Builder
	if code := Run(context.Background(), cfg, strings.NewReader("bad\nerror\nok\n"), &out, &diag, runner); code != 1 {
		t.Fatalf("code=%d, diagnostics=%s", code, diag.String())
	}
	if out.String() != "ok\n" || runner.closed.Load() != 1 {
		t.Errorf("output=%q closed=%d", out.String(), runner.closed.Load())
	}
	checkDiagnostic(t, diag.String(), "invalid_response", 1)
	checkDiagnostic(t, diag.String(), "agent_call_failed", 2)
}

func TestConcurrentAutoReduceRunsOnce(t *testing.T) {
	cfg := testConfig("auto")
	cfg.Concurrency = 3
	var calls atomic.Int32
	runner := &parallelRunner{run: func(_ context.Context, req agent.Request) (agent.Response, error) {
		switch calls.Add(1) {
		case 1:
			return agent.Response{Text: `{"mode":"reduce","reason":"aggregate"}`}, nil
		case 2:
			if !strings.HasSuffix(req.Prompt, `["one","two"]`) {
				t.Errorf("unexpected aggregate prompt: %q", req.Prompt)
			}
			return agent.Response{Text: `{"items":["combined"]}`}, nil
		default:
			return agent.Response{}, errors.New("unexpected call")
		}
	}}
	cfg.Prompt = "Combine the records"
	var out, diag strings.Builder
	if code := Run(context.Background(), cfg, strings.NewReader("one\ntwo\n"), &out, &diag, runner); code != 0 ||
		calls.Load() != 2 || out.String() != "combined\n" {
		t.Fatalf("code=%d calls=%d output=%q diagnostics=%s", code, calls.Load(), out.String(), diag.String())
	}
}

func TestConcurrentAutoMapIncludesFirstRecord(t *testing.T) {
	cfg := testConfig("auto")
	cfg.Concurrency = 2
	cfg.Prompt = "Translate each record"
	var calls atomic.Int32
	runner := &parallelRunner{run: func(_ context.Context, req agent.Request) (agent.Response, error) {
		calls.Add(1)
		if strings.HasPrefix(req.Prompt, "Choose whether") {
			return agent.Response{Text: `{"mode":"map","reason":"independent"}`}, nil
		}
		if strings.HasSuffix(req.Prompt, `"one"`) {
			return agent.Response{Text: `{"items":["one"]}`}, nil
		}
		return agent.Response{Text: `{"items":["two"]}`}, nil
	}}
	var out, diag strings.Builder
	if code := Run(context.Background(), cfg, strings.NewReader("one\ntwo\n"), &out, &diag, runner); code != 0 ||
		calls.Load() != 3 || out.String() != "one\ntwo\n" {
		t.Fatalf("code=%d calls=%d output=%q diagnostics=%s", code, calls.Load(), out.String(), diag.String())
	}
}

func TestConcurrentMapCancelsActiveWorkOnOutputFailure(t *testing.T) {
	cfg := testConfig("map")
	cfg.Concurrency = 2
	started := make(chan struct{})
	stopped := make(chan struct{})
	runner := &parallelRunner{run: func(ctx context.Context, req agent.Request) (agent.Response, error) {
		if strings.HasSuffix(req.Prompt, `"two"`) {
			close(started)
			<-ctx.Done()
			close(stopped)
			return agent.Response{}, ctx.Err()
		}
		<-started
		return agent.Response{Text: `{"items":["one"]}`}, nil
	}}
	var diag strings.Builder
	done := make(chan int, 1)
	go func() {
		done <- Run(context.Background(), cfg, strings.NewReader("one\ntwo\n"), brokenWriter{}, &diag, runner)
	}()
	select {
	case code := <-done:
		if code != 2 || runner.closed.Load() != 1 {
			t.Errorf("code=%d closed=%d diagnostics=%s", code, runner.closed.Load(), diag.String())
		}
		select {
		case <-stopped:
		default:
			t.Error("runner closed before active work stopped")
		}
		checkDiagnostic(t, diag.String(), "output_write_failed", 0)
	case <-time.After(2 * time.Second):
		t.Fatal("map did not cancel active work")
	}
}

func TestConcurrentMapInputError(t *testing.T) {
	cfg := testConfig("map")
	cfg.Concurrency = 2
	runner := &parallelRunner{run: func(context.Context, agent.Request) (agent.Response, error) {
		t.Error("agent called after input read failure")
		return agent.Response{}, nil
	}}
	var out, diag strings.Builder
	if code := Run(context.Background(), cfg, brokenReader{}, &out, &diag, runner); code != 2 ||
		runner.closed.Load() != 1 {
		t.Errorf("code=%d closed=%d diagnostics=%s", code, runner.closed.Load(), diag.String())
	}
	checkDiagnostic(t, diag.String(), "input_read_failed", 0)
}

type signalingBrokenReader struct {
	read chan struct{}
}

func (r signalingBrokenReader) Read([]byte) (int, error) {
	close(r.read)
	return 0, io.ErrClosedPipe
}

func TestConcurrentMapDrainsActiveWorkOnReadError(t *testing.T) {
	cfg := testConfig("map")
	cfg.Concurrency = 2
	started := make(chan struct{})
	release := make(chan struct{})
	runner := &parallelRunner{run: func(ctx context.Context, _ agent.Request) (agent.Response, error) {
		close(started)
		select {
		case <-release:
			return agent.Response{Text: `{"items":["first"]}`}, nil
		case <-ctx.Done():
			return agent.Response{}, ctx.Err()
		}
	}}
	var out, diag strings.Builder
	done := make(chan int, 1)
	broken := signalingBrokenReader{read: make(chan struct{})}
	input := io.MultiReader(strings.NewReader("first\n"), broken)
	go func() { done <- Run(context.Background(), cfg, input, &out, &diag, runner) }()
	for _, signal := range []<-chan struct{}{started, broken.read} {
		select {
		case <-signal:
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatal("map did not start work and encounter input failure")
		}
	}
	close(release)
	select {
	case code := <-done:
		if code != 2 || out.String() != "first\n" || runner.closed.Load() != 1 {
			t.Errorf("code=%d output=%q closed=%d diagnostics=%s", code, out.String(), runner.closed.Load(), diag.String())
		}
		checkDiagnostic(t, diag.String(), "input_read_failed", 0)
	case <-time.After(2 * time.Second):
		t.Fatal("map did not drain after read failure")
	}
}

type notifyingWriter struct {
	output strings.Builder
	wrote  chan struct{}
}

func (w *notifyingWriter) Write(p []byte) (int, error) {
	select {
	case w.wrote <- struct{}{}:
	default:
	}
	return w.output.Write(p)
}

func TestConcurrentMapEmitsWhileNextInputIsPending(t *testing.T) {
	cfg := testConfig("map")
	cfg.Concurrency = 2
	input, writer := io.Pipe()
	defer input.Close()
	out := &notifyingWriter{wrote: make(chan struct{}, 1)}
	var diag strings.Builder
	runner := &parallelRunner{run: func(_ context.Context, req agent.Request) (agent.Response, error) {
		if strings.HasSuffix(req.Prompt, `"one"`) {
			return agent.Response{Text: `{"items":["one"]}`}, nil
		}
		return agent.Response{Text: `{"items":["two"]}`}, nil
	}}
	done := make(chan int, 1)
	go func() { done <- Run(context.Background(), cfg, input, out, &diag, runner) }()
	if _, err := io.WriteString(writer, "one\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-out.wrote:
	case <-time.After(2 * time.Second):
		writer.CloseWithError(io.ErrClosedPipe)
		t.Fatal("first output was blocked waiting for more input")
	}
	if _, err := io.WriteString(writer, "two\n"); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	select {
	case code := <-done:
		if code != 0 || out.output.String() != "one\ntwo\n" {
			t.Errorf("code=%d output=%q diagnostics=%s", code, out.output.String(), diag.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("map did not finish")
	}
}

func TestConcurrentMapCancellation(t *testing.T) {
	cfg := testConfig("map")
	cfg.Concurrency = 2
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{}, 2)
	runner := &parallelRunner{run: func(ctx context.Context, _ agent.Request) (agent.Response, error) {
		started <- struct{}{}
		<-ctx.Done()
		return agent.Response{}, ctx.Err()
	}}
	var out, diag strings.Builder
	done := make(chan int, 1)
	go func() { done <- Run(ctx, cfg, strings.NewReader("one\ntwo\n"), &out, &diag, runner) }()
	for range 2 {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			cancel()
			t.Fatal("map did not start both requests")
		}
	}
	cancel()
	select {
	case code := <-done:
		if code != 2 || runner.closed.Load() != 1 {
			t.Errorf("code=%d closed=%d diagnostics=%s", code, runner.closed.Load(), diag.String())
		}
		if !strings.Contains(diag.String(), `"code":"agent_cancelled"`) &&
			!strings.Contains(diag.String(), `"code":"input_cancelled"`) {
			t.Errorf("missing cancellation diagnostic: %s", diag.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("map did not stop after cancellation")
	}
}
