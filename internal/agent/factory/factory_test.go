package factory

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Songmu/sagepipe/internal/config"
)

func TestSplitAllowedTools(t *testing.T) {
	got, err := splitAllowedTools("Read Bash(git status:*) Grep")
	want := []string{"Read", "Bash(git status:*)", "Grep"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("tools=%q err=%v, want %q", got, err, want)
	}
	for _, input := range []string{"Bash(git status:*", "Read )"} {
		if _, err := splitAllowedTools(input); err == nil {
			t.Errorf("expected invalid pattern: %q", input)
		}
	}
}

func TestEventLimit(t *testing.T) {
	limit, err := eventLimit(8388608)
	if err != nil || limit < 8388608*6 {
		t.Fatalf("event limit=%d err=%v", limit, err)
	}
	for _, value := range []int64{0, -1, 1<<63 - 1} {
		if _, err := eventLimit(value); err == nil {
			t.Errorf("expected error for response limit %d", value)
		}
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
