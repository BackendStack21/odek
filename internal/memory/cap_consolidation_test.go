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

	// Two large adds (env cap 8000; 50% = 4000): the second crosses the
	// threshold deterministically — a long fill spawns many interleaved
	// passes whose under-cap exits race the crossing trigger.
	if err := mm.AddFact("env", "unique fact alpha "+strings.Repeat("x", 2500)); err != nil {
		t.Fatalf("AddFact: %v", err)
	}
	if err := mm.AddFact("env", "unique fact alpha2 "+strings.Repeat("x", 2500)); err != nil {
		t.Fatalf("AddFact: %v", err)
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

	// Deterministic crossing: two adds of 2500c — the second crosses
	// (env cap 8000; 50% = 4000) with a single pass to drain.
	if err := mm.AddFact("env", "unique fact epsilon "+strings.Repeat("x", 2500)); err != nil {
		t.Fatalf("AddFact must not surface consolidation errors: %v", err)
	}
	if err := mm.AddFact("env", "unique fact epsilon2 "+strings.Repeat("x", 2500)); err != nil {
		t.Fatalf("AddFact must not surface consolidation errors: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && llm.calls.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if llm.calls.Load() == 0 {
		t.Fatal("expected the trigger to attempt consolidation despite LLM failure")
	}
}

// A failed consolidation (LLM error on preview) must NOT start the cooldown:
// stamping lastCapConsolidateUnix before the apply succeeds means a failed
// pass burns the full 600s window with nothing consolidated. A later
// crossing with a working LLM must retry.
func TestCapConsolidation_CooldownNotBurnedByFailedPass(t *testing.T) {
	dir := t.TempDir()
	fail := &errLLM{calls: atomic.Int64{}}
	cfg := DefaultMemoryConfig()
	cfg.MergeOnWrite = boolPtr(false)
	cfg.LLMConsolidate = boolPtr(true)
	cfg.ConsolidateOnEnd = boolPtr(false)
	pct := 50
	cfg.ConsolidateAtCapPct = &pct
	mm := NewMemoryManager(dir, fail, cfg)
	t.Cleanup(func() { mm.WaitForBackground(30 * time.Second) })

	// Deterministic crossing: two adds of 2500c — the second crosses.
	if err := mm.AddFact("env", "unique fact delta "+strings.Repeat("x", 2500)); err != nil {
		t.Fatalf("AddFact: %v", err)
	}
	if err := mm.AddFact("env", "unique fact delta2 "+strings.Repeat("x", 2500)); err != nil {
		t.Fatalf("AddFact: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && fail.calls.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if fail.calls.Load() == 0 {
		t.Fatal("first crossing never attempted consolidation")
	}
	mm.WaitForBackground(30 * time.Second)

	// Swap in a working LLM; one more genuine add re-crosses (the failed
	// pass must NOT have stamped the cooldown).
	work := &blockingConsolidLLM{release: make(chan struct{})}
	mm.llm = work
	if err := mm.AddFact("env", "unique fact omega "+strings.Repeat("y", 2500)); err != nil {
		t.Fatalf("AddFact 2: %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && work.calls.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	work.Unblock()
	if work.calls.Load() == 0 {
		t.Fatal("a failed pass burned the cooldown: retry after failure never fired")
	}
	mm.WaitForBackground(30 * time.Second)
}

// A dedup no-op (mutated=false) must not even spawn the consolidation
// goroutine — the corpus is unchanged, so the cap trigger has nothing new
// to cover. Tested directly against maybeConsolidateAtCap so no prior
// consolidation (which rewrites the corpus) can confound the premise.
func TestCapConsolidation_DedupNoOpDoesNotTrigger(t *testing.T) {
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

	// Deterministic crossing with two adds, then drain.
	if err := mm.AddFact("env", "unique fact sigma "+strings.Repeat("x", 2500)); err != nil {
		t.Fatalf("AddFact: %v", err)
	}
	if err := mm.AddFact("env", "unique fact sigma2 "+strings.Repeat("x", 2500)); err != nil {
		t.Fatalf("AddFact: %v", err)
	}
	// The crossing fires a genuine consolidation; drain it so it can't
	// pollute the dedup assertions below.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && llm.calls.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	llm.Unblock()
	mm.WaitForBackground(30 * time.Second)

	// Reset the cooldown and take the no-op gate for a spin at a quiet
	// moment: mutated=false must not spawn a goroutine or reach the LLM.
	before := llm.calls.Load()
	mm.lastCapConsolidateUnix.Store(0)
	mm.maybeConsolidateAtCap("env", false)
	time.Sleep(150 * time.Millisecond)
	if got := llm.calls.Load(); got != before {
		t.Fatalf("dedup no-op triggered %d consolidation call(s), want 0", got-before)
	}

	// Genuine mutations (new unique facts, mutated=true) crossing the cap
	// must still fire. The refill consolidations shrink the corpus, then
	// later adds cross it again — so at least one genuine fire is expected.
	// Refill past the cap (the drained pass merged the corpus down to a
	// single small entry, so two fresh 2500c adds are needed to re-cross).
	if err := mm.AddFact("env", "unique fact tau "+strings.Repeat("y", 2500)); err != nil {
		t.Fatalf("refill AddFact: %v", err)
	}
	if err := mm.AddFact("env", "unique fact tau2 "+strings.Repeat("y", 2500)); err != nil {
		t.Fatalf("refill AddFact 2: %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && llm.calls.Load() == before {
		time.Sleep(5 * time.Millisecond)
	}
	if llm.calls.Load() == before {
		t.Fatal("genuine mutation over cap never fired the trigger")
	}
	llm.Unblock()
}
