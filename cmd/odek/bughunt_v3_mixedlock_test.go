package main

// Bug-hunt v3 residual fix — mixed-version schedule lock gate.
//
// A pre-flock daemon (previous release) holds no flock, so TryLock succeeds
// even while it is alive and ticking; without a secondary pidfile-liveness
// check, a new daemon would start alongside it and double-fire every job —
// the exact failure the flock migration exists to close.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestAcquireScheduleLock_RefusesLiveForeignPid(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	pidPath := filepath.Join(home, ".odek", "schedule.pid")
	if err := os.MkdirAll(filepath.Dir(pidPath), 0o755); err != nil {
		t.Fatal(err)
	}

	// Simulate a pre-flock daemon: a live process that holds no flock,
	// recorded in the pidfile. Its cmdline contains "odek" so the Linux
	// /proc-owned check classifies it as an odek process (on non-Linux
	// platforms the check degrades to conservative-owned).
	cmd := exec.Command("sh", "-c", "sleep 30 # odek")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn sleep: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := acquireScheduleLock(); err == nil {
		t.Fatalf("acquired the schedule lock while a live pre-flock daemon (pid %d) is recorded in the pidfile — mixed-version double-fire window", cmd.Process.Pid)
	}

	// Once the foreign holder dies, acquisition succeeds (no recycled-PID
	// permanent refusal: the stale pidfile pid is dead by now).
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if rel, err := acquireScheduleLock(); err == nil {
			rel()
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("lock still refused after the foreign holder died")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
