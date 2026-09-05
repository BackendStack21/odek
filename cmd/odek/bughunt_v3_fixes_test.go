package main

// RED-first tests for the bughunt-v3 perf/file tool fixes:
//  1. tr string transform unbounded expansion
//  2. base64 decode unwrapped output
//  3. search/multi_grep silently skipping unopenable files (fd pressure)
//  4. unwrapped FS-derived match paths (glob, searchFiles, multiGrep)

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 1. tr: a small input plus a large replacement must be rejected, not expanded.
func TestTr_RejectsUnboundedStringExpansion(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-based test unreliable as root")
	}
	big := strings.Repeat("x", 3<<20) // 3 MiB replacement, 4 occurrences → ~12 MiB
	tool := &trTool{}
	args := fmt.Sprintf(`{"content":"aaaa","transformations":[{"type":"string","from":"a","to":%q}]}`, big)
	result := callJSON(t, tool, args)

	var r struct {
		Result string `json:"result"`
		Error  string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatalf("expected expansion-cap error, got result of %d bytes", len(r.Result))
	}
	if len(r.Result) > 1<<20 {
		t.Errorf("result should not contain the expanded output")
	}
}

// 2. base64: decoded strings cross the trust boundary like every other
// tool output and must be wrapped.
func TestBase64_WrapsDecodedString(t *testing.T) {
	tool := &base64Tool{}
	result := callJSON(t, tool, `{"string":"aGVsbG8=","decode":true}`)
	var r struct {
		Decoded string `json:"decoded"`
	}
	mustUnmarshal(t, result, &r)
	if !strings.HasPrefix(r.Decoded, "<untrusted_content_") {
		t.Fatalf("base64 decode output should be wrapped in untrusted·content, got %q", r.Decoded)
	}
	if !strings.Contains(r.Decoded, "hello") {
		t.Errorf("decoded content should still contain 'hello'")
	}
}

// 3. search_files must surface files it could not open instead of
// silently dropping them (silent drops also masked the fd-leak symptom).
func TestSearchFiles_ReportsUnopenableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-based test unreliable as root")
	}
	dir := t.TempDir()
	good := filepath.Join(dir, "good.txt")
	os.WriteFile(good, []byte("needle here\n"), 0644)
	bad := filepath.Join(dir, "bad.txt")
	os.WriteFile(bad, []byte("needle secret\n"), 0644)
	if err := os.Chmod(bad, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(bad, 0644) })

	tool := &searchFilesTool{}
	args := fmt.Sprintf(`{"pattern":"needle","target":"content","path":%q}`, dir)
	result := callJSON(t, tool, args)
	var r struct {
		Matches []struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		} `json:"matches"`
		Skipped []string `json:"skipped"`
	}
	mustUnmarshal(t, result, &r)
	found := false
	for _, s := range r.Skipped {
		if strings.Contains(s, "bad.txt") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected bad.txt in skipped list, got skipped=%v matches=%d", r.Skipped, len(r.Matches))
	}
	if len(r.Matches) != 1 {
		t.Errorf("expected 1 match from good.txt, got %d", len(r.Matches))
	}
}

// 3b. multi_grep: same silent-skip fix.
func TestMultiGrep_ReportsUnopenableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-based test unreliable as root")
	}
	dir := t.TempDir()
	good := filepath.Join(dir, "good.txt")
	os.WriteFile(good, []byte("needle here\n"), 0644)
	bad := filepath.Join(dir, "bad.txt")
	os.WriteFile(bad, []byte("needle secret\n"), 0644)
	if err := os.Chmod(bad, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(bad, 0644) })

	tool := &multiGrepTool{}
	args := fmt.Sprintf(`{"patterns":["needle"],"path":%q}`, dir)
	result := callJSON(t, tool, args)
	var r struct {
		Results []struct {
			Pattern string   `json:"pattern"`
			Matches []struct {
				Path string `json:"path"`
			} `json:"matches"`
			Skipped []string `json:"skipped"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(r.Results))
	}
	found := false
	for _, s := range r.Results[0].Skipped {
		if strings.Contains(s, "bad.txt") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected bad.txt in skipped list, got %v", r.Results[0].Skipped)
	}
}

// 4. FS-derived match paths must be wrapped like sibling outputs.
func TestGlob_WrapsMatchPaths(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0644)

	tool := &globTool{}
	args := fmt.Sprintf(`{"pattern":"*.txt","path":%q}`, dir)
	result := callJSON(t, tool, args)
	var r struct {
		Matches []struct {
			Path string `json:"path"`
		} `json:"matches"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Matches) == 0 {
		t.Fatal("expected 1 match")
	}
	if !strings.HasPrefix(r.Matches[0].Path, "<untrusted_content_") {
		t.Errorf("glob match path should be wrapped, got %q", r.Matches[0].Path)
	}
}

func TestSearchFiles_GrepWrapsMatchPath(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0644)

	tool := &searchFilesTool{}
	args := fmt.Sprintf(`{"pattern":"hello","target":"content","path":%q}`, dir)
	result := callJSON(t, tool, args)
	var r struct {
		Matches []struct {
			Path string `json:"path"`
		} `json:"matches"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Matches) == 0 {
		t.Fatal("expected 1 match")
	}
	if !strings.HasPrefix(r.Matches[0].Path, "<untrusted_content_") {
		t.Errorf("search_files match path should be wrapped, got %q", r.Matches[0].Path)
	}
}

func TestMultiGrep_WrapsMatchPath(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("needle\n"), 0644)

	tool := &multiGrepTool{}
	args := fmt.Sprintf(`{"patterns":["needle"],"path":%q}`, dir)
	result := callJSON(t, tool, args)
	var r struct {
		Results []struct {
			Matches []struct {
				Path string `json:"path"`
			} `json:"matches"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Results) != 1 || len(r.Results[0].Matches) == 0 {
		t.Fatal("expected 1 match")
	}
	if !strings.HasPrefix(r.Results[0].Matches[0].Path, "<untrusted_content_") {
		t.Errorf("multi_grep match path should be wrapped, got %q", r.Results[0].Matches[0].Path)
	}
}
