package skills

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ── Agent Tools ────────────────────────────────────────────────────────
//
// These are the tools exposed to the kode agent for skill management.
// Each tool implements a Name/Description/Schema/Call contract.

// MaxSkillBodySize is the maximum allowed body size for a skill, in bytes.
const MaxSkillBodySize = 1_048_576 // 1MB

// SkillManager holds the state needed by skill management tools.
// It wraps the skill store and provides access to the scan result.
// Thread-safe: use GetResult/GetTrieIndex for concurrent access.
type SkillManager struct {
	UserDir    string
	ProjectDir string
	Result     *ScanResult
	TrieIndex  *triggerIndex
	mu         sync.RWMutex
}

// NewSkillManager creates a SkillManager with the given directories.
// It scans the directories and builds the trigger index.
func NewSkillManager(userDir, projectDir string) *SkillManager {
	sm := &SkillManager{
		UserDir:    userDir,
		ProjectDir: projectDir,
	}
	sm.reload()
	return sm
}

// reload rescans skill directories and rebuilds the trigger index.
// Must be called with the write lock held or when no concurrent access exists.
func (sm *SkillManager) reload() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.reloadLocked()
}

// reloadLocked rescans without acquiring the lock. Caller must hold sm.mu.
func (sm *SkillManager) reloadLocked() {
	var extraDirs []string
	sm.Result = ScanDirs(sm.ProjectDir, sm.UserDir, extraDirs)

	// Build index from all lazy skills only (auto-load skills are always in context)
	sm.TrieIndex = BuildTriggerIndex(sm.Result.Lazy)
}

// GetResult returns a read-locked copy of the scan result.
func (sm *SkillManager) GetResult() *ScanResult {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.Result == nil {
		return nil
	}
	// Return a shallow copy so callers can iterate safely
	cp := *sm.Result
	return &cp
}

// GetTrieIndex returns the trigger index for read-only use.
// The caller must not modify the returned index.
func (sm *SkillManager) GetTrieIndex() *triggerIndex {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.TrieIndex
}

// ── skill_load ─────────────────────────────────────────────────────────

// SkillLoadTool lets the agent load a skill's full content by name.
type SkillLoadTool struct {
	Manager *SkillManager
}

func (t *SkillLoadTool) Name() string { return "skill_load" }

func (t *SkillLoadTool) Description() string {
	return `Load the full content of a skill by name. Returns the skill's complete text including frontmatter and body. Use this when you need detailed instructions for a specific domain.

Example: {"name": "docker-build"}`
}

func (t *SkillLoadTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "The name of the skill to load",
			},
		},
		"required": []string{"name"},
	}
}

func (t *SkillLoadTool) Call(args string) (string, error) {
	var input struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(args), &input); err != nil {
		return "", fmt.Errorf("skill_load: parse args: %w", err)
	}
	if input.Name == "" {
		return "", fmt.Errorf("skill_load: name is required")
	}

	// Search in auto-load skills first
	for _, s := range t.Manager.Result.AutoLoad {
		if s.Name == input.Name {
			return FormatAsContext(s), nil
		}
	}

	// Then search in lazy skills
	for _, s := range t.Manager.Result.Lazy {
		if s.Name == input.Name {
			return FormatAsContext(s), nil
		}
	}

	return "", fmt.Errorf("skill_load: skill %q not found", input.Name)
}

// ── skill_list ─────────────────────────────────────────────────────────

// SkillListTool lists all available skills with metadata.
type SkillListTool struct {
	Manager *SkillManager
}

func (t *SkillListTool) Name() string { return "skill_list" }

func (t *SkillListTool) Description() string {
	return `List all available skills with their name, description, quality, and trigger keywords. Optionally filter by topic keyword.

Example (all): {}
Example (filtered): {"filter": "docker"}`
}

func (t *SkillListTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"filter": map[string]any{
				"type":        "string",
				"description": "Optional filter: only show skills matching this topic keyword",
			},
		},
	}
}

func (t *SkillListTool) Call(args string) (string, error) {
	var input struct {
		Filter string `json:"filter,omitempty"`
	}
	json.Unmarshal([]byte(args), &input) // ignore error — Filter stays empty

	var skills []Skill
	skills = append(skills, t.Manager.Result.AutoLoad...)
	skills = append(skills, t.Manager.Result.Lazy...)

	var b strings.Builder
	b.WriteString("Available skills:\n\n")

	for _, s := range skills {
		if input.Filter != "" && !containsKeyword(s.Trigger.TopicKeywords, input.Filter) {
			continue
		}

		b.WriteString(fmt.Sprintf("  %-20s [%s]  %s\n", s.Name, s.Quality, s.Description))
		if len(s.Trigger.TopicKeywords) > 0 {
			b.WriteString(fmt.Sprintf("  %-20s  triggers on: %s\n", "", strings.Join(s.Trigger.TopicKeywords, ", ")))
		}
		b.WriteString("\n")
	}

	return strings.TrimRight(b.String(), "\n"), nil
}

func containsKeyword(kws []string, filter string) bool {
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" {
		return true
	}
	for _, kw := range kws {
		if strings.Contains(strings.ToLower(kw), filter) {
			return true
		}
	}
	return false
}

// ── skill_save ─────────────────────────────────────────────────────────

// SkillSaveTool saves a new skill to the user directory.
type SkillSaveTool struct {
	Manager *SkillManager
}

func (t *SkillSaveTool) Name() string { return "skill_save" }

func (t *SkillSaveTool) Description() string {
	return `Save a new skill. The skill will be available in future sessions.

Required: name, description, body
Optional: topic_keywords, action_keywords

Quality gates enforced:
- Overview section required
- Common Pitfalls section required
- Body must be at least 300 characters
- Trigger keywords recommended

Example: {"name": "docker-build", "description": "Build and optimize Docker images", "body": "## Overview\n...", "topic_keywords": ["docker", "container", "build"], "action_keywords": ["build", "optimize"]}`
}

func (t *SkillSaveTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Skill name (lowercase, hyphens, max 64 chars)",
			},
			"description": map[string]any{
				"type":        "string",
				"description": "One-line description (max 120 chars)",
			},
			"body": map[string]any{
				"type":        "string",
				"description": "Full markdown body with ## Overview, ## Step-by-Step, ## Common Pitfalls sections",
			},
			"topic_keywords": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Topic keywords for trigger matching (e.g. docker, container)",
			},
			"action_keywords": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Action keywords for trigger matching (e.g. build, deploy)",
			},
		},
		"required": []string{"name", "description", "body"},
	}
}

func (t *SkillSaveTool) Call(args string) (string, error) {
	var input struct {
		Name           string   `json:"name"`
		Description    string   `json:"description"`
		Body           string   `json:"body"`
		TopicKeywords  []string `json:"topic_keywords,omitempty"`
		ActionKeywords []string `json:"action_keywords,omitempty"`
	}
	if err := json.Unmarshal([]byte(args), &input); err != nil {
		return "", fmt.Errorf("skill_save: parse args: %w", err)
	}

	// Validate
	if input.Name == "" {
		return "", fmt.Errorf("skill_save: name is required")
	}
	if input.Description == "" {
		return "", fmt.Errorf("skill_save: description is required")
	}
	if len(input.Body) < 300 {
		return "", fmt.Errorf("skill_save: body too short (%d chars, minimum 300)", len(input.Body))
	}
	if len(input.Body) > MaxSkillBodySize {
		return "", fmt.Errorf("skill_save: body too large (%d bytes, maximum %d)", len(input.Body), MaxSkillBodySize)
	}

	// Run quality gate
	var warnings []string
	if !strings.Contains(input.Body, "## Overview") {
		warnings = append(warnings, "missing ## Overview section")
	}
	if !strings.Contains(input.Body, "## Common Pitfalls") {
		warnings = append(warnings, "missing ## Common Pitfalls section")
	}
	if len(input.Description) > 120 {
		warnings = append(warnings, fmt.Sprintf("description too long (%d chars, max 120)", len(input.Description)))
	}

	// Derive keywords if not provided
	topics := input.TopicKeywords
	actions := input.ActionKeywords
	if len(topics) == 0 && len(actions) == 0 {
		t, a := DeriveKeywords(input.Body)
		topics = t
		actions = a
	}

	skill := Skill{
		Name:        input.Name,
		Description: input.Description,
		Version:     "1.0.0",
		Author:      "kode",
		AutoLoad:    false,
		Quality:     QualityDraft,
		Trigger: SkillTrigger{
			TopicKeywords:  topics,
			ActionKeywords: actions,
		},
		Body:     input.Body,
		BodyHash: HashBody(input.Body),
	}

	// Check for duplicate
	for _, s := range t.Manager.Result.AutoLoad {
		if s.Name == skill.Name {
			return "", fmt.Errorf("skill_save: skill %q already exists", skill.Name)
		}
	}
	for _, s := range t.Manager.Result.Lazy {
		if s.Name == skill.Name {
			return "", fmt.Errorf("skill_save: skill %q already exists", skill.Name)
		}
		if s.BodyHash == skill.BodyHash {
			warnings = append(warnings, fmt.Sprintf("duplicate body detected — skill %q has identical content", s.Name))
		}
	}

	if err := WriteSkill(t.Manager.UserDir, skill); err != nil {
		return "", fmt.Errorf("skill_save: write: %w", err)
	}

	// Reload to pick up the new skill
	t.Manager.reload()

	result := fmt.Sprintf("✓ Saved skill %q to %s\n", skill.Name, t.Manager.UserDir)
	if len(warnings) > 0 {
		result += fmt.Sprintf("⚠  Quality warnings:\n  - %s\n", strings.Join(warnings, "\n  - "))
		result += "  Run `kode skill curate` to improve quality."
	}
	return result, nil
}

// ── skill_patch ────────────────────────────────────────────────────────

// SkillPatchTool updates an existing skill's body content via find-and-replace.
type SkillPatchTool struct {
	Manager *SkillManager
}

func (t *SkillPatchTool) Name() string { return "skill_patch" }

func (t *SkillPatchTool) Description() string {
	return `Update an existing skill's content by replacing text. Use for corrections and small improvements.
Requires name, old_text, and new_text.

Example: {"name": "docker-build", "old_text": "docker build -t", "new_text": "docker build --cache-from"}`
}

func (t *SkillPatchTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Name of the skill to patch",
			},
			"old_text": map[string]any{
				"type":        "string",
				"description": "Text to find and replace (must be unique in the skill body)",
			},
			"new_text": map[string]any{
				"type":        "string",
				"description": "Replacement text",
			},
		},
		"required": []string{"name", "old_text", "new_text"},
	}
}

func (t *SkillPatchTool) Call(args string) (string, error) {
	var input struct {
		Name    string `json:"name"`
		OldText string `json:"old_text"`
		NewText string `json:"new_text"`
	}
	if err := json.Unmarshal([]byte(args), &input); err != nil {
		return "", fmt.Errorf("skill_patch: parse args: %w", err)
	}
	if input.Name == "" || input.OldText == "" {
		return "", fmt.Errorf("skill_patch: name and old_text are required")
	}

	// Find the skill and its file path
	skill, err := t.findSkill(input.Name)
	if err != nil {
		return "", fmt.Errorf("skill_patch: %w", err)
	}

	// Read the file
	content, err := os.ReadFile(skill.Source.Path)
	if err != nil {
		return "", fmt.Errorf("skill_patch: read: %w", err)
	}

	body := string(content)
	idx := strings.Index(body, input.OldText)
	if idx < 0 {
		return "", fmt.Errorf("skill_patch: text %q not found in skill %q", input.OldText, input.Name)
	}

	newBody := strings.Replace(body, input.OldText, input.NewText, 1) // n=1: unique match enforced above
	if err := os.WriteFile(skill.Source.Path, []byte(newBody), 0644); err != nil {
		return "", fmt.Errorf("skill_patch: write: %w", err)
	}

	t.Manager.reload()
	return fmt.Sprintf("✓ Patched skill %q: replaced %d characters", input.Name, len(input.OldText)), nil
}

func (t *SkillPatchTool) findSkill(name string) (*Skill, error) {
	for i := range t.Manager.Result.AutoLoad {
		if t.Manager.Result.AutoLoad[i].Name == name {
			return &t.Manager.Result.AutoLoad[i], nil
		}
	}
	for i := range t.Manager.Result.Lazy {
		if t.Manager.Result.Lazy[i].Name == name {
			return &t.Manager.Result.Lazy[i], nil
		}
	}
	return nil, fmt.Errorf("skill %q not found", name)
}

// ── skill_delete ───────────────────────────────────────────────────────

// SkillDeleteTool removes a skill file from disk.
type SkillDeleteTool struct {
	Manager *SkillManager
}

func (t *SkillDeleteTool) Name() string { return "skill_delete" }

func (t *SkillDeleteTool) Description() string {
	return `Delete a skill by name. This permanently removes the skill file.

Example: {"name": "docker-build"}`
}

func (t *SkillDeleteTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Name of the skill to delete",
			},
		},
		"required": []string{"name"},
	}
}

func (t *SkillDeleteTool) Call(args string) (string, error) {
	var input struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(args), &input); err != nil {
		return "", fmt.Errorf("skill_delete: parse args: %w", err)
	}
	if input.Name == "" {
		return "", fmt.Errorf("skill_delete: name is required")
	}

	skill, err := findAnySkill(t.Manager, input.Name)
	if err != nil {
		return "", fmt.Errorf("skill_delete: %w", err)
	}

	if err := os.RemoveAll(filepath.Dir(skill.Source.Path)); err != nil {
		return "", fmt.Errorf("skill_delete: remove: %w", err)
	}

	t.Manager.reload()
	return fmt.Sprintf("✓ Deleted skill %q", input.Name), nil
}

// findAnySkill searches for a skill in both auto-load and lazy lists.
func findAnySkill(sm *SkillManager, name string) (*Skill, error) {
	for i := range sm.Result.AutoLoad {
		if sm.Result.AutoLoad[i].Name == name {
			return &sm.Result.AutoLoad[i], nil
		}
	}
	for i := range sm.Result.Lazy {
		if sm.Result.Lazy[i].Name == name {
			return &sm.Result.Lazy[i], nil
		}
	}
	return nil, fmt.Errorf("skill %q not found", name)
}
