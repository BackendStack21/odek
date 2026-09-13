package mcpclient

import (
	"context"
	"fmt"
	"sync"
)

// RequestError describes whether a transport failure may have left a remote
// operation running. An unknown outcome must not be retried as a fresh mutation
// without checking the remote state or using an idempotency key.
type RequestError struct {
	Err            error
	OutcomeUnknown bool
}

func (e *RequestError) Error() string {
	if e.OutcomeUnknown {
		return fmt.Sprintf("MCP request outcome unknown after dispatch; verify remote state before retrying: %v", e.Err)
	}
	return fmt.Sprintf("MCP request not dispatched: %v", e.Err)
}

func (e *RequestError) Unwrap() error { return e.Err }

// queuedRequest owns the transition from cancellable queued work to a write.
// Cancellation and dispatch share a lock so a caller cannot report an
// undispatched cancellation while the writer is starting that request.
type queuedRequest struct {
	ctx        context.Context
	data       []byte
	mu         sync.Mutex
	dispatched bool
	cancelled  bool
}

func (r *queuedRequest) begin() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancelled || r.ctx.Err() != nil {
		return false
	}
	r.dispatched = true
	return true
}

func (r *queuedRequest) fail(err error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancelled = true
	return &RequestError{Err: err, OutcomeUnknown: r.dispatched}
}
