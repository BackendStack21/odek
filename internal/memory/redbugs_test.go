package memory

import (
	"strings"
	"testing"
)

// RED #18 (K4): FactStore joins entries with "\n§\n" but Add accepts
// content containing the separator, so one Add silently becomes multiple
// entries on read-back — injected content can reshape always-injected
// fact files.
func TestRED_FactAddRejectsSeparatorInjection(t *testing.T) {
	fs := NewFactStore(t.TempDir(), 10_000, 10_000)
	err := fs.Add("user", "innocent fact\n§\ninjected second entry")
	if err == nil {
		t.Fatal("Add accepted content containing the entry separator")
	}
	entries, _ := fs.Entries("user")
	if len(entries) != 0 {
		t.Errorf("after rejected add, Entries() = %v; want empty", entries)
	}
}

// RED #19 (K7): Remove swaps with the last element, so removing a fact
// reorders the remaining ones — user-visible ordering of facts injected
// into every system prompt changes unpredictably and ReplaceAt indices
// from Entries() shift.
func TestRED_FactRemovePreservesOrder(t *testing.T) {
	fs := NewFactStore(t.TempDir(), 10_000, 10_000)
	for _, c := range []string{"alpha fact", "beta fact", "gamma fact"} {
		if err := fs.Add("user", c); err != nil {
			t.Fatal(err)
		}
	}
	if err := fs.Remove("user", "alpha"); err != nil {
		t.Fatal(err)
	}
	got, err := fs.Entries("user")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"beta fact", "gamma fact"}
	if len(got) != len(want) || strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("Entries after Remove = %v, want %v (order must be preserved)", got, want)
	}
}
