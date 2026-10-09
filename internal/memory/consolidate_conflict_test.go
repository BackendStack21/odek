package memory

import "testing"

// A fact written to the same target while the consolidation LLM call is in
// flight must conflict the merge instead of being overwritten by it.
func TestConsolidateConflictsWithConcurrentWrite(t *testing.T) {
	llm := slowLLM{make(chan struct{}), make(chan struct{})}
	mgr := newConsolidateMgr(t, llm)
	for _, f := range []string{"alpha fact one", "beta fact two"} {
		if err := mgr.AddFact("env", f); err != nil {
			t.Fatal(err)
		}
	}
	errc := make(chan error, 1)
	go func() { errc <- mgr.Consolidate("env") }()
	<-llm.started
	if err := mgr.AddFact("env", "gamma fact three"); err != nil {
		t.Fatal(err)
	}
	close(llm.release)
	if err := <-errc; err == nil {
		t.Fatal("expected a conflict error")
	}
	entries, _ := mgr.facts.Entries("env")
	if len(entries) != 3 {
		t.Fatalf("concurrent write lost: %q", entries)
	}
}
