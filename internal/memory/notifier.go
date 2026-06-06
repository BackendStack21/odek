package memory

import "time"

// ── Memory Event ────────────────────────────────────────────────────────

// MemoryEvent represents a memory lifecycle event emitted by the
// MemoryManager (and its EpisodeStore). Callers (Terminal, WebUI, Telegram,
// or embedding programs) consume these events to surface memory activity —
// facts being added/merged/consolidated and episodes being stored, deduped,
// evicted, or promoted. Previously every one of these moments was silent.
//
// Not every field is set for every Type; see the per-Type notes below. The
// zero value of a field means "not applicable to this event".
type MemoryEvent struct {
	// Type is the lifecycle moment. One of:
	//   "fact_added"            — a new durable fact was appended (Target, Content)
	//   "fact_merged"           — merge-on-write folded a fact into an existing
	//                             near-duplicate (Target, Content, Similarity)
	//   "fact_replaced"         — an existing fact was replaced (Target, Content)
	//   "fact_removed"          — a fact was removed (Target, Content=old text)
	//   "fact_consolidated"     — LLM consolidation merged entries (Target,
	//                             Count=before, NewCount=after)
	//   "episode_stored"        — a session episode was extracted + persisted
	//                             (SessionID, Count=turns, Content=summary,
	//                             Untrusted)
	//   "episode_deduped"       — a new episode replaced a near-duplicate
	//                             (SessionID, Similarity)
	//   "episode_evicted"       — episodes were pruned by TTL/count cap
	//                             (Sessions, Count=number evicted)
	//   "episode_promoted"      — a tainted episode was user-approved for recall
	//                             (SessionID)
	//   "episode_pending_review"— an untrusted, unapproved episode was stored and
	//                             is excluded from recall until promoted
	//                             (SessionID)
	Type string

	Target     string    // fact scope: "user" or "env"
	SessionID  string    // episode session ID
	Content    string    // fact text or episode summary excerpt
	Count      int       // before-count (consolidate), turns (episode), or evicted count
	NewCount   int       // after-count (consolidate)
	Sessions   []string  // evicted session IDs (episode_evicted)
	Similarity float32   // cosine similarity for merge/dedup events
	Untrusted  bool      // episode derived from a session that touched untrusted content
	Timestamp  time.Time // when the event occurred (UTC)
}

// ── MemoryNotifier Interface ────────────────────────────────────────────

// MemoryNotifier is the observer interface for memory lifecycle events.
// Implementations should be non-blocking in the hot path (fact writes fire
// mid-loop); use channel-based or async dispatch for I/O.
type MemoryNotifier interface {
	Notify(event MemoryEvent)
}

// ── NoopMemoryNotifier ──────────────────────────────────────────────────

// NoopMemoryNotifier is a MemoryNotifier that discards all events.
// Used as the default when no notifier is configured, so callers never have
// to nil-check before firing.
type NoopMemoryNotifier struct{}

// Notify discards the event.
func (NoopMemoryNotifier) Notify(event MemoryEvent) {}

// ── MultiMemoryNotifier ─────────────────────────────────────────────────

// MultiMemoryNotifier fans out each Notify call to all registered notifiers.
// Safe for concurrent use when the notifier slice is set before any calls to
// Notify (the standard pattern: constructed, set on MemoryManager, then
// called from the agent loop / session-end goroutines).
type MultiMemoryNotifier struct {
	notifiers []MemoryNotifier
}

// NewMultiMemoryNotifier creates a MultiMemoryNotifier that fans out to the
// given notifiers.
func NewMultiMemoryNotifier(notifiers ...MemoryNotifier) *MultiMemoryNotifier {
	return &MultiMemoryNotifier{notifiers: notifiers}
}

// Notify fans out the event to all registered notifiers.
func (m *MultiMemoryNotifier) Notify(event MemoryEvent) {
	for _, n := range m.notifiers {
		n.Notify(event)
	}
}
