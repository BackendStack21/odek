package main

// Coverage v3 — serveFileLog.reopenIfRotated error branches: path stat
// failure (file removed) and reopen failure (replacement path not a
// writable regular file). Both must keep writing via the held fd without
// panicking.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServeFileLog_ReopenWhenPathMissing(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, "serve.log")
	l, err := openServeLog(p)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// Remove the path: os.Stat fails → keep the held fd, no panic.
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	l.logf("after-remove") // must not panic
}

func TestServeFileLog_ReopenFailureKeepsOldFD(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, "serve.log")
	l, err := openServeLog(p)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.logf("first")

	// Rotate, then put a DIRECTORY at the old path: reopen fails, so the
	// appender must keep the (renamed) fd instead of losing the write.
	if err := os.Rename(p, p+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	l.logf("after-failed-reopen")

	old, err := os.ReadFile(p + ".1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(old), "after-failed-reopen") {
		t.Fatalf("line lost after failed reopen; serve.log.1 = %q", string(old))
	}
}
