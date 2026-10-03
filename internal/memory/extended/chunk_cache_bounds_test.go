package extended

// Coverage tests for the chunk cache: the wholesale 4096-entry reset
// branch and cache-hit behavior via Get/List.

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// Filling the cache to the 4096 cap must reset it wholesale rather than
// grow unboundedly; entries cached after the reset are still served
// correctly.
func TestAtomStoreChunkCache_CapsAt4096WithWholesaleReset(t *testing.T) {
	dir := t.TempDir()
	s := NewAtomStore(dir)

	const cap = 4096
	// Add cap+1 atoms so the last insert trips the reset branch.
	for i := 0; i < cap+1; i++ {
		id := fmt.Sprintf("20260101-capatom%08d", i)
		if err := s.Add(testAtom(id, fmt.Sprintf("text-%d", i)), 0); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}
	atoms, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(atoms) != cap+1 {
		t.Fatalf("List = %d atoms, want %d", len(atoms), cap+1)
	}

	s.chunkCacheMu.Lock()
	size := len(s.chunkCache)
	s.chunkCacheMu.Unlock()
	if size > cap {
		t.Fatalf("chunk cache = %d entries, must never exceed the %d cap", size, cap)
	}

	// Post-reset the store must still serve correct content.
	got, err := s.Get("20260101-capatom00000000")
	if err != nil {
		t.Fatalf("Get after reset: %v", err)
	}
	if got.Text != "text-0" {
		t.Fatalf("Get after reset = %q, want %q", got.Text, "text-0")
	}
}

// A second read of an unchanged chunk must hit the cache (same mtime),
// and a chunk whose on-disk mtime changed must be re-read.
func TestAtomStoreChunkCache_HitThenMissOnMtimeChange(t *testing.T) {
	dir := t.TempDir()
	s := NewAtomStore(dir)
	id := "20260101-hitmapmmmmmmmmm"

	if err := s.Add(testAtom(id, "v1"), 0); err != nil {
		t.Fatalf("Add: %v", err)
	}
	cached, err := s.chunkText(id)
	if err != nil {
		t.Fatalf("chunkText 1: %v", err)
	}

	s.chunkCacheMu.Lock()
	entry, ok := s.chunkCache[id]
	if !ok {
		s.chunkCacheMu.Unlock()
		t.Fatalf("chunk not cached after first read (text %q)", cached)
	}
	// Same mtime with planted text: a second read must serve the cache.
	s.chunkCache[id] = chunkCacheEntry{mtime: entry.mtime, text: "CACHED"}
	s.chunkCacheMu.Unlock()

	got, err := s.chunkText(id)
	if err != nil {
		t.Fatalf("chunkText 2: %v", err)
	}
	if got != "CACHED" {
		t.Fatalf("chunkText 2 = %q, want cache hit %q", got, "CACHED")
	}

	// On-disk mtime change must bypass the (planted) cache entry.
	path := s.chunkPath(id)
	future := time.Now().Add(2 * time.Second)
	if err := os.WriteFile(path, []byte("v2"), 0600); err != nil {
		t.Fatalf("write chunk: %v", err)
	}
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	got, err = s.chunkText(id)
	if err != nil {
		t.Fatalf("chunkText 3: %v", err)
	}
	if got == "CACHED" {
		t.Fatal("chunkText 3 served a stale cache entry after the chunk changed")
	}
}
