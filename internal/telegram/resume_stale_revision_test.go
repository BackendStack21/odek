package telegram

import (
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/session"
)

// A resumed (archived) session must keep persisting, and must be what the
// chat gets back after a restart.
func TestRED_ResumedSessionLostAfterRestart(t *testing.T) {
	sm, _ := setupTestSessionManager(t)
	const chat int64 = 42
	if err := sm.Save(chat, []session.Message{{Role: "user", Content: "old"}}); err != nil {
		t.Fatal(err)
	}
	if err := sm.ArchiveAndDelete(chat); err != nil {
		t.Fatal(err)
	}
	infos, _ := sm.ListSessions(chat, 0)
	if len(infos) != 1 {
		t.Fatalf("want 1 archive, got %d", len(infos))
	}
	if _, err := sm.ResumeSession(chat, infos[0].ID); err != nil {
		t.Fatal(err)
	}
	msgs := []session.Message{{Role: "user", Content: "old"}, {Role: "assistant", Content: "continued after resume"}}
	if err := sm.SaveNoIndex(chat, msgs); err != nil {
		t.Fatalf("save after resume: %v", err)
	}
	sm2 := NewSessionManager(sm.Store, time.Hour)
	cs, err := sm2.GetOrCreate(chat)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Messages) != 2 {
		t.Fatalf("after restart the resumed conversation is gone: %d messages", len(cs.Messages))
	}
}
