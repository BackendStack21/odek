package telegram

import (
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/session"
)

// A chat carries the overflow filter of spans past the bounds the same way
// as its spans: marked, persisted by the next save, reloaded with the chat
// and kept when an archive is resumed.
func TestChatSessionCarriesTaintOverflow(t *testing.T) {
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sm := NewSessionManager(store, time.Hour)
	const chat = int64(4344)
	cs, err := sm.GetOrCreate(chat)
	if err != nil {
		t.Fatal(err)
	}
	var f session.TaintOverflow
	f.AddValue("host", "evil.example")
	sm.MarkTaintSpans(chat, session.TaintState{Overflow: f})
	if err := sm.Save(chat, []session.Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(cs.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.TaintOverflow.HasValue("host", "evil.example") {
		t.Fatal("the marked filter was not persisted")
	}
	again, err := NewSessionManager(store, time.Hour).GetOrCreate(chat)
	if err != nil {
		t.Fatal(err)
	}
	if !again.TaintSpans().Overflow.HasValue("host", "evil.example") {
		t.Fatal("the reloaded chat lost the filter")
	}
}

func TestResumeArchiveKeepsTaintOverflow(t *testing.T) {
	sm, st := setupTestSessionManager(t)
	const chat int64 = 96
	archive := &session.Session{
		ID:       "tg-96-20260101-000000",
		Task:     "tg-96",
		Messages: []session.Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}},
	}
	archive.TaintOverflow.AddValue("host", "evil.example")
	if err := st.Save(archive); err != nil {
		t.Fatal(err)
	}
	if _, err := sm.ResumeSession(chat, archive.ID); err != nil {
		t.Fatal(err)
	}
	got, err := st.Load("tg-96")
	if err != nil {
		t.Fatal(err)
	}
	if !got.TaintOverflow.HasValue("host", "evil.example") {
		t.Fatal("the canonical session lost the archive's filter")
	}
}
