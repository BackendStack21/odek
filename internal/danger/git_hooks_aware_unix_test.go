//go:build unix

package danger

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A FIFO standing in for the repository config must fail closed without the
// classifier blocking on it.
func TestGitHooksAware_FIFOConfigDoesNotBlock(t *testing.T) {
	isolateGitEnv(t)
	repo := makeRepo(t, t.TempDir(), "")
	cfg := filepath.Join(repo, ".git", "config")
	os.Remove(cfg)
	if err := syscall.Mkfifo(cfg, 0o644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	t.Chdir(repo)
	done := make(chan bool, 1)
	go func() { done <- hasEffect("git status", CodeExecution) }()
	select {
	case armed := <-done:
		if !armed {
			t.Error("FIFO config must fail closed to code_execution")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("classifier blocked reading a FIFO config")
	}
}
