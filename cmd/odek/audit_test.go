package main

import (
	"strings"
	"sync"
	"testing"
)

// TestSetIngestRecorder_RecordsOnWrap verifies that wrapUntrusted
// invokes the active recorder. This is the wire that takes raw
// ingest events from deep inside tool implementations and routes them
// to whatever the loop has set as the recorder (typically an
// AuditStore).
func TestSetIngestRecorder_RecordsOnWrap(t *testing.T) {
	t.Cleanup(func() { setIngestRecorder(nil) })

	var (
		mu       sync.Mutex
		captured []struct{ source, content string }
	)
	setIngestRecorder(func(source, content string) {
		mu.Lock()
		captured = append(captured, struct{ source, content string }{source, content})
		mu.Unlock()
	})

	wrapUntrusted("https://example.com/a", "hello world")
	wrapUntrusted("/etc/hosts", "127.0.0.1 localhost")

	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 2 {
		t.Fatalf("captured %d events, want 2", len(captured))
	}
	if captured[0].source != "https://example.com/a" || !strings.Contains(captured[0].content, "hello") {
		t.Errorf("event 0 wrong: %+v", captured[0])
	}
	if captured[1].source != "/etc/hosts" {
		t.Errorf("event 1 wrong: %+v", captured[1])
	}
}

// TestSetIngestRecorder_NilRecorderIsNoop confirms the recorder can be
// cleared and that wrapUntrusted continues to function without one.
func TestSetIngestRecorder_NilRecorderIsNoop(t *testing.T) {
	setIngestRecorder(nil)
	wrapped := wrapUntrusted("x", "body")
	if !hasUntrustedWrapper(wrapped) {
		t.Errorf("wrapping still required when no recorder is set")
	}
}

// TestSetIngestRecorder_EmptyContentSkipsRecording — wrapUntrusted
// bypasses wrapping for empty input, so we also bypass the recorder.
// The intent: empty tool output is not "ingested content" worth
// auditing.
func TestSetIngestRecorder_EmptyContentSkipsRecording(t *testing.T) {
	t.Cleanup(func() { setIngestRecorder(nil) })
	called := false
	setIngestRecorder(func(source, content string) { called = true })
	wrapUntrusted("x", "")
	if called {
		t.Error("recorder fired for empty content")
	}
}
