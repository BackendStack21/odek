package telegram

import (
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
