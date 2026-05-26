package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/session"
)

// ═════════════════════════════════════════════════════════════════════════
// session_search Tool — Browse, search, and retrieve past sessions
// ═════════════════════════════════════════════════════════════════════════

type sessionSearchTool struct {
	store *session.Store
}

func newSessionSearchTool(store *session.Store) *sessionSearchTool {
	return &sessionSearchTool{store: store}
}

func (t *sessionSearchTool) Name() string        { return "session_search" }
func (t *sessionSearchTool) Description() string  { return `Search and retrieve past agent sessions. Actions: list (recent sessions), search (semantic keyword search through full message content), get (full session by ID including ALL messages), find (sessions by task/title). Uses semantic vector search for the search action — it finds sessions whose conversation content is relevant to your query, even when titles don't match. Use OR between keywords for broad recall.

IMPORTANT: After search returns matching sessions, use get (not search) to read the actual conversation content. get returns the full session_messages array with every user and assistant message.` }

type sessionSearchArgs struct {
	Action string `json:"action"`          // list, search, get, find
	Query  string `json:"query,omitempty"` // keyword or session ID
	Limit  int    `json:"limit,omitempty"` // max results (default: 5)
}

type sessionSummary struct {
	ID        string `json:"id"`
	Task      string `json:"task"`
	Turns     int    `json:"turns"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	Model     string `json:"model,omitempty"`
}

type sessionSearchResult struct {
	Action   string           `json:"action"`
	Query    string           `json:"query,omitempty"` // echoed back for the LLM
	Sessions []sessionSummary `json:"sessions,omitempty"`
	Count    int              `json:"count"`
	// For get action — full session details
	ID        string   `json:"id,omitempty"`
	Task      string   `json:"task,omitempty"`
	Turns     int      `json:"turns,omitempty"`
	CreatedAt string   `json:"created_at,omitempty"`
	UpdatedAt string   `json:"updated_at,omitempty"`
	Model     string   `json:"model,omitempty"`
	Buffer    []string `json:"buffer,omitempty"`
	Messages       int              `json:"messages,omitempty"`
	SessionMessages []sessionMessage `json:"session_messages,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// sessionMessage is a single message in a session.
type sessionMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (t *sessionSearchTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"list", "search", "get", "find"},
				"description": "list=recent sessions, search=keyword in sessions, get=full session by ID, find=sessions matching task title",
			},
			"query": map[string]any{
				"type":        "string",
				"description": "Keyword for search/find, or session ID for get.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Max results (default: 5, max: 20).",
			},
		},
		"required": []string{"action"},
	}
}

func (t *sessionSearchTool) Call(argsJSON string) (result string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("session_search: panic: %v", r)
			result = `{"error":"internal tool error"}`
		}
	}()

	// Guard: store must be initialized (nil when --session is not used).
	if t.store == nil {
		return jsonError("session store is not available (use --session to enable session persistence)")
	}

	var args sessionSearchArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}

	limit := args.Limit
	if limit <= 0 {
		limit = 5
	}
	if limit > 20 {
		limit = 20
	}

	switch args.Action {
	case "list":
		return t.handleList(limit)
	case "search":
		return t.handleSearch(args.Query, limit)
	case "get":
		return t.handleGet(args.Query)
	case "find":
		return t.handleFind(args.Query, limit)
	default:
		return jsonError(fmt.Sprintf("unknown action %q; use list, search, get, or find", args.Action))
	}
}

// ── List ────────────────────────────────────────────────────────────────

func (t *sessionSearchTool) handleList(limit int) (string, error) {
	sessions, err := t.store.List(limit)
	if err != nil {
		return jsonResult(sessionSearchResult{
			Action:   "list",
			Sessions: []sessionSummary{},
			Count:    0,
		})
	}
	results := toSummaries(sessions)
	return jsonResult(sessionSearchResult{
		Action:   "list",
		Sessions: results,
		Count:    len(results),
	})
}

// ── Search ──────────────────────────────────────────────────────────────

// sessionMatch tracks a session with a relevance score and snippet.
type sessionMatch struct {
	session session.Session
	score   int
	snippet string
}

func (t *sessionSearchTool) handleSearch(query string, limit int) (string, error) {
	if query == "" {
		return jsonError("query is required for search")
	}

	tokens := strings.Fields(strings.ToLower(query))

	// Phase 1: Vector search — semantic matching over conversation content.
	// This finds sessions whose message content is semantically similar to
	// the query, even when no keywords match the session title.
	if t.store.Vec != nil && t.store.Vec.Ready() {
		vecResults, err := t.store.Vec.Search(query, limit)
		if err == nil && len(vecResults) > 0 {
			// Load full session metadata for each result.
			results := make([]sessionSummary, 0, len(vecResults))
			for _, vr := range vecResults {
				// Phase 1a: Filter by similarity threshold.
				// Random Projections is bag-of-words, not semantic — when
				// 115/117 sessions are "say hello", generic tech queries
				// (odek, project, agent) match the hello centroid at 0.30-0.36.
				// A threshold of 0.40 keeps only genuinely strong matches and
				// falls through to keyword search for everything else.
				if vr.Score < 0.40 {
					continue
				}
				sess, err := t.store.Load(vr.SessionID)
				if err != nil || sess == nil {
					continue
				}
				scoreLabel := fmt.Sprintf("(score: %.3f)", vr.Score)
				results = append(results, sessionSummary{
					ID:        sess.ID,
					Task:      sess.Task + " " + scoreLabel,
					Turns:     sess.Turns,
					CreatedAt: sess.CreatedAt.UTC().Format(time.RFC3339),
					UpdatedAt: sess.UpdatedAt.UTC().Format(time.RFC3339),
					Model:     sess.Model,
				})
			}
			if len(results) > 0 {
				return jsonResult(sessionSearchResult{
					Action:   "search",
					Query:    query,
					Sessions: results,
					Count:    len(results),
				})
			}
		}
		// Vector search returned no results — fall through to keyword.
	}

	// Phase 2: Keyword search — search ALL sessions, not just recent.
	// "say hello" heartbeats should not drown out older substantive sessions.
	sessions, err := t.store.List(0) // 0 = all sessions
	if err != nil {
		return jsonResult(sessionSearchResult{
			Action:   "search",
			Sessions: []sessionSummary{},
			Count:    0,
		})
	}

	// Phase 2a: score by task + buffer
	var matches []sessionMatch
	for _, s := range sessions {
		m := t.scoreSession(tokens, s)
		if m.score > 0 {
			matches = append(matches, m)
		}
	}

	// Phase 2b: if not enough results, load full sessions and search messages
	if len(matches) < limit {
		matches = t.deepSearch(tokens, sessions, matches, limit)
	}

	// Sort by score desc, then recency
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].session.UpdatedAt.After(matches[j].session.UpdatedAt)
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}

	results := make([]sessionSummary, len(matches))
	for i, m := range matches {
		results[i] = sessionSummary{
			ID:        m.session.ID,
			Task:      m.session.Task,
			Turns:     m.session.Turns,
			CreatedAt: m.session.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt: m.session.UpdatedAt.UTC().Format(time.RFC3339),
			Model:     m.session.Model,
		}
		if m.snippet != "" && m.snippet != m.session.Task {
			results[i].Task = m.session.Task + " — " + m.snippet
		}
	}

	return jsonResult(sessionSearchResult{
		Action:   "search",
		Query:    query,
		Sessions: results,
		Count:    len(results),
	})
}

// scoreSession matches tokens against a session's task and buffer.
func (t *sessionSearchTool) scoreSession(tokens []string, s session.Session) sessionMatch {
	m := sessionMatch{session: s}

	// Task matches: highest weight
	if n := matchTokens(tokens, s.Task); n > 0 {
		m.score += n * 3
		m.snippet = truncate(s.Task, 80)
	}

	// Buffer matches: medium weight
	for _, buf := range s.Buffer {
		if n := matchTokens(tokens, buf); n > 0 {
			m.score += n * 2
			if m.snippet == "" {
				m.snippet = truncate(buf, 80)
			}
		}
	}

	return m
}

// deepSearch loads full sessions and searches within their messages.
func (t *sessionSearchTool) deepSearch(tokens []string, candidates []session.Session, existing []sessionMatch, limit int) []sessionMatch {
	existingIDs := make(map[string]bool, len(existing))
	for _, e := range existing {
		existingIDs[e.session.ID] = true
	}

	for _, s := range candidates {
		if existingIDs[s.ID] {
			continue // already scored from task/buffer
		}

		full, err := t.store.Load(s.ID)
		if err != nil || full == nil {
			continue
		}

		// Track which distinct query tokens matched across all messages.
		// This prevents a single common word like "changes" from qualifying
		// an unrelated 107-message session.
		matchedTokens := make(map[string]bool, len(tokens))
		var snippet string
		for _, msg := range full.Messages {
			if msg.Role != "user" && msg.Role != "assistant" {
				continue
			}
			lower := strings.ToLower(msg.Content)
			for _, tok := range tokens {
				if len(tok) < 2 || matchedTokens[tok] {
					continue
				}
				if strings.Contains(lower, tok) {
					matchedTokens[tok] = true
					if snippet == "" {
						snippet = truncate(msg.Content, 100)
					}
				}
			}
		}

		// Require at least 2 distinct tokens to match (or all if query has only 1 token).
		// A single common word hitting one of 107 messages is noise, not signal.
		if len(matchedTokens) >= 2 || (len(tokens) == 1 && len(matchedTokens) == 1) {
			existing = append(existing, sessionMatch{
				session: *full,
				score:   len(matchedTokens),
				snippet: snippet,
			})
		}
	}
	return existing
}

// ── Get ─────────────────────────────────────────────────────────────────

func (t *sessionSearchTool) handleGet(id string) (string, error) {
	if id == "" {
		return jsonError("session ID is required for get")
	}

	sess, err := t.store.Load(id)
	if err != nil {
		return jsonResult(sessionSearchResult{
			ID:    id,
			Error: fmt.Sprintf("session %q not found: %v", id, err),
		})
	}

	// Build session messages for the LLM to read.
	var sessionMessages []sessionMessage
	for _, m := range sess.Messages {
		if m.Role == "user" || m.Role == "assistant" {
			sessionMessages = append(sessionMessages, sessionMessage{
				Role:    m.Role,
				Content: m.Content,
			})
		}
	}
	msgCount := len(sessionMessages)
	return jsonResult(sessionSearchResult{
		Action:    "get",
		ID:        sess.ID,
		Task:      sess.Task,
		Turns:     sess.Turns,
		CreatedAt: sess.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: sess.UpdatedAt.UTC().Format(time.RFC3339),
		Model:     sess.Model,
		Buffer:    sess.Buffer,
		Messages:  msgCount,
		SessionMessages: sessionMessages,
	})
}

// ── Find ────────────────────────────────────────────────────────────────

func (t *sessionSearchTool) handleFind(query string, limit int) (string, error) {
	if query == "" {
		return jsonError("query is required for find")
	}

	tokens := strings.Fields(strings.ToLower(query))

	sessions, err := t.store.List(limit * 3)
	if err != nil {
		return jsonResult(sessionSearchResult{
			Action:   "find",
			Sessions: []sessionSummary{},
			Count:    0,
		})
	}

	var matched []session.Session
	for _, s := range sessions {
		if matchTokens(tokens, s.Task) > 0 {
			matched = append(matched, s)
		}
	}

	if len(matched) > limit {
		matched = matched[:limit]
	}

	return jsonResult(sessionSearchResult{
		Action:   "find",
		Sessions: toSummaries(matched),
		Count:    len(matched),
	})
}

// ── Helpers ─────────────────────────────────────────────────────────────

// matchTokens counts how many query tokens appear in the text.
func matchTokens(tokens []string, text string) int {
	lower := strings.ToLower(text)
	count := 0
	for _, t := range tokens {
		if len(t) < 2 {
			continue
		}
		if strings.Contains(lower, t) {
			count++
		}
	}
	return count
}

// toSummaries converts session.Session slices to sessionSummary (metadata only).
func toSummaries(sessions []session.Session) []sessionSummary {
	results := make([]sessionSummary, len(sessions))
	for i, s := range sessions {
		results[i] = sessionSummary{
			ID:        s.ID,
			Task:      s.Task,
			Turns:     s.Turns,
			CreatedAt: s.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt: s.UpdatedAt.UTC().Format(time.RFC3339),
			Model:     s.Model,
		}
	}
	return results
}

// Ensure sessionSearchTool implements odek.Tool
var _ odek.Tool = (*sessionSearchTool)(nil)
