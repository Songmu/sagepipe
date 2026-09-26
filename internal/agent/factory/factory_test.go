package factory

import (
	"slices"
	"strings"
	"testing"

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
