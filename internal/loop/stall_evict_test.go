package loop

import (
	"fmt"
	"testing"
)

// Eviction must report WHICH fingerprint was dropped so the caller can also
// drop its stall milestone. Deleting the newly arriving key's (absent)
// milestone instead leaks the evicted fingerprint's escalated threshold —
// a re-added fingerprint would inherit a stale escalated milestone, the
// exact state dropStallMilestones exists to prevent.
func TestEvictLowestStallCountReturnsEvictedKey(t *testing.T) {
	m := make(map[string]int)
	for i := 0; i < 64; i++ {
		m[fmt.Sprintf("toolB\x00arg%02d", i)] = 5
	}
	m["toolA\x00arg"] = 1 // lowest count → must be the evicted one

	evicted, ok := evictLowestStallCount(m)
	if !ok {
		t.Fatal("evictLowestStallCount returned ok=false for a non-empty map")
	}
	if evicted != "toolA\x00arg" {
		t.Fatalf("evicted %q, want the lowest-count key %q", evicted, "toolA\x00arg")
	}
	if _, still := m["toolA\x00arg"]; still {
		t.Fatal("evicted key still present in the map")
	}
}

func TestEvictLowestStallCountEmptyMap(t *testing.T) {
	m := map[string]int{}
	if _, ok := evictLowestStallCount(m); ok {
		t.Fatal("evictLowestStallCount returned ok=true for an empty map")
	}
}
