package session

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
)

// taintStateHolds reports whether st still recognises sp: in its list, or
// in its overflow filter.
func taintStateHolds(st TaintState, sp TaintSpan) bool {
	for _, have := range st.Spans {
		if have.Kind == sp.Kind && have.Value == sp.Value && have.Hash == sp.Hash {
			return true
		}
	}
	if sp.Hash != "" {
		return st.Overflow.HasHash(sp.Kind, sp.Hash)
	}
	return st.Overflow.HasValue(sp.Kind, sp.Value)
}

// overflowFixture returns a full list of newer spans and a set of older
// spans a merge with it must cut, including an over-long value.
func overflowFixture() (newer, older []TaintSpan) {
	for i := 0; i < MaxTaintSpans; i++ {
		newer = append(newer, TaintSpan{Kind: "host", Value: fmt.Sprintf("n%d.example", i), Source: "web"})
	}
	for i := 0; i < 300; i++ {
		older = append(older,
			TaintSpan{Kind: "host", Value: fmt.Sprintf("o%d.example", i), Source: "web"},
			TaintSpan{Kind: "token", Hash: TaintSpanHash(fmt.Sprintf("Tok%04dQwErTyUiOpAsDfGhJkLzXcVbNm", i)), Source: "web"})
	}
	older = append(older, TaintSpan{Kind: "url", Value: "https://evil.example/" + strings.Repeat("a", MaxTaintSpanValueBytes)})
	return newer, older
}

// A merge bounds the span list, but never forgets: every span it cuts (for
// the cap or for an over-long value) goes into the overflow filter, and a
// later merge keeps the filter.
func TestRED_MergeTaintState_RecordsEveryDroppedSpan(t *testing.T) {
	newer, older := overflowFixture()
	got := MergeTaintState(TaintState{Spans: newer}, TaintState{Spans: older})
	if len(got.Spans) != MaxTaintSpans || got.Spans[0].Value != "n0.example" {
		t.Fatalf("merge cap/order: len=%d", len(got.Spans))
	}
	for _, sp := range append(append([]TaintSpan{}, newer...), older...) {
		if !taintStateHolds(got, sp) {
			t.Fatalf("merge dropped a span without recording it: %+v", sp)
		}
	}
	again := MergeTaintState(TaintState{Spans: newer[:10]}, got)
	for _, sp := range older {
		if !taintStateHolds(again, sp) {
			t.Fatalf("a later merge lost the overflow record of %+v", sp)
		}
	}
}

// Store.Save merges onto the file's state; what the merge cuts is persisted
// in the filter and survives a reload byte for byte.
func TestRED_SessionTaintOverflow_SaveMergesAndPersists(t *testing.T) {
	s, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	newer, older := overflowFixture()
	sess := &Session{ID: "20261109-overflow", Task: "t", Messages: []Message{{Role: "user", Content: "hi"}}}
	sess.TaintSpans = older
	if err := s.Save(sess); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	loaded.TaintSpans = newer
	if err := s.Save(loaded); err != nil {
		t.Fatal(err)
	}
	final, err := s.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(final.TaintSpans) != MaxTaintSpans {
		t.Fatalf("persisted spans = %d, want %d", len(final.TaintSpans), MaxTaintSpans)
	}
	for _, sp := range older {
		if !taintStateHolds(final.TaintState(), sp) {
			t.Fatalf("save dropped a span without recording it: %+v", sp)
		}
	}
	if len(final.TaintOverflow) != TaintOverflowBytes || !bytes.Equal(final.TaintOverflow, loaded.TaintOverflow) {
		t.Fatal("the persisted filter changed across a save and reload")
	}
}

func saturated(f TaintOverflow) bool {
	if len(f) != TaintOverflowBytes {
		return false
	}
	for _, b := range f {
		if b != 0xff {
			return false
		}
	}
	return true
}

// A filter of the wrong size cannot rule any span out: it is treated as
// saturated (every lookup hits) and rewritten as a full filter of the right
// size, never trusted and never an error.
func TestTaintOverflow_WrongSizeFailsClosed(t *testing.T) {
	wrong := TaintOverflow{1, 2, 3}
	if !wrong.HasValue("host", "anything.example") || !wrong.HasHash("token", TaintSpanHash("x")) {
		t.Fatal("a wrong-size filter ruled a span out")
	}
	if !saturated(UnionTaintOverflow(wrong, nil)) || !saturated(UnionTaintOverflow(nil, wrong)) {
		t.Fatal("a union with a wrong-size filter is not saturated")
	}
	if !saturated(MergeTaintState(TaintState{Overflow: wrong}, TaintState{}).Overflow) {
		t.Fatal("a merge kept a wrong-size filter")
	}
	added := TaintOverflow{1}
	added.AddValue("host", "x.example")
	if !saturated(added) {
		t.Fatal("adding to a wrong-size filter did not saturate it")
	}

	s, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const id = "20261109-wrongsize"
	data := `{"id":"` + id + `","messages":[{"role":"user","content":"hi"}],"taint_overflow":"AQID"}`
	if err := os.WriteFile(s.path(id), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load(id)
	if err != nil {
		t.Fatalf("a wrong-size filter failed the load: %v", err)
	}
	if !loaded.TaintOverflow.HasValue("host", "never-seen.example") {
		t.Fatal("a loaded wrong-size filter ruled a span out")
	}
	if err := s.Save(loaded); err != nil {
		t.Fatal(err)
	}
	again, err := s.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if !saturated(again.TaintOverflow) {
		t.Fatalf("a save did not rewrite the wrong-size filter as saturated (len %d)", len(again.TaintOverflow))
	}
}

func TestUnionTaintOverflow(t *testing.T) {
	if UnionTaintOverflow(nil, nil) != nil {
		t.Fatal("empty union not nil")
	}
	if UnionTaintOverflow(make(TaintOverflow, TaintOverflowBytes), nil) != nil {
		t.Fatal("an all-zero filter did not normalise to nil")
	}
	var a, b TaintOverflow
	a.AddValue("host", "a.example")
	b.AddHash("token", TaintSpanHash("tok"))
	aCopy, bCopy := bytes.Clone(a), bytes.Clone(b)
	u := UnionTaintOverflow(a, b)
	if !u.HasValue("host", "a.example") || !u.HasValue("token", "tok") || u.HasValue("host", "c.example") {
		t.Fatal("union lost or invented a span")
	}
	if !bytes.Equal(a, aCopy) || !bytes.Equal(b, bCopy) || &u[0] == &a[0] {
		t.Fatal("union modified or aliased an input")
	}
	// A value and its hash set the same bits.
	if !a.HasHash("host", TaintSpanHash("a.example")) {
		t.Fatal("a span added by value is not found by its hash")
	}
	var empty TaintOverflow
	if empty.HasValue("host", "a.example") || empty.HasHash("host", TaintSpanHash("a.example")) {
		t.Fatal("an empty filter holds a span")
	}
	var nilSess *Session
	if !nilSess.TaintState().Empty() {
		t.Fatal("nil session has taint state")
	}
}

// A save served from the settled revision stamp must keep a filter the
// previous revision recorded, even with no spans beside it.
func TestSessionTaintOverflow_SurvivesSettledRevisionCache(t *testing.T) {
	prev := revStampSettle
	revStampSettle = 0
	defer func() { revStampSettle = prev }()
	s, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess := &Session{ID: "20261109-settled", Task: "t", Messages: []Message{{Role: "user", Content: "hi"}}}
	if err := s.Save(sess); err != nil {
		t.Fatal(err)
	}
	if s.cachedRevisionForTest(sess.ID) == nil {
		t.Skip("filesystem gives no nanosecond mtime or inode; the stamp cache is never used here")
	}
	sess.TaintOverflow.AddValue("host", "evil.example")
	if err := s.Save(sess); err != nil {
		t.Fatal(err)
	}
	sess.TaintOverflow = nil
	if err := s.Save(sess); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.TaintOverflow.HasValue("host", "evil.example") {
		t.Fatal("a settled-cache save dropped the previous revision's filter")
	}
}
