package memory

import "testing"

// A consolidated entry carrying the internal separator must not be able to
// split into extra entries (Add/Replace/ReplaceAt all reject it).
func TestRED_ConsolidateRejectsSeparatorInEntry(t *testing.T) {
	mgr := newConsolidateMgr(t, fixedLLM{"[\"one\\n§\\ntwo\\n§\\nthree\"]"})
	for _, f := range []string{"alpha fact one", "beta fact two"} {
		if err := mgr.AddFact("env", f); err != nil {
			t.Fatal(err)
		}
	}
	_ = mgr.Consolidate("env")
	entries, _ := mgr.facts.Entries("env")
	if len(entries) > 2 {
		t.Fatalf("consolidation expanded 2 entries into %d via separator: %q", len(entries), entries)
	}
}
