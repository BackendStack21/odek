package bgproc

import (
	"testing"
	"time"
)

// A job whose timeout fires (or that is stopped) must report timeout/killed
// even when its shell traps TERM and exits 0 on its own. Current code lets
// the process exit code outrank the forced-stop reason, so a forced stop is
// reported as a successful "exited".
func TestRED_TimeoutTrappedExitZeroReportsTimeout(t *testing.T) {
	m := newTestManager(t, nil)
	job, err := m.Start("s1", "trap 'exit 0' TERM; while :; do sleep 0.1; done", "", 300*time.Millisecond)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, 10*time.Second, func() bool {
		j, _ := m.Get("s1", job.ID)
		return j.Status != StatusRunning && !j.EndedAt.IsZero()
	})
	j, _ := m.Get("s1", job.ID)
	if j.Status != StatusTimeout {
		t.Fatalf("status = %q (exit %d), want %q: the job was killed by its timeout, not a successful exit", j.Status, j.ExitCode, StatusTimeout)
	}
}

func TestRED_StopTrappedExitZeroReportsKilled(t *testing.T) {
	m := newTestManager(t, nil)
	job, err := m.Start("s1", "trap 'exit 0' TERM; while :; do sleep 0.1; done", "", 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	j, ok := m.Stop("s1", job.ID)
	if !ok {
		t.Fatal("Stop not found")
	}
	if j.Status != StatusKilled {
		t.Fatalf("status = %q (exit %d), want %q after Stop", j.Status, j.ExitCode, StatusKilled)
	}
}
