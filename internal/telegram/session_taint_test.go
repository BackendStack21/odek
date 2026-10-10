package telegram

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/session"
)

func TestChatSessionUntrustedIngested(t *testing.T) {
	var nilCS *ChatSession
	if nilCS.UntrustedIngested() || (&ChatSession{}).UntrustedIngested() {
		t.Fatal("a chat without a stored session is not tainted")
	}
	cs := &ChatSession{stored: &session.Session{UntrustedIngested: true}}
	if !cs.UntrustedIngested() {
		t.Fatal("persisted taint not reported")
	}
}

// In-run taint that never reaches the history is persisted by the next save.
func TestMarkUntrustedIngestedPersists(t *testing.T) {
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sm := NewSessionManager(store, time.Hour)
	const chat = int64(4242)
	cs, err := sm.GetOrCreate(chat)
	if err != nil {
		t.Fatal(err)
	}
	if cs.UntrustedIngested() {
		t.Fatal("fresh chat tainted")
	}
	sm.MarkUntrustedIngested(chat)
	if err := sm.Save(chat, []session.Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(cs.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.UntrustedIngested {
		t.Fatal("marked taint not persisted")
	}
	sm.MarkUntrustedIngested(9999) // uncached chat: no-op
}

// Marking taint while a save is in flight must not swap the cache entry under
// the save: the save's cache update would be skipped, leaving a stale stored
// revision and failing every later save with a conflict.
func TestRED_MarkUntrustedIngestedConcurrentSaveKeepsRevision(t *testing.T) {
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sm := NewSessionManager(store, time.Hour)
	msgs := []session.Message{{Role: "user", Content: "hi"}}
	for i := 0; i < 200; i++ {
		chat := int64(10000 + i)
		if _, err := sm.GetOrCreate(chat); err != nil {
			t.Fatal(err)
		}
		if err := sm.SaveNoIndex(chat, msgs); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _ = sm.SaveNoIndex(chat, msgs) }()
		go func() { defer wg.Done(); sm.MarkUntrustedIngested(chat) }()
		wg.Wait()
		if err := sm.Save(chat, msgs); err != nil {
			t.Fatalf("iteration %d: save after concurrent mark: %v", i, err)
		}
		loaded, err := store.Load(fmt.Sprintf("tg-%d", chat))
		if err != nil {
			t.Fatal(err)
		}
		if !loaded.UntrustedIngested {
			t.Fatalf("iteration %d: taint lost", i)
		}
	}
}
