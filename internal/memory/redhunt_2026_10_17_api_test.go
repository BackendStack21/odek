package memory

import (
	"strings"
	"testing"
)

// TestRED_FactRemoveExactEntryContainedInAnother: an old_text equal to a
// whole entry identifies that entry even when a longer entry contains it;
// a plain substring shared by several entries stays ambiguous.
func TestRED_FactRemoveExactEntryContainedInAnother(t *testing.T) {
	f := NewFactStore(t.TempDir(), 0, 0)
	for _, e := range []string{"Uses Go 1.27", "Uses Go 1.27 with the race detector on CI"} {
		if err := f.Add("user", e); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Remove("user", "Go 1.27"); err == nil || !strings.Contains(err.Error(), "2 entries contain") {
		t.Fatalf("ambiguous substring remove = %v, want the ambiguity error", err)
	}
	if err := f.ReplaceApproved("user", "Uses Go 1.27", "Uses Go 1.27", "Uses Go 1.28"); err != nil {
		t.Fatalf("approved replace of the exact entry = %v", err)
	}
	if err := f.Remove("user", "  Uses Go 1.28 "); err != nil {
		t.Fatalf("remove of the exact (trimmed) entry = %v", err)
	}
	got, err := f.Entries("user")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "Uses Go 1.27 with the race detector on CI" {
		t.Fatalf("entries after removing the exact entry = %q", got)
	}
}
