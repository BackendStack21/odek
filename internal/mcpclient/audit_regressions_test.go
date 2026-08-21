package mcpclient

// Regression test for the 2026-08 security audit: call() wrote the request
// to the server's stdin inline while holding c.mu, with no deadline. A
// server that answers initialize/tools/list and then stops reading stdin
// fills the pipe buffer, and every subsequent call() blocks at the mutex —
// before the ctx/select that is supposed to bound it — so neither
// timeout_seconds nor caller cancellation can ever engage. Long-lived
// clients (odek serve / telegram share one Client per server for the
// process lifetime) wedge permanently.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func newStuckPipeClient(timeout time.Duration) (*Client, func()) {
	// stdout: the server never sends anything.
	clientRead, serverWrite := io.Pipe()
	// stdin: an unbuffered pipe nobody ever reads — the first Write blocks.
	serverRead, clientWrite := io.Pipe()
	_ = serverRead
	_ = serverWrite

	c := &Client{
		name:      "stuck",
		stdin:     clientWrite,
		stdout:    bufio.NewReader(clientRead),
		lineCh:    make(chan lineResult, 10),
		done:      make(chan struct{}),
		writeCh:   make(chan []byte, 2),
		writeDone: make(chan struct{}),
		closed:    make(chan struct{}),
		pending:   make(map[int]chan callResponse),
		timeout:   timeout,
	}
	go c.readLoop()
	go c.writeLoop()
	cleanup := func() {
		c.closeOnce.Do(func() { close(c.closed) })
		clientWrite.Close()
		clientRead.Close()
	}
	return c, cleanup
}

func TestAudit_CallBoundedWhenServerStopsReading(t *testing.T) {
	c, cleanup := newStuckPipeClient(150 * time.Millisecond)
	defer cleanup()

	bigParams := json.RawMessage(`"` + strings.Repeat("a", 128*1024) + `"`)
	start := time.Now()
	_, err := c.call(context.Background(), "tools/call", bigParams)
	if err == nil {
		t.Fatal("expected an error from a server that never responds")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("call took %v; the per-server timeout must bound a stuck writer (audit: inline write held c.mu forever)", elapsed)
	}

	// A second call after the write path is wedged must also stay bounded.
	start = time.Now()
	_, err = c.call(context.Background(), "tools/call", bigParams)
	if err == nil {
		t.Fatal("expected an error from a server that never responds")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("second call took %v; callers behind a full pipe must still be ctx-bounded", elapsed)
	}
}

// TestAudit_WriteErrorStickyFailsFast pins the sticky writeErr fast path:
// once the writer goroutine has failed (dead stdin), later callers must get
// an immediate "write:" error instead of waiting out the full per-server
// timeout against a connection that can never respond.
func TestAudit_WriteErrorStickyFailsFast(t *testing.T) {
	c, cleanup := newStuckPipeClient(10 * time.Second)
	defer cleanup()

	// Kill the writer by closing its stdin out from under it.
	c.mu.Lock()
	c.writeErr = fmt.Errorf("test: connection dead")
	c.mu.Unlock()

	start := time.Now()
	_, err := c.call(context.Background(), "tools/call", json.RawMessage(`"x"`))
	if err == nil || !strings.Contains(err.Error(), "write:") {
		t.Fatalf("call after writer failure = %v, want immediate write error", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("call took %v; sticky writeErr must fail fast, not after the 10s timeout", elapsed)
	}
}
