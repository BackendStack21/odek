package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
)

// ── Bug 1: glob / search_files(target=files) promise newest-first, but
// confinedGlob used to stop the walk at `limit` in lexical order, so files
// modified recently but sitting late in the walk never reached the mtime
// sort. The fix collects up to maxGlobWalkMatches, sorts by mtime, and only
// then truncates to the caller-visible limit.

// writeOrderedFixture writes n files f00.txt..f(N-1).txt with strictly
// increasing mtimes: f00 oldest, f(N-1) newest. One minute of separation
// defeats filesystem mtime granularity.
func writeOrderedFixture(t *testing.T, dir string, n int) {
	t.Helper()
	base := time.Now().Add(-time.Duration(n+1) * time.Minute)
	for i := 0; i < n; i++ {
		name := filepath.Join(dir, fmt.Sprintf("f%02d.txt", i))
		if err := os.WriteFile(name, []byte("x"), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		stamp := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(name, stamp, stamp); err != nil {
			t.Fatalf("chtimes %s: %v", name, err)
		}
	}
}

func TestGlob_NewestFirstWinsOverWalkOrder(t *testing.T) {
	dir := t.TempDir()
	// 60 files > the default glob limit (50): truncation must keep the 50
	// NEWEST, not the lexically-first 50.
	writeOrderedFixture(t, dir, 60)

	tool := &globTool{dangerousConfig: danger.DangerousConfig{}}
	result := callJSON(t, tool, fmt.Sprintf(`{"pattern":"*.txt","path":%q}`, dir))
	var r struct {
		Matches []globMatch `json:"matches"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Matches) != 50 {
		t.Fatalf("matches = %d, want 50 (post-sort truncation)", len(r.Matches))
	}
	if got := filepath.Base(unwrapUntrusted(r.Matches[0].Path)); got != "f59.txt" {
		t.Fatalf("first match = %s, want f59.txt (newest file was lost to the lexical walk truncation)", got)
	}
	if got := filepath.Base(unwrapUntrusted(r.Matches[len(r.Matches)-1].Path)); got != "f10.txt" {
		t.Fatalf("last match = %s, want f10.txt (oldest of the newest 50)", got)
	}
}

func TestSearchFilesFiles_NewestFirstWinsOverWalkOrder(t *testing.T) {
	dir := t.TempDir()
	writeOrderedFixture(t, dir, 60)

	tool := &searchFilesTool{dangerousConfig: danger.DangerousConfig{}}
	result := callJSON(t, tool, fmt.Sprintf(`{"pattern":"*.txt","path":%q,"target":"files"}`, dir))
	var r struct {
		Matches []struct {
			Path string `json:"path"`
		} `json:"matches"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Matches) != 50 {
		t.Fatalf("matches = %d, want 50", len(r.Matches))
	}
	if got := filepath.Base(unwrapUntrusted(r.Matches[0].Path)); got != "f59.txt" {
		t.Fatalf("first match = %s, want f59.txt", got)
	}
}

// ── Bug 2: isBinary must not classify multi-byte UTF-8 text as binary. The
// old ratio heuristic counted every byte >= 0x7F as non-printable, so
// Russian/CJK prose was rejected as binary by read_file / batch_read.

const cyrillicProse = "Съешь же ещё этих мягких французских булок, да выпей чаю. "

func TestIsBinary_UTF8TextNotBinary(t *testing.T) {
	if isBinary([]byte(cyrillicProse)) {
		t.Errorf("Cyrillic UTF-8 prose classified as binary")
	}
	// Longer than binarySampleLen so the sample cut lands inside a
	// multi-byte rune — the trim-to-rune-boundary path must not misread it.
	if isBinary([]byte(strings.Repeat(cyrillicProse, 200))) {
		t.Errorf("long Cyrillic UTF-8 prose (sample cut mid-rune) classified as binary")
	}
	if !isBinary([]byte("plain text\x00with a NUL byte")) {
		t.Errorf("NUL-containing content not detected as binary")
	}
}

func TestBatchRead_CyrillicTextFileNotBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ru.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat(cyrillicProse, 100)), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	tool := &batchReadTool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"files":[{"path":%q}]}`, path))
	var r struct {
		Results []struct {
			Path    string `json:"path"`
			Content string `json:"content"`
			Error   string `json:"error,omitempty"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(r.Results))
	}
	if r.Results[0].Error != "" {
		t.Fatalf("batch_read misclassified Cyrillic text as binary: %s", r.Results[0].Error)
	}
	if !strings.Contains(r.Results[0].Content, "Съешь") {
		t.Errorf("batch_read returned no content for a UTF-8 text file")
	}
}

// ── Bug 3: tree must apply the same per-discovered-path skip rules as the
// search tools (checkSearchPath). tree($HOME, include_hidden=true) used to
// list ~/.odek, ~/.ssh, … because only the requested root was classified.

func TestTree_SkipsSensitiveHiddenDirs(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("get home dir: %v", err)
	}
	// Same environmental guard as read_symlink_test.go: when HOME is a temp
	// dir, the temp-dir rule (local_write) outranks the ~/.odek trust-anchor
	// rule and the deny policy never fires. Environmental, not a regression.
	if strings.HasPrefix(home, "/tmp") || strings.HasPrefix(home, "/var/folders") {
		t.Skip("HOME is a temp dir — ~/.odek does not classify as system_write there")
	}
	if err := os.MkdirAll(filepath.Join(home, ".odek"), 0700); err != nil {
		t.Fatalf("ensure ~/.odek: %v", err)
	}

	// Deny system_write so sensitive children are skipped, never prompted.
	dc := danger.DangerousConfig{
		Classes: map[danger.RiskClass]danger.Action{
			danger.SystemWrite: danger.Deny,
		},
	}
	tool := &treeTool{dangerousConfig: dc}
	result := callJSON(t, tool, fmt.Sprintf(`{"path":%q,"max_depth":1,"include_hidden":true}`, home))

	var r struct {
		Tree  treeEntry `json:"tree"`
		Error string    `json:"error,omitempty"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error != "" {
		t.Fatalf("tree error: %s", r.Error)
	}
	for _, child := range r.Tree.Children {
		if unwrapUntrusted(child.Path) == ".odek" {
			t.Fatalf("tree listed ~/.odek although the search tools would skip it")
		}
	}
}
