package memory

import (
	"context"
	"testing"
	"time"
)

type slowLLM struct{ started, release chan struct{} }

func (s slowLLM) SimpleCall(ctx context.Context, system, user string) (string, error) {
	close(s.started)
	<-s.release
	return `["merged"]`, nil
}

// Consolidate must not hold the facts flock across the LLM call: an AddFact
// in the same session would stall for the whole LLM duration.
func TestRED_ConsolidateDoesNotHoldLockAcrossLLM(t *testing.T) {
	llm := slowLLM{make(chan struct{}), make(chan struct{})}
	mgr := newConsolidateMgr(t, llm)
	for _, f := range []string{"alpha fact one", "beta fact two"} {
		if err := mgr.AddFact("env", f); err != nil {
			t.Fatal(err)
		}
	}
	consolidated := make(chan struct{})
	go func() { _ = mgr.Consolidate("env"); close(consolidated) }()
	<-llm.started
	done := make(chan error, 1)
	go func() { done <- mgr.AddFact("user", "gamma user pref") }()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		close(llm.release)
		<-consolidated
		t.Fatal("AddFact blocked while Consolidate awaits the LLM (flock held across LLM call)")
	}
	close(llm.release)
	<-consolidated // let the write finish before TempDir cleanup
}
