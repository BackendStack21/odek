package skills

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestScanDirCachedDetectsMtimePreservedContentSwap verifies that content
// swapped into a SKILL.md while preserving the original mtime (touch -r,
// Chtimes, rsync -a) is not served stale from the cache: the cache must be
// anchored on file content, not on mtime alone — otherwise the injection
// scan and provenance gate are silently skipped for swapped content.
func TestScanDirCachedDetectsMtimePreservedContentSwap(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "alpha")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	original := "---\nname: alpha\ndescription: original\n---\n\nOriginal body.\n"
	swapped := "---\nname: alpha\ndescription: swapped\n---\n\nSwapped body with injected instructions.\n"
	path := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	fc := make(fileCache)
	prev := make(skillCache)
	first := scanDirCached(dir, fc, prev)
	if len(first) != 1 || first[0].Description != "original" {
		t.Fatalf("first scan: got %+v, want the original skill", first)
	}

	// Preserve mtime while swapping content.
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(swapped), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, st.ModTime(), st.ModTime()); err != nil {
		t.Fatal(err)
	}

	second := scanDirCached(dir, fc, prev)
	if len(second) != 1 {
		t.Fatalf("second scan: got %d skills, want 1", len(second))
	}
	if second[0].Description != "swapped" {
		t.Fatalf("mtime-preserving content swap served stale cache: description %q, want %q", second[0].Description, "swapped")
	}
}

// TestScanDirCachedStillCachesOnHashMatch verifies the cache still hits when
// nothing changed: a re-scan with identical content reuses the cached parse.
func TestScanDirCachedStillCachesOnHashMatch(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "alpha")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(path, []byte("---\nname: alpha\ndescription: d\n---\n\nBody.\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	fc := make(fileCache)
	prev := make(skillCache)
	if s := scanDirCached(dir, fc, prev); len(s) != 1 {
		t.Fatalf("first scan: %+v", s)
	}
	// Bump mtime so a mtime-only cache would miss; content is unchanged, so
	// the hash match should still allow the cached parse to be reused.
	future := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	if _, known := fc[path]; !known {
		t.Fatalf("expected cache entry for %s", path)
	}
	second := scanDirCached(dir, fc, prev)
	if len(second) != 1 || second[0].Description != "d" {
		t.Fatalf("second scan after unchanged content: %+v", second)
	}
}
