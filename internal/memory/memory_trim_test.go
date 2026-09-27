package memory

import (
	"testing"
)

// AddFact must normalise (trim) content consistently with what FactStore
// stores on disk. Otherwise padded duplicates append phantom entries to the
// merge-detector corpus that never exist in the fact store.
func TestAddFact_PaddedDuplicatesProduceSingleCorpusEntry(t *testing.T) {
	cfg := DefaultMemoryConfig()
	cfg.MergeOnWrite = boolPtr(true)
	mm := NewMemoryManager(t.TempDir(), nil, cfg)

	if err := mm.AddFact("env", "   go 1.22   "); err != nil {
		t.Fatalf("first AddFact: %v", err)
	}
	if err := mm.AddFact("env", "   go 1.22   "); err != nil {
		t.Fatalf("second AddFact: %v", err)
	}
	if got := len(mm.merge.Corpus()); got != 1 {
		t.Errorf("corpus length after padded duplicate adds = %d, want 1", got)
	}
	// The corpus entry must be the normalized (trimmed) form, matching the
	// normalized fact content — not a padded variant.
	if got := mm.merge.Corpus(); len(got) == 1 && got[0] != "go 1.22" {
		t.Errorf("corpus entry = %q, want trimmed %q", got[0], "go 1.22")
	}
}
