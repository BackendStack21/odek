package schedule

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestReadJSONRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(target, []byte(`{"version":1,"jobs":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, schedulesFile)
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	var doc scheduleDoc
	if err := readJSON(path, &doc); err == nil {
		t.Fatal("readJSON followed symlink")
	}
}

func TestReadJSONDoesNotBlockOnFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), schedulesFile)
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Skipf("FIFO unavailable: %v", err)
	}
	done := make(chan error, 1)
	go func() { var doc scheduleDoc; done <- readJSON(path, &doc) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("readJSON accepted FIFO")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("readJSON blocked opening FIFO")
	}
}
