package session

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// A rename or pin while a turn runs must not invalidate the turn's snapshot:
// UpdateMeta leaves Revision alone, and the turn's next save adopts the newer
// metadata instead of overwriting it or failing with ErrConflict.
func TestRED_WSUpdateMetaKeepsLiveSnapshotSavable(t *testing.T) {
	for _, settled := range []bool{false, true} {
		s := newTestStore(t)
		live, err := s.Create([]Message{{Role: "user", Content: "hi"}}, "m", "original")
		if err != nil {
			t.Fatal(err)
		}
		if settled {
			// Let the revision stamp settle so the save takes the
			// cached (no-Load) revision path.
			time.Sleep(revStampSettle + 10*time.Millisecond)
		}
		rev := live.Revision
		got, err := s.UpdateMeta(live.ID, func(x *Session) { x.Task = "renamed"; x.Pinned = true })
		if err != nil {
			t.Fatalf("UpdateMeta: %v", err)
		}
		if got.Revision != rev || got.MetaRevision != 1 || got.Task != "renamed" || !got.Pinned {
			t.Fatalf("UpdateMeta result rev=%d meta=%d task=%q pinned=%v", got.Revision, got.MetaRevision, got.Task, got.Pinned)
		}
		if settled {
			time.Sleep(revStampSettle + 10*time.Millisecond)
		}
		live.Messages = append(live.Messages, Message{Role: "assistant", Content: "step"})
		if err := s.SaveNoIndex(live); err != nil {
			t.Fatalf("settled=%v: live snapshot save after a metadata edit: %v", settled, err)
		}
		if live.Task != "renamed" || !live.Pinned || live.MetaRevision != 1 {
			t.Fatalf("live snapshot did not adopt the edit: task=%q pinned=%v meta=%d", live.Task, live.Pinned, live.MetaRevision)
		}
		stored, err := s.Load(live.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Task != "renamed" || !stored.Pinned || len(stored.Messages) != 2 {
			t.Fatalf("stored task=%q pinned=%v messages=%d", stored.Task, stored.Pinned, len(stored.Messages))
		}
		// A second edit after the turn's save still lands.
		if _, err := s.UpdateMeta(live.ID, func(x *Session) { x.Pinned = false }); err != nil {
			t.Fatal(err)
		}
		if err := s.Save(live); err != nil {
			t.Fatal(err)
		}
		if stored, _ = s.Load(live.ID); stored.Pinned {
			t.Fatal("unpin overwritten by the live snapshot")
		}
	}
}

// A stale transcript snapshot is still rejected: metadata merging never
// relaxes the revision check for message history.
func TestRED_WSUpdateMetaKeepsTranscriptConflict(t *testing.T) {
	s := newTestStore(t)
	a, err := s.Create([]Message{{Role: "user", Content: "hi"}}, "m", "t")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Load(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.Messages = append(a.Messages, Message{Role: "assistant", Content: "a"})
	if err := s.Save(a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateMeta(a.ID, func(x *Session) { x.Task = "x" }); err != nil {
		t.Fatal(err)
	}
	b.Messages = append(b.Messages, Message{Role: "assistant", Content: "b"})
	if err := s.Save(b); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale transcript save err = %v, want ErrConflict", err)
	}
}

func TestRED_WSUpdateMetaMissingSession(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.UpdateMeta("20261017-0123456789abcdef0123456789abcdef", func(*Session) {}); err == nil {
		t.Fatal("UpdateMeta on a missing session succeeded")
	}
}

// Metadata edits racing a stream of transcript saves all succeed and none of
// the transcript saves conflict.
func TestRED_WSUpdateMetaConcurrentWithTurnSaves(t *testing.T) {
	s := newTestStore(t)
	live, err := s.Create([]Message{{Role: "user", Content: "hi"}}, "m", "t")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			if _, err := s.UpdateMeta(live.ID, func(x *Session) { x.Pinned = !x.Pinned }); err != nil {
				errs <- err
			}
		}
	}()
	for i := 0; i < 20; i++ {
		live.Messages = append(live.Messages, Message{Role: "assistant", Content: "step"})
		if err := s.SaveNoIndex(live); err != nil {
			errs <- err
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent save: %v", err)
	}
	stored, err := s.Load(live.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Pinned {
		t.Fatal("20 pin toggles should leave the session unpinned")
	}
	if len(stored.Messages) != 21 {
		t.Fatalf("messages = %d, want 21", len(stored.Messages))
	}
}

// A transcript save by another process between UpdateMeta's load and write
// is a conflict UpdateMeta retries on a fresh load, keeping both changes.
func TestRED_WSUpdateMetaRetriesForeignWriter(t *testing.T) {
	s := newTestStore(t)
	sess, err := s.Create([]Message{{Role: "user", Content: "hi"}}, "m", "t")
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewStoreWithDir(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	got, err := s.UpdateMeta(sess.ID, func(x *Session) {
		calls++
		if calls == 1 {
			foreign, err := other.Load(sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			foreign.Messages = append(foreign.Messages, Message{Role: "assistant", Content: "foreign"})
			if err := other.Save(foreign); err != nil {
				t.Fatal(err)
			}
		}
		x.Pinned = true
	})
	if err != nil {
		t.Fatalf("UpdateMeta: %v", err)
	}
	if calls != 2 {
		t.Fatalf("edit applied %d times, want a retry (2)", calls)
	}
	stored, err := s.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Pinned || !stored.Pinned || len(stored.Messages) != 2 {
		t.Fatalf("pinned=%v/%v messages=%d", got.Pinned, stored.Pinned, len(stored.Messages))
	}
}

func TestRED_WSUpdateMetaRevisionExhausted(t *testing.T) {
	s := newTestStore(t)
	sess, err := s.Create([]Message{{Role: "user", Content: "hi"}}, "m", "t")
	if err != nil {
		t.Fatal(err)
	}
	sess.MetaRevision = ^uint64(0)
	if err := s.Save(sess); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateMeta(sess.ID, func(x *Session) { x.Pinned = true }); err == nil {
		t.Fatal("UpdateMeta past the last meta revision succeeded")
	}
	if stored, _ := s.Load(sess.ID); stored.Pinned || stored.MetaRevision != ^uint64(0) {
		t.Fatalf("failed update changed the file: pinned=%v meta=%d", stored.Pinned, stored.MetaRevision)
	}
}

// Two stores on one directory model two processes (separate s.mu, shared
// flock). A metadata edit committed by the other store between this
// store's load and write must not silently discard this store's edit.
func TestRED_WSUpdateMetaCrossStoreLostUpdate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	a, err := NewStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := a.Create([]Message{{Role: "user", Content: "hi"}}, "m", "original")
	if err != nil {
		t.Fatal(err)
	}
	once := false
	got, err := a.UpdateMeta(sess.ID, func(x *Session) {
		x.Pinned = true
		if !once {
			once = true
			if _, err := b.UpdateMeta(sess.ID, func(y *Session) { y.Task = "renamed" }); err != nil {
				t.Fatal(err)
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := a.Load(sess.ID)
	t.Logf("returned pinned=%v task=%q; stored pinned=%v task=%q meta=%d", got.Pinned, got.Task, stored.Pinned, stored.Task, stored.MetaRevision)
	if !stored.Pinned || stored.Task != "renamed" {
		t.Fatalf("lost update: stored pinned=%v task=%q (want pinned=true task=renamed)", stored.Pinned, stored.Task)
	}
}

// A transcript snapshot from another process that is older than a meta edit
// keeps saving and adopts it (cross-store), and an older-revision snapshot is
// still rejected.
func TestRED_WSCrossStoreTranscriptAdoptsMetaAndStaleRejected(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	a, _ := NewStoreWithDir(dir)
	b, _ := NewStoreWithDir(dir)
	sess, err := a.Create([]Message{{Role: "user", Content: "hi"}}, "m", "original")
	if err != nil {
		t.Fatal(err)
	}
	stale, _ := b.Load(sess.ID)
	if _, err := b.UpdateMeta(sess.ID, func(y *Session) { y.Pinned = true }); err != nil {
		t.Fatal(err)
	}
	sess.Messages = append(sess.Messages, Message{Role: "assistant", Content: "x"})
	if err := a.SaveNoIndex(sess); err != nil {
		t.Fatalf("live save: %v", err)
	}
	if !sess.Pinned {
		t.Fatal("pin not adopted")
	}
	stale.Messages = append(stale.Messages, Message{Role: "assistant", Content: "other"})
	if err := b.Save(stale); err == nil {
		t.Fatal("stale snapshot accepted")
	}
}
