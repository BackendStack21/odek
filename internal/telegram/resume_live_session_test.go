package telegram

import (
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

// Resuming an archive while another conversation is live archives the live
// one instead of overwriting it, and the resumed session keeps saving.
func TestResumeSession_ArchivesLiveSession(t *testing.T) {
	sm, _ := setupTestSessionManager(t)
	const chat int64 = 77
	if err := sm.Save(chat, []session.Message{{Role: "user", Content: "first"}}); err != nil {
		t.Fatal(err)
	}
	if err := sm.ArchiveAndDelete(chat); err != nil {
		t.Fatal(err)
	}
	infos, _ := sm.ListSessions(chat, 0)
	if len(infos) != 1 {
		t.Fatalf("want 1 archive, got %d", len(infos))
	}
	oldID := infos[0].ID
	if _, err := sm.GetOrCreate(chat); err != nil {
		t.Fatal(err)
	}
	if err := sm.Save(chat, []session.Message{{Role: "user", Content: "live"}}); err != nil {
		t.Fatal(err)
	}
	cs, err := sm.ResumeSession(chat, oldID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.SessionID != "tg-77" || len(cs.Messages) != 1 || cs.Messages[0].Content != "first" {
		t.Fatalf("unexpected resumed session: %+v", cs)
	}
	infos, _ = sm.ListSessions(chat, 0)
	live := false
	for _, in := range infos {
		if in.ID != oldID && in.ID != "tg-77" {
			live = true // the previously live conversation was archived
		}
	}
	if !live {
		t.Fatalf("live conversation was not archived: %+v", infos)
	}
	if err := sm.Save(chat, []session.Message{{Role: "user", Content: "first"}, {Role: "assistant", Content: "again"}}); err != nil {
		t.Fatalf("save after resume: %v", err)
	}
}

// Resuming the canonical session id carries the stored revision.
func TestResumeSession_CanonicalSavesAfterResume(t *testing.T) {
	sm, _ := setupTestSessionManager(t)
	const chat int64 = 78
	if err := sm.Save(chat, []session.Message{{Role: "user", Content: "a"}}); err != nil {
		t.Fatal(err)
	}
	sm.Mu.Lock()
	delete(sm.Cache, chat)
	sm.Mu.Unlock()
	if _, err := sm.ResumeSession(chat, "tg-78"); err != nil {
		t.Fatal(err)
	}
	if err := sm.SaveNoIndex(chat, []session.Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}}); err != nil {
		t.Fatalf("save after resume: %v", err)
	}
}
