package session

// Coverage tests for the index cache stamp branches: the inode component
// of the stamp, the index-file-gone reset, and the transient-stat-error
// serve-stale path.

import (
	"os"
	"testing"
)

// TestIndexCache_InodeStampDetectsRenameRewrite: an atomic-rename rewrite
// of index.json always mints a fresh inode. When the rewrite lands with
// the same size and (restored) mtime as the cached stamp, the inode term
// must still force a re-read — otherwise a rewritten index whose content
// differs would be served stale from the cache.
func TestIndexCache_InodeStampDetectsRenameRewrite(t *testing.T) {
	s := newTestStore(t)
	sess := newPerfTestSession("inode-stamp")
	if err := s.Save(sess); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Emulate another process rewriting index.json via temp+rename with
	// different content, then restoring the cached mtime so only the
	// inode differs.
	other := newPerfTestSession("inode-other")
	idx := s.loadIndex()
	idx[other.ID] = indexEntry(other)
	entries := make([]*IndexEntry, 0, len(idx))
	for _, e := range idx {
		entries = append(entries, e)
	}
	if err := writeRawIndex(t, s, entries); err != nil {
		t.Fatalf("external index rewrite: %v", err)
	}
	path := s.indexPath()
	if err := os.Chtimes(path, s.idxMod, s.idxMod); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	readsBefore := s.indexDiskReads
	got := s.loadIndex()
	if s.indexDiskReads-readsBefore == 0 {
		t.Fatal("loadIndex served the cache despite a fresh inode — inode stamp not effective")
	}
	if _, ok := got[other.ID]; !ok {
		t.Fatal("rewritten index entry missing after re-read")
	}
}

// TestIndexCache_FileGoneReturnsEmpty: if index.json disappears (external
// cleanup), the cache must reset and callers must observe an empty index
// instead of stale entries.
func TestIndexCache_FileGoneReturnsEmpty(t *testing.T) {
	s := newTestStore(t)
	sess := newPerfTestSession("file-gone")
	if err := s.Save(sess); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := os.Remove(s.indexPath()); err != nil {
		t.Fatalf("remove index: %v", err)
	}
	got := s.loadIndex()
	if len(got) != 0 {
		t.Fatalf("loadIndex returned %d entries after index deletion, want 0", len(got))
	}
	if s.idxLoaded {
		t.Fatal("idxLoaded still true after index deletion")
	}
	// Recreating the index must be picked up again.
	sess2 := newPerfTestSession("file-back")
	if err := s.Save(sess2); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	if got := s.loadIndex(); len(got) != 1 {
		t.Fatalf("loadIndex after re-save = %d entries, want 1", len(got))
	}
}

// TestIndexCache_TransientStatServesStale: a transient stat failure on
// index.json (not a not-exist) must serve the last known-good cached
// index rather than an empty map that would make every session vanish.
func TestIndexCache_TransientStatServesStale(t *testing.T) {
	s := newTestStore(t)
	sess := newPerfTestSession("stale-on-error")
	if err := s.Save(sess); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Make the directory temporarily unsearchable so os.Stat fails with a
	// permission error (not NotExist).
	dir := s.Dir()
	orig, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	restore := func() { _ = os.Chmod(dir, orig.Mode().Perm()) }
	defer restore()

	got := s.loadIndex()
	restore()
	if _, ok := got[sess.ID]; !ok {
		t.Fatalf("loadIndex dropped cached session %q on transient stat error; got %d entries", sess.ID, len(got))
	}

	// After the transient error clears, the cache must still work and
	// reload normally.
	final := s.loadIndex()
	if _, ok := final[sess.ID]; !ok {
		t.Fatal("loadIndex lost the session after the transient error cleared")
	}
}
