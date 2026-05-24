package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/BackendStack21/kode"
	"github.com/BackendStack21/kode/internal/session"
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
func (t *sessionSearchTool) Description() string  { return `Search and retrieve past agent sessions. Actions: list (recent sessions), search (keyword search), get (full session by ID), find (sessions by task/title). Use this when the user asks about previously discussed topics.` }

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
	Messages  int      `json:"messages,omitempty"`
	Error     string   `json:"error,omitempty"`
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

	// Get generous candidate list
	listLimit := limit * 4
	if listLimit < 20 {
		listLimit = 20
	}

	sessions, err := t.store.List(listLimit)
	if err != nil {
		return jsonResult(sessionSearchResult{
			Action:   "search",
			Sessions: []sessionSummary{},
			Count:    0,
		})
	}

	// Phase 1: score by task + buffer
	var matches []sessionMatch
	for _, s := range sessions {
		m := t.scoreSession(tokens, s)
		if m.score > 0 {
			matches = append(matches, m)
		}
	}

	// Phase 2: if not enough results, load full sessions and search messages
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

		score := 0
		var snippet string
		for _, msg := range full.Messages {
			if msg.Role != "user" && msg.Role != "assistant" {
				continue
			}
			if n := matchTokens(tokens, msg.Content); n > 0 {
				score += n
				if snippet == "" {
					snippet = truncate(msg.Content, 100)
				}
			}
		}

		if score > 0 {
			existing = append(existing, sessionMatch{
				session: *full,
				score:   score,
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

	msgCount := len(sess.Messages)
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
