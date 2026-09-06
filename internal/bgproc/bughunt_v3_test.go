package bgproc

// Bug-hunt v3 residual fix — bgproc child env stripping coverage.
//
// Config.StripEnvNames (fed by dangerous.strip_secrets_env_children) removes
// the listed names from host-mode job children; empty inherits unchanged.

import (
	"strings"
	"testing"
	"time"
)

// waitTerminal polls a bgproc job to a terminal state.
func waitTerminal(t *testing.T, m *Manager, session, jobID string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if j, ok := m.Get(session, jobID); ok && j.Status != StatusRunning {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("bg job did not reach a terminal state in time")
}

func TestManager_Start_StripsSecretEnvNames(t *testing.T) {
	t.Setenv("BGPROC_V3_SECRET", "leak-me")

	m := NewManager(Config{MaxOutputBytes: 1 << 20, StripEnvNames: []string{"BGPROC_V3_SECRET"}}, nil)
	job, err := m.Start("sess-strip", "echo value=$BGPROC_V3_SECRET", "", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	waitTerminal(t, m, "sess-strip", job.ID, 10*time.Second)

	out, _, err := m.Output("sess-strip", job.ID, 0, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "leak-me") {
		t.Fatalf("stripped secret leaked into bg job output: %q", out)
	}
	if !strings.Contains(out, "value=") {
		t.Fatalf("job did not run: %q", out)
	}

	// Unstripped control: without the knob the variable flows through.
	m2 := NewManager(Config{MaxOutputBytes: 1 << 20}, nil)
	job2, err := m2.Start("sess-strip2", "echo value=$BGPROC_V3_SECRET", "", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	waitTerminal(t, m2, "sess-strip2", job2.ID, 10*time.Second)
	out2, _, err := m2.Output("sess-strip2", job2.ID, 0, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out2, "leak-me") {
		t.Fatalf("control: variable should flow through without stripping, got %q", out2)
	}
}
