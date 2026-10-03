package extended

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testAtom(id, text string) MemoryAtom {
	return MemoryAtom{ID: id, Text: text, SourceClass: "test", Type: "fact", CreatedAt: time.Now().UTC()}
}

// The chunk cache must serve unchanged chunks from cache (same mtime) and
// pick up externally modified chunks once their mtime changes — a recall
// query must never see stale text.
func TestAtomStoreChunkCache_PicksUpExternalEdits(t *testing.T) {
	dir := t.TempDir()
	s := NewAtomStore(dir)
	id := "20260101-testcacheaaaaaaaa"

	if err := s.Add(testAtom(id, "original"), 0); err != nil {
		t.Fatalf("Add: %v", err)
	}
	atoms, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(atoms) != 1 || atoms[0].Text != "original" {
		t.Fatalf("List = %+v, want [original]", atoms)
	}

	// External edit with a future mtime must invalidate the cache.
	chunk := filepath.Join(dir, "chunks", id+".md")
	future := time.Now().Add(2 * time.Second)
	if err := os.WriteFile(chunk, []byte("edited"), 0600); err != nil {
		t.Fatalf("write chunk: %v", err)
	}
	if err := os.Chtimes(chunk, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	got, err := s.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Text != "edited" {
		t.Fatalf("Get returned stale text %q, want %q", got.Text, "edited")
	}
}

// Remove must invalidate the cached entry: recreating the same atom ID
// must not resurrect the old text via a cache hit.
func TestAtomStoreChunkCache_RemoveInvalidates(t *testing.T) {
	dir := t.TempDir()
	s := NewAtomStore(dir)
	id := "20260101-testcachebbbbbbbb"

	if err := s.Add(testAtom(id, "v1"), 0); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := s.List(); err != nil {
		t.Fatalf("List: %v", err)
	}
	if err := s.Remove(id); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := s.Add(testAtom(id, "v2"), 0); err != nil {
		t.Fatalf("re-Add: %v", err)
	}
	got, err := s.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Text != "v2" {
		t.Fatalf("Get = %q, want v2", got.Text)
	}
}
