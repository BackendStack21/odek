package session

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
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
		{"equal lists, equal filters", TaintState{Spans: full, Overflow: ab}, TaintState{Spans: append([]TaintSpan{}, full...), Overflow: bytes.Clone(ab)}},
		{"same filter array", TaintState{Spans: full, Overflow: ab}, TaintState{Spans: full, Overflow: ab}},
		{"new spans in front", TaintState{Spans: fresh, Overflow: ab}, TaintState{Spans: full, Overflow: a}},
		{"a refreshed span", TaintState{Spans: refreshed, Overflow: a}, TaintState{Spans: full, Overflow: ab}},
		{"older only", TaintState{}, TaintState{Spans: full, Overflow: b}},
		{"newer only", TaintState{Spans: full}, TaintState{}},
		{"disjoint filters", TaintState{Spans: spanList("x", 10), Overflow: a}, TaintState{Spans: spanList("y", 10), Overflow: b}},
		{"newer filter inside older", TaintState{Overflow: a}, TaintState{Overflow: ab}},
		{"all-zero filter", TaintState{Spans: spanList("x", 3), Overflow: zero}, TaintState{}},
		{"wrong-size filter", TaintState{Overflow: wrong}, TaintState{Overflow: a}},
		{"messy entries", TaintState{Spans: messy}, TaintState{Spans: append(spanList("p", 5), messy...), Overflow: a}},
		{"past the cap twice", TaintState{Spans: full, Overflow: a}, TaintState{Spans: spanList("q", MaxTaintSpans), Overflow: a}},
	}
	for _, tc := range cases {
		before := []TaintState{
			{Spans: append([]TaintSpan(nil), tc.newer.Spans...), Overflow: bytes.Clone(tc.newer.Overflow)},
			{Spans: append([]TaintSpan(nil), tc.older.Spans...), Overflow: bytes.Clone(tc.older.Overflow)},
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
	got := MergeTaintState(TaintState{Spans: full, Overflow: f}, TaintState{Spans: append([]TaintSpan{}, full...), Overflow: bytes.Clone(f)})
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
	cut := MergeTaintState(TaintState{Spans: full, Overflow: shared}, TaintState{Spans: spanList("q", 1)})
	if !bytes.Equal(shared, saved) || !cut.Overflow.HasValue("host", "q0.example") {
		t.Error("a dropped span was added to the shared input filter, or lost")
	}
}

func TestTaintStateCovers(t *testing.T) {
	base := TaintState{Spans: []TaintSpan{{Kind: "host", Value: "a.example"}, {Kind: "token", Hash: TaintSpanHash("tok")}}, Overflow: overflowWith("cut.example")}
	for _, tc := range []struct {
		name  string
		other TaintState
		want  bool
	}{
		{"empty", TaintState{}, true},
		{"same spans", TaintState{Spans: base.Spans}, true},
		{"span in the filter", TaintState{Spans: []TaintSpan{{Kind: "host", Value: "cut.example"}}}, true},
		{"nameless entries", TaintState{Spans: []TaintSpan{{Kind: ""}, {Kind: "host"}}}, true},
		{"new span", TaintState{Spans: []TaintSpan{{Kind: "host", Value: "b.example"}}}, false},
		{"new filter bits", TaintState{Overflow: overflowWith("other.example")}, false},
	} {
		if got := base.Covers(tc.other); got != tc.want {
			t.Errorf("%s: Covers = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// AddTaintState writes only what the file lacks, keeps the transcript
// revision, and reports a missing session.
func TestStoreAddTaintState(t *testing.T) {
	s, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Create([]Message{{Role: "user", Content: "hi"}}, "m", "t")
	if err != nil {
		t.Fatal(err)
	}
	sess.TaintSpans = []TaintSpan{{Kind: "host", Value: "a.example"}}
	if err := s.Save(sess); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Load(sess.ID)
	if err := s.AddTaintState(sess.ID, TaintState{Spans: []TaintSpan{{Kind: "host", Value: "a.example"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTaintState(sess.ID, TaintState{}); err != nil {
		t.Fatal(err)
	}
	same, _ := s.Load(sess.ID)
	if same.MetaRevision != before.MetaRevision || same.Revision != before.Revision {
		t.Fatal("a state the file already covers was written")
	}
	add := TaintState{Spans: []TaintSpan{{Kind: "host", Value: "b.example"}}, Overflow: overflowWith("cut.example")}
	if err := s.AddTaintState(sess.ID, add); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Load(sess.ID)
	if !got.TaintState().Covers(add) || !got.TaintState().Covers(before.TaintState()) {
		t.Fatalf("merged state = %+v", got.TaintSpans)
	}
	if got.Revision != before.Revision || got.MetaRevision == before.MetaRevision {
		t.Fatalf("revision %d→%d, meta revision %d→%d: the transcript revision moved", before.Revision, got.Revision, before.MetaRevision, got.MetaRevision)
	}
	if err := s.AddTaintState("20261110-missing", add); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing session: %v", err)
	}
}

func TestTaintOverflowCovers(t *testing.T) {
	a, ab := overflowWith("a1"), overflowWith("a1", "b1")
	zero := make(TaintOverflow, TaintOverflowBytes)
	wrong := TaintOverflow{1, 2, 3}
	for _, tc := range []struct {
		name string
		f, g TaintOverflow
		want bool
	}{
		{"empty g", nil, nil, true},
		{"all-zero g", nil, zero, true},
		{"subset", ab, a, true},
		{"superset", a, ab, false},
		{"equal copies", ab, bytes.Clone(ab), true},
		{"empty f", nil, a, false},
		{"saturated f", wrong, ab, true},
		{"wrong-size g", ab, wrong, false},
	} {
		if got := tc.f.Covers(tc.g); got != tc.want {
			t.Errorf("%s: Covers = %v, want %v", tc.name, got, tc.want)
		}
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
