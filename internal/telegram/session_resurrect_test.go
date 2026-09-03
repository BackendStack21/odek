package telegram

import (
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/llm"
)

// /new archives and deletes the session while a turn may still be running
// (the command path is not serialized with the run's persist callbacks).
// A subsequent SaveNoIndex from that dying turn hit the cache-miss branch,
// re-created the cache entry AND re-saved the session under the old
// "tg-<chatID>" ID — "starting fresh" silently resumed the archived
// conversation. SaveNoIndex must not resurrect a session that was archived
// out from under it: the cache-miss write is skipped entirely.
func TestSaveNoIndex_DoesNotResurrectArchivedSession(t *testing.T) {
	sm, _ := setupTestSessionManager(t)
	var chatID int64 = 42

	// An existing conversation.
	if err := sm.Save(chatID, []llm.Message{
		{Role: "user", Content: "old question"},
		{Role: "assistant", Content: "old answer"},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// /new: archive + delete.
	if err := sm.ArchiveAndDelete(chatID); err != nil {
		t.Fatalf("ArchiveAndDelete: %v", err)
	}

	// The still-running turn's persist callback fires after the archive.
	if err := sm.SaveNoIndex(chatID, []llm.Message{
		{Role: "user", Content: "old question"},
		{Role: "assistant", Content: "mid-turn partial"},
	}); err != nil {
		t.Fatalf("SaveNoIndex after archive: %v", err)
	}

	// The next message must start fresh, not resurrect the archive.
	cs, err := sm.GetOrCreate(chatID)
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	if len(cs.Messages) != 0 {
		t.Fatalf("archived session resurrected: %d messages survive /new (first: %+v)", len(cs.Messages), firstOrNil(cs.Messages))
	}
}

func firstOrNil(msgs []llm.Message) *llm.Message {
	if len(msgs) == 0 {
		return nil
	}
	return &msgs[0]
}

// Guard: SaveNoIndex must still checkpoint NORMALLY (cache present) — the
// resurrect fix must not break mid-turn crash-safe persistence.
func TestSaveNoIndex_StillCheckpointsLiveSession(t *testing.T) {
	sm, _ := setupTestSessionManager(t)
	var chatID int64 = 7

	// Turn start: GetOrCreate populates the cache.
	if _, err := sm.GetOrCreate(chatID); err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	if err := sm.SaveNoIndex(chatID, []llm.Message{
		{Role: "user", Content: "live question"},
		{Role: "assistant", Content: "live partial"},
	}); err != nil {
		t.Fatalf("SaveNoIndex: %v", err)
	}

	// Fresh manager over the same store sees the checkpoint (crash-resume).
	sm2 := NewSessionManager(sm.Store, 24*time.Hour)
	cs, err := sm2.GetOrCreate(chatID)
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	if len(cs.Messages) != 2 || cs.Messages[1].Content != "live partial" {
		t.Fatalf("live checkpoint lost: %+v", cs.Messages)
	}
}
