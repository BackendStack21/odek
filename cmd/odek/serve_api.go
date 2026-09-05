package main

// serve_api.go — REST management endpoints for `odek serve`:
//
//   GET  /api/health                        server info (version, uptime, model, …)
//   GET  /api/sessions?limit=&offset=&q=    session list w/ pagination + search
//   GET  /api/sessions/{id}/export?format=  transcript export (md | json)
//   GET  /api/sessions/{id}/plan            read-only structured plan view
//   GET  /api/memory                        facts + pending-review episodes
//   POST /api/memory/facts                  add a fact          {target, content}
//   DEL  /api/memory/facts                  remove a fact       {target, old_text}
//   POST /api/memory/episodes/promote       promote an episode  {session_id}
//   GET  /api/skills                        skill listing (source, provenance)
//   GET  /api/tools                         tool registry + filter state
//   GET  /api/models                        provider ListModels + configured model
//
// Every handler is mounted behind the apiAuth wrapper in serveCmd (per-instance
// CSRF token + loopback Host + local-origin on mutations), so anything here is
// reachable only by the operator who holds the token URL. Session-scoped reads
// (export) additionally require the session auth token, mirroring
// handleSessionByID. The agent itself cannot reach these endpoints: its
// browser/http tools refuse loopback via the SSRF dial guard and it never sees
// the instance token — which keeps the human gates (episode promotion, fact
// edits) human.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/guard"
	"github.com/BackendStack21/odek/internal/llmclient"
	"github.com/BackendStack21/odek/internal/loop"
	"github.com/BackendStack21/odek/internal/memory"
	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/skills"
)

// serveState carries the immutable server metadata shared by the management
// endpoints: process start time and the resolved configuration snapshot taken
// when the listener was created.
type serveState struct {
	startedAt time.Time
	resolved  config.ResolvedConfig
}

// serveWSConnections counts live handleWS goroutines for /api/health and the
// pong info payload. Incremented in handleWS, decremented on exit.
var serveWSConnections int64

// maxSessionListLimit caps the ?limit= parameter on GET /api/sessions.
const maxSessionListLimit = 200

// ── GET /api/health ─────────────────────────────────────────────────────

// handleHealth reports server metadata for monitoring and the WebUI status
// popover. Deliberately carries no secrets: the model ID and flags come from
// the operator's own config, and ws_connections is a count, not a list.
func handleHealth(st *serveState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeAPIJSON(w, http.StatusOK, map[string]any{
			"status":         "ok",
			"version":        version,
			"started_at":     st.startedAt.UTC().Format(time.RFC3339),
			"uptime_seconds": int64(time.Since(st.startedAt).Seconds()),
			"model":          st.resolved.Model,
			"sandbox":        st.resolved.Sandbox,
			"stream":         st.resolved.Stream,
			"ws_connections": atomic.LoadInt64(&serveWSConnections),
		})
	}
}

// ── GET /api/sessions (?q= &limit= &offset=) ────────────────────────────

// handleSessionListPaged extends the legacy fixed-50 listing with server-side
// search and pagination. With no query parameters the response is identical to
// the legacy handler (tests pin that shape), so the WebUI and bodek keep
// working while they adopt the new params.
func handleSessionListPaged(store *session.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		query := r.URL.Query()
		// Response shapes: with NO query parameters return the legacy bare
		// array (tests and external clients pin that shape); with any of
		// q/limit/offset present return the pagination envelope.
		paged := query.Has("q") || query.Has("limit") || query.Has("offset")
		q := strings.ToLower(strings.TrimSpace(query.Get("q")))
		limit := parseIntDefault(query.Get("limit"), 50)
		if limit < 1 {
			limit = 50
		}
		if limit > maxSessionListLimit {
			limit = maxSessionListLimit
		}
		offset := parseIntDefault(query.Get("offset"), 0)
		if offset < 0 {
			offset = 0
		}

		// Paged requests (q/limit/offset) always scan the full store: a
		// search query's matches may sit arbitrarily deep in recency
		// order, and pinned sessions must float on the FULL list before
		// the window is cut. Floating inside a pre-windowed slice
		// (List(limit+offset)) structurally hides a pin whose recency rank
		// is beyond the window from every page — it floats to the window
		// front and the offset slice cuts it off again — and shifts
		// boundary entries between pages (duplicates across pages).
		// List is an index read — cheap.
		var sessions []session.Session
		if paged {
			all, err := store.List(0)
			if err == nil {
				sessions = all
			}
		} else {
			s, err := store.List(limit + offset)
			if err == nil {
				sessions = s
			}
		}
		if sessions == nil {
			sessions = []session.Session{}
		}

		// Never leak session-scoped auth tokens in listings.
		for i := range sessions {
			sessions[i].AuthToken = ""
		}

		if q != "" {
			filtered := sessions[:0]
			for _, s := range sessions {
				hay := strings.ToLower(s.Task + " " + s.Model + " " + s.ID)
				if strings.Contains(hay, q) {
					filtered = append(filtered, s)
				}
			}
			sessions = filtered
		}

		// Pinned sessions float to the top (presentation only; store order
		// stays updated-desc). Stable sort keeps recency order within each
		// group, including across pagination windows.
		sort.SliceStable(sessions, func(i, j int) bool {
			return sessions[i].Pinned && !sessions[j].Pinned
		})
		if !paged {
			if sessions == nil {
				sessions = []session.Session{}
			}
			writeAPIJSON(w, http.StatusOK, sessions)
			return
		}
		if offset > len(sessions) {
			sessions = []session.Session{}
		} else {
			end := offset + limit
			if end > len(sessions) {
				end = len(sessions)
			}
			sessions = sessions[offset:end]
		}
		if sessions == nil {
			sessions = []session.Session{}
		}

		writeAPIJSON(w, http.StatusOK, map[string]any{
			"sessions": sessions,
			"offset":   offset,
			"limit":    limit,
			"count":    len(sessions),
			"query":    q,
		})
	}
}

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
		if n > 1_000_000 {
			return 1_000_000
		}
	}
	return n
}

// ── GET /api/sessions/{id}/export?format=md|json ─────────────────────────

// handleSessionExport streams a transcript as a downloadable file. Markdown
// is a human-shareable rendering; json is the raw session record. Session
// token auth matches handleSessionByID (this handler is reached through it).
func handleSessionExport(sess *session.Session, format string, w http.ResponseWriter) {
	switch format {
	case "", "md", "markdown":
		md := exportSessionMarkdown(sess)
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=odek-session-%s.md", shortID(sess.ID)))
		_, _ = w.Write([]byte(md))
	case "json":
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=odek-session-%s.json", shortID(sess.ID)))
		_ = json.NewEncoder(w).Encode(sess)
	default:
		http.Error(w, "unsupported format (md|json)", http.StatusBadRequest)
	}
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// ── GET /api/sessions/{id}/plan ──────────────────────────────────────────

// sessionPlanStep is one step of the structured plan view. Note is omitted
// when empty so the shape stays compact for consumers that don't use it.
type sessionPlanStep struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
}

// sessionPlanResponse is the read-only plan view. found=false (still 200)
// means the transcript carries no parseable plan message; version and steps
// stay zero/empty in that case. A collapsed all-done plan parses to a
// version with no step rows — steps is [] then, not null.
type sessionPlanResponse struct {
	SessionID string            `json:"session_id"`
	Version   int               `json:"version"`
	Steps     []sessionPlanStep `json:"steps"`
	Found     bool              `json:"found"`
}

// handleSessionPlan renders the session's persisted plan state as JSON.
// Read-only by contract: it parses the newest parseable "[Current plan:"
// system message with the same strict resume parser the engine uses
// (loop.ExtractPlan) and never mutates anything. Reached through
// handleSessionByID, so rate limiting + session-token auth match the
// sibling GET endpoints exactly.
func handleSessionPlan(sess *session.Session, w http.ResponseWriter) {
	resp := sessionPlanResponse{
		SessionID: sess.ID,
		Steps:     []sessionPlanStep{},
	}
	if plan, ok := loop.ExtractPlan(sess.Messages); ok {
		resp.Found = true
		resp.Version = plan.Version
		for _, st := range plan.Steps {
			resp.Steps = append(resp.Steps, sessionPlanStep{
				ID:     st.ID,
				Title:  st.Title,
				Status: string(st.Status),
				Note:   st.Note,
			})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// exportSessionMarkdown renders a session as a standalone markdown document.
// All message bodies are emitted inside fenced blocks so model-generated
// markdown in the transcript can never break the document structure.
func exportSessionMarkdown(sess *session.Session) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# odek session %s\n\n", sess.ID)
	fmt.Fprintf(&b, "- **Task:** %s\n", orDash(sess.Task))
	fmt.Fprintf(&b, "- **Model:** %s\n", orDash(sess.Model))
	fmt.Fprintf(&b, "- **Turns:** %d\n", sess.Turns)
	fmt.Fprintf(&b, "- **Created:** %s\n", sess.CreatedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "- **Updated:** %s\n", sess.UpdatedAt.UTC().Format(time.RFC3339))
	if sess.Sandbox {
		b.WriteString("- **Sandboxed:** yes\n")
	}
	b.WriteString("\n---\n\n")

	for _, m := range sess.Messages {
		switch m.Role {
		case "system":
			continue // empty head / internal injections — not part of the transcript
		case "user":
			body := stripUntrustedEnvelopes(m.Content)
			fence := codeFence(body)
			b.WriteString("## user\n\n" + fence + "text\n")
			b.WriteString(body)
			b.WriteString("\n" + fence + "\n\n")
		case "assistant":
			if m.ReasoningContent != "" {
				body := stripUntrustedEnvelopes(m.ReasoningContent)
				fence := codeFence(body)
				b.WriteString("<details><summary>reasoning</summary>\n\n" + fence + "text\n")
				b.WriteString(body)
				b.WriteString("\n" + fence + "\n</details>\n\n")
			}
			for _, tc := range m.ToolCalls {
				args := prettyJSONArgs(tc.Function.Arguments)
				fence := codeFence(args)
				fmt.Fprintf(&b, "### tool call: %s\n\n%sjson\n", tc.Function.Name, fence)
				b.WriteString(args)
				b.WriteString("\n" + fence + "\n\n")
			}
			if m.Content != "" {
				body := stripUntrustedEnvelopes(m.Content)
				fence := codeFence(body)
				b.WriteString("## assistant\n\n" + fence + "markdown\n")
				b.WriteString(body)
				b.WriteString("\n" + fence + "\n\n")
			}
		case "tool":
			body := stripUntrustedEnvelopes(m.Content)
			fence := codeFence(body)
			fmt.Fprintf(&b, "### tool result: %s\n\n%stext\n", orDash(m.Name), fence)
			b.WriteString(body)
			b.WriteString("\n" + fence + "\n\n")
		}
	}
	return b.String()
}

// codeFence returns a backtick fence strictly longer than any backtick run
// in body (minimum 4). Transcript content is model/tool generated — a fixed
// 4-backtick fence let any content line of exactly ```` close it early and
// forge document structure in the "human-shareable" export (audit 2026-08).
func codeFence(body string) string {
	longest, cur := 0, 0
	for i := 0; i < len(body); i++ {
		if body[i] == '`' {
			cur++
			if cur > longest {
				longest = cur
			}
		} else {
			cur = 0
		}
	}
	n := longest + 1
	if n < 4 {
		n = 4
	}
	return strings.Repeat("`", n)
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func prettyJSONArgs(args string) string {
	var obj map[string]any
	if err := json.Unmarshal([]byte(args), &obj); err != nil {
		return args
	}
	out, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return args
	}
	return string(out)
}

// ── /api/memory ─────────────────────────────────────────────────────────

// newServeMemoryManager builds a MemoryManager over the operator's memory
// directory for the REST surface. The LLM client is nil: the endpoints here
// only use the file-backed paths (ReadFacts / AddFact / RemoveFact), which
// never call the provider.
func newServeMemoryManager(dir string, cfg memory.MemoryConfig) *memory.MemoryManager {
	if cfg.Enabled == nil {
		t := true
		cfg.Enabled = &t
	}
	return memory.NewMemoryManager(dir, nil, cfg)
}

// splitFactEntries splits a raw facts-file body into entries. Entries are
// separated by a line containing only "§" (memory.entrySep).
func splitFactEntries(content string) []string {
	if strings.TrimSpace(content) == "" {
		return []string{}
	}
	parts := strings.Split(content, "\n§\n")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// handleMemoryGet reports facts (user/env), their configured caps, and the
// pending-review episode queue (tainted episodes awaiting human promotion).
func handleMemoryGet(memoryDir string, cfg memory.MemoryConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		mm := newServeMemoryManager(memoryDir, cfg)
		userFacts, envFacts, err := mm.ReadFacts()
		if err != nil {
			http.Error(w, "memory unavailable: "+err.Error(), http.StatusInternalServerError)
			return
		}

		episodes := memory.NewEpisodeStore(memoryDir, nil)
		pending, err := episodes.PendingReview()
		if err != nil {
			pending = nil
		}
		total := 0
		if idx, err := episodes.ReadIndex(); err == nil {
			total = len(idx)
		}

		writeAPIJSON(w, http.StatusOK, map[string]any{
			"facts": map[string][]string{
				"user": splitFactEntries(userFacts),
				"env":  splitFactEntries(envFacts),
			},
			"fact_limits": map[string]int{
				"user": factCap(cfg.FactsLimitUser, 4000),
				"env":  factCap(cfg.FactsLimitEnv, 8000),
			},
			"episodes": map[string]any{
				"total":   total,
				"pending": pending,
			},
		})
	}
}

// factCap mirrors FactStore cap resolution: operator value when set, else the
// built-in default (the constants are unexported in internal/memory).
func factCap(configured, def int) int {
	if configured > 0 {
		return configured
	}
	return def
}

// handleMemoryFactsAdd appends a fact via the same MemoryManager path the
// agent's memory tool uses (including the unsafe-content filter).
// POST {target: "user"|"env", content: "..."}
func handleMemoryFactsAdd(memoryDir string, cfg memory.MemoryConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Target  string `json:"target"`
			Content string `json:"content"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if body.Target != "user" && body.Target != "env" {
			http.Error(w, "target must be \"user\" or \"env\"", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(body.Content) == "" {
			http.Error(w, "content required", http.StatusBadRequest)
			return
		}
		if err := newServeMemoryManager(memoryDir, cfg).AddFact(body.Target, body.Content); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleMemoryFactsRemove deletes the fact whose text matches old_text.
// DELETE {target, old_text}
func handleMemoryFactsRemove(memoryDir string, cfg memory.MemoryConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Target  string `json:"target"`
			OldText string `json:"old_text"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if body.Target != "user" && body.Target != "env" {
			http.Error(w, "target must be \"user\" or \"env\"", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(body.OldText) == "" {
			http.Error(w, "old_text required", http.StatusBadRequest)
			return
		}
		if err := newServeMemoryManager(memoryDir, cfg).RemoveFact(body.Target, body.OldText); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleMemoryEpisodePromote promotes a tainted episode to recallable state.
// This is the same human gate as `odek memory promote <id>` — reachable only
// with the operator instance token, never by the agent.
// POST {session_id}
func handleMemoryEpisodePromote(memoryDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			SessionID string `json:"session_id"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if body.SessionID == "" {
			http.Error(w, "session_id required", http.StatusBadRequest)
			return
		}
		if err := memory.NewEpisodeStore(memoryDir, nil).Promote(body.SessionID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// ── GET /api/skills ─────────────────────────────────────────────────────

// skillSummary is the wire form of a skill for the listing endpoint. Bodies
// are omitted: they can be large and the UI loads them through skill_load /
// the session context, never from a listing.
type skillSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	AutoLoad    bool   `json:"auto_load"`
	UsageCount  int    `json:"usage_count"`
	Source      string `json:"source"`
	NeedsReview bool   `json:"needs_review"`
	Untrusted   bool   `json:"untrusted"`
}

// handleSkills lists discovered skills with their provenance state, so the
// operator can see what the agent may auto-load and what is pinned
// NeedsReview (excluded from trigger matching until promoted).
func handleSkills(sc skills.SkillsConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		extra := sc.Dirs
		for i := range extra {
			extra[i] = expandHome(extra[i])
		}
		res := skills.ScanDirs(skills.ProjectSkillsDir(), expandHome("~/.odek/skills"), extra)

		var out []skillSummary
		for _, s := range append(append([]skills.Skill{}, res.AutoLoad...), res.Lazy...) {
			out = append(out, skillSummary{
				Name:        s.Name,
				Description: s.Description,
				AutoLoad:    s.AutoLoad,
				UsageCount:  s.UsageCount,
				Source:      s.Source.Dir,
				NeedsReview: s.Provenance.NeedsReview,
				Untrusted:   s.Provenance.Untrusted,
			})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		if out == nil {
			out = []skillSummary{}
		}
		writeAPIJSON(w, http.StatusOK, map[string]any{"skills": out})
	}
}

// ── GET /api/tools ──────────────────────────────────────────────────────

// toolSummary names one tool and whether the resolved tool filter exposes it
// to the model.
type toolSummary struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// handleTools lists the built-in tool registry with its enabled/disabled
// state after the resolved tools.enabled / tools.disabled filter (the same
// rule internal/tool.FilterTools applies). MCP tools are listed per server
// name — the concrete tool list is per-connection.
func handleTools(resolved config.ResolvedConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		names := make([]string, 0, 32)
		for name := range reservedBuiltinToolNames() {
			names = append(names, name)
		}
		sort.Strings(names)

		enabledSet := map[string]bool{}
		for _, n := range resolved.Tools.Enabled {
			enabledSet[n] = true
		}
		disabledSet := map[string]bool{}
		for _, n := range resolved.Tools.Disabled {
			disabledSet[n] = true
		}
		whitelistActive := resolved.Tools.Enabled != nil

		out := make([]toolSummary, 0, len(names))
		for _, n := range names {
			// Shared filter rule (introspect.go) — the same one the
			// agent-facing list_tools tool applies.
			out = append(out, toolSummary{Name: n, Enabled: toolEnabled(n, enabledSet, disabledSet, whitelistActive)})
		}
		writeAPIJSON(w, http.StatusOK, map[string]any{
			"tools":       out,
			"mcp_servers": len(resolved.MCPServers),
		})
	}
}

// ── shared helpers ──────────────────────────────────────────────────────

// writeAPIJSON writes a JSON response body with the given status. (Named
// distinctly from the WebSocket-side writeJSON helpers.)
func writeAPIJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// stripUntrustedEnvelopes removes every <untrusted_content_<nonce>> wrapper
// from an exported message body, keeping the inner text (group 3 of reWrapper,
// defined in untrusted.go). Both delimiter nonces must match; forged,
// mismatched tags are left untouched.
func stripUntrustedEnvelopes(s string) string {
	matches := reWrapper.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		if len(m) < 10 || m[2] < 0 || m[6] < 0 || m[8] < 0 ||
			s[m[2]:m[3]] != s[m[8]:m[9]] {
			continue
		}
		b.WriteString(s[last:m[0]])
		b.WriteString(s[m[6]:m[7]])
		last = m[1]
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// ── GET /api/config (sanitized) ───────────────────────────────────────

// handleConfigView reports the operator-relevant resolved configuration as
// scalars and flags ONLY — the sanitization contract lives on
// buildConfigView (introspect.go), which excludes secrets structurally. The
// agent-facing config_view tool renders the same map; parity is pinned by
// TestRESTConfigViewMatchesToolAll.
func handleConfigView(resolved config.ResolvedConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeAPIJSON(w, http.StatusOK, buildConfigView(resolved))
	}
}

// guardScanView renders the guard scan toggles, tolerating a nil ScanConfig
// (the zero-value ResolvedConfig carries one; resolved configs never do).
func guardScanView(sc *guard.ScanConfig) map[string]any {
	if sc == nil {
		return nil
	}
	return map[string]any{
		"memory":           sc.Memory,
		"system_prompt":    sc.SystemPrompt,
		"mcp_descriptions": sc.MCPDescriptions,
		"skills":           sc.Skills,
		"tool_outputs":     sc.ToolOutputs,
	}
}

// ── GET /api/mcp ──────────────────────────────────────────────────────

// mcpEntry is one sanitized MCP server row shared by GET /api/mcp and the
// list_tools tool (buildMCPServersView).
type mcpEntry struct {
	Name             string   `json:"name"`
	Command          string   `json:"command"`
	Args             []string `json:"args,omitempty"`
	Project          bool     `json:"project,omitempty"`
	AutoApprove      bool     `json:"auto_approve,omitempty"`
	TimeoutSeconds   int      `json:"timeout_seconds,omitempty"`
	MaxResponseBytes int64    `json:"max_response_bytes,omitempty"`
	MaxResultChars   int      `json:"max_result_chars,omitempty"`
}

// handleMCPServers lists configured MCP servers with their extension
// limits. Command/args are operator config (the interactive approval UI
// already displays them verbatim); env values are withheld — they may carry
// credentials. The row shape lives on buildMCPServersView (introspect.go).
func handleMCPServers(resolved config.ResolvedConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		out := buildMCPServersView(resolved)
		writeAPIJSON(w, http.StatusOK, map[string]any{"servers": out, "count": len(out)})
	}
}

// ── POST /api/skills/promote ──────────────────────────────────────────

// handleSkillPromote is the REST face of `odek skill promote` — clears
// NeedsReview so a skill can auto-load. Tainted skills (untrusted origin)
// still require force, exactly like the CLI. Operator-gated by the
// instance token; the agent cannot call it.
func handleSkillPromote() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Name  string `json:"name"`
			Force bool   `json:"force"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if body.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		if err := promoteSkill(expandHome("~/.odek/skills"), body.Name, body.Force); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// ── POST /api/memory/consolidate ──────────────────────────────────────

// handleMemoryConsolidate merges similar facts for a target through the
// LLM — the same MemoryManager.Consolidate the agent uses. The LLM client
// is built from the resolved provider config; without an API key this
// fails cleanly.
func handleMemoryConsolidate(memoryDir string, resolved config.ResolvedConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Target string `json:"target"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if body.Target != "user" && body.Target != "env" {
			http.Error(w, "target must be \"user\" or \"env\"", http.StatusBadRequest)
			return
		}
		timeout := config.DefaultRequestTimeoutSeconds
		if resolved.LLM.RequestTimeoutSeconds > 0 {
			timeout = resolved.LLM.RequestTimeoutSeconds
		}
		client, err := llmclient.Dial(resolved.Provider, resolved.Model, resolved.APIKey, resolved.BaseURL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		client.Thinking = resolved.Thinking
		client.SetRequestTimeout(time.Duration(timeout) * time.Second)
		cfg := resolved.Memory
		if cfg.Enabled == nil {
			t := true
			cfg.Enabled = &t
		}
		if err := memory.NewMemoryManager(memoryDir, client, cfg).Consolidate(body.Target); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// ── POST /api/shutdown ────────────────────────────────────────────────

// handleShutdown triggers the graceful drain (stop accepting → close
// WebSockets → wait for sandbox cleanup) that SIGINT normally starts.
func handleShutdown() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeAPIJSON(w, http.StatusOK, map[string]any{"status": "shutting_down"})
		requestServeShutdown()
	}
}
