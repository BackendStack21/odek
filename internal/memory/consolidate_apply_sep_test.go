package memory

import "testing"

// ApplyConsolidation also refuses an entry that would split into several
// entries through the on-disk separator.
func TestApplyConsolidationRejectsSeparatorEntry(t *testing.T) {
	mgr := newConsolidateMgr(t, fixedLLM{`["x"]`})
	if err := mgr.AddFact("env", "alpha fact one"); err != nil {
		t.Fatal(err)
	}
	before, _ := mgr.facts.Entries("env")
	err := mgr.ApplyConsolidation("env", ConsolidationPreview{
		Before: before,
		After:  []string{"one" + entrySep + "two"},
	})
	if err == nil {
		t.Fatal("expected rejection")
	}
}
