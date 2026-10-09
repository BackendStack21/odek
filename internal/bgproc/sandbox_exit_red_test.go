package bgproc

import (
	"sync/atomic"
	"testing"
	"time"
)

// On a natural exit the host path signals the whole process group, so a
// background grandchild left behind by the job is reaped. The sandbox path
// has no such reach (the host-side docker-exec client's group does not
// contain container processes), so the sandbox follow-up kill is the only
// cleanup for container-side descendants. Current code runs that follow-up
// only after a forced stop (killed/timeout), never after a natural exit, so a
// sandboxed job that leaves a child running and exits 0 leaks the child
// past the job and past its session.
func TestRED_SandboxNaturalExitRunsFollowUp(t *testing.T) {
	var followUps atomic.Int32
	m := NewManager(Config{
		MaxOutputBytes: 1 << 20,
		SandboxWrap: func(command string) ([]string, func(), error) {
			return []string{"sh", "-c", command}, func() { followUps.Add(1) }, nil
		},
	}, nil)
	job, err := m.Start("s1", "sleep 30 & echo started", "", 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, 10*time.Second, func() bool {
		j, _ := m.Get("s1", job.ID)
		return j.Status != StatusRunning && !j.EndedAt.IsZero()
	})
	if n := followUps.Load(); n != 1 {
		t.Fatalf("sandbox follow-up ran %d times after a natural exit, want 1: container-side descendants of the job are never reaped", n)
	}
}

// The follow-up runs exactly once for a forced stop as well.
func TestSandboxStopRunsFollowUpOnce(t *testing.T) {
	var followUps atomic.Int32
	m := NewManager(Config{
		MaxOutputBytes: 1 << 20,
		SandboxWrap: func(command string) ([]string, func(), error) {
			return []string{"sh", "-c", command}, func() { followUps.Add(1) }, nil
		},
	}, nil)
	job, err := m.Start("s1", "sleep 30", "", 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	m.Stop("s1", job.ID)
	waitFor(t, 10*time.Second, func() bool {
		j, _ := m.Get("s1", job.ID)
		return j.Status != StatusRunning && !j.EndedAt.IsZero()
	})
	time.Sleep(50 * time.Millisecond)
	if n := followUps.Load(); n != 1 {
		t.Fatalf("follow-up ran %d times, want 1", n)
	}
}
