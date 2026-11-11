package session

import (
	"fmt"
	"math"
	"testing"
)

// The filter is persisted, so its bit layout is part of the session file
// format: these positions must never change silently. A filter written
// with another layout would match different spans. The values were checked
// against an independent implementation of the documented derivation
// (FNV-1a 64 of kind NUL hex-SHA-256; h2 continues it with 0xff, forced
// odd; position i is the top 20 bits of h1 + i*h2).
func TestTaintOverflowProbe_Layout(t *testing.T) {
	for _, tc := range []struct {
		kind, value string
		want        [taintOverflowProbes]uint32
	}{
		{"host", "evil.example", [taintOverflowProbes]uint32{584535, 1040401, 447692, 903559}},
		{"url", "https://evil.attacker.io/collect", [taintOverflowProbes]uint32{603440, 146444, 738023, 281027}},
		{"path", "/opt/drop/x.sh", [taintOverflowProbes]uint32{89677, 908458, 678663, 448868}},
		{"token", "Zx9QwErTyUiOpAsDfGhJkLzXcVbNm1234567", [taintOverflowProbes]uint32{741037, 811325, 881612, 951900}},
	} {
		got := taintOverflowProbe(tc.kind, TaintSpanHash(tc.value))
		if got != tc.want {
			t.Errorf("%s %q: positions %v, want %v", tc.kind, tc.value, got, tc.want)
		}
	}
}

// The measured false-positive rate of absent keys matches the estimate
// (1 - e^(-4n/m))^4 for the filter's size.
func TestTaintOverflow_FalsePositiveRate(t *testing.T) {
	if testing.Short() {
		t.Skip("inserts 50,000 keys")
	}
	const probes = 200000
	for _, n := range []int{10000, 50000} {
		var f TaintOverflow
		for i := 0; i < n; i++ {
			f.AddValue("host", fmt.Sprintf("in%d.example", i))
		}
		hits := 0
		for i := 0; i < probes; i++ {
			if f.HasValue("host", fmt.Sprintf("out%d.example", i)) {
				hits++
			}
		}
		measured := float64(hits) / probes
		estimate := math.Pow(1-math.Exp(-float64(taintOverflowProbes*n)/taintOverflowBits), taintOverflowProbes)
		t.Logf("n=%d: %d of %d absent keys hit (%.2e), estimate %.2e", n, hits, probes, measured, estimate)
		// Three standard deviations of a binomial count, plus a few hits
		// for the small estimate at n = 10,000.
		bound := estimate*probes + 3*math.Sqrt(estimate*probes) + 3
		if float64(hits) > bound {
			t.Errorf("n=%d: %d false positives, above the bound %.0f", n, hits, bound)
		}
	}
}
