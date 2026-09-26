package agent

import (
	"context"
	"errors"
)

// ErrNoTextResponse indicates that an agent completed without returning text.
var ErrNoTextResponse = errors.New("agent returned no text response")

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
type Launch struct {
	command string
	args    []string
	cwd     string
}

// NewLaunch copies launch metadata for diagnostics.
func NewLaunch(command string, args []string, cwd string) Launch {
	return Launch{command: command, args: append([]string(nil), args...), cwd: cwd}
}

func (l Launch) Command() string { return l.command }
func (l Launch) Args() []string  { return append([]string(nil), l.args...) }
func (l Launch) CWD() string     { return l.cwd }

// Usage contains usage figures when the selected agent reports them.
type Usage struct {
	InputTokens       int64
	OutputTokens      int64
	CachedInputTokens int64
}

// Model identifies the model selected for an agent response.
type Model struct {
	ID     string
	Name   string
	Source string
}

const (
	ModelSourceExplicit      = "explicit"
	ModelSourceSessionConfig = "session_config"
)

// Response contains only the agent's final answer, never protocol events.
type Response struct {
	Text     string
	Model    *Model
	Usage    *Usage
	Warnings []string
}

// Runner executes requests without sharing conversation history between them.
type Runner interface {
	Run(context.Context, Request) (Response, error)
	Close() error
}
