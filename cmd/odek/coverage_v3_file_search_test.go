package main

// Coverage v3 — searchContent residual skip branches: maxSearchResultBytes
// break, scanner error (over-long line), and unreadable file (symlink →
// O_NOFOLLOW ELOOP).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchContent_ResultBytesCapBreaksWalk(t *testing.T) {
	dir := t.TempDir()
	// Two matching lines of ~700 KB each: the second exceeds the 1 MiB
	// result-byte cap and must trip the break (walk stops, no error).
	var b strings.Builder
	b.WriteString("needle ")
	for i := 0; i < 70000; i++ {
		b.WriteString("0123456789")
	}
	line := b.String()
	os.WriteFile(filepath.Join(dir, "big.txt"), []byte(line+"\n"+line+"\n"), 0644)

	tool := &searchFilesTool{}
	out, err := tool.searchContent(searchFilesArgs{Pattern: "needle", Path: dir, Limit: 50})
	if err != nil {
		t.Fatalf("searchContent: %v", err)
	}
	if !strings.Contains(out, `"matches"`) {
		t.Fatalf("expected matches in output: %s", out)
	}
	if strings.Contains(out, "search failed") {
		t.Fatalf("result-byte cap must break, not error: %s", out)
	}
	// Only the first ~700 KB line fits under the 1 MiB cap.
	if got := strings.Count(out, `"line":1`); got != 1 {
		t.Fatalf("want exactly 1 match under the byte cap, got %d: %s", got, out[:200])
	}
}

func TestSearchContent_ScannerErrorOnOverlongLine(t *testing.T) {
	dir := t.TempDir()
	// A single line beyond the 1 MiB scanner buffer: bufio.ErrTooLong must
	// surface the file as skipped rather than silently dropping it.
	var b strings.Builder
	b.WriteString("needle ")
	for i := 0; i < 120000; i++ {
		b.WriteString("0123456789")
	}
	os.WriteFile(filepath.Join(dir, "longline.txt"), []byte(b.String()+"\n"), 0644)

	tool := &searchFilesTool{}
	out, err := tool.searchContent(searchFilesArgs{Pattern: "needle", Path: dir, Limit: 50})
	if err != nil {
		t.Fatalf("searchContent: %v", err)
	}
	if !strings.Contains(out, `"skipped"`) || !strings.Contains(out, "longline.txt") {
		t.Fatalf("over-long line should be reported as skipped, got: %s", out)
	}
}

func TestSearchContent_UnopenableFileSkipped(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission-based open failure unreachable")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "real.txt"), []byte("needle here\n"), 0644)
	// A permission-denied file: the O_NOFOLLOW open fails → the file lands
	// in skipped, the walk continues to the real file.
	p := filepath.Join(dir, "unreadable.txt")
	os.WriteFile(p, []byte("needle secret\n"), 0644)
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatal(err)
	}

	tool := &searchFilesTool{}
	out, err := tool.searchContent(searchFilesArgs{Pattern: "needle", Path: dir, Limit: 50})
	if err != nil {
		t.Fatalf("searchContent: %v", err)
	}
	if !strings.Contains(out, "unreadable.txt") {
		t.Fatalf("open failure should be surfaced as skipped: %s", out)
	}
	if !strings.Contains(out, "real.txt") {
		t.Fatalf("walk must continue past the skipped file: %s", out)
	}
}
