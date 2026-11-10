package session

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// referenceMergeTaintState is MergeTaintState without its shortcuts: every
// span looked up in a string-keyed set, both filters always ORed into a new
// one. The shortcuts must not change the result.
func referenceMergeTaintState(newer, older TaintState) TaintState {
	out := TaintState{Overflow: UnionTaintOverflow(newer.Overflow, older.Overflow)}
	seen := map[string]bool{}
	for _, list := range [][]TaintSpan{newer.Spans, older.Spans} {
		for _, sp := range list {
			if sp.Kind == "" {
				continue
			}
			key := sp.Kind + "\x00" + sp.Value + "\x00" + sp.Hash
			if seen[key] {
				continue
			}
			seen[key] = true
			if len(out.Spans) == MaxTaintSpans || (sp.Value == "") == (sp.Hash == "") || len(sp.Value) > MaxTaintSpanValueBytes || len(sp.Hash) > 64 {
				if sp.Value != "" {
					out.Overflow.AddValue(sp.Kind, sp.Value)
				}
				if sp.Hash != "" {
					out.Overflow.AddHash(sp.Kind, sp.Hash)
				}
				continue
			}
			if len(sp.Source) > maxTaintSpanSourceBytes {
				sp.Source = truncateUTF8(sp.Source, maxTaintSpanSourceBytes)
			}
			out.Spans = append(out.Spans, sp)
		}
	}
	return out
}

func overflowWith(values ...string) TaintOverflow {
	var f TaintOverflow
	for _, v := range values {
		f.AddValue("host", v)
	}
	return f
}

func spanList(prefix string, n int) []TaintSpan {
	var out []TaintSpan
	for i := 0; i < n; i++ {
		out = append(out, TaintSpan{Kind: "host", Value: fmt.Sprintf("%s%d.example", prefix, i), Source: "web"})
	}
	return out
}

// The merge's shortcuts (skipping older entries that repeat newer, sharing a
// covering filter, the hashed dedupe index) give exactly the result of the
// plain merge, and never modify an input.
func TestMergeTaintState_MatchesReference(t *testing.T) {
	full := spanList("p", MaxTaintSpans)
	fresh := append(spanList("new", 3), full[:MaxTaintSpans-3]...)
	refreshed := append([]TaintSpan{full[100]}, append(append([]TaintSpan{}, full[:100]...), full[101:]...)...)
	messy := []TaintSpan{
		{Kind: "host", Value: "a.example"}, {Kind: "host", Value: "a.example", Source: "again"},
		{Kind: "", Value: "nokind"}, {Kind: "host", Value: "both", Hash: "h"}, {Kind: "host"},
		{Kind: "url", Value: "https://x.example/" + strings.Repeat("a", MaxTaintSpanValueBytes)},
		{Kind: "token", Hash: strings.Repeat("f", 65)},
		{Kind: "host", Value: "long-source.example", Source: strings.Repeat("é", maxTaintSpanSourceBytes)},
	}
	a, b := overflowWith("a1", "a2"), overflowWith("b1")
	ab := UnionTaintOverflow(a, b)
	zero := make(TaintOverflow, TaintOverflowBytes)
	wrong := TaintOverflow{1, 2, 3}
	cases := []struct {
		name         string
		newer, older TaintState
	}{
		{"empty", TaintState{}, TaintState{}},
		{"equal lists, equal filters", TaintState{full, ab}, TaintState{append([]TaintSpan{}, full...), bytes.Clone(ab)}},
		{"same filter array", TaintState{full, ab}, TaintState{full, ab}},
		{"new spans in front", TaintState{fresh, ab}, TaintState{full, a}},
		{"a refreshed span", TaintState{refreshed, a}, TaintState{full, ab}},
		{"older only", TaintState{}, TaintState{full, b}},
		{"newer only", TaintState{full, nil}, TaintState{}},
		{"disjoint filters", TaintState{spanList("x", 10), a}, TaintState{spanList("y", 10), b}},
		{"newer filter inside older", TaintState{nil, a}, TaintState{nil, ab}},
		{"all-zero filter", TaintState{spanList("x", 3), zero}, TaintState{}},
		{"wrong-size filter", TaintState{nil, wrong}, TaintState{nil, a}},
		{"messy entries", TaintState{messy, nil}, TaintState{append(spanList("p", 5), messy...), a}},
		{"past the cap twice", TaintState{full, a}, TaintState{spanList("q", MaxTaintSpans), a}},
	}
	for _, tc := range cases {
		before := []TaintState{
			{append([]TaintSpan(nil), tc.newer.Spans...), bytes.Clone(tc.newer.Overflow)},
			{append([]TaintSpan(nil), tc.older.Spans...), bytes.Clone(tc.older.Overflow)},
		}
		got := MergeTaintState(tc.newer, tc.older)
		want := referenceMergeTaintState(before[0], before[1])
		if len(want.Spans) == 0 {
			want.Spans = nil
		}
		if !reflect.DeepEqual(got.Spans, want.Spans) {
			t.Errorf("%s: spans differ from the plain merge (%d vs %d)", tc.name, len(got.Spans), len(want.Spans))
		}
		if !bytes.Equal(got.Overflow, want.Overflow) || (got.Overflow == nil) != (want.Overflow == nil) {
			t.Errorf("%s: filter differs from the plain merge (len %d vs %d)", tc.name, len(got.Overflow), len(want.Overflow))
		}
		if !reflect.DeepEqual(tc.newer.Spans, before[0].Spans) || !bytes.Equal(tc.newer.Overflow, before[0].Overflow) ||
			!reflect.DeepEqual(tc.older.Spans, before[1].Spans) || !bytes.Equal(tc.older.Overflow, before[1].Overflow) {
			t.Errorf("%s: the merge modified an input", tc.name)
		}
	}
}

// In the steady state of per-step saves the merge shares the covering
// filter instead of allocating a union, and skips the repeated spans.
func TestMergeTaintState_SteadyStateShares(t *testing.T) {
	full := spanList("p", MaxTaintSpans)
	f := overflowWith("old1", "old2")
	got := MergeTaintState(TaintState{full, f}, TaintState{append([]TaintSpan{}, full...), bytes.Clone(f)})
	if len(got.Overflow) == 0 || &got.Overflow[0] != &f[0] {
		t.Error("an equal filter was copied instead of shared")
	}
	if n := repeatedTaintSpans(full, full); n != len(full) {
		t.Errorf("repeated spans of an equal list = %d", n)
	}
	fresh := append(spanList("new", 3), full[:MaxTaintSpans-3]...)
	if n := repeatedTaintSpans(fresh, full); n != MaxTaintSpans-3 {
		t.Errorf("repeated spans behind three new ones = %d", n)
	}
	if n := repeatedTaintSpans(append(spanList("new", repeatedTaintSpanWindow), full...), full); n != 0 {
		t.Errorf("repeated spans past the search window = %d", n)
	}
	// A span the cap drops is added to a copy, never to the shared filter.
	shared := overflowWith("old1")
	saved := bytes.Clone(shared)
	cut := MergeTaintState(TaintState{full, shared}, TaintState{spanList("q", 1), nil})
	if !bytes.Equal(shared, saved) || !cut.Overflow.HasValue("host", "q0.example") {
		t.Error("a dropped span was added to the shared input filter, or lost")
	}
}

func TestTaintOverflowFill(t *testing.T) {
	var empty TaintOverflow
	if empty.Fill() != 0 {
		t.Fatal("an empty filter is not empty")
	}
	if (TaintOverflow{1, 2, 3}).Fill() != 1 {
		t.Fatal("a wrong-size filter does not count as full")
	}
	half := make(TaintOverflow, TaintOverflowBytes)
	for i := range half {
		half[i] = 0xf0
	}
	if got := half.Fill(); got != 0.5 {
		t.Fatalf("half-set filter fill = %v", got)
	}
	var one TaintOverflow
	one.AddValue("host", "a.example")
	if got := one.Fill(); got <= 0 || got > float64(taintOverflowProbes)/taintOverflowBits {
		t.Fatalf("one-span filter fill = %v", got)
	}
	if saturatedTaintOverflow().Fill() != 1 {
		t.Fatal("a saturated filter is not full")
	}
}
