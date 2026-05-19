package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// maxEpisodeSummaryBytes caps how much summary text we store per episode.
const maxEpisodeSummaryBytes = 1024

// episodeIndexFile is the index filename inside the episodes dir.
const episodeIndexFile = "index.json"

// EpisodeMeta holds metadata for a single episode.
type EpisodeMeta struct {
	SessionID string    `json:"session_id"`
	Turns     int       `json:"turns"`
	CreatedAt time.Time `json:"created_at"`
	Summary   string    `json:"summary"` // truncated for index listing
}

// RankStrategy is an injectable function for ranking episodes by relevance
// to a query. The default implementation uses SimpleCall; tests can inject
// a deterministic mock.
type RankStrategy func(query string, episodes []EpisodeMeta) ([]EpisodeMeta, error)

// EpisodeStore manages on-disk episode summaries (Tier 3 memory).
// Written after sessions with sufficient turns, searchable via SimpleCall.
type EpisodeStore struct {
	dir    string
	rankFn RankStrategy
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
// directory and updates the index.
func (e *EpisodeStore) Write(sessionID, summary string, turns int) error {
	if err := os.MkdirAll(e.dir, 0755); err != nil {
		return fmt.Errorf("memory: episodes mkdir: %w", err)
	}

	// Truncate summary to cap
	if len(summary) > maxEpisodeSummaryBytes {
		summary = summary[:maxEpisodeSummaryBytes] + "..."
	}

	// Write summary file
	path := filepath.Join(e.dir, sessionID+".md")
	if err := os.WriteFile(path, []byte(summary), 0600); err != nil {
		return fmt.Errorf("memory: write episode: %w", err)
	}

	// Update index
	return e.addToIndex(EpisodeMeta{
		SessionID: sessionID,
		Turns:     turns,
		CreatedAt: time.Now().UTC(),
		Summary:   truncateForIndex(summary),
	})
}

// WriteIfEnough calls Write only if turns >= extractThreshold (3).
// Returns nil without writing if the threshold isn't met.
func (e *EpisodeStore) WriteIfEnough(sessionID, summary string, turns int) error {
	const extractThreshold = 3
	if turns < extractThreshold {
		return nil
	}
	return e.Write(sessionID, summary, turns)
}

// Read returns the full summary content for a session.
func (e *EpisodeStore) Read(sessionID string) (string, error) {
	path := filepath.Join(e.dir, sessionID+".md")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("memory: read episode %s: %w", sessionID, err)
	}
	return string(data), nil
}

// ReadIndex reads the episode index from disk. Returns empty slice if the
// index file doesn't exist yet. Entries are ordered newest-first.
func (e *EpisodeStore) ReadIndex() ([]EpisodeMeta, error) {
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
	return idx, nil
}

// Search returns the most relevant episodes for a query, ranked by the
// configured RankStrategy. Limited to limit results.
func (e *EpisodeStore) Search(query string, limit int) ([]EpisodeMeta, error) {
	idx, err := e.ReadIndex()
	if err != nil {
		return nil, err
	}
	if len(idx) == 0 {
		return nil, nil
	}

	ranked, err := e.rankFn(query, idx)
	if err != nil {
		return nil, fmt.Errorf("memory: search episodes: %w", err)
	}

	if limit > 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}
	return ranked, nil
}

// ── Index helpers ─────────────────────────────────────────────────────

// addToIndex appends an entry to the index and writes it.
func (e *EpisodeStore) addToIndex(meta EpisodeMeta) error {
	idx, err := e.ReadIndex()
	if err != nil {
		// Index error means we start fresh
		idx = []EpisodeMeta{}
	}
	idx = append(idx, meta)
	return e.writeIndex(idx)
}

// writeIndex serializes the index to disk.
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
	return os.Rename(tmpPath, idxPath)
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
