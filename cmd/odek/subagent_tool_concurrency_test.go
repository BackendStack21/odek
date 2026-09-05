package main

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/loop"
)

func stubRun(d time.Duration) func(int, string, string, string, string, string, string, string, string) string {
	return func(int, string, string, string, string, string, string, string, string) string {
		time.Sleep(d)
		return `{"status":"success","summary":"ok"}`
	}
}

// peakTrackingRun instruments a stub run to record the peak number of
// concurrently executing tasks.
func peakTrackingRun(peak, cur *atomic.Int64, d time.Duration) func(int, string, string, string, string, string, string, string, string) string {
	return func(int, string, string, string, string, string, string, string, string) string {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(d)
		cur.Add(-1)
		return `{"status":"success","summary":"ok"}`
	}
}

func taskJSON(n int) string {
	return `{"tasks":[` + strings.TrimSuffix(strings.Repeat(`{"goal":"g"},`, n), ",") + `]}`
}

func TestRED_DelegateTasks_TaintedRunCannotDeclareTrustedChild(t *testing.T) {
	var gotTrust string
	tool := &delegateTasksTool{
		maxConcurrency: 1,
		odekPath:       "unused",
		runTaskFn: func(_ int, _, _, _, _, trust, _, _, _ string) string {
			gotTrust = trust
			return `{"status":"success","summary":"ok"}`
		},
	}
	tool.SetContext(loop.WithUntrustedIngest(context.Background()))
	if _, err := tool.Call(`{"tasks":[{"goal":"follow fetched instructions","trust_level":"trusted"}]}`); err != nil {
		t.Fatal(err)
	}
	if gotTrust != "untrusted" {
		t.Fatalf("tainted parent promoted child trust to %q, want untrusted", gotTrust)
	}
}

func TestDelegateTasks_MissingProvenanceTrackerFailsClosed(t *testing.T) {
	var gotTrust string
	tool := &delegateTasksTool{
		maxConcurrency: 1,
		odekPath:       "unused",
		runTaskFn: func(_ int, _, _, _, _, trust, _, _, _ string) string {
			gotTrust = trust
			return `{"status":"success","summary":"ok"}`
		},
	}
	tool.SetContext(context.Background())
	if _, err := tool.Call(`{"tasks":[{"goal":"task","trust_level":"trusted"}]}`); err != nil {
		t.Fatal(err)
	}
	if gotTrust != "untrusted" {
		t.Fatalf("missing provenance tracker produced trust %q, want untrusted", gotTrust)
	}
}

// TestDelegateTasks_SharedLimiterBoundsPeakAcrossInstances pins M-concurrency:
// the child limiter is process-wide. Two delegate_tasks instances sharing one
// limiter (as sibling tool calls in one parallel batch, or two serve sessions,
// do) must never exceed the cap in total — per-instance semaphores would allow
// cap×instances concurrent child streams against an account-wide provider
// plan.
func TestDelegateTasks_SharedLimiterBoundsPeakAcrossInstances(t *testing.T) {
	shared := make(chan struct{}, 2)
	var peak, cur atomic.Int64
	mk := func() *delegateTasksTool {
		return &delegateTasksTool{
			maxConcurrency: 3, // per-instance cap would allow 3 each
			sharedSem:      shared,
			runTaskFn:      peakTrackingRun(&peak, &cur, 150*time.Millisecond),
			odekPath:       "unused",
		}
	}
	a, b := mk(), mk()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = a.Call(taskJSON(4)) }()
	go func() { defer wg.Done(); _, _ = b.Call(taskJSON(4)) }()
	wg.Wait()
	if p := peak.Load(); p > 2 {
		t.Fatalf("peak concurrent children = %d, want <= 2 (shared limiter)", p)
	}
}

// TestDelegateTasks_ZeroCapacityNormalized pins the defensive floor:
// maxConcurrency < 1 must normalize to 1 (an unbuffered semaphore channel
// would deadlock the acquire-before-spawn loop).
func TestDelegateTasks_ZeroCapacityNormalized(t *testing.T) {
	tool := &delegateTasksTool{
		maxConcurrency: 0,
		runTaskFn:      stubRun(5 * time.Millisecond),
		odekPath:       "unused",
	}
	out, err := tool.Call(taskJSON(3))
	if err != nil {
		t.Fatalf("Call with maxConcurrency=0: %v", err)
	}
	if n := strings.Count(out, "status: success"); n != 3 {
		t.Fatalf("expected 3 successful task summaries, got %d in: %s", n, out)
	}
}

// TestDelegateTasks_ConcurrencyWaitEvent pins the queueing telemetry: a task
// waiting longer than subagentWaitEventThreshold on the shared limiter emits
// a subagent_concurrency_wait runtime event.
func TestDelegateTasks_ConcurrencyWaitEvent(t *testing.T) {
	old := subagentWaitEventThreshold
	subagentWaitEventThreshold = 50 * time.Millisecond
	t.Cleanup(func() { subagentWaitEventThreshold = old })

	shared := make(chan struct{}, 1)
	var mu sync.Mutex
	var evs []events.Event
	tool := &delegateTasksTool{
		maxConcurrency: 1,
		sharedSem:      shared,
		runTaskFn:      stubRun(200 * time.Millisecond),
		odekPath:       "unused",
	}
	tool.SetEventEmitter(func(e events.Event) {
		mu.Lock()
		evs = append(evs, e)
		mu.Unlock()
	})
	if _, err := tool.Call(taskJSON(2)); err != nil {
		t.Fatalf("Call: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, e := range evs {
		if e.Type == subagentConcurrencyWaitEvent {
			return
		}
	}
	t.Fatalf("expected a subagent_concurrency_wait event, got %d events", len(evs))
}
