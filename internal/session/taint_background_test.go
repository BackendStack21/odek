package session

import "testing"

// The background-job fact is part of the sticky untrusted-span state: merges
// OR it, it makes a state non-empty, and a state without it does not cover
// one with it.
func TestTaintState_BackgroundFact(t *testing.T) {
	bg := TaintState{Background: true}
	if bg.Empty() {
		t.Fatal("a state holding only the background fact counts as empty")
	}
	if !MergeTaintState(TaintState{}, bg).Background || !MergeTaintState(bg, TaintState{}).Background {
		t.Fatal("a merge dropped the background fact")
	}
	if (TaintState{}).Covers(bg) {
		t.Fatal("a state without the background fact covers one with it")
	}
	if !bg.Covers(TaintState{}) || !bg.Covers(bg) {
		t.Fatal("Covers rejects a state it holds")
	}
	var s Session
	s.SetTaintState(bg)
	if !s.TaintBackground || !s.TaintState().Background {
		t.Fatal("the session does not carry the background fact")
	}
}

// Saves never clear the background fact, also when the revision stamp cache
// serves the save, and AddTaintState writes it when the file lacks it.
func TestTaintState_BackgroundStickyOnDisk(t *testing.T) {
	prev := revStampSettle
	revStampSettle = 0
	defer func() { revStampSettle = prev }()
	s, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess := &Session{ID: "20261112-bg", Task: "t", Messages: []Message{{Role: "user", Content: "hi"}}}
	if err := s.Save(sess); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTaintState(sess.ID, TaintState{Background: true}); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.TaintBackground {
		t.Fatal("AddTaintState did not write the background fact")
	}
	loaded.TaintBackground = false
	if err := s.Save(loaded); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(loaded); err != nil {
		t.Fatal(err)
	}
	again, err := s.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !again.TaintBackground {
		t.Fatal("a later snapshot without the fact cleared it on disk")
	}
}
