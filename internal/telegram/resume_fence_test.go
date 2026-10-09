package telegram

import (
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

// A turn that began before /resume must not overwrite the resumed
// conversation with its late per-step save.
func TestRED_LateTurnSaveDroppedAfterResume(t *testing.T) {
	sm, _ := setupTestSessionManager(t)
	const chat int64 = 93
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
	if err := sm.Save(chat, []session.Message{{Role: "user", Content: "running turn"}}); err != nil {
		t.Fatal(err)
	}
	gen := sm.Generation(chat) // the running turn captures this at start

	if _, err := sm.ResumeSession(chat, archiveID); err != nil {
		t.Fatal(err)
	}
	// The pre-resume turn finishes a step and persists late.
	late := []session.Message{{Role: "user", Content: "running turn"}, {Role: "assistant", Content: "late"}}
	if err := sm.SaveNoIndexAt(chat, gen, late); err != nil {
		t.Fatalf("late save should be dropped silently, got %v", err)
	}
	stored, err := sm.Store.Load("tg-93")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Messages) != 1 || stored.Messages[0].Content != "archived work" {
		t.Fatalf("late save overwrote the resumed session: %+v", stored.Messages)
	}

	// A turn started after the resume saves normally.
	fresh := sm.Generation(chat)
	next := []session.Message{{Role: "user", Content: "archived work"}, {Role: "assistant", Content: "ok"}}
	if err := sm.SaveNoIndexAt(chat, fresh, next); err != nil {
		t.Fatal(err)
	}
	stored, _ = sm.Store.Load("tg-93")
	if len(stored.Messages) != 2 {
		t.Fatalf("current-generation save was dropped: %+v", stored.Messages)
	}
}

// A manager built without the constructor still advances generations.
func TestResumeSession_GenerationWithoutConstructor(t *testing.T) {
	sm, _ := setupTestSessionManager(t)
	sm.gens = nil
	const chat int64 = 94
	if err := sm.Save(chat, []session.Message{{Role: "user", Content: "x"}}); err != nil {
		t.Fatal(err)
	}
	before := sm.Generation(chat)
	if _, err := sm.ResumeSession(chat, "tg-94"); err != nil {
		t.Fatal(err)
	}
	if sm.Generation(chat) == before {
		t.Fatal("generation did not advance on resume")
	}
}
