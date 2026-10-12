package agent

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// LoadProjectFile Lstat-checks AGENTS.md for symlinks and size, then calls
// os.ReadFile without requiring a regular file. A project (untrusted
// checkout / extracted archive) that ships AGENTS.md as a named pipe makes
// odek block forever at agent construction — every New() on that cwd hangs
// before the first prompt (CLI, serve REST runs, sub-agents).
func TestRED_RuntimeLoadProjectFileFIFOHangs(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, ProjectFileName), 0o644); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	t.Chdir(dir)

	done := make(chan string, 1)
	go func() { done <- LoadProjectFile() }()
	select {
	case got := <-done:
		if got != "" {
			t.Fatalf("FIFO AGENTS.md produced content %q", got)
		}
	case <-time.After(2 * time.Second):
		// Unblock the stuck reader so the test process can exit cleanly.
		if f, err := os.OpenFile(filepath.Join(dir, ProjectFileName), os.O_WRONLY, 0); err == nil {
			f.Close()
		}
		<-done
		t.Fatal("LoadProjectFile blocked on a FIFO AGENTS.md; a non-regular project file must be refused")
	}
}
