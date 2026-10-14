package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRED_ReservedStoreFileIDsRejected(t *testing.T) {
	for _, id := range []string{"index", "vectors_meta"} {
		if err := ValidateSessionID(id); err == nil {
			t.Errorf("ValidateSessionID(%q) accepted an id that aliases a store-owned file", id)
		}
	}
	if err := ValidateSessionID("indexed"); err != nil {
		t.Errorf("ordinary id rejected: %v", err)
	}
}

func TestRED_DeleteIndexDoesNotRemoveIndexFile(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Create([]Message{{Role: "user", Content: "hi"}}, "m", "task"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "index.json")); err != nil {
		t.Skipf("index.json not present: %v", err)
	}
	for _, id := range []string{"index", "vectors_meta"} {
		if err := st.Delete(id); err == nil {
			t.Errorf("Delete(%q) succeeded", id)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "index.json")); err != nil {
		t.Fatalf("index.json was removed: %v", err)
	}
}

func TestRED_VectorsMetaIsNotASessionFile(t *testing.T) {
	if isSessionFile("vectors_meta.json") {
		t.Error("vectors_meta.json treated as a session file")
	}
	if isSessionFile("index.json") {
		t.Error("index.json treated as a session file")
	}
	if !isSessionFile("20260518-abc123.json") {
		t.Error("ordinary session file rejected")
	}
}
