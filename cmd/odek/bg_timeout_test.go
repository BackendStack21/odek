package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
)

// bg_start multiplies the model-supplied seconds by time.Second in an int64
// Duration; an enormous value wraps negative and is treated as "no timeout",
// bypassing the operator's background.max_timeout_seconds clamp.
func TestRED_BGStartTimeoutOverflowBypassesMaxTimeout(t *testing.T) {
	rt := newBackgroundRuntime(BackgroundSettings{Enabled: true, MaxTimeoutSeconds: 2}, "sess-red", "", nil)
	if rt == nil {
		t.Fatal("no runtime")
	}
	defer rt.Shutdown()
	tool := &bgStartTool{rt: rt, shell: &shellTool{dangerousConfig: danger.DangerousConfig{}}}
	out, err := tool.Call(`{"command":"sleep 30","timeout_seconds":9223372037}`)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	var r struct {
		JobID string `json:"job_id"`
	}
	_ = json.Unmarshal([]byte(out), &r)
	jobs := rt.mgr.List("sess-red")
	if len(jobs) != 1 {
		t.Fatalf("jobs=%d", len(jobs))
	}
	if jobs[0].Timeout <= 0 || jobs[0].Timeout > 2*time.Second {
		t.Fatalf("job timeout = %v, want clamped to <= 2s (operator max_timeout_seconds)", jobs[0].Timeout)
	}
}

func TestBGStartRejectsNegativeTimeout(t *testing.T) {
	rt := newBackgroundRuntime(BackgroundSettings{Enabled: true}, "sess-neg", "", nil)
	defer rt.Shutdown()
	tool := &bgStartTool{rt: rt, shell: &shellTool{dangerousConfig: danger.DangerousConfig{}}}
	_, err := tool.Call(`{"command":"sleep 30","timeout_seconds":-5}`)
	if err == nil || !strings.Contains(err.Error(), "timeout_seconds") {
		t.Fatalf("want negative timeout rejected, got %v", err)
	}
}

func TestBGStartClampsHugeTimeoutWithoutOperatorCap(t *testing.T) {
	rt := newBackgroundRuntime(BackgroundSettings{Enabled: true}, "sess-huge", "", nil)
	defer rt.Shutdown()
	tool := &bgStartTool{rt: rt, shell: &shellTool{dangerousConfig: danger.DangerousConfig{}}}
	if _, err := tool.Call(`{"command":"sleep 30","timeout_seconds":9223372037}`); err != nil {
		t.Fatal(err)
	}
	jobs := rt.mgr.List("sess-huge")
	if len(jobs) != 1 || jobs[0].Timeout <= 0 {
		t.Fatalf("want a positive clamped timeout, got %+v", jobs)
	}
}
