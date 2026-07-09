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
			"enum":        []string{"add", "replace", "remove", "consolidate", "read", "search", "view", "add_atom", "search_atoms", "forget_atom"},
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
			"description": "Atom ID (for forget_atom)",
		},
		"atom_type": map[string]any{
			"type":        "string",
			"enum":        []string{"fact", "observation", "preference", "intent"},
			"description": "Atom type for add_atom (default: observation)",
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
		atomType = extended.TypeObservation
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
	if err := t.manager.extended.AddAtom(nilContext, atom); err != nil {
		return errorJSON(err.Error()), nil
	}
	return successJSON(fmt.Sprintf("added atom: %s", truncate(content, 60))), nil
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

var nilContext = context.Background()
