package main

// Regression test for batch-3 finding B3-TOOLS-3: bg_status's description
// promises "exit code, duration, and output size" but the handler never
// emitted output_bytes, so the model cannot decide whether bg_output is
// worth calling.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/bgproc"
)

func TestBGStatus_ReportsOutputBytes(t *testing.T) {
	mgr := bgproc.NewManager(bgproc.Config{MaxJobsPerSession: 4, MaxOutputBytes: 1 << 20}, nil)
	rt := &bgRuntime{mgr: mgr, session: "s-bgtest"}

	started, err := mgr.Start(rt.session, "echo bg-status-bytes", "", 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		job, ok := mgr.Get(rt.session, started.ID)
		if !ok {
			t.Fatal("job disappeared from the registry")
		}
		if job.Status != bgproc.StatusRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job did not finish within 10s")
		}
		time.Sleep(20 * time.Millisecond)
	}

	out, err := (&bgStatusTool{rt: rt}).Call(`{"job_id":"` + started.ID + `"}`)
	if err != nil {
		t.Fatalf("bg_status Call: %v", err)
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(out), &entry); err != nil {
		t.Fatalf("decode bg_status response %q: %v", out, err)
	}
	size, ok := entry["output_bytes"].(float64)
	if !ok {
		t.Fatalf("BUG B3-TOOLS-3: bg_status response missing output_bytes: %s", out)
	}
	if size <= 0 {
		t.Fatalf("BUG B3-TOOLS-3: output_bytes = %v, want > 0 for a job with stdout", size)
	}
}
