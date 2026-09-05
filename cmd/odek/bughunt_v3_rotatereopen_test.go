package main

// Bug-hunt v3 (fix/bughunt-v3) RED test — serve log appender vs rotation.
//
// rotateLogs renames serve.log to serve.log.1 and recreates an empty file,
// but serveFileLog keeps the process-lifetime O_APPEND fd opened at startup.
// After rotation the fd's inode is renamed away (gen 1) and can be UNLINKED
// by the next rotation (gen 2) — every subsequent line vanishes with it.
// The appender must detect the inode swap and reopen the path.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestServeFileLog_ReopensAfterRotation pins the reopen-on-inode-change
// contract: once the log file at the path has been swapped (the rotateLogs
// rename+recreate dance, simulated here step by step), subsequent logf
// writes must land in the CURRENT file at the path, not the stranded inode.
func TestServeFileLog_ReopensAfterRotation(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, "serve.log")

	l, err := openServeLog(p)
	if err != nil {
		t.Fatal(err)
	}
	l.logf("before-rotation")

	// Rotation generation 1: rename current → .1, recreate empty at path
	// (exactly what maintenance.rotateLogs does).
	if err := os.Rename(p, p+".1"); err != nil {
		t.Fatal(err)
	}
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); err != nil {
		t.Fatal(err)
	} else {
		f.Close()
	}

	l.logf("after-rotation")

	cur, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cur), "after-rotation") {
		t.Fatalf("post-rotation logf line missing from current serve.log (appender kept writing to the stranded pre-rotation inode); serve.log = %q", string(cur))
	}
}
