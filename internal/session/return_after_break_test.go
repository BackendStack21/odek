package session

import (
	"strings"
	"testing"
)

// The summary is run-only presentation: saving a session never persists it,
// so resumes cannot accumulate copies, and it is never indexed as text.
func TestRED_ReturnAfterBreak_NeverPersistedOrIndexed(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	msgs := []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Name: ReturnAfterBreakName, Content: "where you left off SUMMARYTEXT"},
		{Role: "user", Content: "next"},
	}
	sess, err := store.Create(msgs, "m", "task")
	if err != nil {
		t.Fatal(err)
	}
	for _, save := range []func(*Session) error{store.Save, store.SaveNoIndex} {
		sess.Messages = append(sess.Messages, Message{Role: "user", Name: ReturnAfterBreakName, Content: "SUMMARYTEXT again"})
		if err := save(sess); err != nil {
			t.Fatal(err)
		}
		loaded, err := store.Load(sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range loaded.Messages {
			if m.Name == ReturnAfterBreakName || strings.Contains(m.Content, "SUMMARYTEXT") {
				t.Fatalf("return-after-break summary persisted: %+v", m)
			}
		}
		if loaded.Turns != 2 {
			t.Fatalf("turns = %d, want 2", loaded.Turns)
		}
	}
	if text := BuildConversationText(msgs); strings.Contains(text, "SUMMARYTEXT") {
		t.Errorf("summary indexed as conversation text: %q", text)
	}
}

// The return-after-break summary is never a turn and never the protected
// original task.
func TestRED_ReturnAfterBreak_NotATurnNorTheTask(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Name: ReturnAfterBreakName, Content: "where you left off"},
		{Role: "user", Content: "task"},
		{Role: "assistant", Content: "ok"},
	}
	if got := protectedHeadLen(msgs); got != 3 {
		t.Errorf("protectedHeadLen = %d, want 3 (through the real task)", got)
	}
	if got := countUserTurns(msgs); got != 1 {
		t.Errorf("countUserTurns = %d, want 1", got)
	}
	for name, want := range map[string]bool{"bg-notice": true, "bg-wake": true, ReturnAfterBreakName: true, "": false, "alice": false} {
		if IsSyntheticUserName(name) != want {
			t.Errorf("IsSyntheticUserName(%q) != %v", name, want)
		}
	}
	for name, want := range map[string]bool{"bg-notice": true, "bg-wake": false, ReturnAfterBreakName: true, "": false} {
		if IsNoticeUserName(name) != want {
			t.Errorf("IsNoticeUserName(%q) != %v", name, want)
		}
	}
}
