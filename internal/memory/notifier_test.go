package memory

import (
	"sync"
	"testing"
)

// recordingNotifier captures every MemoryEvent for assertions. Safe for
// concurrent use because session-end episode/consolidation events fire from
// background goroutines.
type recordingNotifier struct {
	mu     sync.Mutex
	events []MemoryEvent
}

func (r *recordingNotifier) Notify(ev MemoryEvent) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *recordingNotifier) typesSeen() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := make(map[string]int)
	for _, e := range r.events {
		m[e.Type]++
	}
	return m
}

func (r *recordingNotifier) find(typ string) (MemoryEvent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		if e.Type == typ {
			return e, true
		}
	}
	return MemoryEvent{}, false
}

// ── Notifier plumbing ──────────────────────────────────────────────────

func TestNoopMemoryNotifier_DoesNotPanic(t *testing.T) {
	NoopMemoryNotifier{}.Notify(MemoryEvent{Type: "fact_added"})
}

func TestMultiMemoryNotifier_FansOut(t *testing.T) {
	var n1, n2 recordingNotifier
	mn := NewMultiMemoryNotifier(&n1, &n2)
	mn.Notify(MemoryEvent{Type: "fact_added", Target: "user"})

	if len(n1.events) != 1 || len(n2.events) != 1 {
		t.Fatalf("both notifiers should receive the event: n1=%d n2=%d", len(n1.events), len(n2.events))
	}
}

func TestSetNotifier_NilIsSafe(t *testing.T) {
	mm := NewMemoryManager(t.TempDir(), nil, DefaultMemoryConfig())
	mm.SetNotifier(nil) // must fall back to Noop, not panic on the next fire
	if err := mm.AddFact("user", "a durable user preference here"); err != nil {
		t.Fatal(err)
	}
}

// ── Fact lifecycle events ──────────────────────────────────────────────

func TestAddFact_FiresFactAdded(t *testing.T) {
	rec := &recordingNotifier{}
	mm := NewMemoryManager(t.TempDir(), nil, DefaultMemoryConfig())
	mm.SetNotifier(rec)

	if err := mm.AddFact("user", "user prefers tabs over spaces"); err != nil {
		t.Fatal(err)
	}
	ev, ok := rec.find("fact_added")
	if !ok {
		t.Fatalf("expected fact_added event, got %v", rec.typesSeen())
	}
	if ev.Target != "user" {
		t.Errorf("expected target=user, got %q", ev.Target)
	}
	if ev.Timestamp.IsZero() {
		t.Error("expected timestamp to be stamped")
	}
}

func TestAddFact_SilentDedupDoesNotFire(t *testing.T) {
	rec := &recordingNotifier{}
	// Disable merge-on-write so the second add hits the plain-dedup path.
	cfg := DefaultMemoryConfig()
	cfg.MergeOnWrite = boolPtr(false)
	mm := NewMemoryManager(t.TempDir(), nil, cfg)
	mm.SetNotifier(rec)

	const fact = "the build command is make build"
	if err := mm.AddFact("env", fact); err != nil {
		t.Fatal(err)
	}
	if err := mm.AddFact("env", fact); err != nil {
		t.Fatal(err)
	}
	if got := rec.typesSeen()["fact_added"]; got != 1 {
		t.Fatalf("expected exactly 1 fact_added (dedup must be silent), got %d", got)
	}
}

func TestReplaceAndRemoveFact_FireEvents(t *testing.T) {
	rec := &recordingNotifier{}
	mm := NewMemoryManager(t.TempDir(), nil, DefaultMemoryConfig())
	mm.SetNotifier(rec)

	if err := mm.AddFact("user", "user likes the color blue"); err != nil {
		t.Fatal(err)
	}
	if err := mm.ReplaceFact("user", "blue", "user likes the color green"); err != nil {
		t.Fatal(err)
	}
	if err := mm.RemoveFact("user", "green"); err != nil {
		t.Fatal(err)
	}
	seen := rec.typesSeen()
	if seen["fact_replaced"] != 1 {
		t.Errorf("expected 1 fact_replaced, got %d", seen["fact_replaced"])
	}
	if seen["fact_removed"] != 1 {
		t.Errorf("expected 1 fact_removed, got %d", seen["fact_removed"])
	}
}

func TestConsolidate_FiresFactConsolidated(t *testing.T) {
	rec := &recordingNotifier{}
	// LLM merges two entries into one.
	llm := &mockLLM{responses: map[string]string{
		"consolidation": `["the project uses Go and the chi router"]`,
	}}
	// Disable merge-on-write so both facts survive as distinct entries until
	// the explicit Consolidate call.
	cfg := DefaultMemoryConfig()
	cfg.MergeOnWrite = boolPtr(false)
	mm := NewMemoryManager(t.TempDir(), llm, cfg)
	mm.SetNotifier(rec)

	if err := mm.AddFact("env", "the project uses Go"); err != nil {
		t.Fatal(err)
	}
	if err := mm.AddFact("env", "the project uses the chi router"); err != nil {
		t.Fatal(err)
	}
	if err := mm.Consolidate("env"); err != nil {
		t.Fatal(err)
	}
	ev, ok := rec.find("fact_consolidated")
	if !ok {
		t.Fatalf("expected fact_consolidated, got %v", rec.typesSeen())
	}
	if ev.Count < ev.NewCount {
		t.Errorf("consolidation should not grow entries: before=%d after=%d", ev.Count, ev.NewCount)
	}
}

// ── Episode lifecycle events ───────────────────────────────────────────

func TestEpisodeStore_FiresStoredAndPendingReview(t *testing.T) {
	rec := &recordingNotifier{}
	es := NewEpisodeStore(t.TempDir(), defaultRanker)
	es.SetNotifier(rec)

	if err := es.WriteWithProvenance("20260606-a", "implemented the notifier", 5,
		EpisodeProvenance{Untrusted: true, Sources: []string{"browser"}}); err != nil {
		t.Fatal(err)
	}
	seen := rec.typesSeen()
	if seen["episode_stored"] != 1 {
		t.Errorf("expected 1 episode_stored, got %d", seen["episode_stored"])
	}
	if seen["episode_pending_review"] != 1 {
		t.Errorf("expected 1 episode_pending_review for untrusted episode, got %d", seen["episode_pending_review"])
	}
	ev, _ := rec.find("episode_stored")
	if !ev.Untrusted {
		t.Error("episode_stored should carry Untrusted=true")
	}
}

func TestEpisodeStore_FiresEvicted(t *testing.T) {
	rec := &recordingNotifier{}
	// Cap at 1 episode so the second write evicts the first.
	es := NewEpisodeStoreWithLifecycle(t.TempDir(), defaultRanker, 0, 1, 0)
	es.SetNotifier(rec)

	if err := es.WriteWithProvenance("20260606-a", "first episode summary alpha", 5, EpisodeProvenance{}); err != nil {
		t.Fatal(err)
	}
	if err := es.WriteWithProvenance("20260606-b", "second episode summary beta", 5, EpisodeProvenance{}); err != nil {
		t.Fatal(err)
	}
	ev, ok := rec.find("episode_evicted")
	if !ok {
		t.Fatalf("expected episode_evicted, got %v", rec.typesSeen())
	}
	if ev.Count != 1 || len(ev.Sessions) != 1 {
		t.Errorf("expected 1 evicted session, got count=%d sessions=%v", ev.Count, ev.Sessions)
	}
}

func TestEpisodeStore_FiresPromoted(t *testing.T) {
	rec := &recordingNotifier{}
	es := NewEpisodeStore(t.TempDir(), defaultRanker)
	es.SetNotifier(rec)

	if err := es.WriteWithProvenance("20260606-a", "a tainted episode summary", 5,
		EpisodeProvenance{Untrusted: true}); err != nil {
		t.Fatal(err)
	}
	if err := es.Promote("20260606-a"); err != nil {
		t.Fatal(err)
	}
	if _, ok := rec.find("episode_promoted"); !ok {
		t.Fatalf("expected episode_promoted, got %v", rec.typesSeen())
	}
}

// TestManager_PropagatesNotifierToEpisodes ensures SetNotifier on the manager
// reaches the underlying EpisodeStore (one sink for facts AND episodes).
func TestManager_PropagatesNotifierToEpisodes(t *testing.T) {
	rec := &recordingNotifier{}
	cfg := DefaultMemoryConfig()
	mm := NewMemoryManager(t.TempDir(), nil, cfg)
	mm.SetNotifier(rec)

	if err := mm.episodes.WriteWithProvenance("20260606-x", "summary via manager store", 5, EpisodeProvenance{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := rec.find("episode_stored"); !ok {
		t.Fatalf("manager.SetNotifier should propagate to episodes; got %v", rec.typesSeen())
	}
}
