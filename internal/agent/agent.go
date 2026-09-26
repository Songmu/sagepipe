package agent

import (
	"context"
	"strings"
)

// Request is one independent agent invocation. Prompt contains all fallback
// format instructions; NativeSchema is only an optional generation hint.
// Callers must always validate the final answer themselves.
type Request struct {
	Prompt           string
	NativeSchema     []byte
	MaxResponseBytes int64
	OnLaunch         func(Launch)
}

// Launch describes an agent subprocess immediately before it is started.
// Arguments containing prompts, schemas, tool rules, or credential-like values
// are redacted before the launch is exposed to callers.
type Launch struct {
	command string
	args    []string
	cwd     string
}

// NewLaunch returns launch metadata safe for diagnostics.
func NewLaunch(command string, args []string, cwd string) Launch {
	return Launch{command: command, args: redactLaunchArgs(args), cwd: cwd}
}

func (l Launch) Command() string { return l.command }
func (l Launch) Args() []string  { return append([]string(nil), l.args...) }
func (l Launch) CWD() string     { return l.cwd }

func redactLaunchArgs(args []string) []string {
	safe := append([]string(nil), args...)
	for i := 0; i < len(safe); i++ {
		arg := safe[i]
		name, value, inline := strings.Cut(arg, "=")
		if !strings.HasPrefix(name, "-") {
			if sensitiveLaunchValue(arg) {
				safe[i] = "<redacted>"
			}
			continue
		}
		if launchArgList(name) {
			if inline {
				safe[i] = name + "=<redacted>"
				continue
			}
			for i+1 < len(safe) && !strings.HasPrefix(safe[i+1], "-") {
				i++
				safe[i] = "<redacted>"
			}
			continue
		}
		if sensitiveLaunchArg(name) {
			if inline {
				safe[i] = name + "=<redacted>"
			} else if i+1 < len(safe) {
				safe[i+1] = "<redacted>"
				i++
			}
			continue
		}
		if inline && sensitiveLaunchValue(value) {
			safe[i] = name + "=<redacted>"
		}
	}
	return safe
}

func launchArgList(name string) bool {
	return name == "--allowedTools" || name == "--allow-tool" || name == "--available-tools"
}

func sensitiveLaunchArg(name string) bool {
	switch name {
	case "-p", "--prompt", "--json-schema", "--additional-mcp-config":
		return true
	}
	return sensitiveLaunchValue(name)
}

func sensitiveLaunchValue(value string) bool {
	normalized := strings.NewReplacer("-", "", "_", "").Replace(strings.ToLower(value))
	for _, marker := range []string{"api", "auth", "credential", "key", "pass", "secret", "token"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

// Usage contains usage figures when the selected agent reports them.
type Usage struct {
	InputTokens       int64
	OutputTokens      int64
	CachedInputTokens int64
}

// Response contains only the agent's final answer, never protocol events.
type Response struct {
	Text     string
	Usage    *Usage
	Warnings []string
}

// Runner executes requests without sharing conversation history between them.
type Runner interface {
	Run(context.Context, Request) (Response, error)
	Close() error
}
