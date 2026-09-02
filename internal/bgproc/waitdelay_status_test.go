package bgproc

// Regression test for batch-3 finding B3-TOOLS-2: a job that exits 0 while
// a grandchild holds the stdout pipe hits cmd.WaitDelay; Wait then returns
// exec.ErrWaitDelay while ProcessState still carries the real exit status.
// The old classifier read only the error, reporting failed/-1 for a clean
// exit — and the same priority ordering let a Stop that raced a self-exit
// flip a success to killed.

import (
	"strings"
	"testing"
	"time"
)

func TestWaitDelayCleanExitReportedExited(t *testing.T) {
	m := newTestManager(t, nil)
	// Parent exits 0 immediately; the backgrounded sleep keeps the stdout
	// pipe open until the 3s WaitDelay expires.
	job, err := m.Start("s1", "echo bg-waitdelay-ok; sleep 30 &", "", 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	var got Job
	waitFor(t, 15*time.Second, func() bool {
		got, _ = m.Get("s1", job.ID)
		return got.Status != StatusRunning
	})
	if got.Status != StatusExited {
		t.Fatalf("BUG B3-TOOLS-2: status = %q (err=%q exit=%d), want exited — WaitDelay output truncation must not misreport a clean exit-0", got.Status, got.Err, got.ExitCode)
	}
	if got.ExitCode != 0 {
		t.Fatalf("BUG B3-TOOLS-2: exit code = %d, want 0", got.ExitCode)
	}
	out, _, err := m.Output("s1", job.ID, 0, 0)
	if err != nil || !strings.Contains(out, "bg-waitdelay-ok") {
		t.Fatalf("output = %q, %v — want the echo'ed marker", out, err)
	}
}
