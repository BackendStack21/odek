package telegram

import (
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

// A prefix or task-text /resume must restore the full transcript, not the
// metadata-only row that the store listing returns.
func TestRED_ResumePrefixKeepsMessages(t *testing.T) {
	sm, _ := setupTestSessionManager(t)
	const chat int64 = 91
	if err := sm.Save(chat, []session.Message{{Role: "user", Content: "old work"}}); err != nil {
		t.Fatal(err)
	}
	if err := sm.ArchiveAndDelete(chat); err != nil {
		t.Fatal(err)
	}
	infos, _ := sm.ListSessions(chat, 0)
	archiveID := infos[0].ID
	if _, err := sm.GetOrCreate(chat); err != nil {
		t.Fatal(err)
	}
	if err := sm.Save(chat, []session.Message{{Role: "user", Content: "live"}}); err != nil {
		t.Fatal(err)
	}
	cs, err := sm.ResumeSession(chat, archiveID[:len(archiveID)-3])
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Messages) != 1 || cs.Messages[0].Content != "old work" {
		t.Fatalf("prefix resume lost the transcript: %+v", cs.Messages)
	}
	stored, err := sm.Store.Load("tg-91")
	if err != nil || len(stored.Messages) != 1 || stored.Messages[0].Content != "old work" {
		t.Fatalf("canonical session not restored: %+v %v", stored, err)
	}
}
