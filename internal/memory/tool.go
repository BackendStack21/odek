package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/BackendStack21/odek/internal/memory/extended"
	"github.com/BackendStack21/odek/internal/session"
)

// memoryToolSchema is the JSON schema for the `memory` tool.
// Used by the agent to call add/replace/remove/consolidate/read/search.
var memoryToolSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"action": map[string]any{
			"type":        "string",
			"enum":        []string{"add", "replace", "remove", "consolidate", "read", "search", "view", "add_atom", "search_atoms", "forget_atom", "list_quarantine", "pin_atom", "confirm_pending_review", "reject_pending_review", "list_pending_review"},
			"description": "What to do with memory",
		},
		"target": map[string]any{
			"type":        "string",
			"enum":        []string{"user", "env", "episodes"},
			"description": "Which fact file to modify (for add/replace/remove/consolidate), or 'episodes' for view",
		},
		"content": map[string]any{
			"type":        "string",
			"description": "The entry content (for add/replace/add_atom)",
		},
		"old_text": map[string]any{
			"type":        "string",
			"description": "Unique substring to identify an existing entry (for replace/remove/search)",
		},
		"query": map[string]any{
			"type":        "string",
			"description": "Search query for episode recall (for search) or atom search (for search_atoms)",
		},
		"atom_id": map[string]any{
			"type":        "string",
			"description": "Atom ID (for forget_atom/pin_atom)",
		},
		"pending_id": map[string]any{
			"type":        "string",
			"description": "Pending review ID (for confirm_pending_review/reject_pending_review)",
		},
		"atom_type": map[string]any{
			"type":        "string",
			"enum":        []string{"fact", "preference", "intent", "decision", "goal", "convention", "file", "error", "question"},
			"description": "Atom type for add_atom (default: fact)",
		},
		"confidence": map[string]any{
			"type":        "number",
			"description": "Confidence 0.0-1.0 for add_atom (default: 1.0)",
		},
	},
	"required": []string{"action"},
}

// MemoryTool wraps a MemoryManager as a odek-compatible Tool.
type MemoryTool struct {
	manager *MemoryManager
}

// NewMemoryTool creates a tool that exposes memory CRUD + search to the agent.
func NewMemoryTool(mm *MemoryManager) *MemoryTool {
	return &MemoryTool{manager: mm}
}

func (t *MemoryTool) Name() string { return "memory" }
func (t *MemoryTool) Description() string {
	return "Manage persistent memory across sessions: read, add, update, remove facts, consolidate related entries, or search past episode summaries."
}
func (t *MemoryTool) Schema() any { return memoryToolSchema }

func (t *MemoryTool) Call(args string) (string, error) {
	var params struct {
		Action     string  `json:"action"`
		Target     string  `json:"target"`
		Content    string  `json:"content"`
		OldText    string  `json:"old_text"`
		Query      string  `json:"query"`
		AtomID     string  `json:"atom_id"`
		PendingID  string  `json:"pending_id"`
		AtomType   string  `json:"atom_type"`
		Confidence float32 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return errorJSON("invalid arguments: " + err.Error()), nil
	}

	switch params.Action {
	case "add":
		return t.handleAdd(params.Target, params.Content)
	case "replace":
		return t.handleReplace(params.Target, params.OldText, params.Content)
	case "remove":
		return t.handleRemove(params.Target, params.OldText)
	case "consolidate":
		return t.handleConsolidate(params.Target)
	case "read":
		return t.handleRead()
	case "search":
		return t.handleSearch(params.Query)
	case "view":
		return t.handleView(params.Target, params.Query)
	case "add_atom":
		return t.handleAddAtom(params.Content, params.AtomType, params.Confidence)
	case "search_atoms":
		return t.handleSearchAtoms(params.Query)
	case "forget_atom":
		return t.handleForgetAtom(params.AtomID)
	case "list_quarantine":
		return t.handleListQuarantine()
	case "pin_atom":
		return t.handlePinAtom(params.AtomID)
	case "confirm_pending_review":
		return t.handleConfirmPendingReview(params.PendingID)
	case "reject_pending_review":
		return t.handleRejectPendingReview(params.PendingID)
	case "list_pending_review":
		return t.handleListPendingReview()
	default:
		return errorJSON(fmt.Sprintf("unknown action: %q", params.Action)), nil
	}
}

func (t *MemoryTool) handleAdd(target, content string) (string, error) {
	if content == "" {
		return errorJSON("content is required for add"), nil
	}
	if err := t.manager.AddFact(target, content); err != nil {
		return errorJSON(err.Error()), nil
	}
	entries, _ := t.manager.facts.Entries(target)
	return successJSONWithEntries(fmt.Sprintf("added to %s: %s", target, truncate(content, 60)), entries), nil
}

func (t *MemoryTool) handleReplace(target, oldText, content string) (string, error) {
	if oldText == "" || content == "" {
		return errorJSON("old_text and content are required for replace"), nil
	}
	if err := t.manager.ReplaceFact(target, oldText, content); err != nil {
		return errorJSON(err.Error()), nil
	}
	entries, _ := t.manager.facts.Entries(target)
	return successJSONWithEntries(fmt.Sprintf("replaced in %s: %s", target, truncate(content, 60)), entries), nil
}

func (t *MemoryTool) handleRemove(target, oldText string) (string, error) {
	if oldText == "" {
		return errorJSON("old_text is required for remove"), nil
	}
	if err := t.manager.RemoveFact(target, oldText); err != nil {
		return errorJSON(err.Error()), nil
	}
	entries, _ := t.manager.facts.Entries(target)
	return successJSONWithEntries(fmt.Sprintf("removed from %s matching %q", target, oldText), entries), nil
}

func (t *MemoryTool) handleConsolidate(target string) (string, error) {
	if target == "" {
		return errorJSON("target is required for consolidate"), nil
	}
	entries, _ := t.manager.facts.Entries(target)
	if len(entries) <= 1 {
		return successJSON("nothing to consolidate (1 or fewer entries)"), nil
	}
	if err := t.manager.Consolidate(target); err != nil {
		return errorJSON(err.Error()), nil
	}
	// Read back to report actual new count
	newEntries, _ := t.manager.facts.Entries(target)
	return successJSON(fmt.Sprintf("consolidated %s (%d → %d entries)", target, len(entries), len(newEntries))), nil
}

func (t *MemoryTool) handleRead() (string, error) {
	user, env, err := t.manager.ReadFacts()
	if err != nil {
		return errorJSON(err.Error()), nil
	}
	var b strings.Builder
	b.WriteString("── User Profile ──\n")
	if user != "" {
		b.WriteString(user)
	} else {
		b.WriteString("(empty)")
	}
	b.WriteString("\n\n── Environment ──\n")
	if env != "" {
		b.WriteString(env)
	} else {
		b.WriteString("(empty)")
	}
	return successJSON(b.String()), nil
}

func (t *MemoryTool) handleSearch(query string) (string, error) {
	if query == "" {
		return errorJSON("query is required for search"), nil
	}
	results, err := t.manager.SearchEpisodes(query, 5)
	if err != nil {
		return errorJSON(err.Error()), nil
	}
	if len(results) == 0 {
		return successJSON("no matching episodes found"), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Found %d matching episode(s):\n\n", len(results))
	for _, r := range results {
		fmt.Fprintf(&b, "• %s (%d turns)\n  %s\n\n", r.SessionID, r.Turns, truncate(r.Summary, 120))
	}
	return successJSON(b.String()), nil
}

func (t *MemoryTool) handleView(target, query string) (string, error) {
	if target != "episodes" {
		return errorJSON("view target must be 'episodes'"), nil
	}
	if query == "" {
		return errorJSON("query (session_id) is required for view"), nil
	}
	if err := session.ValidateSessionID(query); err != nil {
		return errorJSON("invalid session_id: " + err.Error()), nil
	}
	content, err := t.manager.episodes.Read(query)
	if err != nil {
		return errorJSON(err.Error()), nil
	}
	return successJSON(content), nil
}

// ── JSON helpers ────────────────────────────────────────────────────

func successJSON(msg string) string {
	data, _ := json.Marshal(map[string]any{
		"success": true,
		"message": msg,
	})
	return string(data)
}

func successJSONWithEntries(msg string, entries []string) string {
	data, _ := json.Marshal(map[string]any{
		"success": true,
		"message": msg,
		"entries": entries,
	})
	return string(data)
}

func errorJSON(msg string) string {
	data, _ := json.Marshal(map[string]any{
		"success": false,
		"error":   msg,
	})
	return string(data)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

func (t *MemoryTool) handleAddAtom(content, atomType string, confidence float32) (string, error) {
	if content == "" {
		return errorJSON("content is required for add_atom"), nil
	}
	if atomType == "" {
		atomType = extended.TypeFact
	}
	if !extended.ValidType(atomType) {
		return errorJSON(fmt.Sprintf("invalid atom_type: %q", atomType)), nil
	}
	if confidence <= 0 || confidence > 1.0 {
		confidence = 1.0
	}
	if t.manager.extended == nil {
		return errorJSON("extended memory is not initialized or disabled"), nil
	}
	atom := extended.MemoryAtom{
		Text:        content,
		SourceClass: extended.SourceUserApproved,
		Type:        atomType,
		Confidence:  confidence,
	}
	// AddAtom returns nil even when the guard scan rejected the atom and it
	// was routed to quarantine (by design — a human reviews false positives).
	// Diff the quarantine list before/after so the agent is told the truth
	// instead of a false "added atom". ListQuarantineEntries exposes the
	// rejection reason; the store is small and this is not a hot path.
	beforeIDs := quarantineIDs(t.manager.extended)
	if err := t.manager.extended.AddAtom(nilContext, atom); err != nil {
		return errorJSON(err.Error()), nil
	}
	if reason, ok := newQuarantinedEntry(t.manager.extended, beforeIDs, content); ok {
		return successJSON(fmt.Sprintf("quarantined for human review (reason: %s): %s", reason, truncate(content, 60))), nil
	}
	return successJSON(fmt.Sprintf("added atom: %s", truncate(content, 60))), nil
}

// quarantineIDs returns the set of quarantined atom IDs currently in the
// extended store. A listing error yields an empty set — the after-diff then
// simply finds no match and the caller falls back to the "added" message.
func quarantineIDs(em *extended.ExtendedMemory) map[string]bool {
	ids := make(map[string]bool)
	entries, err := em.ListQuarantineEntries()
	if err != nil {
		return ids
	}
	for _, e := range entries {
		ids[e.ID] = true
	}
	return ids
}

// newQuarantinedEntry reports whether an atom matching text appeared in
// quarantine that was not in beforeIDs, returning its rejection reason.
func newQuarantinedEntry(em *extended.ExtendedMemory, beforeIDs map[string]bool, text string) (string, bool) {
	entries, err := em.ListQuarantineEntries()
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if beforeIDs[e.ID] {
			continue
		}
		if strings.TrimSpace(e.Text) == strings.TrimSpace(text) {
			if e.Reason == "" {
				return "untrusted content", true
			}
			return e.Reason, true
		}
	}
	return "", false
}

func (t *MemoryTool) handleSearchAtoms(query string) (string, error) {
	if query == "" {
		return errorJSON("query is required for search_atoms"), nil
	}
	if t.manager.extended == nil {
		return errorJSON("extended memory is not initialized or disabled"), nil
	}
	atoms, err := t.manager.extended.SearchAtoms(nilContext, query)
	if err != nil {
		return errorJSON(err.Error()), nil
	}
	if len(atoms) == 0 {
		return successJSON("no matching atoms found"), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Found %d matching atom(s):\n\n", len(atoms))
	for _, a := range atoms {
		fmt.Fprintf(&b, "• [%s] %s (confidence %.2f, source %s)\n", a.Type, truncate(a.Text, 120), a.Confidence, a.SourceClass)
	}
	return successJSON(b.String()), nil
}

func (t *MemoryTool) handleForgetAtom(id string) (string, error) {
	if id == "" {
		return errorJSON("atom_id is required for forget_atom"), nil
	}
	if err := session.ValidateSessionID(id); err != nil {
		return errorJSON("invalid atom_id: " + err.Error()), nil
	}
	if t.manager.extended == nil {
		return errorJSON("extended memory is not initialized or disabled"), nil
	}
	if err := t.manager.extended.ForgetAtom(id); err != nil {
		return errorJSON(err.Error()), nil
	}
	return successJSON(fmt.Sprintf("forgot atom %s", id)), nil
}

func (t *MemoryTool) handleListQuarantine() (string, error) {
	if t.manager.extended == nil {
		return errorJSON("extended memory is not initialized or disabled"), nil
	}
	atoms, err := t.manager.extended.ListQuarantine()
	if err != nil {
		return errorJSON(err.Error()), nil
	}
	if len(atoms) == 0 {
		return successJSON("no atoms in quarantine"), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d atom(s) in quarantine:\n\n", len(atoms))
	for _, a := range atoms {
		fmt.Fprintf(&b, "• %s [%s] %s\n", a.ID, a.SourceClass, truncate(a.Text, 120))
	}
	return successJSON(b.String()), nil
}

func (t *MemoryTool) handlePinAtom(id string) (string, error) {
	if id == "" {
		return errorJSON("atom_id is required for pin_atom"), nil
	}
	if err := session.ValidateSessionID(id); err != nil {
		return errorJSON("invalid atom_id: " + err.Error()), nil
	}
	if t.manager.extended == nil {
		return errorJSON("extended memory is not initialized or disabled"), nil
	}
	if err := t.manager.extended.PinAtom(id); err != nil {
		return errorJSON(err.Error()), nil
	}
	return successJSON(fmt.Sprintf("pinned atom %s", id)), nil
}

func (t *MemoryTool) handleConfirmPendingReview(id string) (string, error) {
	if id == "" {
		return errorJSON("pending_id is required for confirm_pending_review"), nil
	}
	// Security: the agent cannot confirm its own inferences. This action is
	// reserved for the human-gated CLI surface.
	return errorJSON("pending reviews must be confirmed by the user via the CLI (odek memory extended confirm <id>)"), nil
}

func (t *MemoryTool) handleRejectPendingReview(id string) (string, error) {
	if id == "" {
		return errorJSON("pending_id is required for reject_pending_review"), nil
	}
	if err := session.ValidateSessionID(id); err != nil {
		return errorJSON("invalid pending_id: " + err.Error()), nil
	}
	if t.manager.extended == nil {
		return errorJSON("extended memory is not initialized or disabled"), nil
	}
	if err := t.manager.extended.RejectPendingReview(id); err != nil {
		return errorJSON(err.Error()), nil
	}
	return successJSON(fmt.Sprintf("rejected pending review %s", id)), nil
}

func (t *MemoryTool) handleListPendingReview() (string, error) {
	if t.manager.extended == nil {
		return errorJSON("extended memory is not initialized or disabled"), nil
	}
	pending, err := t.manager.extended.ListPendingReview()
	if err != nil {
		return errorJSON(err.Error()), nil
	}
	if len(pending) == 0 {
		return successJSON("no pending reviews"), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d pending review(s):\n\n", len(pending))
	for _, p := range pending {
		fmt.Fprintf(&b, "• %s | %s = %q (confidence %.2f)\n", p.ID, p.Field, truncate(p.Value, 120), p.Confidence)
		if p.Evidence != "" {
			fmt.Fprintf(&b, "  evidence: %s\n", truncate(p.Evidence, 120))
		}
	}
	return successJSON(b.String()), nil
}

var nilContext = context.Background()
