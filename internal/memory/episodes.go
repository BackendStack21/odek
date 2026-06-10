package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/BackendStack21/go-vector/pkg/vector"
	"github.com/BackendStack21/odek/internal/session"
)

// maxEpisodeSummaryBytes caps how much summary text we store per episode.
const maxEpisodeSummaryBytes = 1024

// episodeIndexFile is the index filename inside the episodes dir.
const episodeIndexFile = "index.json"

// EpisodeMeta holds metadata for a single episode.
type EpisodeMeta struct {
	SessionID  string            `json:"session_id"`
	Turns      int               `json:"turns"`
	CreatedAt  time.Time         `json:"created_at"`
	Summary    string            `json:"summary"` // truncated for index listing
	Provenance EpisodeProvenance `json:"provenance,omitempty"`
}

// RankStrategy is an injectable function for ranking episodes by relevance
// to a query. The default implementation uses SimpleCall; tests can inject
// a deterministic mock.
type RankStrategy func(query string, episodes []EpisodeMeta) ([]EpisodeMeta, error)

// EpisodeStore manages on-disk episode summaries (Tier 3 memory).
// Written after sessions with sufficient turns, searchable via SimpleCall.
// Index operations are protected by a mutex to prevent TOCTOU races
// between concurrent sessions sharing the same memory directory.
//
// The in-memory idxCache avoids reading + unmarshalling index.json from
// disk on every FormatEpisodeContext call (which fires every loop turn).
// The cache is invalidated after every write.
type EpisodeStore struct {
	mu       sync.Mutex
	dir      string
	rankFn   RankStrategy
	idxCache []EpisodeMeta // cached index, nil = not loaded
	muCache  sync.RWMutex  // fine-grained lock for cache reads

	// newEmbedder builds the embedding backend used for per-turn recall
	// (via sharedEpisodeIndex) and write-time dedup. A factory rather than
	// an instance because the default RandomProjections embedder is
	// stateful per fitted corpus — the shared index and each dedup pass
	// need their own instance. Defaults to RandomProjections; overridden
	// via setEmbedderFactory when memory.embedding is configured.
	newEmbedder func() textEmbedder

	// notifier receives episode lifecycle events (stored, deduped, evicted,
	// promoted, pending-review). Defaults to a NoopMemoryNotifier so callers
	// never have to nil-check before firing. Set via SetNotifier, normally
	// propagated from the owning MemoryManager.
	notifier MemoryNotifier

	// Lifecycle policy (see NewEpisodeStoreWithLifecycle). Zero values disable
	// each mechanism, so the bare NewEpisodeStore constructor keeps the legacy
	// behavior (no dedup, no eviction); production wiring supplies defaults via
	// MemoryConfig.
	dedupThreshold float32 // cosine ≥ this → new episode replaces near-duplicate; 0 disables
	maxEpisodes    int     // keep at most this many episodes; 0 disables the cap
	ttlDays        int     // evict episodes older than this many days; 0 disables TTL

	// queryCache caches the last Search query result to avoid
	// re-ranking identical queries on consecutive turns.
	// Protected by muQuery.
	lastQuery  string
	lastResult []EpisodeMeta
	muQuery    sync.RWMutex
}

// NewEpisodeStore creates an EpisodeStore rooted at dir with lifecycle
// management disabled (no dedup, no eviction). If rankFn is nil, a default
// ranker is used (SimpleCall-based — requires LLM client).
func NewEpisodeStore(dir string, rankFn RankStrategy) *EpisodeStore {
	return NewEpisodeStoreWithLifecycle(dir, rankFn, 0, 0, 0)
}

// NewEpisodeStoreWithLifecycle creates an EpisodeStore with dedup + eviction
// policy. dedupThreshold is the cosine above which a new episode replaces an
// existing near-duplicate (0 disables); maxEpisodes caps the stored count
// (0 disables); ttlDays evicts episodes older than that many days (0 disables).
func NewEpisodeStoreWithLifecycle(dir string, rankFn RankStrategy, dedupThreshold float32, maxEpisodes, ttlDays int) *EpisodeStore {
	if rankFn == nil {
		rankFn = defaultRanker
	}
	return &EpisodeStore{
		dir:            dir,
		rankFn:         rankFn,
		dedupThreshold: dedupThreshold,
		maxEpisodes:    maxEpisodes,
		ttlDays:        ttlDays,
		notifier:       NoopMemoryNotifier{},
		newEmbedder:    defaultEmbedderFactory,
	}
}

// defaultEmbedderFactory preserves the pre-config behavior: local
// RandomProjections at the index dimensionality.
func defaultEmbedderFactory() textEmbedder { return newRPTextEmbedder(episodeVectorDim) }

// setEmbedderFactory replaces the embedding backend factory. Passing nil
// restores the default. Normally called once at construction time by
// NewMemoryManager, before any recall/write traffic.
func (e *EpisodeStore) setEmbedderFactory(f func() textEmbedder) {
	if f == nil {
		f = defaultEmbedderFactory
	}
	e.newEmbedder = f
}

// SetNotifier replaces the episode store's lifecycle notifier. If n is nil a
// NoopMemoryNotifier is used so the fire path is always safe.
func (e *EpisodeStore) SetNotifier(n MemoryNotifier) {
	if n == nil {
		n = NoopMemoryNotifier{}
	}
	e.mu.Lock()
	e.notifier = n
	e.mu.Unlock()
}

// notifyAll fires the given events on the configured notifier, stamping the
// timestamp on any that left it zero. It MUST be called WITHOUT holding e.mu:
// notifiers run arbitrary caller code (a WebSocket send under `odek serve`, or
// a user handler that could re-enter the store), so firing under the lock would
// serialize writes behind the sink and risk a reentrancy deadlock. Lifecycle
// methods therefore collect events while locked and fan them out after Unlock.
// Safe even if the notifier is nil.
func (e *EpisodeStore) notifyAll(events []MemoryEvent) {
	if e.notifier == nil || len(events) == 0 {
		return
	}
	now := time.Now().UTC()
	for _, ev := range events {
		if ev.Timestamp.IsZero() {
			ev.Timestamp = now
		}
		e.notifier.Notify(ev)
	}
}

// Write stores an episode summary for a session. Creates the episodes
// directory and updates the index. Equivalent to WriteWithProvenance
// with a zero-value (trusted) provenance.
func (e *EpisodeStore) Write(sessionID, summary string, turns int) error {
	return e.WriteWithProvenance(sessionID, summary, turns, EpisodeProvenance{})
}

// WriteWithProvenance stores an episode and attaches the given
// provenance to the index entry. An episode written with Untrusted=true
// is kept on disk but never auto-replayed (Search filters it out unless
// UserApproved=true).
// sessionID is validated for path traversal before use.
func (e *EpisodeStore) WriteWithProvenance(sessionID, summary string, turns int, prov EpisodeProvenance) error {
	if err := session.ValidateSessionID(sessionID); err != nil {
		return fmt.Errorf("memory: episodes write: %w", err)
	}
	if err := os.MkdirAll(e.dir, 0700); err != nil {
		return fmt.Errorf("memory: episodes mkdir: %w", err)
	}

	// Truncate summary to cap
	if len(summary) > maxEpisodeSummaryBytes {
		// Truncate at a rune boundary to avoid storing invalid UTF-8.
		summary = truncateAtRune(summary, maxEpisodeSummaryBytes) + "..."
	}

	e.mu.Lock()
	events, err := e.writeLocked(sessionID, summary, turns, prov)
	e.mu.Unlock()
	// Fan out lifecycle events only after releasing the lock (see notifyAll).
	e.notifyAll(events)
	return err
}

// writeLocked performs the full episode write under e.mu: dedup against
// existing episodes, write the summary file, update + prune the index, and
// mark the vector index dirty. File mutations happen before writeIndex, which
// happens before markDirty, so a crash leaves at most a dangling index entry
// (rebuild/recall tolerate a missing .md) rather than an orphan file in the
// index. Caller must hold e.mu. Lifecycle events are returned (not fired) so the
// caller can fan them out after releasing the lock (see notifyAll).
func (e *EpisodeStore) writeLocked(sessionID, summary string, turns int, prov EpisodeProvenance) ([]MemoryEvent, error) {
	var events []MemoryEvent
	idx, err := e.ReadIndex()
	if err != nil {
		idx = []EpisodeMeta{}
	}

	// Drop any existing entry for this same sessionID (re-running a session
	// overwrites its episode rather than appending a duplicate index entry).
	idx = removeBySessionID(idx, sessionID)

	// Dedup: if a near-duplicate exists, replace it with this newer episode —
	// but never let an untrusted episode evict a trusted/approved one.
	if e.dedupThreshold > 0 {
		if dupIdx, sim := e.findDuplicate(summary, idx); dupIdx >= 0 && sim >= e.dedupThreshold {
			if trustRank(prov) >= trustRank(idx[dupIdx].Provenance) {
				deduped := idx[dupIdx].SessionID
				e.removeEpisodeFile(deduped)
				idx = append(idx[:dupIdx], idx[dupIdx+1:]...)
				events = append(events, MemoryEvent{
					Type:       "episode_deduped",
					SessionID:  deduped,
					Similarity: sim,
				})
			}
		}
	}

	// Write the summary file.
	path := filepath.Join(e.dir, sessionID+".md")
	if err := os.WriteFile(path, []byte(summary), 0600); err != nil {
		return events, fmt.Errorf("memory: write episode: %w", err)
	}

	idx = append(idx, EpisodeMeta{
		SessionID:  sessionID,
		Turns:      turns,
		CreatedAt:  time.Now().UTC(),
		Summary:    truncateForIndex(summary),
		Provenance: prov,
	})

	// Evict by TTL + count cap; remove the corresponding summary files.
	idx, removed := e.pruneLocked(idx)
	for _, sid := range removed {
		e.removeEpisodeFile(sid)
	}

	if err := e.writeIndex(idx); err != nil {
		return events, err
	}

	// Mark the vector index dirty so it rebuilds on the next recall, picking up
	// the new episode and dropping any evicted/replaced ids.
	sharedEpisodeIndex(e.dir, e.newEmbedder).markDirty()

	// Collect lifecycle signals only after the write has durably succeeded. They
	// are fired by the caller once e.mu is released (see notifyAll).
	events = append(events, MemoryEvent{
		Type:      "episode_stored",
		SessionID: sessionID,
		Content:   truncateForIndex(summary),
		Count:     turns,
		Untrusted: prov.Untrusted,
	})
	// An untrusted, unapproved episode is persisted but excluded from recall
	// until `odek memory promote` — make that gap observable.
	if prov.Untrusted && !prov.UserApproved && !prov.AutoApproved {
		events = append(events, MemoryEvent{Type: "episode_pending_review", SessionID: sessionID})
	}
	if len(removed) > 0 {
		events = append(events, MemoryEvent{Type: "episode_evicted", Sessions: removed, Count: len(removed)})
	}
	return events, nil
}

// WriteIfEnough calls Write only if turns >= threshold.
// Returns nil without writing if the threshold isn't met.
func (e *EpisodeStore) WriteIfEnough(sessionID, summary string, turns int) error {
	return e.WriteIfEnoughWithProvenance(sessionID, summary, turns, EpisodeProvenance{})
}

// WriteIfEnoughWithProvenance is the provenance-carrying counterpart of
// WriteIfEnough.
func (e *EpisodeStore) WriteIfEnoughWithProvenance(sessionID, summary string, turns int, prov EpisodeProvenance) error {
	const defaultThreshold = 3
	if turns < defaultThreshold {
		return nil
	}
	return e.WriteWithProvenance(sessionID, summary, turns, prov)
}

// Read returns the full summary content for a session.
// sessionID is validated for path traversal before use.
func (e *EpisodeStore) Read(sessionID string) (string, error) {
	if err := session.ValidateSessionID(sessionID); err != nil {
		return "", fmt.Errorf("memory: episodes read: %w", err)
	}
	path := filepath.Join(e.dir, sessionID+".md")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("memory: read episode %s: %w", sessionID, err)
	}
	return string(data), nil
}

// ReadIndex reads the episode index from disk. Returns empty slice if the
// index file doesn't exist yet. Entries are ordered newest-first.
//
// Uses an in-memory cache to avoid disk I/O on every Search() call.
// The cache is invalidated after every writeIndex().
func (e *EpisodeStore) ReadIndex() ([]EpisodeMeta, error) {
	// Fast path: return cached index if available.
	e.muCache.RLock()
	if e.idxCache != nil {
		cpy := make([]EpisodeMeta, len(e.idxCache))
		copy(cpy, e.idxCache)
		e.muCache.RUnlock()
		return cpy, nil
	}
	e.muCache.RUnlock()

	// Slow path: read from disk.
	idxPath := filepath.Join(e.dir, episodeIndexFile)
	data, err := os.ReadFile(idxPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []EpisodeMeta{}, nil
		}
		return nil, fmt.Errorf("memory: read episode index: %w", err)
	}
	var idx []EpisodeMeta
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("memory: parse episode index: %w", err)
	}
	// Ensure newest-first
	sort.Slice(idx, func(i, j int) bool {
		return idx[i].CreatedAt.After(idx[j].CreatedAt)
	})

	// Populate cache.
	e.muCache.Lock()
	e.idxCache = idx
	e.muCache.Unlock()

	// Return a copy to prevent callers from mutating the cache.
	cpy := make([]EpisodeMeta, len(idx))
	copy(cpy, idx)
	return cpy, nil
}

// Search returns the most relevant episodes for a query, ranked by the
// configured RankStrategy. Limited to limit results.
func (e *EpisodeStore) Search(query string, limit int) ([]EpisodeMeta, error) {
	// Check query cache under a single lock to close the window between the
	// "same query?" check and the actual read. The old pattern (RLock/RUnlock
	// then RLock again) allowed the cache to be invalidated in between.
	e.muQuery.RLock()
	if query == e.lastQuery && e.lastResult != nil {
		result := make([]EpisodeMeta, len(e.lastResult))
		copy(result, e.lastResult)
		e.muQuery.RUnlock()
		if limit > 0 && len(result) > limit {
			result = result[:limit]
		}
		return result, nil
	}
	e.muQuery.RUnlock()

	idx, err := e.ReadIndex()
	if err != nil {
		return nil, err
	}
	// Filter out tainted episodes that the user has not promoted. They
	// remain on disk for audit (see ReadIndex) but must not be replayed
	// into a new session's context — that is the persistence vector
	// EpisodeProvenance exists to close.
	filtered := idx[:0:len(idx)]
	for _, ep := range idx {
		if ep.Provenance.Untrusted && !ep.Provenance.UserApproved && !ep.Provenance.AutoApproved {
			continue
		}
		filtered = append(filtered, ep)
	}
	if len(filtered) == 0 {
		return nil, nil
	}

	ranked, err := e.rankFn(query, filtered)
	if err != nil {
		return nil, fmt.Errorf("memory: search episodes: %w", err)
	}

	// Cache result for this query
	e.muQuery.Lock()
	e.lastQuery = query
	e.lastResult = make([]EpisodeMeta, len(ranked))
	copy(e.lastResult, ranked)
	e.muQuery.Unlock()

	if limit > 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}
	return ranked, nil
}

// recallByVector is the per-turn recall path. It searches the cached go-vector
// index (zero LLM calls), applies the provenance filter, and returns up to k
// trusted episodes. Falls back to nil on any error so the turn is never blocked.
func (e *EpisodeStore) recallByVector(query string, k int) ([]EpisodeMeta, error) {
	if k <= 0 {
		k = 3
	}
	// Over-fetch so the provenance filter still leaves k usable results.
	// Discard zero-score results: when the query has no vocabulary overlap with
	// the episode corpus (all-OOV), go-vector returns a zero vector and every
	// Store.Search result has cosine similarity = 0. Returning those as
	// "candidates" would make SearchEpisodes skip the LLM fallback with noise.
	raw := sharedEpisodeIndex(e.dir, e.newEmbedder).search(query, k*3+5)
	scored := raw[:0:len(raw)]
	for _, s := range raw {
		if s.Score > 0 {
			scored = append(scored, s)
		}
	}
	if len(scored) == 0 {
		return nil, nil
	}
	idx, err := e.ReadIndex()
	if err != nil {
		return nil, err
	}
	byID := make(map[string]EpisodeMeta, len(idx))
	for _, ep := range idx {
		byID[ep.SessionID] = ep
	}
	out := make([]EpisodeMeta, 0, k)
	for _, s := range scored {
		ep, ok := byID[s.ID]
		if !ok {
			continue
		}
		if ep.Provenance.Untrusted && !ep.Provenance.UserApproved && !ep.Provenance.AutoApproved {
			continue
		}
		out = append(out, ep)
		if len(out) >= k {
			break
		}
	}
	return out, nil
}

// ── Promotion (human-gated escape hatch) ──────────────────────────────

// Promote marks a tainted episode as user-approved so it can be replayed
// into future sessions. This is the human-gated escape hatch for episodes
// whose originating session legitimately touched external content. It is
// intentionally NOT exposed to the agent (only via `odek memory promote`) so
// that a prompt-injected agent cannot self-approve poisoned memory.
//
// Returns an error if the session is unknown or already approved.
func (e *EpisodeStore) Promote(sessionID string) error {
	if err := session.ValidateSessionID(sessionID); err != nil {
		return fmt.Errorf("memory: episodes promote: %w", err)
	}
	e.mu.Lock()

	idx, err := e.ReadIndex()
	if err != nil {
		e.mu.Unlock()
		return err
	}
	found := false
	for i := range idx {
		if idx[i].SessionID == sessionID {
			found = true
			if idx[i].Provenance.UserApproved {
				e.mu.Unlock()
				return fmt.Errorf("memory: episode %q is already approved", sessionID)
			}
			idx[i].Provenance.UserApproved = true
		}
	}
	if !found {
		e.mu.Unlock()
		return fmt.Errorf("memory: episode %q not found", sessionID)
	}
	if err := e.writeIndex(idx); err != nil {
		e.mu.Unlock()
		return err
	}
	e.mu.Unlock()
	// Fired after releasing the lock (see notifyAll).
	e.notifyAll([]MemoryEvent{{Type: "episode_promoted", SessionID: sessionID}})
	return nil
}

// PendingReview returns the episodes that are untrusted and not yet
// user-approved — the ones currently excluded from recall that a user may
// want to promote. Ordered newest-first (as ReadIndex returns them).
func (e *EpisodeStore) PendingReview() ([]EpisodeMeta, error) {
	idx, err := e.ReadIndex()
	if err != nil {
		return nil, err
	}
	var pending []EpisodeMeta
	for _, ep := range idx {
		if ep.Provenance.Untrusted && !ep.Provenance.UserApproved && !ep.Provenance.AutoApproved {
			pending = append(pending, ep)
		}
	}
	return pending, nil
}

// ── Lifecycle helpers ─────────────────────────────────────────────────

// Prune evicts episodes by TTL and count cap (see NewEpisodeStoreWithLifecycle)
// and removes their summary files. Safe to call at session end or from a CLI.
// No-op when both the cap and TTL are disabled.
func (e *EpisodeStore) Prune() error {
	e.mu.Lock()

	if e.maxEpisodes <= 0 && e.ttlDays <= 0 {
		e.mu.Unlock()
		return nil
	}
	idx, err := e.ReadIndex()
	if err != nil {
		e.mu.Unlock()
		return err
	}
	idx, removed := e.pruneLocked(idx)
	if len(removed) == 0 {
		e.mu.Unlock()
		return nil
	}
	for _, sid := range removed {
		e.removeEpisodeFile(sid)
	}
	if err := e.writeIndex(idx); err != nil {
		e.mu.Unlock()
		return err
	}
	sharedEpisodeIndex(e.dir, e.newEmbedder).markDirty()
	e.mu.Unlock()
	// Fired after releasing the lock (see notifyAll).
	e.notifyAll([]MemoryEvent{{Type: "episode_evicted", Sessions: removed, Count: len(removed)}})
	return nil
}

// pruneLocked applies TTL then count-cap eviction, returning the kept entries
// and the sessionIDs whose summary files should be removed. Trust-blind by
// design: eviction is a disk/recall budget, not a trust decision. Caller must
// hold e.mu.
func (e *EpisodeStore) pruneLocked(idx []EpisodeMeta) ([]EpisodeMeta, []string) {
	var removed []string

	if e.ttlDays > 0 {
		cutoff := time.Now().UTC().Add(-time.Duration(e.ttlDays) * 24 * time.Hour)
		kept := make([]EpisodeMeta, 0, len(idx))
		for _, m := range idx {
			if m.CreatedAt.Before(cutoff) {
				removed = append(removed, m.SessionID)
			} else {
				kept = append(kept, m)
			}
		}
		idx = kept
	}

	if e.maxEpisodes > 0 && len(idx) > e.maxEpisodes {
		// Newest-first so the oldest fall off the end.
		sort.Slice(idx, func(i, j int) bool {
			return idx[i].CreatedAt.After(idx[j].CreatedAt)
		})
		for _, m := range idx[e.maxEpisodes:] {
			removed = append(removed, m.SessionID)
		}
		idx = idx[:e.maxEpisodes]
	}

	return idx, removed
}

// findDuplicate returns the index of the existing episode most similar to
// newSummary and that similarity, comparing full on-disk summaries via an
// ephemeral embedder from the store's factory. Returns (-1, 0) for an empty
// corpus or when embedding fails (no dedup is safer than a wrong dedup —
// writeLocked would DELETE the matched episode). Caller must hold e.mu.
func (e *EpisodeStore) findDuplicate(newSummary string, idx []EpisodeMeta) (int, float32) {
	if len(idx) == 0 {
		return -1, 0
	}
	corpus := make([]string, len(idx))
	for i, m := range idx {
		if s, err := e.Read(m.SessionID); err == nil {
			corpus[i] = s
		} else {
			corpus[i] = m.Summary // fallback to the index summary
		}
	}

	emb := e.newEmbedder()
	if err := emb.fit(append(append([]string{}, corpus...), newSummary)); err != nil {
		return -1, 0
	}
	newVec, err := emb.embed(newSummary)
	if err != nil {
		return -1, 0
	}
	vecs, err := emb.embedAll(corpus)
	if err != nil {
		return -1, 0
	}

	best := -1
	var bestSim float32
	for i, vec := range vecs {
		if vec == nil {
			continue
		}
		if sim := cosineVector(newVec, vec); sim > bestSim {
			bestSim = sim
			best = i
		}
	}
	return best, bestSim
}

// removeEpisodeFile deletes a session's summary file, but ONLY after validating
// the sessionID — defense-in-depth so a crafted/corrupted index.json entry can
// never make eviction/dedup os.Remove a path outside the episodes dir. Mirrors
// the validation that Read/Write/Promote already apply. Best-effort.
func (e *EpisodeStore) removeEpisodeFile(sessionID string) {
	if err := session.ValidateSessionID(sessionID); err != nil {
		return
	}
	_ = os.Remove(filepath.Join(e.dir, sessionID+".md"))
}

// removeBySessionID returns idx without any entry matching sessionID.
func removeBySessionID(idx []EpisodeMeta, sessionID string) []EpisodeMeta {
	out := idx[:0]
	for _, m := range idx {
		if m.SessionID != sessionID {
			out = append(out, m)
		}
	}
	return out
}

// trustRank maps provenance to a coarse trust level for dedup gating: an
// untrusted, unapproved episode ranks below any trusted/approved one, so it
// can never evict it. Mirrors the recall provenance filter.
func trustRank(p EpisodeProvenance) int {
	if p.Untrusted && !p.UserApproved && !p.AutoApproved {
		return 0
	}
	return 1
}

// writeIndex serializes the index to disk atomically (temp + rename).
// Caller must hold e.mu.
// Invalidates the in-memory cache after a successful write so the next
// ReadIndex call picks up the new data.
func (e *EpisodeStore) writeIndex(idx []EpisodeMeta) error {
	// Write to temp + rename for atomicity
	idxPath := filepath.Join(e.dir, episodeIndexFile)
	tmpPath := idxPath + ".tmp"

	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fmt.Errorf("memory: marshal index: %w", err)
	}
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, idxPath); err != nil {
		os.Remove(tmpPath) // best-effort cleanup
		return err
	}

	// Update in-memory cache with sorted copy (newest-first).
	sorted := make([]EpisodeMeta, len(idx))
	copy(sorted, idx)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].CreatedAt.After(sorted[j].CreatedAt)
	})
	e.muCache.Lock()
	e.idxCache = sorted
	e.muCache.Unlock()

	return nil
}

// truncateForIndex shortens the summary for the index entry (first 120 chars).
func truncateForIndex(summary string) string {
	if len(summary) > 120 {
		return summary[:117] + "..."
	}
	return summary
}

// ── Default ranker ────────────────────────────────────────────────────

// defaultRanker returns all episodes sorted by recency (no LLM).
func defaultRanker(query string, episodes []EpisodeMeta) ([]EpisodeMeta, error) {
	out := make([]EpisodeMeta, len(episodes))
	copy(out, episodes)
	return out, nil
}

// NewLLMRanker creates a RankStrategy that uses an LLM client to rank
// episodes by semantic relevance to the query. Falls back to recency
// ordering if the LLM call fails or returns unparseable output.
func NewLLMRanker(llm LLMClient) RankStrategy {
	return func(query string, episodes []EpisodeMeta) ([]EpisodeMeta, error) {
		if len(episodes) == 0 {
			return episodes, nil
		}

		// Build a compact prompt listing episodes
		var b strings.Builder
		fmt.Fprintf(&b, "Rank these memory summaries by relevance to: %s\n\n", query)
		for i, ep := range episodes {
			// Truncate summary for the prompt (already truncated in index, but be safe)
			summary := ep.Summary
			if len(summary) > 200 {
				summary = summary[:197] + "..."
			}
			fmt.Fprintf(&b, "[%d] %s\n", i, summary)
		}
		b.WriteString("\nReturn only the indices of the most relevant entries, ordered by relevance (most relevant first).\n")
		b.WriteString("Format: a single line of comma-separated numbers, e.g. \"3,0,1\"\n")
		b.WriteString("If none are relevant, return \"none\".")

		resp, err := llm.SimpleCall(context.Background(),
			"You are a relevance ranking system. Given a query and a list of items, return the indices of the most relevant items ordered by relevance. Return only a comma-separated list of numbers or the word 'none'.",
			b.String(),
		)
		if err != nil || strings.TrimSpace(resp) == "" {
			return defaultRanker(query, episodes)
		}

		resp = strings.TrimSpace(resp)
		if resp == "none" {
			return nil, nil
		}

		// Parse "3,0,1" or "3, 0, 1" into indices
		parts := strings.Split(resp, ",")
		seen := make(map[int]bool)
		var ranked []EpisodeMeta
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			idx := 0
			for _, c := range p {
				if c >= '0' && c <= '9' {
					idx = idx*10 + int(c-'0')
				}
			}
			if idx >= 0 && idx < len(episodes) && !seen[idx] {
				ranked = append(ranked, episodes[idx])
				seen[idx] = true
			}
		}

		if len(ranked) == 0 {
			return defaultRanker(query, episodes)
		}
		return ranked, nil
	}
}

// NewRPRanker creates a RankStrategy that uses RandomProjections (go-vector)
// for semantic similarity search over episodes. No LLM calls — pure vector
// math. Falls back to recency ordering if RP fitting fails.
func NewRPRanker(dims int) RankStrategy {
	if dims <= 0 {
		dims = 64 // lower dims = faster, fine for short summaries
	}
	return newEmbedderRanker(func() textEmbedder { return newRPTextEmbedder(dims) })
}

// newEmbedderRanker creates a RankStrategy that ranks episodes by cosine
// similarity in the embedding space produced by newEmb. Falls back to recency
// ordering (the input order) when embedding fails.
//
// The ranker fits a fresh embedder on episode summaries on each call. For
// RandomProjections that takes < 1ms at typical episode counts; for HTTP
// backends it costs one batch call, which only the explicit memory-search
// path pays (per-turn recall goes through the cached shared index instead).
func newEmbedderRanker(newEmb func() textEmbedder) RankStrategy {
	return func(query string, episodes []EpisodeMeta) ([]EpisodeMeta, error) {
		if len(episodes) <= 1 {
			out := make([]EpisodeMeta, len(episodes))
			copy(out, episodes)
			return out, nil
		}

		// Build corpus from episode summaries
		corpus := make([]string, len(episodes))
		for i, ep := range episodes {
			corpus[i] = ep.Summary
		}

		recency := func() []EpisodeMeta {
			out := make([]EpisodeMeta, len(episodes))
			copy(out, episodes)
			return out
		}

		emb := newEmb()
		if err := emb.fit(append(corpus, query)); err != nil {
			return recency(), nil
		}
		queryVec, err := emb.embed(query)
		if err != nil {
			return recency(), nil
		}
		vecs, err := emb.embedAll(corpus)
		if err != nil {
			return recency(), nil
		}

		// Score each episode by cosine similarity
		type scored struct {
			idx   int
			score float32
		}
		scores := make([]scored, len(episodes))
		for i, vec := range vecs {
			scores[i] = scored{idx: i, score: cosineVector(queryVec, vec)}
		}

		// Sort by score descending
		sort.Slice(scores, func(i, j int) bool {
			return scores[i].score > scores[j].score
		})

		out := make([]EpisodeMeta, len(episodes))
		for i, s := range scores {
			out[i] = episodes[s.idx]
		}
		return out, nil
	}
}

// cosineVector computes cosine similarity between two go-vector Vectors.
func cosineVector(a, b vector.Vector) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		da := float64(a[i])
		db := float64(b[i])
		dot += da * db
		normA += da * da
		normB += db * db
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	sim := dot / (math.Sqrt(normA) * math.Sqrt(normB))
	if math.IsNaN(sim) || math.IsInf(sim, 0) {
		// A buggy/hostile embedding backend can return NaN/Inf components; a
		// NaN score breaks sort ordering (non-strict-weak). Treat as "no
		// similarity" so ranking stays well-defined. Mirrors the NaN guard in
		// MergeDetector.Classify.
		return 0
	}
	return float32(sim)
}

// truncateAtRune returns s truncated to at most maxBytes bytes, always
// cutting at a rune boundary so the result is valid UTF-8.
func truncateAtRune(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// Walk backwards from maxBytes until we find a valid rune boundary.
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}
