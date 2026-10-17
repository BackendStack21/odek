package session

import (
	"errors"
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
