package telegram

import (
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

// After /resume the chat's live session is the canonical tg-<chat> id, so a
// caller that reloads cs.SessionID keeps working once the archive is pruned
// and every per-session key matches the id that progress is saved under.
func TestRED_ResumeBindsCanonicalSessionID(t *testing.T) {
	sm, _ := setupTestSessionManager(t)
	const chat int64 = 92
	if err := sm.Save(chat, []session.Message{{Role: "user", Content: "old"}}); err != nil {
		t.Fatal(err)
	}
	if err := sm.ArchiveAndDelete(chat); err != nil {
		t.Fatal(err)
	}
	infos, _ := sm.ListSessions(chat, 0)
	archiveID := infos[0].ID
	cs, err := sm.ResumeSession(chat, archiveID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.SessionID != "tg-92" {
		t.Fatalf("SessionID = %q, want canonical tg-92", cs.SessionID)
	}
	if err := sm.Store.Delete(archiveID); err != nil {
		t.Fatal(err)
	}
	if _, err := sm.Store.Load(cs.SessionID); err != nil {
		t.Fatalf("reload after archive pruned: %v", err)
	}
}
