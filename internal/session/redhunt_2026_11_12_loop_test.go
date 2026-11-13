package session

import (
	"strings"
	"testing"
	"time"
)

// A save merges the taint state of the revision on disk into the snapshot.
// When the store wrote that revision itself it trusts its stamp of it
// instead of re-reading the file, and the stamp must then give the merge
// everything a re-read would. A clean session's file holds a scan coverage
// and no span; a later snapshot without the coverage (an engine reset
// before the save, a build that records none, an embedder that sets the
// state without merging) keeps it on a re-read, and must keep it on the
// stamp path too.
func TestRED_SessionStampPathKeepsCoverage(t *testing.T) {
	cov := TaintCoverage{Messages: 2, Digest: strings.Repeat("d", 64)}
	save := func(t *testing.T, reread bool) (TaintCoverage, bool) {
		store, err := NewStoreWithDir(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		sess := &Session{ID: "20261113-stampcov", Task: "t", Messages: []Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}}}
		sess.SetTaintState(TaintState{Coverage: cov})
		if err := store.Save(sess); err != nil {
			t.Fatal(err)
		}
		if !sess.TaintState().Empty() {
			t.Fatal("precondition: the clean session holds span state")
		}
		time.Sleep(revStampSettle + 10*time.Millisecond)
		if reread {
			delete(store.revStamps, sess.ID)
		}
		sess.SetTaintState(TaintState{})
		sess.Messages = append(sess.Messages, Message{Role: "user", Content: "more"})
		loads := store.revisionLoads
		if err := store.Save(sess); err != nil {
			t.Fatal(err)
		}
		stamped := store.revisionLoads == loads
		got, err := store.Load(sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got.TaintCoverage, stamped
	}
	if got, stamped := save(t, true); stamped || got != cov {
		t.Fatalf("precondition: a save that re-reads the file keeps %+v (stamp path %v), want %+v", got, stamped, cov)
	}
	got, stamped := save(t, false)
	if !stamped {
		t.Skip("this filesystem's clock cannot tell two writes apart: saves always re-read the file")
	}
	if got != cov {
		t.Fatalf("a save on the stamp path left %+v on disk; a re-read keeps %+v", got, cov)
	}
}
