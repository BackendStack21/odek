package telegram

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── reopenIfRotated guard branch ─────────────────────────────────────────────

// The guard early-returns when path is empty, fileP is nil, or *fileP is nil
// (e.g. a stderr logger that never had a path).
func TestReopenIfRotated_GuardBranches(t *testing.T) {
	reopenIfRotated("", nil) // path=="" → return (also nil fileP)
	fp := (*os.File)(nil)
	reopenIfRotated(filepath.Join(t.TempDir(), "x.log"), &fp) // *fileP==nil
	var fileP *os.File
	reopenIfRotated("/some/path", &fileP) // fileP==nil
}

// ── reopenIfRotated stat-error branch ────────────────────────────────────────

// The held fd is open but the remembered path no longer exists (deleted, not
// yet rotated back): err1 != nil must keep the old fd, not crash.
func TestReopenIfRotated_PathStatError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.txt")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fp := f
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	reopenIfRotated(path, &fp)
	if fp != f {
		t.Fatal("fd must be kept when path stat fails")
	}
}

// ── reopenIfRotated reopen-failure branch ────────────────────────────────────

// Rotation detected (different inode) but reopening fails (read-only dir):
// the old fd is kept rather than swapped to a broken one.
func TestReopenIfRotated_ReopenFailsKeepsOldFd(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-based failure injection does not work as root")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "log.txt")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fp := f

	// Simulate rotation: move the file out and recreate it, then make the
	// dir read-only so the reopen OpenFile fails.
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0755) //nolint:errcheck
	reopenIfRotated(path, &fp)
	if fp != f {
		t.Fatal("old fd must be kept when reopen fails")
	}
}

// End-to-end rotation detection via the public Logger API: a rotated log
// (rename + recreate) still receives subsequent lines through the new fd.
func TestFileLogger_ReopenedAfterRotationWritesNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.txt")
	l := NewFileLogger(LogInfo, path)
	l.Info("before-rotation")
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	nf, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	nf.Close()
	l.Info("after-rotation")
	time.Sleep(10 * time.Millisecond) // appends are unbuffered but be safe
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "after-rotation") {
		t.Fatalf("rotated log missing new line:\n%s", s)
	}
	old, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(old), "before-rotation") {
		t.Fatalf("rotated-away log missing old line:\n%s", old)
	}
}

// ── NewFileLogger stderr fallback ────────────────────────────────────────────

// An unopenable path (parent dir missing) falls back to stderr — the logger
// must be non-nil and usable.
func TestNewFileLogger_StderrFallbackUsable(t *testing.T) {
	l := NewFileLogger(LogInfo, filepath.Join(t.TempDir(), "no-such-dir", "log.txt"))
	if l == nil {
		t.Fatal("nil logger on open failure")
	}
	l.Info("stderr fallback line") // must not panic
	child := l.With("k", "v")
	child.Info("child fallback line")
}
