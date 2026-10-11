package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/mcpclient"
)

// serveFixNewRun builds a REST run wired to a wsApprover exactly like
// startServeRun does: typed approvalRequest values go through
// recordApprovalRequest, map frames through record.
func serveFixNewRun() (*serveRun, *wsApprover) {
	run := &serveRun{ID: "run-redhunt", Status: "running", pending: map[string]*approvalRequest{}}
	run.cond = sync.NewCond(&run.mu)
	approver := newWSApprover(func(v any) error {
		if ar, ok := v.(approvalRequest); ok {
			run.recordApprovalRequest(ar)
			return nil
		}
		return run.record(v)
	})
	run.approver = approver
	return run, approver
}

// An approval that times out on a headless REST run must leave the run's
// pending list and stop reporting waiting_approval: the loop has already
// moved on (the tool was denied by timeout) and answering the stale id
// returns 404.
func TestRED_RunApprovalExpiryLeavesStaleWaitingApproval(t *testing.T) {
	run, approver := serveFixNewRun()
	approver.SetApprovalTimeout(50 * time.Millisecond)

	err := approver.PromptCommand(danger.SystemWrite, "touch /etc/x", "")
	if err == nil {
		t.Fatal("PromptCommand returned nil; want an approval timeout error")
	}

	snap := run.snapshot(false)
	pending, _ := snap["pending_approvals"].([]map[string]any)
	if snap["status"] != "running" || len(pending) != 0 {
		t.Fatalf("after approval expiry: status=%v pending=%d; want status=running and no pending approvals (the approval is gone: answering it returns 404)", snap["status"], len(pending))
	}
}

// The per-session wake rate limit must count each wake once. dispatch
// filters the timestamp window in place into the same backing array but
// does not store the filtered slice back, so an expired entry is replaced
// by a duplicate of a live one and the next successful wake keeps it.
func TestRED_WakeRateLimitDoubleCountsAfterWindowSlides(t *testing.T) {
	router := newFakeWakeRouter(wakeIdle)
	d := newTestDispatcher(router, time.Millisecond, 3)
	defer d.Stop()

	now := time.Now()
	d.mu.Lock()
	// One wake outside the trailing hour, one inside it.
	d.wakes["sess-1"] = []time.Time{now.Add(-61 * time.Minute), now.Add(-30 * time.Minute)}
	d.pending["sess-1"] = 1
	d.mu.Unlock()

	d.dispatch("sess-1")
	if n := router.postCount(); n != 1 {
		t.Fatalf("first dispatch posted %d wakes, want 1", n)
	}

	d.mu.Lock()
	got := len(d.wakes["sess-1"])
	d.mu.Unlock()
	// Real wakes inside the trailing hour: the -30m one and the one just sent.
	if got != 2 {
		t.Fatalf("recorded wakes in the trailing hour = %d, want 2 (a live timestamp was duplicated)", got)
	}

	// With max 3/h and only 2 real wakes in the window, the next wake must go out.
	d.mu.Lock()
	d.pending["sess-1"] = 1
	d.mu.Unlock()
	d.dispatch("sess-1")
	if n := router.postCount(); n != 2 {
		t.Fatalf("second dispatch: posts=%d, want 2 (only 2 of 3 hourly wakes were used)", n)
	}
}

// newServeAgent must stop the MCP subprocesses it already started when a
// later construction step fails. With sandbox enabled and Docker
// unavailable, setupSandbox fails AFTER loadMCPTools spawned the servers,
// and the error return drops mcpCleanup — every WebUI (re)connect leaks one
// set of MCP server processes.
func TestRED_ServeAgentSandboxFailureLeaksMCPServers(t *testing.T) {
	fake := fakeMCPPath(t) // build before PATH is emptied
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "mcp.pid")
	script := filepath.Join(dir, "mcp.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho $$ > "+pidFile+"\nexec "+fake+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", t.TempDir())
	resolved := loadJSONMockResolved()
	resolved.Sandbox = true
	resolved.SandboxImage = "alpine:latest" // skip the Dockerfile approval path
	resolved.MCPServers = map[string]mcpclient.ServerConfig{
		"leaky": {Command: script, AutoApprove: true},
	}
	// No docker binary reachable: the sandbox step fails deterministically.
	t.Setenv("PATH", t.TempDir())

	agent, _, _, _, _, _, _, err := newServeAgent(resolved, "sys", "run-leak", func(any) error { return nil }, &wsDeltaCounters{}, false, nil)
	if err == nil {
		agent.Close()
		t.Skip("sandbox setup unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "sandbox") {
		t.Fatalf("newServeAgent error = %v, want a sandbox failure", err)
	}

	raw, rerr := os.ReadFile(pidFile)
	if rerr != nil {
		t.Fatalf("MCP server never started (no pidfile): %v", rerr)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	if pid <= 0 {
		t.Fatalf("bad pid %q", raw)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return // process gone: cleaned up
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("MCP server pid %d still running 3s after newServeAgent returned %q: subprocess leaked", pid, err)
}
