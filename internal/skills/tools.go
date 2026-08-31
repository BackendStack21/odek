package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/BackendStack21/odek/internal/embedding"
	"github.com/BackendStack21/odek/internal/guard"
)

// ── Agent Tools ────────────────────────────────────────────────────────
//
// These are the tools exposed to the odek agent for skill management.
// Each tool implements a Name/Description/Schema/Call contract.

// MaxSkillBodySize is the maximum allowed body size for a skill, in bytes.
const MaxSkillBodySize = 1_048_576 // 1MB

// SkillManager holds the state needed by skill management tools.
// It wraps the skill store and provides access to the scan result.
// Thread-safe: use GetResult/GetTrieIndex for concurrent access.
type SkillManager struct {
	UserDir       string
	ProjectDir    string
	Result        *ScanResult
	TrieIndex     *triggerIndex  // kept for backward compat (GetTrieIndex)
	VectorMatcher *VectorMatcher // semantic vector matcher (go-vector RP)
	ScoredMatcher *ScoredMatcher // NEW: scoring-based matcher (replaces trie by default)
	Notifier      SkillNotifier  // receives skill lifecycle events
	mu            sync.RWMutex

	// Skills file cache — tracks mod times and pre-parsed skills to avoid
	// re-reading unchanged SKILL.md files on Reload().
	fileTimes  fileCache        // path → last-known mod time
	prevSkills map[string]Skill // path → cached parsed skill
	dirty      bool             // true after explicit mutation — bypasses cache on Reload

	// embeddingCfg optionally selects a remote (HTTP) embedding backend for
	// semantic skill matching. nil (default) = local RandomProjections. Set via
	// NewSkillManagerWithEmbedding; used when (re)building the VectorMatcher.
	embeddingCfg *embedding.Config

	// guard and guardCfg provide prompt-injection scanning for skill
	// bodies at load and save time. The fast local rule scan always runs;
	// the guard sidecar is only consulted when the "skills" scan scope is
	// enabled (see guard.ScanContentWithScope).
	guard    guard.Guard
	guardCfg guard.Config
}

// NewSkillManager creates a SkillManager with the given directories.
// It scans the directories and builds the trigger index.
// On first call, it loads a persistent cache from ~/.odek/skills/ to
// avoid re-parsing unchanged skills across process restarts.
func NewSkillManager(userDir, projectDir string) *SkillManager {
	return NewSkillManagerWithEmbedding(userDir, projectDir, nil)
}

// NewSkillManagerWithEmbedding is like NewSkillManager but selects an embedding
// backend for the semantic skill matcher. embCfg nil (or non-HTTP) keeps the
// default local RandomProjections; an HTTP config opts into remote semantic
// matching (time-bounded, with keyword fallback).
func NewSkillManagerWithEmbedding(userDir, projectDir string, embCfg *embedding.Config) *SkillManager {
	fc, prev := loadPersistentCache(userDir)
	sm := &SkillManager{
		UserDir:      userDir,
		ProjectDir:   projectDir,
		Notifier:     &NoopNotifier{},
		fileTimes:    fc,
		prevSkills:   prev,
		embeddingCfg: embCfg,
	}
	sm.Reload()
	return sm
}

// MatchLazySkills selects lazy skills for the user input. When a remote
// (HTTP) embedding backend is configured it tries semantic matching first and
// falls back to the keyword ScoredMatcher on no match, a failed/timed-out
// embed, or a down backend. Otherwise it uses the keyword ScoredMatcher
// directly (the default), then the vector and trie matchers. This is the
// single entry point the agent loop wires as its skill loader.
func (sm *SkillManager) MatchLazySkills(input string, maxSlots int) []Skill {
	sm.mu.RLock()
	vm, scored, trie := sm.VectorMatcher, sm.ScoredMatcher, sm.TrieIndex
	sm.mu.RUnlock()

	// Prefer semantic matching only when an HTTP backend is configured; the
	// local RP vector matcher is not obviously better than the keyword matcher
	// and stays a fallback.
	if vm.Semantic() {
		if m := vm.MatchSkills(input, maxSlots); len(m) > 0 {
			return m
		}
		// No match, or the query embed failed/timed out — fall through.
	}
	if scored != nil {
		return scored.MatchSkills(input, maxSlots)
	}
	if vm != nil {
		return vm.MatchSkills(input, maxSlots)
	}
	if trie != nil {
		return trie.MatchSkills(input, maxSlots)
	}
	return nil
}

// SetNotifier replaces the current notifier. If n is nil, a NoopNotifier is used.
func (sm *SkillManager) SetNotifier(n SkillNotifier) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if n == nil {
		n = &NoopNotifier{}
	}
	sm.Notifier = n
}

// SetGuard installs a prompt-injection guard and its config. The guard is used
// when skills are loaded (flagged auto-load skills are moved to lazy) and when
// skills are saved or patched via the skill management tools.
func (sm *SkillManager) SetGuard(g guard.Guard, cfg guard.Config) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.guard = g
	sm.guardCfg = cfg
}

// scanSkill checks a skill body for prompt-injection patterns. The fast
// local rule scan always runs (even when the skills scope or the guard
// itself is disabled); the sidecar second opinion only runs when the
// "skills" scope is enabled. If the body is flagged, it sets
// Provenance.NeedsReview so the skill cannot be auto-loaded without
// explicit promotion.
func (sm *SkillManager) scanSkill(ctx context.Context, s *Skill) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := guard.ScanContentWithScope(ctx, s.Body, sm.guard, &sm.guardCfg, "skills"); err != nil {
		log.Printf("guard: skill %q body flagged: %v", s.Name, err)
		s.Provenance.NeedsReview = true
		return true
	}
	return false
}

// applyGuardToSkills scans loaded skills and moves flagged auto-load skills to
// the lazy list so they are never injected into the system prompt automatically.
// It runs even without a configured guard — the local rule scan still catches
// pattern injections.
func (sm *SkillManager) applyGuardToSkills() {
	if sm.Result == nil {
		return
	}
	kept := make([]Skill, 0, len(sm.Result.AutoLoad))
	for i := range sm.Result.AutoLoad {
		if sm.scanSkill(context.Background(), &sm.Result.AutoLoad[i]) {
			sm.Result.Lazy = append(sm.Result.Lazy, sm.Result.AutoLoad[i])
		} else {
			kept = append(kept, sm.Result.AutoLoad[i])
		}
	}
	sm.Result.AutoLoad = kept

	for i := range sm.Result.Lazy {
		sm.scanSkill(context.Background(), &sm.Result.Lazy[i])
	}
}

// MarkDirty forces the next Reload() to do a full rescan, bypassing the
// file modification time cache. Call after writing, patching, or deleting
// skill files from outside the SkillManager (e.g. auto-save, import).
func (sm *SkillManager) MarkDirty() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.dirty = true
}

// Reload rescans skill directories and rebuilds the trigger index.
// Call after saving or deleting skills to keep the manager in sync.
func (sm *SkillManager) Reload() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.reloadLocked()
}

// reloadLocked rescans without acquiring the lock. Caller must hold sm.mu.
func (sm *SkillManager) reloadLocked() {
	var extraDirs []string
	if sm.dirty {
		// After explicit mutation (save/patch/delete), bypass the file cache
		// to avoid stale results from sub-second mtime granularity.
		sm.Result = ScanDirs(sm.ProjectDir, sm.UserDir, extraDirs)
		sm.fileTimes = make(fileCache)
		sm.prevSkills = make(map[string]Skill)
		clearPersistentCache(sm.UserDir)
		sm.dirty = false
	} else {
		sm.Result = scanDirsCached(sm.ProjectDir, sm.UserDir, extraDirs, sm.fileTimes, sm.prevSkills)
	}

	// Persist cache for next process invocation.
	// Only the user dir is cached (global skills); project-level skills
	// are re-scanned on each project switch.
	savePersistentCache(sm.UserDir, sm.fileTimes, sm.prevSkills)

	// Scan first so flagged auto-load skills are demoted before the
	// trigger matchers are built.
	sm.applyGuardToSkills()

	// Build trigger matchers from the lazy skills eligible for injection.
	// NeedsReview skills stay in ScanResult.Lazy (metadata listing and
	// promotion still show them) but are excluded here so a flagged or
	// tainted skill cannot be trigger-injected into context until
	// explicitly promoted — skill_load likewise refuses to serve their
	// bodies on demand.
	matchable := make([]Skill, 0, len(sm.Result.Lazy))
	for _, s := range sm.Result.Lazy {
		if s.Provenance.NeedsReview {
			continue
		}
		matchable = append(matchable, s)
	}

	// Build index from all lazy skills only (auto-load skills are always in context)
	sm.TrieIndex = BuildTriggerIndex(matchable)

	// Build scoring-based matcher (fixes AND-lock, adds stemming + synonyms)
	sm.ScoredMatcher = NewScoredMatcher(matchable, DefaultScoredConfig())

	// Build vector matcher for semantic skill matching (RP by default, or the
	// opt-in HTTP embedding backend when configured).
	sm.VectorMatcher = NewVectorMatcherWithConfig(matchable, DefaultMatcherConfig, sm.embeddingCfg)
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

// RecordUsage marks a skill as used, updating LastUsed and UsageCount.
// Safe for concurrent access. Called when a skill is loaded into context.
func (sm *SkillManager) RecordUsage(name string) {
	sm.mu.Lock()
	found := false
	for i := range sm.Result.AutoLoad {
		if sm.Result.AutoLoad[i].Name == name {
			sm.Result.AutoLoad[i].LastUsed = time.Now().UTC()
			sm.Result.AutoLoad[i].UsageCount++
			found = true
			break
		}
	}
	if !found {
		for i := range sm.Result.Lazy {
			if sm.Result.Lazy[i].Name == name {
				sm.Result.Lazy[i].LastUsed = time.Now().UTC()
				sm.Result.Lazy[i].UsageCount++
				break
			}
		}
	}
	notifier := sm.Notifier
	sm.mu.Unlock()

	if notifier != nil {
		notifier.Notify(SkillEvent{
			Type:      "used",
			SkillName: name,
			Timestamp: time.Now().UTC(),
		})
	}
}

// AllSkills returns a copy of all loaded skills (auto-load + lazy).
// Thread-safe.
func (sm *SkillManager) AllSkills() []Skill {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	var all []Skill
	if sm.Result != nil {
		all = append(all, sm.Result.AutoLoad...)
		all = append(all, sm.Result.Lazy...)
	}
	return all
}

// ── skill_load ─────────────────────────────────────────────────────────

// SkillLoadTool lets the agent load a skill's full content by name.
type SkillLoadTool struct {
	Manager *SkillManager
}

func (t *SkillLoadTool) Name() string { return "skill_load" }

func (t *SkillLoadTool) Description() string {
	return `Load the full content of a skill by name. Returns the skill's complete text including frontmatter and body. Use this when you need detailed instructions for a specific domain.

Skills pinned NeedsReview (pending human review) are refused — their bodies stay withheld until promoted via ` + "`odek skill promote`" + `.

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

	// AllSkills snapshots the skill list under the manager's read lock —
	// RecordUsage mutates these entries concurrently under max_tool_parallel.
	for _, s := range t.Manager.AllSkills() {
		if s.Name != input.Name {
			continue
		}
		// Provenance gate: NeedsReview skills stay metadata-visible in
		// listings, but their bodies are withheld from the agent until a
		// human promotes them — an on-demand body read must not bypass
		// the same gate that keeps them out of trigger matching.
		if s.Provenance.NeedsReview {
			return "", fmt.Errorf("skill_load: skill %q is pinned NeedsReview and cannot be loaded until a human reviews and promotes it (odek skill promote %s)", input.Name, input.Name)
		}
		return FormatAsContext(s), nil
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

Skills pinned NeedsReview are listed for visibility only — their bodies cannot be loaded until promoted via ` + "`odek skill promote`" + `.

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

	skills := t.Manager.AllSkills()

	var b strings.Builder
	b.WriteString("Available skills:\n\n")

	for _, s := range skills {
		if input.Filter != "" && !containsKeyword(s.Trigger.TopicKeywords, input.Filter) {
			continue
		}

		fmt.Fprintf(&b, "  %-20s [%s]  %s\n", s.Name, s.Quality, s.Description)
		if len(s.Trigger.TopicKeywords) > 0 {
			fmt.Fprintf(&b, "  %-20s  triggers on: %s\n", "", strings.Join(s.Trigger.TopicKeywords, ", "))
		}
		if s.Provenance.NeedsReview {
			fmt.Fprintf(&b, "  %-20s  [needs review] body withheld until promoted (human runs: odek skill promote %s)\n", "", s.Name)
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

// ── skill deletion (human CLI; agent-facing skill_delete tool removed
// with the self-learning feature) ─────────────────────────────────────

// DeleteSkill removes a skill's file (or directory) from disk and reloads
// the manager. The skill may live in either the user or the project dir;
// the file's parent directory is removed, mirroring the loader's
// one-directory-per-skill layout.
func (sm *SkillManager) DeleteSkill(name string) error {
	skill, err := findAnySkill(sm, name)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Dir(skill.Source.Path)); err != nil {
		return fmt.Errorf("skill delete: remove: %w", err)
	}
	sm.MarkDirty()
	sm.Reload()
	if sm.Notifier != nil {
		sm.Notifier.Notify(SkillEvent{
			Type:      "deleted",
			SkillName: name,
			Timestamp: time.Now().UTC(),
		})
	}
	return nil
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
