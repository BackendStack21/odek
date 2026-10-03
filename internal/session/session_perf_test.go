package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSave_SingleMarshalPerTurn asserts that an ordinary per-turn save (no
// size-cap trim) marshals the session exactly once. The double marshal was
// identified as the dominant per-turn cost: the transcript is serialized
// before the size check and again after the redact-boundary update, even
// though the boundary can be set before the first marshal.
func TestSave_SingleMarshalPerTurn(t *testing.T) {
	s := newTestStore(t)
	sess := newPerfTestSession("marshal-once")
	if err := s.Save(sess); err != nil {
		t.Fatalf("initial save: %v", err)
	}
	before := s.marshalCount
	for i := 0; i < 3; i++ {
		sess.Messages = append(sess.Messages, Message{Role: "user", Content: "turn"})
		if err := s.Save(sess); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	if got := s.marshalCount - before; got != 3 {
		t.Fatalf("per-turn saves marshaled the session %d times, want 1 per save (3 total)", got/3)
	}
}

// TestSave_IndexNotRereadFromDiskWhenCached asserts the index is read from
// disk at most once: after the first save populates the in-memory cache,
// subsequent saves must not re-read or re-parse index.json. The O(total
// sessions) disk read per turn was the second dominant per-turn cost.
func TestSave_IndexNotRereadFromDiskWhenCached(t *testing.T) {
	s := newTestStore(t)
	sess := newPerfTestSession("index-cache")
	if err := s.Save(sess); err != nil {
		t.Fatalf("initial save: %v", err)
	}
	readsAfterFirst := s.indexDiskReads
	for i := 0; i < 5; i++ {
		sess.Messages = append(sess.Messages, Message{Role: "user", Content: "turn"})
		if err := s.Save(sess); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	if got := s.indexDiskReads - readsAfterFirst; got != 0 {
		t.Fatalf("subsequent saves re-read index.json from disk %d times, want 0 (cache hit expected)", got)
	}
}

// TestIndexCache_InvalidatedByExternalRewrite asserts the mtime/size stamp
// detects an index.json rewritten by another process and reloads it: the
// cache must never serve stale entries across an external modification.
func TestIndexCache_InvalidatedByExternalRewrite(t *testing.T) {
	s := newTestStore(t)
	sess := newPerfTestSession("external-rewrite")
	if err := s.Save(sess); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Simulate another process saving a session and rewriting the index.
	other := newPerfTestSession("other-process-session")
	idx := s.loadIndex()
	idx[other.ID] = indexEntry(other)
	entries := make([]*IndexEntry, 0, len(idx))
	for _, e := range idx {
		entries = append(entries, e)
	}
	if err := writeRawSession(t, s, other); err != nil {
		t.Fatalf("external session write: %v", err)
	}
	if err := writeRawIndex(t, s, entries); err != nil {
		t.Fatalf("external index write: %v", err)
	}

	latest, err := s.Latest()
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if latest.ID != other.ID {
		t.Fatalf("cached index served stale data: latest = %q, want externally added %q", latest.ID, other.ID)
	}
}

// TestIndexCache_ReturnsMutableCopies asserts callers may mutate the map
// returned by loadIndex (Save/Delete do exactly that) without corrupting
// the cache or racing other readers.
func TestIndexCache_ReturnsMutableCopies(t *testing.T) {
	s := newTestStore(t)
	sess := newPerfTestSession("mutable-copy")
	if err := s.Save(sess); err != nil {
		t.Fatalf("save: %v", err)
	}
	a := s.loadIndex()
	delete(a, sess.ID)
	b := s.loadIndex()
	if _, ok := b[sess.ID]; !ok {
		t.Fatal("mutating a returned index map leaked into the cache")
	}
}

func newPerfTestSession(id string) *Session {
	return &Session{
		ID:        id,
		Task:      "perf test",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Messages: []Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "hello"},
		},
	}
}

// writeRawSession emulates an external process writing a session file.
func writeRawSession(t *testing.T, s *Store, sess *Session) error {
	t.Helper()
	data, err := json.Marshal(sess)
	if err != nil {
		return err
	}
	return os.WriteFile(s.Path(sess.ID), data, 0600)
}

// writeRawIndex emulates an external process writing index.json without
// going through the store (no cache update), advancing mtime.
func writeRawIndex(t *testing.T, s *Store, entries []*IndexEntry) error {
	t.Helper()
	data, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.Dir(), ".index-external.tmp")
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.indexPath())
}
