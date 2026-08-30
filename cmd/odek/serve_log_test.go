package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestServeRun_LogsTurnLifecycleToFile pins fix 4 of the sub-agent
// reliability work: serve must keep a durable log of turn/run lifecycle so a
// provider failure (e.g. 429 saturation) is visible after the fact instead
// of surfacing only as a missing reply in the UI. Prompt content must never
// be logged.
func TestServeRun_LogsTurnLifecycleToFile(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "serve.log")
	l, err := openServeLog(logPath)
	if err != nil {
		t.Fatalf("openServeLog: %v", err)
	}
	setServeLog(l)
	t.Cleanup(func() { setServeLog(nil); _ = l.Close() })

	// Permissions must be operator-only, even if the file pre-existed with
	// looser mode.
	if fi, err := os.Stat(logPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("serve log mode = %v (err: %v), want 0600", fi.Mode(), err)
	}

	llmSrv := alwaysUnauthorized(t)
	defer llmSrv.Close()

	env := newRestRunEnv(t, llmSrv.URL, nil)
	_, resp := startTestRun(t, env, `{"content":"log me PAPAYA","approval_timeout_seconds":10}`)
	runID, _ := resp["run_id"].(string)
	if runID == "" {
		t.Fatal("missing run_id")
	}
	waitRunStatus(t, runID, 30*time.Second)

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read serve log: %v", err)
	}
	s := string(data)
	for _, want := range []string{
		"run_started run_id=" + runID,
		"turn_started session=",
		"turn_failed session=",
		"run_finished run_id=" + runID,
		"status=failed",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("serve log missing %q\nlog:\n%s", want, s)
		}
	}
	if strings.Contains(s, "PAPAYA") {
		t.Error("serve log must never contain prompt content")
	}
}

// TestOpenServeLog_RefusesSymlink pins the symlink defense: a symlink planted
// at the log path must be rejected, never appended through.
func TestOpenServeLog_RefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "victim.txt")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "serve.log")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := openServeLog(link); err == nil {
		t.Fatal("openServeLog accepted a symlinked log path")
	}
}
