package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/BackendStack21/odek/internal/render"
)

// ── Regression (sort order handling) ────────────────────────────────────
// Two defects in sortTool.Call's direction logic:
//
//  1. desc := order == "desc" || reverse collapsed `reverse` into the
//     order flag, so order:"desc" + reverse:true — "reverse the sort
//     order" per the schema — produced descending output again instead
//     of flipping it to ascending.
//
//  2. The order value was compared case-sensitively and unknown values
//     silently sorted ascending: order:"DESC" returned a,b,c for input
//     b,a,c with no error. The schema enum is ["asc","desc"]; unknown
//     values must be rejected, not misinterpreted.

func writeSortFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.txt")
	if err := os.WriteFile(path, []byte("b\na\nc\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func sortOutput(t *testing.T, args string) string {
	t.Helper()
	var r struct {
		Output string `json:"output"`
		Error  string `json:"error"`
	}
	mustUnmarshal(t, callJSON(t, &sortTool{}, args), &r)
	return unwrapUntrusted(r.Output)
}

func TestSort_ReverseFlipsDirection(t *testing.T) {
	path := writeSortFixture(t)

	if got := sortOutput(t, `{"path":"`+path+`","order":"desc","reverse":true}`); got != "a\nb\nc" {
		t.Errorf("desc+reverse = %q, want ascending %q (reverse must flip the order)", got, "a\\nb\\nc")
	}
	if got := sortOutput(t, `{"path":"`+path+`","order":"asc","reverse":true}`); got != "c\nb\na" {
		t.Errorf("asc+reverse = %q, want descending %q", got, "c\\nb\\na")
	}
}

func TestSort_OrderValueValidation(t *testing.T) {
	path := writeSortFixture(t)

	// Case-insensitive enum match.
	if got := sortOutput(t, `{"path":"`+path+`","order":"DESC"}`); got != "c\nb\na" {
		t.Errorf(`order:"DESC" = %q, want descending (case-insensitive enum)`, got)
	}
	// Unknown values: clean error, never a silent ascending sort.
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, callJSON(t, &sortTool{}, `{"path":"`+path+`","order":"descending"}`), &r)
	if !strings.Contains(r.Error, "order") {
		t.Errorf(`order:"descending": error = %q, want an invalid-order error`, r.Error)
	}
}

// ── Regression (ToolPreview / extractJSONField) ─────────────────────────
// extractJSONField located values by the literal `"key": "` byte
// sequence, so compact JSON args ({"path":"main.go"} — no space after
// the colon, emitted constantly by models) silently lost their preview.
// The truncation sites then sliced bytes (p[:37]), which split multi-byte
// UTF-8 runes and produced invalid strings for CJK/emoji content.
func TestToolPreview_CompactJSONArgs(t *testing.T) {
	if got := render.ToolPreview("read_file", `{"path":"main.go"}`); got != "main.go" {
		t.Errorf("compact JSON preview = %q, want %q", got, "main.go")
	}
	if got := render.ToolPreview("search_files", `{"pattern":"TODO fix"}`); got != "TODO fix" {
		t.Errorf("compact JSON preview = %q, want %q", got, "TODO fix")
	}
	// Spaced form keeps working.
	if got := render.ToolPreview("read_file", `{"path": "main.go"}`); got != "main.go" {
		t.Errorf("spaced JSON preview = %q, want %q", got, "main.go")
	}
}

func TestToolPreview_TruncationIsValidUTF8(t *testing.T) {
	pattern := strings.Repeat("日本語", 20) // 180 bytes / 60 runes
	// Spaced form reaches the truncation path pre-compact-fix too.
	preview := render.ToolPreview("search_files", `{"pattern": "`+pattern+`"}`)
	if !utf8.ValidString(preview) {
		t.Errorf("preview is not valid UTF-8 (byte-sliced mid-rune): %q", preview)
	}
	if strings.ContainsRune(preview, '\uFFFD') {
		t.Errorf("preview contains U+FFFD replacement chars: %q", preview)
	}
	if !strings.HasPrefix(preview, "日本語日本語") {
		t.Errorf("preview lost its leading content: %q", preview)
	}
}

// ── Regression (tree on a symlinked directory root) ─────────────────────
// buildTree Lstat'd the root, so a symlinked directory (e.g. /tmp →
// /private/tmp on macOS, or a user's ~/link) was reported as a
// non-directory "file" whose size is the byte length of the link target —
// no children, no error. The explicitly-requested root must be resolved;
// descendants keep Lstat semantics (symlinked entries are shown, never
// followed).
func TestTree_SymlinkedDirRoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "inner.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "linked")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}

	var r struct {
		Tree struct {
			IsDir     bool `json:"is_dir"`
			FileCount int  `json:"file_count"`
			Children  []struct {
				Name string `json:"path"`
			} `json:"children"`
		} `json:"tree"`
		Error string `json:"error"`
	}
	mustUnmarshal(t, callJSON(t, &treeTool{}, `{"path":"`+link+`"}`), &r)
	if r.Error != "" {
		t.Fatalf("tree(symlinked dir) error = %q", r.Error)
	}
	if !r.Tree.IsDir {
		t.Errorf("tree(symlinked dir root) is_dir = false, want true (root must resolve)")
	}
	if len(r.Tree.Children) == 0 {
		t.Errorf("tree(symlinked dir root) has no children; the directory was not walked")
	}
}

// ── Regression (directory inputs across sibling tools) ──────────────────
// count_lines rejects directories with a clear "is a directory — use tree"
// error. head_tail and word_count lacked the guard: they surfaced raw
// scanner errors ("cannot read ... is a directory"), and word_count even
// reported the directory's stat size as `bytes` alongside the error.
func TestDirectoryInputs_ConsistentErrors(t *testing.T) {
	dir := t.TempDir()

	var ht struct {
		Results []struct {
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, callJSON(t, &headTailTool{}, `{"files":[{"path":"`+dir+`"}]}`), &ht)
	if len(ht.Results) != 1 || !strings.Contains(ht.Results[0].Error, "is a directory") {
		t.Errorf("head_tail(dir) error = %+v, want a clear is-a-directory error", ht.Results)
	}

	var wc struct {
		Results []struct {
			Error string `json:"error"`
			Bytes int64  `json:"bytes"`
		} `json:"results"`
	}
	mustUnmarshal(t, callJSON(t, &wordCountTool{}, `{"files":[{"path":"`+dir+`"}]}`), &wc)
	if len(wc.Results) != 1 || !strings.Contains(wc.Results[0].Error, "is a directory") {
		t.Errorf("word_count(dir) error = %+v, want a clear is-a-directory error", wc.Results)
	}
	if wc.Results[0].Bytes != 0 {
		t.Errorf("word_count(dir) bytes = %d, want 0 (no misleading stat size next to an error)", wc.Results[0].Bytes)
	}
}
