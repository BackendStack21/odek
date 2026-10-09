package telegram

import (
	"errors"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

// The running turn's final save (and cancel-path save) must also drop after a
// /resume: it follows handleChatMessage's call order and must not overwrite
// the resumed conversation.
func TestRED_FinalTurnSaveDroppedAfterResume(t *testing.T) {
	sm, _ := setupTestSessionManager(t)
	const chat int64 = 94
	if err := sm.Save(chat, []session.Message{{Role: "user", Content: "archived work"}}); err != nil {
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
	gen := sm.Generation(chat)
	turn := []session.Message{{Role: "user", Content: "running turn"}}
	if err := sm.SaveCheckpoint(chat, turn); err != nil {
		t.Fatal(err)
	}
	if _, err := sm.ResumeSession(chat, archiveID); err != nil {
		t.Fatal(err)
	}

	final := append(turn, session.Message{Role: "assistant", Content: "late final"})
	if err := sm.SaveAt(chat, gen, final); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("SaveAt err = %v, want ErrStaleGeneration", err)
	}
	if err := sm.SaveCheckpointAt(chat, gen, final); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("SaveCheckpointAt err = %v, want ErrStaleGeneration", err)
	}
	stored, err := sm.Store.Load("tg-94")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Messages) != 1 || stored.Messages[0].Content != "archived work" {
		t.Fatalf("old turn overwrote the resumed session: %+v", stored.Messages)
	}

	// Current generation still saves.
	if err := sm.SaveAt(chat, sm.Generation(chat), []session.Message{{Role: "user", Content: "archived work"}, {Role: "assistant", Content: "ok"}}); err != nil {
		t.Fatal(err)
	}
	stored, _ = sm.Store.Load("tg-94")
	if len(stored.Messages) != 2 {
		t.Fatalf("current-generation save dropped: %+v", stored.Messages)
	}
}
