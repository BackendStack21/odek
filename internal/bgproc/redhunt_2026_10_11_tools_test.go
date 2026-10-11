package bgproc

import (
	"testing"
	"time"
)

// A job that handles SIGTERM the conventional way — exiting with 128+15
// (JVM shutdown hooks, bash `trap 'exit 143' TERM`, many supervisors) — is
// still a job that was stopped/timed out by odek. It must report
// killed/timeout, not "failed (exit 143)".
func TestRED_StopTrappedExit143ReportsKilled(t *testing.T) {
	m := newTestManager(t, nil)
	job, err := m.Start("s1", "trap 'exit 143' TERM; while :; do sleep 0.1; done", "", 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	j, ok := m.Stop("s1", job.ID)
	if !ok {
		t.Fatal("Stop not found")
	}
	if j.Status != StatusKilled {
		t.Fatalf("status = %q (exit %d, err %q), want %q after Stop", j.Status, j.ExitCode, j.Err, StatusKilled)
	}
	if j.ExitCode != 143 {
		t.Fatalf("exit code = %d, want the real 143", j.ExitCode)
	}
}

func TestRED_TimeoutTrappedExit143ReportsTimeout(t *testing.T) {
	m := newTestManager(t, nil)
	job, err := m.Start("s1", "trap 'exit 143' TERM; while :; do sleep 0.1; done", "", 300*time.Millisecond)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, 10*time.Second, func() bool {
		j, _ := m.Get("s1", job.ID)
		return j.Status != StatusRunning && !j.EndedAt.IsZero()
	})
	j, _ := m.Get("s1", job.ID)
	if j.Status != StatusTimeout {
		t.Fatalf("status = %q (exit %d), want %q: the job was killed by its timeout", j.Status, j.ExitCode, StatusTimeout)
	}
}

// A job that exits non-zero on its own, with no stop pending, is still failed.
func TestTrappedExitWithoutStopStaysFailedTools(t *testing.T) {
	m := newTestManager(t, nil)
	job, err := m.Start("s1", "exit 143", "", 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, 10*time.Second, func() bool {
		j, _ := m.Get("s1", job.ID)
		return j.Status != StatusRunning && !j.EndedAt.IsZero()
	})
	j, _ := m.Get("s1", job.ID)
	if j.Status != StatusFailed || j.ExitCode != 143 {
		t.Fatalf("status = %q exit %d, want failed 143", j.Status, j.ExitCode)
	}
}

// A non-signal failure exit after a Stop still reports failed: only the
// conventional signal exits count as handling the termination signal.
func TestStopOtherExitStaysFailedTools(t *testing.T) {
	m := newTestManager(t, nil)
	job, err := m.Start("s1", "trap 'exit 3' TERM; while :; do sleep 0.1; done", "", 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	j, _ := m.Stop("s1", job.ID)
	if j.Status != StatusFailed || j.ExitCode != 3 {
		t.Fatalf("status = %q exit %d, want failed 3", j.Status, j.ExitCode)
	}
}
