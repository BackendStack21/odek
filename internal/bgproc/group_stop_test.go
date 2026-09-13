package bgproc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestStopEscalatesAfterLeaderExits(t *testing.T) {
	m := newTestManager(t, nil)
	marker := filepath.Join(t.TempDir(), "child-pid")
	command := fmt.Sprintf(`sh -c 'trap "" TERM; echo $$ > %s; while :; do sleep 1; done' >/dev/null 2>&1 & wait`, marker)
	job, err := m.Start("owned", command, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	entry := m.findLocked("owned", job.ID)
	pgid := entry.cmd.Process.Pid
	m.mu.Unlock()
	t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
	var child int
	waitFor(t, 2*time.Second, func() bool {
		b, err := os.ReadFile(marker)
		if err != nil {
			return false
		}
		child, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		return child > 0
	})
	start := time.Now()
	stopped, _ := m.Stop("owned", job.ID)
	if stopped.Status != StatusKilled {
		t.Fatalf("status = %s: %s", stopped.Status, stopped.Err)
	}
	if time.Since(start) < stopGrace {
		t.Fatal("returned before the resistant group was escalated")
	}
	waitFor(t, time.Second, func() bool { return !processRunning(child) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := m.ShutdownContext(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestLeaderExitCleansUpRemainingChildren(t *testing.T) {
	m := newTestManager(t, nil)
	marker := filepath.Join(t.TempDir(), "child-pid")
	job, err := m.Start("owned", fmt.Sprintf("sleep 30 >/dev/null 2>&1 & echo $! > %s", marker), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var child int
	waitFor(t, 2*time.Second, func() bool {
		b, err := os.ReadFile(marker)
		if err != nil {
			return false
		}
		child, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		return child > 0
	})
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
	waitFor(t, stopGrace+time.Second, func() bool { j, _ := m.Get("owned", job.ID); return j.Status == StatusExited })
	waitFor(t, time.Second, func() bool { return !processRunning(child) })
}

func processRunning(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	// A dead orphan can remain a zombie briefly in Linux containers whose
	// init is slow to reap. It cannot execute any further work.
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		if end := strings.LastIndexByte(string(b), ')'); end >= 0 {
			fields := strings.Fields(string(b[end+1:]))
			if len(fields) > 0 && fields[0] == "Z" {
				return false
			}
		}
	}
	return true
}
