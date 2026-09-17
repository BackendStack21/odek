package memory

import (
	"strings"
	"testing"
	"time"
)

// Regression: the pending trigger carried a single target (latest-wins),
// so a 'user' trigger arriving after an 'env' one dropped the env
// crossing entirely. Both targets must be tracked and both consolidated.
func TestCapConsolidation_BothTargetsPendingNotLost(t *testing.T) {
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

	// Seed both targets over their 50% line, drain the initial passes.
	for _, f := range []string{
		"user seed a " + strings.Repeat("x", 1900),
		"user seed b " + strings.Repeat("y", 1900),
	} {
		if err := mm.AddFact("user", f); err != nil {
			t.Fatalf("AddFact user: %v", err)
		}
	}
	for _, f := range []string{
		"env seed a " + strings.Repeat("x", 2500),
		"env seed b " + strings.Repeat("y", 2500),
	} {
		if err := mm.AddFact("env", f); err != nil {
			t.Fatalf("AddFact env: %v", err)
		}
	}
	llm.Unblock()
	mm.WaitForBackground(30 * time.Second)
	mm.lastCapConsolidateUnix.Store(0)

	// Block the LLM; re-fill user past its line so a pass is in flight.
	b2 := &blockingConsolidLLM{release: make(chan struct{})}
	mm.llm = b2
	for _, f := range []string{
		"user fact c " + strings.Repeat("x", 1900),
		"user fact d " + strings.Repeat("y", 1900),
	} {
		if err := mm.AddFact("user", f); err != nil {
			t.Fatalf("AddFact user refill: %v", err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && b2.calls.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if b2.calls.Load() == 0 {
		t.Fatal("user pass did not start (premise broken)")
	}
	// env crossing arrives while the user pass is blocked mid-flight.
	if err := mm.AddFact("env", "env fact c "+strings.Repeat("z", 4500)); err != nil {
		t.Fatalf("AddFact env: %v", err)
	}

	// Release: user completes, then env must ALSO be consolidated — two
	// LLM calls minimum (user + env), one per target.
	b2.Unblock()
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && b2.calls.Load() < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	if b2.calls.Load() < 2 {
		t.Fatal("env crossing was dropped by the latest-wins pending overwrite")
	}
	mm.WaitForBackground(30 * time.Second)
}

// Regression: pct > 100 silently disabled the trigger while CONFIG.md
// documents values above 99 as treated as 99.
func TestConsolidateAtCap_PctAbove99ClampedNotDisabled(t *testing.T) {
	dir := t.TempDir()
	llm := &blockingConsolidLLM{release: make(chan struct{})}
	cfg := DefaultMemoryConfig()
	cfg.MergeOnWrite = boolPtr(false)
	cfg.LLMConsolidate = boolPtr(true)
	cfg.ConsolidateOnEnd = boolPtr(false)
	pct := 150 // docs: treated as 99
	cfg.ConsolidateAtCapPct = &pct
	mm := NewMemoryManager(dir, llm, cfg)
	t.Cleanup(func() {
		llm.Unblock()
		mm.WaitForBackground(30 * time.Second)
	})

	// env cap 8000; 99% = 7920 — adds of 4000 + 3950 cross it without
	// exceeding the 8000-char cap.
	for _, f := range []string{
		"clamp fact a " + strings.Repeat("x", 4000),
		"clamp fact b " + strings.Repeat("y", 3950),
	} {
		if err := mm.AddFact("env", f); err != nil {
			t.Fatalf("AddFact: %v", err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && llm.calls.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if llm.calls.Load() == 0 {
		t.Fatal("pct=150 disabled the trigger; docs say >99 is treated as 99")
	}
	llm.Unblock()
	mm.WaitForBackground(30 * time.Second)
}
