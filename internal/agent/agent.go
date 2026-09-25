package agent

import "context"

// Request is one independent agent invocation. Prompt contains all fallback
// format instructions; NativeSchema is only an optional generation hint.
// Callers must always validate the final answer themselves.
type Request struct {
	Prompt           string
	NativeSchema     []byte
	MaxResponseBytes int64
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
