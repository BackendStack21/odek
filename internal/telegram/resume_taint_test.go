package telegram

import (
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

// Resuming an archive rebuilds the chat's canonical session; the archive's
// sticky taint flags must carry over even when the content that set them is
// no longer in its history (trimmed or compacted away).
func TestRED_ResumeArchiveKeepsStickyTaint(t *testing.T) {
	sm, st := setupTestSessionManager(t)
	const chat int64 = 93
	archive := &session.Session{
		ID:                "tg-93-20260101-000000",
		Task:              "tg-93",
		Messages:          []session.Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}},
		UntrustedIngested: true,
		EpisodeUntrusted:  true,
	}
	if err := st.Save(archive); err != nil {
		t.Fatal(err)
	}
	cs, err := sm.ResumeSession(chat, archive.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !cs.UntrustedIngested() {
		t.Fatal("resumed chat lost the ingest taint")
	}
	got, err := st.Load("tg-93")
	if err != nil {
		t.Fatal(err)
	}
	if !got.UntrustedIngested || !got.EpisodeUntrusted || !got.EpisodeTaintTracked {
		t.Fatalf("canonical session lost sticky flags: ingested=%v episode=%v tracked=%v", got.UntrustedIngested, got.EpisodeUntrusted, got.EpisodeTaintTracked)
	}
}

// Archiving a chat whose in-run taint was never persisted keeps it.
func TestArchiveKeepsInRunTaint(t *testing.T) {
	sm, st := setupTestSessionManager(t)
	const chat int64 = 94
	if err := sm.Save(chat, []session.Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	sm.MarkUntrustedIngested(chat)
	if err := sm.ArchiveAndDelete(chat); err != nil {
		t.Fatal(err)
	}
	infos, err := sm.ListSessions(chat, 0)
	if err != nil || len(infos) == 0 {
		t.Fatalf("archive not listed: %v", err)
	}
	got, err := st.Load(infos[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.UntrustedIngested {
		t.Fatal("archive dropped the chat's in-run taint")
	}
}
