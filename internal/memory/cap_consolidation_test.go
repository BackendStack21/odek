package memory

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ── Cap-aware auto-consolidation (B4 step 2) ────────────────────────────
//
// Session-end consolidation never fires for long-lived serve/REPL
// sessions, so facts fossilize near the cap. Crossing a configurable
// percentage of the cap on AddFact must fire ONE background consolidation
// per crossing (in-flight guard), and 0 must disable the trigger.

// blockingConsolidLLM lets tests hold a consolidation in flight.
type blockingConsolidLLM struct {
	calls    atomic.Int64
	release  chan struct{}
	closeOne sync.Once
	mock     *mockLLM
}

func (b *blockingConsolidLLM) Unblock() { b.closeOne.Do(func() { close(b.release) }) }

func (b *blockingConsolidLLM) SimpleCall(ctx context.Context, system, user string) (string, error) {
	b.calls.Add(1)
	// Respond only for the consolidation prompt shape; extraction prompts
	// (if any) proxy to the mock.
	if strings.Contains(user, "Consolidate the following memory entries") {
		<-b.release
		return `["merged fact"]`, nil
	}
	if b.mock != nil {
		return b.mock.SimpleCall(ctx, system, user)
	}
	return `["x"]`, nil
}

func capFillingFacts(prefix string, n, size int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, prefix+" "+strings.Repeat("x", size)+time.Now().Format("150405.000000000"))
	}
	return out
}

func TestAddFact_CapCrossingFiresConsolidation(t *testing.T) {
	dir := t.TempDir()
	llm := &blockingConsolidLLM{release: make(chan struct{})}
	cfg := DefaultMemoryConfig()
	cfg.MergeOnWrite = boolPtr(false)
	cfg.LLMConsolidate = boolPtr(true)
	cfg.ConsolidateOnEnd = boolPtr(false) // isolate the cap trigger
	pct := 50                             // tiny dir caps → cross easily
	cfg.ConsolidateAtCapPct = &pct
	mm := NewMemoryManager(dir, llm, cfg)
	t.Cleanup(func() {
		llm.Unblock() // unblock any late-fired consolidation first
		mm.WaitForBackground(30 * time.Second)
	})

	// Fill past the trigger (env cap 8000; 50% = 4000): 6 × ~830 chars ≈ 5000.
	for _, f := range capFillingFacts("unique fact alpha", 6, 800) {
		if err := mm.AddFact("env", f); err != nil {
			t.Fatalf("AddFact: %v", err)
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if llm.calls.Load() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if llm.calls.Load() == 0 {
		t.Fatal("crossing the cap threshold never fired a background consolidation")
	}
	llm.Unblock()
	mm.WaitForBackground(30 * time.Second)
}

func TestAddFact_CapTriggerDisabledAtZero(t *testing.T) {
	dir := t.TempDir()
	llm := &blockingConsolidLLM{release: make(chan struct{})}
	cfg := DefaultMemoryConfig()
	cfg.MergeOnWrite = boolPtr(false)
	cfg.LLMConsolidate = boolPtr(true)
	cfg.ConsolidateOnEnd = boolPtr(false)
	pct := 0 // disabled
	cfg.ConsolidateAtCapPct = &pct
	mm := NewMemoryManager(dir, llm, cfg)
	t.Cleanup(func() { mm.WaitForBackground(30 * time.Second) })

	for _, f := range capFillingFacts("unique fact beta", 6, 800) {
		if err := mm.AddFact("env", f); err != nil {
			t.Fatalf("AddFact: %v", err)
		}
	}
	time.Sleep(100 * time.Millisecond)
	if got := llm.calls.Load(); got != 0 {
		t.Fatalf("disabled trigger fired %d consolidation(s), want 0", got)
	}
}

func TestAddFact_InFlightGuardSingleFire(t *testing.T) {
	dir := t.TempDir()
	llm := &blockingConsolidLLM{release: make(chan struct{})}
	cfg := DefaultMemoryConfig()
	cfg.MergeOnWrite = boolPtr(false)
	cfg.LLMConsolidate = boolPtr(true)
	cfg.ConsolidateOnEnd = boolPtr(false)
	pct := 50
	cfg.ConsolidateAtCapPct = &pct
	mm := NewMemoryManager(dir, llm, cfg)
	t.Cleanup(func() {
		llm.Unblock()
		mm.WaitForBackground(30 * time.Second)
	})

	// Cross the threshold (env cap 8000; 50% = 4000) and start an in-flight
	// consolidation. Two entries: Consolidate no-ops on single-entry dirs.
	if err := mm.AddFact("env", "unique fact gamma "+strings.Repeat("x", 2400)); err != nil {
		t.Fatalf("seed add: %v", err)
	}
	if err := mm.AddFact("env", "unique fact zeta "+strings.Repeat("y", 2400)); err != nil {
		t.Fatalf("seed add 2: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && llm.calls.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if llm.calls.Load() == 0 {
		t.Fatal("consolidation did not start (release channel would deadlock test)")
	}

	// More adds while the consolidation is in flight must NOT stack calls.
	for _, f := range capFillingFacts("unique fact delta", 3, 600) {
		if err := mm.AddFact("env", f); err != nil {
			t.Fatalf("AddFact: %v", err)
		}
	}
	if got := llm.calls.Load(); got != 1 {
		t.Fatalf("consolidation calls while in flight = %d, want 1 (in-flight guard missing)", got)
	}
	llm.Unblock()
	mm.WaitForBackground(30 * time.Second)
}

// errLLM always fails — the cap trigger must be best-effort: AddFact still
// succeeds and errors never surface to the caller.
type errLLM struct{ calls atomic.Int64 }

func (e *errLLM) SimpleCall(ctx context.Context, system, user string) (string, error) {
	e.calls.Add(1)
	return "", errors.New("llm down")
}

func TestAddFact_CapTriggerBestEffort(t *testing.T) {
	dir := t.TempDir()
	llm := &errLLM{}
	cfg := DefaultMemoryConfig()
	cfg.MergeOnWrite = boolPtr(false)
	cfg.LLMConsolidate = boolPtr(true)
	cfg.ConsolidateOnEnd = boolPtr(false)
	pct := 50
	cfg.ConsolidateAtCapPct = &pct
	mm := NewMemoryManager(dir, llm, cfg)
	t.Cleanup(func() { mm.WaitForBackground(30 * time.Second) })

	for _, f := range capFillingFacts("unique fact epsilon", 6, 800) {
		if err := mm.AddFact("env", f); err != nil {
			t.Fatalf("AddFact must not surface consolidation errors: %v", err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && llm.calls.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if llm.calls.Load() == 0 {
		t.Fatal("expected the trigger to attempt consolidation despite LLM failure")
	}
}
