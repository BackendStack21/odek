package main

import (
	"context"
	"fmt"
	"github.com/BackendStack21/odek/internal/session"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
)

// ────────────────────────────────────────────────────────────────────────
// tr "char" mode falls back to whole-string ReplaceAll when
// from/to have equal length or to is a single char — so the classic POSIX
// char map tr 'abc' 'xyz' is a no-op. The Map path also indexes To by
// BYTE offset with a RUNE index.
func TestRED_GlobStarStarMatchesRootFiles(t *testing.T) {
	re, err := globToRegex("**/*.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !re.MatchString("root.txt") {
		t.Errorf("glob **/*.txt must match root-level root.txt (regex: %s)", re.String())
	}
	if !re.MatchString("sub/nested.txt") {
		t.Errorf("glob **/*.txt must match sub/nested.txt")
	}
	re2, err := globToRegex("a/**/b.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !re2.MatchString("a/b.txt") {
		t.Errorf("glob a/**/b.txt must match a/b.txt (zero dirs)")
	}

	// End-to-end through the tool.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "root.txt"), []byte("r"), 0o644)
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub", "nested.txt"), []byte("n"), 0o644)

	gtool := &globTool{}
	res := callJSON(t, gtool, fmt.Sprintf(`{"pattern":"**/*.txt","path":%q}`, dir))
	var gr struct {
		Matches []struct {
			Path string `json:"path"`
		} `json:"matches"`
	}
	mustUnmarshal(t, res, &gr)
	foundRoot, foundNested := false, false
	for _, m := range gr.Matches {
		// Paths are wrapped in <untrusted_content_*> tags, so match on
		// containment rather than suffix.
		if strings.Contains(m.Path, "root.txt") {
			foundRoot = true
		}
		if strings.Contains(m.Path, "nested.txt") {
			foundNested = true
		}
	}
	if !foundRoot || !foundNested {
		t.Errorf("glob **/*.txt matches missing (root=%v nested=%v)", foundRoot, foundNested)
	}
}

// ────────────────────────────────────────────────────────────────────────
// file_info classifies the RAW path while every sibling read
// tool classifies the symlink-resolved one — a directory symlink into a
// sensitive tree bypasses the approval gate for metadata access.
func TestRED_FileInfoClassifiesResolvedSymlinkPath(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink("/etc", link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	if _, err := os.Stat("/etc/passwd"); err != nil {
		t.Skip("/etc/passwd unavailable")
	}

	cfg := danger.DangerousConfig{
		Classes: map[danger.RiskClass]danger.Action{danger.SystemWrite: danger.Deny},
	}
	tool := &fileInfoTool{dangerousConfig: cfg}
	res := callJSON(t, tool, fmt.Sprintf(`{"path":%q}`, filepath.Join(link, "passwd")))

	var r struct {
		Error     string `json:"error"`
		SizeBytes int64  `json:"size"`
	}
	mustUnmarshal(t, res, &r)
	if r.Error == "" {
		t.Errorf("file_info via symlinked dir into /etc succeeded (size=%d); expected SystemWrite denial like resolved-path tools", r.SizeBytes)
	}
}

// ────────────────────────────────────────────────────────────────────────
// buildTree truncates entries BEFORE the hidden-file filter,
// so in hidden-heavy directories the truncation budget is consumed by
// entries that are filtered out anyway and visible files vanish; the
// notice also claims entries were shown that were not.
func TestRED_TreeFiltersHiddenBeforeTruncating(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < maxTreeEntries; i++ { // hidden files fill the raw cap exactly
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf(".h%04d", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	vis1 := filepath.Join(dir, "aaa_visible.txt")
	vis2 := filepath.Join(dir, "zzz_visible.txt")
	os.WriteFile(vis1, []byte("a"), 0o644)
	os.WriteFile(vis2, []byte("z"), 0o644)

	tool := &treeTool{}
	res := callJSON(t, tool, fmt.Sprintf(`{"path":%q,"max_depth":1}`, dir))
	var r struct {
		Tree struct {
			Children []struct {
				Path string `json:"path"`
			} `json:"children"`
			ErrMsg string `json:"error"`
		} `json:"tree"`
	}
	mustUnmarshal(t, res, &r)

	if r.Tree.ErrMsg != "" {
		t.Errorf("unexpected truncation notice: %q (hidden entries must be filtered before the cap applies)", r.Tree.ErrMsg)
	}
	if len(r.Tree.Children) != 2 {
		names := make([]string, len(r.Tree.Children))
		for i, c := range r.Tree.Children {
			names[i] = c.Path
		}
		t.Errorf("children = %d (%v), want both visible files", len(r.Tree.Children), names)
	}
	sawA, sawZ := false, false
	for _, c := range r.Tree.Children {
		if strings.Contains(c.Path, "aaa_visible") {
			sawA = true
		}
		if strings.Contains(c.Path, "zzz_visible") {
			sawZ = true
		}
	}
	if !sawA || !sawZ {
		t.Errorf("visible files lost in tree output (aaa=%v zzz=%v)", sawA, sawZ)
	}
}

// ────────────────────────────────────────────────────────────────────────
// count_lines adds +1 per scanned line unconditionally, so
// files without a trailing newline overcount chars vs their own bytes.
func TestRED_BatchReadRejectsNegativeOffset(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	os.WriteFile(p, []byte("l1\nl2\nl3\n"), 0o644)

	tool := &batchReadTool{}
	res := callJSON(t, tool, fmt.Sprintf(`{"files":[{"path":%q,"offset":-5}]}`, p))
	var r struct {
		Results []struct {
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, res, &r)
	if len(r.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(r.Results))
	}
	if r.Results[0].Error == "" {
		t.Error("batch_read accepted negative offset; want rejection consistent with read_file")
	}
}

// ────────────────────────────────────────────────────────────────────────
// Repeatable --ctx flags overwrite instead of accumulate
// so `odek run --ctx a.txt --ctx b.txt` silently drops a.txt.
func TestRED_ParseRunFlagsCtxAccumulates(t *testing.T) {
	f, err := parseRunFlags([]string{"--ctx", "a.txt", "--ctx", "b.txt", "do work"})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Ctx) != 2 || f.Ctx[0] != "a.txt" || f.Ctx[1] != "b.txt" {
		t.Errorf("Ctx = %v, want [a.txt b.txt]", f.Ctx)
	}
}

// ────────────────────────────────────────────────────────────────────────
// The wrapper-source desanitiser maps ' → " even though the
// sanitiser never maps " → anything but '. Every literal apostrophe in a
// shell command source is corrupted, breaking audit source matching.
func TestRED_UntrustedSourceDesanitiseLossless(t *testing.T) {
	src := `$ grep 'TODO' main.go`
	wrapped := wrapUntrusted(context.Background(), src, "body")
	got := untrustedSourcesAll(wrapped)
	if len(got) != 1 {
		t.Fatalf("sources = %v, want 1", got)
	}
	if got[0] != src {
		t.Errorf("desanitised source = %q, want original %q", got[0], src)
	}
}

// ────────────────────────────────────────────────────────────────────────
// wsApprover.Cancel closes the cancel channel under a
// select/default race guard — concurrent calls can double-close and panic
// the serve process ("Safe to call multiple times" only holds
// sequentially).
func TestRED_WSApproverCancelConcurrentIdempotent(t *testing.T) {
	// Each round races several fresh approvers so at least two callers sit
	// inside the select/default window before any close lands. A sync.Once-
	// style implementation can never fail here; the select/default idiom
	// eventually double-closes.
	const rounds = 3000
	panicCh := make(chan string, 1)
	for r := 0; r < rounds; r++ {
		a := newWSApprover(func(v any) error { return nil })
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					if rec := recover(); rec != nil {
						select {
						case panicCh <- fmt.Sprint(rec):
						default:
						}
					}
				}()
				<-start
				a.Cancel()
			}()
		}
		close(start)
		wg.Wait()
		select {
		case msg := <-panicCh:
			t.Fatalf("concurrent Cancel panicked: %v", msg)
		default:
		}
	}
}

// Regression: auditTurnDelta must clamp when the engine trimmed history
// in place during the run — the pre-run histLen can exceed the returned
// slice and the old inline `allMessages[histLen:]` panicked.
func TestRED_AuditTurnDeltaClampsTrimmedHistory(t *testing.T) {
	msgs := []session.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
		{Role: "assistant", Content: "answer"},
	}
	if got := auditTurnDelta(msgs, 20); len(got) != 2 || got[0].Content != "task" {
		t.Errorf("auditTurnDelta(len=%d, histLen=20) = %v, want newest user turn", len(msgs), got)
	}
	delta := auditTurnDelta(msgs, 1)
	if len(delta) != 2 || delta[0].Content != "task" {
		t.Errorf("auditTurnDelta(histLen=1) = %+v, want messages[1:]", delta)
	}
	if got := auditTurnDelta(nil, 0); got != nil {
		t.Errorf("auditTurnDelta(nil, 0) = %v, want nil", got)
	}
	withNotice := append(append([]session.Message{}, msgs...),
		session.Message{Role: "user", Name: "bg-notice", Content: "background complete"})
	if got := auditTurnDelta(withNotice, 1); len(got) != 3 || got[0].Content != "task" {
		t.Errorf("auditTurnDelta selected synthetic background notice: %+v", got)
	}
}
