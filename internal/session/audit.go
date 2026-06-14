// Package session — audit subsystem.
//
// The audit log records every time the agent ingested externally-
// sourced content (a fetched page, a file outside the working
// directory, an MCP tool response, etc.) along with a heuristic
// `suspicious_divergence` flag for turns where the agent's tool calls
// reference resources that did not appear in the user's preceding
// message. Together they let a user retroactively spot prompt-
// injection attempts that influenced an agent run.
//
// The audit log is local-only; nothing in this package transmits or
// uploads it.

package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// AuditIngest records that the agent ingested an untrusted-source
// content blob at the given turn.
type AuditIngest struct {
	Turn        int       `json:"turn"`
	Source      string    `json:"source"`       // e.g. "https://x", "/abs/path", "mcp:server:tool"
	ContentHash string    `json:"content_hash"` // sha256 of the ingested body (first 16 hex)
	At          time.Time `json:"at"`
}

// AuditTurn records the per-turn divergence assessment.
type AuditTurn struct {
	Turn                 int      `json:"turn"`
	UserMessage          string   `json:"user_message"`
	ToolCalls            []string `json:"tool_calls"`                   // names of tools called this turn
	NovelResources       []string `json:"novel_resources,omitempty"`    // resources referenced by tools but not by user
	UntrustedResources   []string `json:"untrusted_resources,omitempty"` // resources from untrusted content that were later referenced
	IngestedUntrusted    bool     `json:"ingested_untrusted"`
	SuspiciousDivergence bool     `json:"suspicious_divergence"`
}

// AuditLog is the per-session aggregate.
type AuditLog struct {
	SessionID string        `json:"session_id"`
	Ingests   []AuditIngest `json:"ingests"`
	Turns     []AuditTurn   `json:"turns"`
}

// AuditStore manages per-session audit logs under <dir>/audit/.
type AuditStore struct {
	mu  sync.Mutex
	dir string
}

// NewAuditStore returns a store rooted at dir; the audit subdir is
// created on first write.
func NewAuditStore(dir string) *AuditStore {
	return &AuditStore{dir: filepath.Join(dir, "audit")}
}

// RecordIngest appends an ingest entry for a session.
func (s *AuditStore) RecordIngest(sessionID string, turn int, source, content string) error {
	if err := ValidateSessionID(sessionID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	log, _ := s.loadLocked(sessionID)
	log.SessionID = sessionID
	sum := sha256.Sum256([]byte(content))
	log.Ingests = append(log.Ingests, AuditIngest{
		Turn:        turn,
		Source:      source,
		ContentHash: hex.EncodeToString(sum[:8]),
		At:          time.Now().UTC(),
	})
	return s.saveLocked(sessionID, log)
}

// RecordTurn appends a turn assessment for a session.
func (s *AuditStore) RecordTurn(sessionID string, turn AuditTurn) error {
	if err := ValidateSessionID(sessionID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	log, _ := s.loadLocked(sessionID)
	log.SessionID = sessionID
	log.Turns = append(log.Turns, turn)
	return s.saveLocked(sessionID, log)
}

// Load returns the audit log for a session, or empty if not present.
func (s *AuditStore) Load(sessionID string) (AuditLog, error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return AuditLog{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked(sessionID)
}

func (s *AuditStore) loadLocked(sessionID string) (AuditLog, error) {
	path := filepath.Join(s.dir, sessionID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return AuditLog{SessionID: sessionID}, nil
	}
	var log AuditLog
	if err := json.Unmarshal(data, &log); err != nil {
		return AuditLog{SessionID: sessionID}, err
	}
	return log, nil
}

func (s *AuditStore) saveLocked(sessionID string, log AuditLog) error {
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(s.dir, sessionID+".json")
	data, err := json.MarshalIndent(log, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// ── Divergence heuristic ─────────────────────────────────────────────

// reResource matches strings that look like a resource the agent might
// act on: URLs, absolute paths, dotted file extensions, command names.
// The heuristic compares resources referenced by tool calls against
// the user's preceding message; anything novel is suspicious when the
// session has ingested untrusted content this turn.
//
// Optional surrounding quotes are captured so JSON-encoded tool arguments
// (e.g. {"path":"README.md"}) are inspected correctly.
var reResource = regexp.MustCompile(`(?i)["']?(https?://[^\s'"<>]+|/[A-Za-z0-9_./-]{2,}|[A-Za-z0-9_-]+\.[A-Za-z]{2,5})["']?`)

// ResourcesIn returns the set of resource-like tokens found in text.
func ResourcesIn(text string) []string {
	matches := reResource.FindAllStringSubmatch(text, -1)
	seen := make(map[string]bool, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		res := strings.TrimRight(m[1], ".,);")
		if seen[res] || res == "" {
			continue
		}
		seen[res] = true
		out = append(out, res)
	}
	return out
}

// NovelResources returns resources from toolText that do not appear in
// userText. Order preserved; case-insensitive comparison.
func NovelResources(userText string, toolText string) []string {
	userSet := make(map[string]bool)
	for _, r := range ResourcesIn(userText) {
		userSet[strings.ToLower(r)] = true
	}
	var novel []string
	for _, r := range ResourcesIn(toolText) {
		if !userSet[strings.ToLower(r)] {
			novel = append(novel, r)
		}
	}
	return novel
}
