package main

import (
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

// ── P0-3: batched tool calls must be correlatable in `odek session show` ─
//
// Parallel tool calls are stored CALL,CALL,…,RESULT,RESULT,… A transcript
// parser that pairs them sequentially attaches another call's output to a
// call — which scored three real compromises as clean in the injection
// study. Both headers now carry a stable call label.

func saveBatchedSession(t *testing.T) *session.Store {
	t.Helper()
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess := &session.Session{
		ID:   session.GenerateID(),
		Task: "batched calls",
		Messages: []session.Message{
			{Role: "user", Content: "run three things"},
			{Role: "assistant", Content: "on it", ToolCalls: []session.ToolCall{
				func() session.ToolCall {
					var tc session.ToolCall
					tc.ID = "call_aaa"
					tc.Type = "function"
					tc.Function.Name = "shell"
					tc.Function.Arguments = `{"command":"echo one"}`
					return tc
				}(),
				func() session.ToolCall {
					var tc session.ToolCall
					tc.ID = "call_bbb"
					tc.Type = "function"
					tc.Function.Name = "write_file"
					tc.Function.Arguments = `{"path":"a.txt","content":"1"}`
					return tc
				}(),
				func() session.ToolCall {
					var tc session.ToolCall
					tc.ID = "" // provider omitted the id — synthetic label path
					tc.Type = "function"
					tc.Function.Name = "tree"
					tc.Function.Arguments = `{}`
					return tc
				}(),
			}},
			// Results deliberately NOT in call order — the whole point.
			{Role: "tool", Name: "write_file", ToolCallID: "call_bbb", Content: "wrote a.txt"},
			{Role: "tool", Name: "tree", ToolCallID: "", Content: "dir listing"},
			{Role: "tool", Name: "shell", ToolCallID: "call_aaa", Content: "one"},
		},
	}
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestShowSession_CallLabelsPairCallsAndResults(t *testing.T) {
	store := saveBatchedSession(t)

	var out string
	out = captureStdout(func() {
		if err := showSession(store, nil); err != nil {
			t.Fatalf("showSession: %v", err)
		}
	})

	// Each TOOL RESULT header must carry the same label as its call.
	if !strings.Contains(out, "[TOOL CALL: shell #call_aaa]") {
		t.Errorf("missing labeled shell call, got:\n%s", out)
	}
	if !strings.Contains(out, "[TOOL RESULT: shell #call_aaa]") {
		t.Errorf("shell result not correlated with #call_aaa, got:\n%s", out)
	}
	if !strings.Contains(out, "[TOOL CALL: write_file #call_bbb]") {
		t.Errorf("missing labeled write_file call, got:\n%s", out)
	}
	if !strings.Contains(out, "[TOOL RESULT: write_file #call_bbb]") {
		t.Errorf("write_file result not correlated with #call_bbb, got:\n%s", out)
	}
	// Provider-omitted ID gets a deterministic synthetic label on both halves.
	if !strings.Contains(out, "[TOOL CALL: tree #m1-c2]") {
		t.Errorf("missing synthetic label for empty-ID call, got:\n%s", out)
	}
	if !strings.Contains(out, "[TOOL RESULT: tree #m1-c2]") {
		t.Errorf("tree result not correlated with synthetic label, got:\n%s", out)
	}
}

func TestShowSession_UnmatchedResultGetsMarker(t *testing.T) {
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess := &session.Session{
		ID:   session.GenerateID(),
		Task: "trimmed",
		Messages: []session.Message{
			{Role: "user", Content: "go"},
			// Assistant turn with the call was trimmed away; only the result remains.
			{Role: "tool", Name: "shell", ToolCallID: "call_gone", Content: "orphan"},
		},
	}
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(func() {
		if err := showSession(store, nil); err != nil {
			t.Fatalf("showSession: %v", err)
		}
	})
	if !strings.Contains(out, "[TOOL RESULT: shell #unmatched]") {
		t.Errorf("orphan result should be marked #unmatched, got:\n%s", out)
	}
}
