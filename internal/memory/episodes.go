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

	// queryCache caches the last Search query result to avoid
	// re-ranking identical queries on consecutive turns.
	// Protected by muQuery.
	lastQuery  string
	lastResult []EpisodeMeta
	muQuery    sync.RWMutex
}

// NewEpisodeStore creates an EpisodeStore rooted at dir. If rankFn is nil,
// a default ranker is used (SimpleCall-based — requires LLM client).
func NewEpisodeStore(dir string, rankFn RankStrategy) *EpisodeStore {
	if rankFn == nil {
		rankFn = defaultRanker
	}
	return &EpisodeStore{
		dir:    dir,
		rankFn: rankFn,
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

	// Write summary file
	path := filepath.Join(e.dir, sessionID+".md")
	if err := os.WriteFile(path, []byte(summary), 0600); err != nil {
		return fmt.Errorf("memory: write episode: %w", err)
	}

	// Update index
	return e.addToIndex(EpisodeMeta{
		SessionID:  sessionID,
		Turns:      turns,
		CreatedAt:  time.Now().UTC(),
		Summary:    truncateForIndex(summary),
		Provenance: prov,
	})
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
		if ep.Provenance.Untrusted && !ep.Provenance.UserApproved {
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

// ── Index helpers ─────────────────────────────────────────────────────

// addToIndex appends an entry to the index and writes it.
// Caller must hold e.mu (acquired by Write).
func (e *EpisodeStore) addToIndex(meta EpisodeMeta) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	idx, err := e.ReadIndex()
	if err != nil {
		// Index error means we start fresh
		idx = []EpisodeMeta{}
	}
	idx = append(idx, meta)
	return e.writeIndex(idx)
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
//
// The ranker fits the RP embedder on episode summaries on each call.
// With the typical number of episodes (< 100, 120-char summaries each),
// fitting takes < 1ms and is negligible compared to LLM latency.
func NewRPRanker(dims int) RankStrategy {
	if dims <= 0 {
		dims = 64 // lower dims = faster, fine for short summaries
	}
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

		// Fit RP and embed query + corpus
		rp := vector.NewRandomProjections(dims)
		rp.Fit(append(corpus, query))
		queryVec, _ := rp.Embed(query)

		// Score each episode by cosine similarity
		type scored struct {
			idx   int
			score float32
		}
		scores := make([]scored, len(episodes))
		for i, summary := range corpus {
			vec, _ := rp.Embed(summary)
			sim := cosineVector(queryVec, vec)
			scores[i] = scored{idx: i, score: sim}
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
	return float32(dot / (math.Sqrt(normA) * math.Sqrt(normB)))
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
