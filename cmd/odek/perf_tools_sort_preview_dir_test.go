package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/BackendStack21/odek/internal/render"
)

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
// head_tail rejects directories with a clear "is a directory — use tree"
// error instead of surfacing raw scanner errors.
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
}
