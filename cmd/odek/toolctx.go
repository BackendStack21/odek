package main

import (
	"context"
	"sync"
)

// ctxTool is embedded by tools that support agent-context cancellation. The
// agent loop calls SetContext on any tool implementing it (see internal/loop)
// right before invoking the tool, so cancelling the agent context — Ctrl-C, a
// turn timeout — interrupts the tool's in-flight network request or subprocess
// instead of letting it run to completion (or hang) unsupervised.
//
// The mutex matters: when the LLM emits two calls to the SAME tool in one
// turn, the loop runs them in parallel goroutines and calls SetContext on the
// shared instance from each. Without synchronisation that is a data race on the
// context field even though the value is identical for the turn.
type ctxTool struct {
	mu  sync.Mutex
	ctx context.Context
}

// SetContext records the agent context for the next Call. Safe for concurrent
// use by parallel invocations of the same tool instance.
func (c *ctxTool) SetContext(ctx context.Context) {
	c.mu.Lock()
	c.ctx = ctx
	c.mu.Unlock()
}

// toolCtx returns the recorded agent context, or context.Background() if none
// was set (e.g. tools invoked directly in tests or outside the agent loop).
func (c *ctxTool) toolCtx() context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx == nil {
		return context.Background()
	}
	return c.ctx
}
