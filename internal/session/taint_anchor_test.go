package session

import (
	"testing"
)

func taintCall(id, name, args string) Message {
	tc := ToolCall{ID: id, Type: "function"}
	tc.Function.Name = name
	tc.Function.Arguments = args
	return Message{Role: "assistant", ToolCalls: []ToolCall{tc}}
}

func anchorBase() []Message {
	return []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
		taintCall("p1", "patch", `{"path":"a.go"}`),
		{Role: "tool", ToolCallID: "p1", Content: "ok"},
		taintCall("p2", "patch", `{"path":"b.go"}`),
		{Role: "tool", ToolCallID: "p2", Content: "ok"},
	}
}

func browserRewrite() []Message {
	msgs := anchorBase()
	msgs[2] = taintCall("b1", "browser", `{"url":"http://e.example"}`)
	msgs[3] = Message{Role: "tool", ToolCallID: "b1", Content: "<untrusted_content_ab source=\"tool:browser\">\npage\n</untrusted_content_ab>"}
	return msgs
}

// Per-step saves scan only the messages appended since the last committed
// save, whether the caller hands back the unredacted live history or the
// struct the store just redacted; a rewritten earlier message, another
// writer or a fresh store rescans everything.
func TestTaintScanAnchorScansOnlyNewMessages(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	live := anchorBase()
	live[3].Content = "token sk-live-1234567890abcdef1234567890abcdef"
	sess, err := store.Create(CloneMessages(live), "m", "task")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Messages[3].Content == live[3].Content {
		t.Fatal("fixture: secret was not redacted")
	}

	// Live (unredacted) history plus one new message.
	live = append(live, Message{Role: "assistant", Content: "a"})
	sess.Messages = CloneMessages(live)
	before := store.taintScannedMsgs
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	if got := store.taintScannedMsgs - before; got != 1 {
		t.Fatalf("live snapshot scanned %d messages, want 1", got)
	}

	// The redacted struct the store returned plus one new message.
	sess.Messages = append(sess.Messages, Message{Role: "user", Content: "b"})
	before = store.taintScannedMsgs
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	if got := store.taintScannedMsgs - before; got != 1 {
		t.Fatalf("written snapshot scanned %d messages, want 1", got)
	}

	// Rewriting an early message invalidates the anchor.
	sess.Messages[1].Content = "changed"
	before = store.taintScannedMsgs
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	if got := store.taintScannedMsgs - before; got != len(sess.Messages) {
		t.Fatalf("rewritten snapshot scanned %d messages, want %d", got, len(sess.Messages))
	}

	// Another writer moves the revision: this store's memo no longer applies.
	other, err := NewStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := other.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Save(theirs); err != nil {
		t.Fatal(err)
	}
	mine, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	before = store.taintScannedMsgs
	if err := store.Save(mine); err != nil {
		t.Fatal(err)
	}
	if got := store.taintScannedMsgs - before; got != len(mine.Messages) {
		t.Fatalf("save after a foreign write scanned %d messages, want %d", got, len(mine.Messages))
	}
}

// A failed save leaves no memo, so a retry from a fresh struct rescans.
func TestTaintScanAnchorOnlyAfterCommit(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create(anchorBase(), "m", "task")
	if err != nil {
		t.Fatal(err)
	}
	stale := *sess
	stale.Messages = CloneMessages(sess.Messages)
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	stale.Messages = browserRewrite()
	if err := store.Save(&stale); err == nil {
		t.Fatal("fixture: stale save should conflict")
	}
	fresh, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	fresh.Messages = browserRewrite()
	if err := store.Save(fresh); err != nil {
		t.Fatal(err)
	}
	if !fresh.EpisodeUntrusted || !fresh.UntrustedIngested {
		t.Fatalf("retry lost taint: episode=%v ingested=%v", fresh.EpisodeUntrusted, fresh.UntrustedIngested)
	}
}

// A snapshot that rewrites messages below the redaction boundary while
// keeping the boundary's anchor message (an in-loop persist after context
// trimming re-grew the history) must still be scanned for both taint flags.
func TestRED_TaintScanIgnoresStaleRedactBoundary(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create(anchorBase(), "m", "task")
	if err != nil {
		t.Fatal(err)
	}
	if sess.RedactBoundary != 6 || sess.EpisodeUntrusted || sess.UntrustedIngested {
		t.Fatalf("fixture: boundary=%d episode=%v ingested=%v", sess.RedactBoundary, sess.EpisodeUntrusted, sess.UntrustedIngested)
	}
	sess.Messages = browserRewrite()
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	if !sess.EpisodeUntrusted {
		t.Fatal("episode taint missed below a stale redaction boundary")
	}
	if !sess.UntrustedIngested {
		t.Fatal("ingest taint missed below a stale redaction boundary")
	}
}
