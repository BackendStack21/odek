package telegram

// Bug-hunt v3 (fix/bughunt-v3) RED test — fileLogger vs log rotation.
//
// maintenance.rotateLogs renames telegram.log and recreates it, but
// fileLogger holds the process-lifetime O_APPEND fd from NewFileLogger.
// After rotation the fd points at the renamed (later unlinked) inode and
// every subsequent log line is lost. The logger must detect the inode swap
// and reopen the path.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFileLogger_ReopensAfterRotation pins the reopen-on-inode-change
// contract for the Telegram file logger.
func TestFileLogger_ReopensAfterRotation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "telegram.log")

	fl := NewFileLogger(LogInfo, p)
	fl.Info("before-rotation")

	// Rotation, as maintenance.rotateLogs performs it.
	if err := os.Rename(p, p+".1"); err != nil {
		t.Fatal(err)
	}
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); err != nil {
		t.Fatal(err)
	} else {
		f.Close()
	}

	fl.Info("after-rotation")

	cur, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cur), "after-rotation") {
		t.Fatalf("post-rotation log line missing from current telegram.log (logger kept writing to the stranded pre-rotation inode); telegram.log = %q", string(cur))
	}
}
