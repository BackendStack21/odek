package memory

import (
	"context"
	"fmt"
	"strings"
)

// Default memory dir relative to ~/.kode/
const defaultMemoryDir = "memory"

// Default buffer size (lines).
const defaultBufferLines = 20

// MemoryConfig holds configuration for the memory system.
// Mirrors the JSON config section.
type MemoryConfig struct {
	Enabled        bool    `json:"enabled"`
	FactsLimitUser int     `json:"facts_limit_user"`
	FactsLimitEnv  int     `json:"facts_limit_env"`
	BufferLines    int     `json:"buffer_lines"`
	BufferEnabled  bool    `json:"buffer_enabled"`
	MergeOnWrite   bool    `json:"merge_on_write"`
	ExtractOnEnd   bool    `json:"extract_on_end"`
	LLMSearch      bool    `json:"llm_search"`
	LLMExtract     bool    `json:"llm_extract"`
	LLMConsolidate bool    `json:"llm_consolidate"`
	MergeThreshold float32 `json:"merge_threshold"`
	AddThreshold   float32 `json:"add_threshold"`
}

// DefaultMemoryConfig returns sensible defaults.
func DefaultMemoryConfig() MemoryConfig {
	return MemoryConfig{
		Enabled:        true,
		FactsLimitUser: defaultFactsLimitUser,
		FactsLimitEnv:  defaultFactsLimitEnv,
		BufferLines:    defaultBufferLines,
		BufferEnabled:  true,
		MergeOnWrite:   true,
		ExtractOnEnd:   true,
		LLMSearch:      true,
		LLMExtract:     true,
		LLMConsolidate: true,
		MergeThreshold: MergeThreshold,
		AddThreshold:   AddThreshold,
	}
}

// LLMClient abstracts the LLM calls needed by the memory system
// (SimpleCall for consolidation, episode extraction, and search).
type LLMClient interface {
	SimpleCall(ctx context.Context, system, user string) (string, error)
}

// MemoryManager orchestrates all three tiers of memory:
// Facts (durable, in system prompt), Buffer (session-level context),
// and Episodes (on-disk extracted summaries with search).
type MemoryManager struct {
	facts    *FactStore
	buffer   *Buffer
	episodes *EpisodeStore
	merge    *MergeDetector
	llm      LLMClient
	cfg      MemoryConfig
}

// NewMemoryManager creates a fully wired MemoryManager.
// If llc is nil, LLM-dependent features (consolidation, episode search)
// degrade gracefully (no LLM call, fallback behavior).
func NewMemoryManager(memoryDir string, llc LLMClient, cfg MemoryConfig) *MemoryManager {
	if cfg.FactsLimitUser <= 0 {
		cfg.FactsLimitUser = defaultFactsLimitUser
	}
	if cfg.FactsLimitEnv <= 0 {
		cfg.FactsLimitEnv = defaultFactsLimitEnv
	}
	if cfg.BufferLines <= 0 {
		cfg.BufferLines = defaultBufferLines
	}
	if cfg.MergeThreshold <= 0 {
		cfg.MergeThreshold = MergeThreshold
	}
	if cfg.AddThreshold <= 0 {
		cfg.AddThreshold = AddThreshold
	}

	factsDir := memoryDir
	episodesDir := memoryDir

	factStore := NewFactStore(factsDir, cfg.FactsLimitUser, cfg.FactsLimitEnv)
	// Use LLM-based episode ranker when an LLM client is available and enabled
	var rankFn RankStrategy
	if llc != nil && cfg.LLMSearch {
		rankFn = NewLLMRanker(llc)
	}
	episodeStore := NewEpisodeStore(episodesDir, rankFn)
	mergeDetector := NewMergeDetectorWithThresholds(0, cfg.MergeThreshold, cfg.AddThreshold)

	return &MemoryManager{
		facts:    factStore,
		buffer:   NewBuffer(cfg.BufferLines),
		episodes: episodeStore,
		merge:    mergeDetector,
		llm:      llc,
		cfg:      cfg,
	}
}

// ── Fact Operations ─────────────────────────────────────────────────

// AddFact appends a new fact entry. Performs:
//  1. Security scan (reject if dangerous)
//  2. Dedup (FactStore handles this)
//  3. Merge-on-write using go-vector RP if MergeOnWrite is enabled
//  4. Fits the merge detector after mutation
func (m *MemoryManager) AddFact(target, content string) error {
	if !m.cfg.Enabled {
		return fmt.Errorf("memory: disabled")
	}

	// Security scan
	if err := ScanContent(content); err != nil {
		return err
	}

	// Merge-on-write: check similarity against existing entries
	if m.cfg.MergeOnWrite {
		entries, _ := m.facts.Entries(target)
		if len(entries) > 0 {
			m.merge.Fit(entries)
			action, similarIdx, similarity := m.merge.Classify(content)

			switch action {
			case "merge":
				// Auto-merge: replace similar entry with merged content
				merged := mergeEntries(entries[similarIdx], content)
				return m.facts.Replace(target, entries[similarIdx][:min(30, len(entries[similarIdx]))], merged)
			case "judge":
				// Borderline: use LLM to decide
				if m.llm != nil {
					decision, err := m.judgeMerge(target, entries[similarIdx], content)
					if err == nil {
						if decision == "merge" {
							merged := mergeEntries(entries[similarIdx], content)
							return m.facts.Replace(target, entries[similarIdx][:min(30, len(entries[similarIdx]))], merged)
						}
						// decision == "add" — fall through to normal add
					}
				}
				// No LLM available or LLM failed: let agent decide (just add)
				fallthrough
			case "add":
				// No overlap — normal add
			case "nobody":
				// Empty corpus — normal add
			}
			_ = similarity
		}
	}

	// Standard add
	if err := m.facts.Add(target, content); err != nil {
		return err
	}

	// Re-fit merge detector after mutation
	if m.cfg.MergeOnWrite {
		entries, _ := m.facts.Entries(target)
		m.merge.Fit(entries)
	}

	return nil
}

// ReplaceFact replaces an existing fact entry.
func (m *MemoryManager) ReplaceFact(target, oldText, content string) error {
	if !m.cfg.Enabled {
		return fmt.Errorf("memory: disabled")
	}
	if err := ScanContent(content); err != nil {
		return err
	}
	if err := m.facts.Replace(target, oldText, content); err != nil {
		return err
	}
	// Re-fit merge detector
	if m.cfg.MergeOnWrite {
		entries, _ := m.facts.Entries(target)
		m.merge.Fit(entries)
	}
	return nil
}

// RemoveFact removes a fact entry by substring.
func (m *MemoryManager) RemoveFact(target, oldText string) error {
	if !m.cfg.Enabled {
		return fmt.Errorf("memory: disabled")
	}
	if err := m.facts.Remove(target, oldText); err != nil {
		return err
	}
	// Re-fit merge detector
	if m.cfg.MergeOnWrite {
		entries, _ := m.facts.Entries(target)
		m.merge.Fit(entries)
	}
	return nil
}

// ReadFacts returns the full content of user and env fact files.
func (m *MemoryManager) ReadFacts() (userContent, envContent string, err error) {
	u, err := m.facts.Read("user")
	if err != nil {
		return "", "", err
	}
	e, err := m.facts.Read("env")
	if err != nil {
		return "", "", err
	}
	return u, e, nil
}

// Consolidate uses the LLM to merge related entries in a target file
// for better density. Falls back to no-op if LLM is unavailable or
// LLMConsolidate is disabled in config.
func (m *MemoryManager) Consolidate(target string) error {
	if !m.cfg.Enabled {
		return fmt.Errorf("memory: disabled")
	}
	if m.llm == nil || !m.cfg.LLMConsolidate {
		return fmt.Errorf("memory: consolidation requires LLM client")
	}

	entries, err := m.facts.Entries(target)
	if err != nil {
		return err
	}
	if len(entries) <= 1 {
		return nil // nothing to consolidate
	}

	// Use LLM to merge
	prompt := fmt.Sprintf(`Consolidate the following memory entries into a concise set of facts. Merge related entries, remove redundancy. Output each entry on a separate line, separated by the delimiter " § ".

Entries for %s:
%s`, target, strings.Join(entries, "\n"))

	merged, err := m.llm.SimpleCall(context.Background(),
		"You are a memory consolidation system. Merge related entries into concise facts. Output only the merged entries separated by ' § '. Never more than the original count of entries.",
		prompt,
	)
	if err != nil {
		return fmt.Errorf("memory: consolidate LLM: %w", err)
	}

	// Parse merged entries
	merged = strings.TrimSpace(merged)
	newEntries := strings.Split(merged, " § ")
	if len(newEntries) == 0 || (len(newEntries) == 1 && newEntries[0] == "") {
		return nil // LLM returned nothing useful
	}

	// Write back
	return m.facts.writeEntries(target, newEntries)
}

// ── Buffer Operations ────────────────────────────────────────────────

// AppendBuffer adds a turn summary to the in-memory ring buffer.
func (m *MemoryManager) AppendBuffer(role, message string) {
	if !m.cfg.BufferEnabled {
		return
	}
	line := FormatBufferLine(role, message)
	m.buffer.Append(line)
}

// GetBuffer returns the current buffer lines (for system prompt injection).
func (m *MemoryManager) GetBuffer() []string {
	if !m.cfg.BufferEnabled {
		return nil
	}
	return m.buffer.Lines()
}

// RestoreBuffer loads buffer lines from a saved slice (e.g., from session).
func (m *MemoryManager) RestoreBuffer(lines []string) {
	if !m.cfg.BufferEnabled {
		return
	}
	m.buffer.Clear()
	for _, line := range lines {
		m.buffer.Append(line)
	}
}

// ClearBuffer resets the buffer for a new session.
func (m *MemoryManager) ClearBuffer() {
	m.buffer.Clear()
}

// ── Episode Operations ───────────────────────────────────────────────

// OnSessionEnd is called when a session ends. If turns >= threshold,
// extracts durable facts using the LLM and stores them as an episode.
func (m *MemoryManager) OnSessionEnd(sessionID string, turns int, messages []string) {
	if !m.cfg.ExtractOnEnd || !m.cfg.LLMExtract || m.llm == nil || turns < 3 || len(messages) == 0 {
		return
	}

	// Build conversation text for extraction
	convText := strings.Join(messages, "\n")

	extraction, err := m.llm.SimpleCall(context.Background(),
		"Extract 1-3 concise, durable facts from this conversation that the agent should remember for future sessions. Output as plain text, one fact per line. Skip task-specific details (PR numbers, commit SHAs, file paths). Focus on user preferences, tool quirks, project rules, and environment details.",
		convText,
	)
	if err != nil {
		return
	}

	extraction = strings.TrimSpace(extraction)
	if extraction == "" {
		return
	}

	// Write as episode
	m.episodes.WriteIfEnough(sessionID, extraction, turns)
}

// SearchEpisodes returns the most relevant episodes for a query.
func (m *MemoryManager) SearchEpisodes(query string, limit int) ([]EpisodeMeta, error) {
	return m.episodes.Search(query, limit)
}

// ── System Prompt Builder ────────────────────────────────────────────

// BuildSystemPrompt returns the memory section to inject into the system
// prompt. Returns empty string if memory is disabled or nothing to show.
func (m *MemoryManager) BuildSystemPrompt() string {
	if !m.cfg.Enabled {
		return ""
	}

	userFact, _ := m.facts.Read("user")
	envFact, _ := m.facts.Read("env")
	bufferLines := m.GetBuffer()

	if userFact == "" && envFact == "" && len(bufferLines) == 0 {
		return ""
	}

	totalChars := len(userFact) + len(envFact)
	maxChars := m.cfg.FactsLimitUser + m.cfg.FactsLimitEnv
	var pct int
	if maxChars > 0 {
		pct = totalChars * 100 / maxChars
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("\n═══ MEMORY [%d%% — %d/%d chars] ═══\n", pct, totalChars, maxChars))

	if userFact != "" {
		b.WriteString("── User Profile ──\n")
		b.WriteString(userFact)
		b.WriteString("\n")
	}
	if envFact != "" {
		if userFact != "" {
			b.WriteString("§\n")
		}
		b.WriteString("── Environment ──\n")
		b.WriteString(envFact)
		b.WriteString("\n")
	}

	if len(bufferLines) > 0 {
		b.WriteString("§\n── Current Session ──\n")
		for _, line := range bufferLines {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}

	b.WriteString("───────────────────────────────\n")
	return b.String()
}

// ── Private helpers ──────────────────────────────────────────────────

// judgeMerge asks the LLM whether two entries should be merged.
// Returns "merge" or "add".
func (m *MemoryManager) judgeMerge(target, existing, newEntry string) (string, error) {
	prompt := fmt.Sprintf(`I have two memory entries for the "%s" category:

EXISTING: %s
NEW: %s

Should the new entry be MERGED into the existing one (they are related or redundant)
or ADDED as a separate entry (they are distinct topics)?

Reply with exactly one word: "merge" or "add"`, target, existing, newEntry)

	decision, err := m.llm.SimpleCall(context.Background(),
		"You are a memory deduplication system. Reply with exactly one word: 'merge' or 'add'.",
		prompt,
	)
	if err != nil {
		return "add", err
	}

	decision = strings.TrimSpace(strings.ToLower(decision))
	if strings.Contains(decision, "merge") {
		return "merge", nil
	}
	return "add", nil
}

// mergeEntries combines two related entries into one.
func mergeEntries(a, b string) string {
	if strings.Contains(a, b) {
		return a
	}
	if strings.Contains(b, a) {
		return b
	}
	return a + ". " + b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
