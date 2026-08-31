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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/BackendStack21/odek/internal/artifact"
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

// TestAudit_RenderedEnvelopeOutputCapped pins the full-output cap on the
// envelope path: only the envelope's compact text used to pass
// max_result_chars, so a server could park megabytes in the id/summary
// fields that Render inlines afterwards (~45x amplification with a 9 MiB
// id). The full rendered output — text, metadata lines, and the truncation
// notice combined — must fit the cap, with the metadata lines (the compact
// payload) preserved. The two-argument renderCappedEnvelope call is the
// fix; this test does not compile against the pre-fix tree (RED signal).
func TestAudit_RenderedEnvelopeOutputCapped(t *testing.T) {
	c := &Client{name: "cap-srv", maxResultChars: 100000}
	hugeID := strings.Repeat("A", 9<<20)
	env := &artifact.Envelope{
		Schema: artifact.SchemaToolResult,
		Text:   strings.Repeat("t", 300000),
		Artifacts: []artifact.Ref{{
			Schema:    artifact.SchemaArtifactRef,
			ID:        hugeID,
			URI:       "file:///tmp/x.bin",
			MediaType: "text/plain",
			Summary:   strings.Repeat("s", 50000),
		}},
	}

	out := c.renderCappedEnvelope("log_scan", env)
	if n := utf8.RuneCountInString(out); n > 100000 {
		t.Errorf("rendered envelope = %d chars, want <= 100000 (audit: id/summary fields rode past the cap)", n)
	}
	if !strings.Contains(out, "result truncated") {
		t.Errorf("truncation notice missing: %.200q...", out)
	}
	if !strings.Contains(out, `"cap-srv"`) || !strings.Contains(out, `"log_scan"`) {
		t.Errorf("truncation notice must name the server and tool: %.200q...", out)
	}
	if !strings.Contains(out, "\n- artifact ") {
		t.Errorf("artifact metadata line must survive the cap: %.300q...", out)
	}
	if strings.Contains(out, hugeID) {
		t.Errorf("the unbounded id leaked into the model-facing output")
	}
}

// TestAudit_EnvelopeHugeIDTextCappedEndToEnd drives the same guarantee
// through CallTool against the mock extension server, with the server
// inflating both the envelope text and the artifact id via the
// FAKE_ARTIFACT_TEXT_SIZE / FAKE_ARTIFACT_ID_SIZE knobs (mirroring the
// FAKE_ERROR_SIZE idiom). Before the cap, the rendered envelope sailed
// past the configured limit.
func TestAudit_EnvelopeHugeIDTextCappedEndToEnd(t *testing.T) {
	const limit = 5000
	root := t.TempDir()
	path := filepath.Join(root, "report.txt")
	if err := os.WriteFile(path, []byte("report body"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := artifactClientWithLimits(t, ServerConfig{
		ArtifactRoots:  []string{root},
		MaxResultChars: limit,
	}, map[string]string{
		"FAKE_ARTIFACT_PATH":      path,
		"FAKE_ARTIFACT_ID_SIZE":   "50000",
		"FAKE_ARTIFACT_TEXT_SIZE": "20000",
	})

	out, err := client.CallTool(context.Background(), "artifact_result", `{}`)
	if err != nil {
		t.Fatalf("valid oversized envelope must not error: %v", err)
	}
	if n := utf8.RuneCountInString(out); n > limit {
		t.Errorf("rendered envelope = %d chars, want <= %d (audit: metadata fields rode past the cap)", n, limit)
	}
	if !strings.Contains(out, "result truncated") {
		t.Errorf("truncation notice missing: %.200q...", out)
	}
	if !strings.Contains(out, `- artifact "`) {
		t.Errorf("artifact metadata line must survive the cap: %.300q...", out)
	}
	if strings.Contains(out, strings.Repeat("i", 5000)) {
		t.Errorf("huge id field landed in the model-facing output unbounded")
	}
	if strings.Contains(out, strings.Repeat("t", 5000)) {
		t.Errorf("huge envelope text landed in the model-facing output unbounded")
	}
	if strings.Contains(out, path) {
		t.Errorf("rendered envelope leaks the absolute artifact path: %.300q...", out)
	}
}
