package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/llm"
)

// Tests for the write-path size-cap wiring in saveLocked and for
// trimToFileCapLocked edge cases that do not require multi-MiB fixtures (the
// trimmer is called directly with synthetic oversized payloads).
//
// Unreachable-by-design branches (left uncovered intentionally):
//   - session.go:370-372, 448-450, 458-460 (json.Marshal error returns) and
//     381-383 (the trim-error propagation in saveLocked): every field of
//     Session and llm.Message is a concrete JSON-marshalable type (strings,
//     ints, bools, time.Time, nested structs), so json.Marshal cannot fail
//     for these values; the error branches are dead defensive code and the
//     trim-error branch therefore cannot fire either.

// TestSave_IndexWriteError covers the saveIndexLocked error path in
// saveLocked: the session file write succeeds, but the atomic index rename
// fails because a directory sits at the index path.
func TestSave_IndexWriteError(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStoreWithDir(dir)
	if err != nil {
		t.Fatalf("NewStoreWithDir() error: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "index.json"), 0755); err != nil {
		t.Fatal(err)
	}

	sess := &Session{
		ID:       "20260101-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Messages: []llm.Message{{Role: "user", Content: "hi"}},
	}
	if err := store.Save(sess); err == nil || !strings.Contains(err.Error(), "write index") {
		t.Errorf("Save() error = %v, want a write index error", err)
	}
}

// TestTrimToFileCap_NothingDroppable covers the degenerate break branch: a
// single system message with nothing left to drop is returned as-is.
func TestTrimToFileCap_NothingDroppable(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewStoreWithDir() error: %v", err)
	}
	sess := &Session{
		ID:       "20260101-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Messages: []llm.Message{{Role: "system", Content: "you are odek"}},
	}
	oversized := make([]byte, MaxSessionFileBytes+1)
	data, err := store.trimToFileCapLocked(sess, oversized)
	if err != nil {
		t.Fatalf("trimToFileCapLocked() error: %v", err)
	}
	if len(data) != len(oversized) {
		t.Errorf("len(data) = %d, want %d (nothing droppable — returned as-is)", len(data), len(oversized))
	}
	if len(sess.Messages) != 1 || sess.Messages[0].Role != "system" {
		t.Errorf("system message should have been kept: %+v", sess.Messages)
	}
}

// TestTrimToFileCap_DropsToolGroups exercises the group-sizing pass directly:
// an assistant tool_calls message is dropped together with its tool results,
// and the trim ends once the re-marshaled session fits.
func TestTrimToFileCap_DropsToolGroups(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewStoreWithDir() error: %v", err)
	}
	call := llm.Message{Role: "assistant", Content: "calling tools"}
	call.ToolCalls = append(call.ToolCalls, llm.ToolCall{ID: "c1", Type: "function"})
	sess := &Session{
		ID: "20260101-cccccccccccccccccccccccccccccccc",
		Messages: []llm.Message{
			{Role: "system", Content: "you are odek"},
			call,
			{Role: "tool", Name: "shell", Content: "result 1"},
			{Role: "tool", Name: "shell", Content: "result 2"},
			{Role: "user", Content: "next question"},
		},
	}
	// Synthetic oversized payload: the excess is 1 byte, so one pass drops
	// exactly the first group (the assistant tool_calls message together
	// with its tool results) and the re-marshal fits.
	oversized := make([]byte, MaxSessionFileBytes+1)
	data, err := store.trimToFileCapLocked(sess, oversized)
	if err != nil {
		t.Fatalf("trimToFileCapLocked() error: %v", err)
	}
	if len(data) > MaxSessionFileBytes {
		t.Errorf("len(data) = %d, exceeds cap %d", len(data), MaxSessionFileBytes)
	}
	if len(sess.Messages) != 2 || sess.Messages[0].Role != "system" || sess.Messages[1].Role != "user" {
		t.Errorf("system + trailing user message should survive: %+v", sess.Messages)
	}
	for _, m := range sess.Messages {
		if m.Role == "tool" {
			t.Error("trimmed transcript must not contain orphaned tool messages")
		}
	}
}
