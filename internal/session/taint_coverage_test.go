package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The coverage is a hint: a merge keeps the newer valid one, drops a
// malformed one, and never makes a state count as non-empty or uncovered.
func TestTaintCoverage_MergeAndEmpty(t *testing.T) {
	older := TaintCoverage{Messages: 3, Digest: strings.Repeat("a", 64)}
	newer := TaintCoverage{Messages: 5, Digest: strings.Repeat("b", 64)}
	bad := TaintCoverage{Messages: 5, Digest: "nothex"}
	if got := MergeTaintState(TaintState{Coverage: newer}, TaintState{Coverage: older}).Coverage; got != newer {
		t.Fatalf("merge kept %+v, want the newer coverage", got)
	}
	if got := MergeTaintState(TaintState{Coverage: bad}, TaintState{Coverage: older}).Coverage; got != older {
		t.Fatalf("merge kept %+v, want the older valid coverage", got)
	}
	if got := MergeTaintState(TaintState{Coverage: bad}, TaintState{}).Coverage; got != (TaintCoverage{}) {
		t.Fatalf("a malformed coverage survived a merge: %+v", got)
	}
	if !(TaintState{Coverage: newer}).Empty() {
		t.Fatal("a coverage alone makes the span state non-empty")
	}
	if !(TaintState{}).Covers(TaintState{Coverage: newer}) {
		t.Fatal("Covers asks for a coverage hint")
	}
}

// The coverage is saved and loaded with the session, is absent from files
// that never had one, and a malformed value on disk is not valid.
func TestTaintCoverage_PersistedWithSession(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	cov := TaintCoverage{Messages: 1, Digest: strings.Repeat("c", 64)}
	sess := &Session{ID: "20261112-cov", Task: "t", Messages: []Message{{Role: "user", Content: "hi"}}}
	sess.SetTaintState(TaintState{Coverage: cov})
	if err := s.Save(sess); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.TaintState().Coverage != cov {
		t.Fatalf("coverage after load = %+v", loaded.TaintCoverage)
	}

	plain := &Session{ID: "20261112-plain", Task: "t", Messages: []Message{{Role: "user", Content: "hi"}}}
	if err := s.Save(plain); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, plain.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "taint_coverage") {
		t.Fatal("a session without coverage writes the field")
	}

	var damaged Session
	if err := json.Unmarshal([]byte(`{"id":"x","taint_coverage":{"messages":-4,"digest":"zz"}}`), &damaged); err != nil {
		t.Fatal(err)
	}
	if damaged.TaintState().Coverage.Valid() {
		t.Fatal("a malformed coverage on disk counts as valid")
	}
}
