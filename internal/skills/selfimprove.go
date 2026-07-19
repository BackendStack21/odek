package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/BackendStack21/odek/internal/guard"
	"github.com/BackendStack21/odek/internal/memory"
)

// ── Self-Improvement Heuristics ───────────────────────────────────────
//
// These heuristics detect patterns in tool execution history that suggest
// a reusable skill should be saved. Each heuristic is a pure function.

// ToolCall represents a single tool invocation captured during a session.
type ToolCall struct {
	Tool     string // "terminal", "read_file", "write_file", etc.
	Input    string // the full command or args passed to the tool
	Output   string // the tool's output (first 500 chars)
	ExitCode int    // 0 = success, non-zero = failure
	Turn     int    // which iteration of the loop this happened in
}

// SkillSuggestion represents a detected opportunity to save a skill.
type SkillSuggestion struct {
	Name        string          // suggested name
	Description string          // one-line description
	Body        string          // generated markdown body
	Heuristic   string          // which heuristic detected it
	CommandLog  []string        // commands that were executed (for context)
	Provenance  SkillProvenance // trust signals of the session that produced this suggestion
}

// IsTainted reports whether the suggestion was derived from content outside
// the agent's trust boundary. Tainted suggestions are refused by auto-save
// unless the caller explicitly allows them, and cannot be promoted without
// the --force flag.
func (s SkillSuggestion) IsTainted() bool {
	return s.Provenance.Untrusted || len(s.Provenance.Sources) > 0
}

// isTerminalTool returns true if the tool name represents a shell/terminal tool.
// The agent's shell tool is named "shell"; tests and older call patterns use "terminal".
func isTerminalTool(name string) bool {
	return name == "shell" || name == "terminal"
}

// DetectMultiStepProcedure detects 4+ sequential terminal calls on related topics.
func DetectMultiStepProcedure(calls []ToolCall) []SkillSuggestion {
	if len(calls) < 4 {
		return nil
	}

	// Group calls into contiguous terminal-only sequences
	var sequences [][]ToolCall
	var current []ToolCall
	for _, c := range calls {
		if isTerminalTool(c.Tool) && c.ExitCode == 0 {
			current = append(current, c)
		} else if isTerminalTool(c.Tool) && c.ExitCode != 0 {
			// failed call breaks the sequence
			if len(current) >= 4 {
				sequences = append(sequences, current)
			}
			current = nil
		} else {
			// non-terminal call — don't break, but don't count either
			continue
		}
	}
	if len(current) >= 4 {
		sequences = append(sequences, current)
	}

	var suggestions []SkillSuggestion
	for _, seq := range sequences {
		suggestion := buildSuggestionFromSequence(seq, "multi-step")
		if suggestion != nil {
			suggestions = append(suggestions, *suggestion)
		}
	}
	return suggestions
}

// DetectErrorRecovery detects a failure → (one or more retries) → success pattern.
// The original heuristic required exactly 3 calls (fail, retry, success).
// Real-world recovery often takes more attempts, so we now find the first
// failure and scan forward for the first subsequent success.
func DetectErrorRecovery(calls []ToolCall) []SkillSuggestion {
	const maxRetries = 5 // don't look too far; more retries = less useful skill
	for i := 0; i < len(calls)-1; i++ {
		if !isTerminalTool(calls[i].Tool) || calls[i].ExitCode == 0 {
			continue
		}
		// Found a failure; scan forward for a success within maxRetries.
		failCall := calls[i]
		for j := i + 1; j < len(calls) && j <= i+maxRetries; j++ {
			if !isTerminalTool(calls[j].Tool) {
				continue
			}
			if calls[j].ExitCode != 0 {
				continue // another failure, keep scanning
			}
			// Success found — first failure → first success is the skill.
			oldCmd := failCall.Input
			newCmd := calls[j].Input
			summary := extractRelevantChange(oldCmd, newCmd)
			return []SkillSuggestion{
				{
					Name:        "fix-" + extractTopic(oldCmd),
					Description: fmt.Sprintf("Recover from %s errors", extractTopic(oldCmd)),
					Heuristic:   "error-recovery",
					Body:        generateErrorRecoveryBody(oldCmd, newCmd, summary),
					CommandLog:  []string{oldCmd, newCmd},
				},
			}
		}
	}
	return nil
}

// DetectCorrection detects a user-corrected approach.
// The heuristic scans for keywords suggesting redirection.
func DetectCorrection(calls []ToolCall, userMessages []string) []SkillSuggestion {
	if len(userMessages) == 0 {
		return nil
	}

	// Look for correction patterns in user messages
	correctionWords := []string{"no", "instead", "try", "actually", "wrong", "not what", "different"}
	for _, msg := range userMessages {
		lower := strings.ToLower(msg)
		for _, word := range correctionWords {
			if strings.Contains(lower, word) {
				// Found a correction — check if the next terminal sequence succeeded
				for i := len(calls) - 1; i >= 2; i-- {
					if isTerminalTool(calls[i].Tool) && calls[i].ExitCode == 0 &&
						isTerminalTool(calls[i-1].Tool) && calls[i-1].ExitCode == 0 {
						return []SkillSuggestion{
							{
								Name:        "corrected-" + extractTopic(calls[i].Input),
								Description: fmt.Sprintf("User-corrected approach for: %s", extractTopic(calls[i].Input)),
								Heuristic:   "user-correction",
								Body:        fmt.Sprintf("## Overview\n\nUser corrected the approach for %s.\n\n## Step-by-Step\n\n1. %s\n\n## Common Pitfalls\n\n- The initial approach (%s) was incorrect\n- User suggested a different method\n\n## Verification\n\n- %s", extractTopic(calls[i].Input), calls[i].Input, calls[i-1].Input, calls[i].Input),
								CommandLog:  []string{calls[i-1].Input, calls[i].Input},
							},
						}
					}
				}
			}
		}
	}
	return nil
}

// DetectRepeatedAction detects the same tool sequence appearing twice.
func DetectRepeatedAction(calls []ToolCall) []SkillSuggestion {
	if len(calls) < 6 {
		return nil
	}

	// Simple: check if the same command pattern appears more than once
	seen := make(map[string]int)
	for _, c := range calls {
		if isTerminalTool(c.Tool) && c.ExitCode == 0 {
			// Normalize: remove args that change (file paths, versions)
			normalized := normalizeCommand(c.Input)
			seen[normalized]++
		}
	}

	for cmd, count := range seen {
		if count >= 2 {
			return []SkillSuggestion{
				{
					Name:        "repeated-" + extractTopic(cmd),
					Description: fmt.Sprintf("Repeated pattern: %s (used %d times)", extractTopic(cmd), count),
					Heuristic:   "repeated-action",
					Body:        fmt.Sprintf("## Overview\n\nRepeated tool use pattern detected.\n\n## Step-by-Step\n\n1. %s\n\nThis pattern was used %d times in this session.\n\n## Common Pitfalls\n\n- N/A (extracted from usage pattern)\n\n## Verification\n\n- Run the command and verify output", cmd, count),
					CommandLog:  []string{cmd},
				},
			}
		}
	}
	return nil
}

// DetectExplicitInstruction returns a suggestion if any user message
// explicitly asks to save something as a skill.
func DetectExplicitInstruction(userMessages []string, calls []ToolCall) []SkillSuggestion {
	for _, msg := range userMessages {
		lower := strings.ToLower(msg)
		if strings.Contains(lower, "save this") || strings.Contains(lower, "add a skill") ||
			strings.Contains(lower, "remember") || strings.Contains(lower, "create skill about") ||
			strings.Contains(lower, "save as skill") || strings.Contains(lower, "make a skill") {
			return []SkillSuggestion{
				{
					Name:        extractNameFromMessage(msg),
					Description: "User-requested skill",
					Heuristic:   "explicit-instruction",
					Body:        fmt.Sprintf("## Overview\n\nAs requested, here is a skill for: %s\n\n## Step-by-Step\n\n%s\n\n## Common Pitfalls\n\n- Fill in known pitfalls for this operation", extractNameFromMessage(msg), extractCommands(calls)),
					CommandLog:  extractCommandLog(calls),
				},
			}
		}
	}
	return nil
}

//── ExtractToolCalls ─────────────────────────────────────────────────────
//
// ExtractToolCalls parses llm message history and extracts ToolCall structs
// for heuristic analysis. Each assistant tool call is paired with its
// corresponding tool result message.

func ExtractToolCalls(messages []LlmMessage) []ToolCall {
	var calls []ToolCall

	for i := 0; i < len(messages); i++ {
		msg := messages[i]

		// Look for assistant messages with tool calls
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				input := tc.Function.Arguments
				// For shell/terminal tools, extract the command from JSON args
				// e.g., {"command":"echo hello"} → "echo hello"
				if tc.Function.Name == "shell" || tc.Function.Name == "terminal" {
					var shellArgs struct {
						Command string `json:"command"`
					}
					if err := json.Unmarshal([]byte(input), &shellArgs); err == nil && shellArgs.Command != "" {
						input = shellArgs.Command
					}
				}

				call := ToolCall{
					Tool:     tc.Function.Name,
					Input:    input,
					ExitCode: 0, // assume success by default
					Turn:     i,
				}

				// Find the corresponding tool result message
				for j := i + 1; j < len(messages); j++ {
					if messages[j].Role == "tool" && messages[j].ToolCallID == tc.ID {
						output := messages[j].Content
						if strings.Contains(output, "error:") {
							call.ExitCode = 1
						}
						if len(output) > 500 {
							output = output[:500]
						}
						call.Output = output
						break
					}
				}

				calls = append(calls, call)
			}
		}
	}

	return calls
}

// LlmMessage is a subset of llm.Message used for extraction.
// We define it here to avoid importing the llm package.
type LlmMessage struct {
	Role       string
	Content    string
	Name       string
	ToolCallID string
	ToolCalls  []LlmToolCall
}

// LlmToolCall is a subset of llm.ToolCall used for extraction.
type LlmToolCall struct {
	ID       string
	Function struct {
		Name      string
		Arguments string
	}
}

// ── RunAllHeuristics ──────────────────────────────────────────────────
//
// RunAllHeuristics runs all 5 self-improvement heuristics on a session's
// message history and returns deduplicated suggestions.

func RunAllHeuristics(messages []LlmMessage, userMessages []string) []SkillSuggestion {
	calls := ExtractToolCalls(messages)
	if len(calls) == 0 {
		return nil
	}

	var suggestions []SkillSuggestion
	seen := make(map[string]bool) // dedup by heuristic type

	// Run each heuristic
	all := [][]SkillSuggestion{
		DetectMultiStepProcedure(calls),
		DetectErrorRecovery(calls),
		DetectCorrection(calls, userMessages),
		DetectRepeatedAction(calls),
		DetectExplicitInstruction(userMessages, calls),
	}

	for _, list := range all {
		for _, s := range list {
			if !seen[s.Heuristic] {
				seen[s.Heuristic] = true
				suggestions = append(suggestions, s)
			}
		}
	}

	return suggestions
}

// ── Builders ───────────────────────────────────────────────────────────

func buildSuggestionFromSequence(seq []ToolCall, heuristic string) *SkillSuggestion {
	if len(seq) < 4 {
		return nil
	}

	topic := extractTopic(seq[0].Input)
	var steps []string
	for _, c := range seq {
		steps = append(steps, c.Input)
	}

	return &SkillSuggestion{
		Name:        "procedure-" + topic,
		Description: fmt.Sprintf("Multi-step procedure: %s (%d steps)", topic, len(seq)),
		Heuristic:   heuristic,
		Body:        generateProcedureBody(topic, steps),
		CommandLog:  steps,
	}
}

func generateProcedureBody(topic string, steps []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Overview\n\nProcedure for: %s\n\n", topic)
	b.WriteString("## Step-by-Step\n\n")
	for i, step := range steps {
		fmt.Fprintf(&b, "%d. `%s`\n", i+1, step)
	}
	b.WriteString("\n## Common Pitfalls\n\n")
	b.WriteString("- Verify each step's output before proceeding\n")
	b.WriteString("- Exit code 0 means success\n")
	b.WriteString("\n## Verification\n\n")
	fmt.Fprintf(&b, "- `%s` should exit with code 0\n", steps[len(steps)-1])
	return b.String()
}

func generateErrorRecoveryBody(oldCmd, newCmd, summary string) string {
	return fmt.Sprintf("## Overview\n\nError recovery for: %s\n\n## Step-by-Step\n\n1. The original command `%s` failed.\n2. Instead, use: `%s`\n%s\n\n## Common Pitfalls\n\n- The original approach doesn't work in all environments\n- Always verify the correct approach matches your setup\n\n## Verification\n\n- `%s` should exit with code 0",
		extractTopic(oldCmd), oldCmd, newCmd, summary, newCmd)
}

func extractRelevantChange(oldCmd, newCmd string) string {
	// Simple diff: show what changed
	oldWords := strings.Fields(oldCmd)
	newWords := strings.Fields(newCmd)

	if len(oldWords) <= 3 || len(newWords) <= 3 {
		return ""
	}

	var changed []string
	for i := 0; i < len(oldWords) && i < len(newWords); i++ {
		if oldWords[i] != newWords[i] {
			changed = append(changed, fmt.Sprintf("'%s' → '%s'", oldWords[i], newWords[i]))
		}
	}
	if len(changed) > 0 {
		return fmt.Sprintf("   Key change: %s", strings.Join(changed, ", "))
	}
	return ""
}

func extractTopic(cmd string) string {
	// Take the first meaningful word from the command
	words := strings.Fields(strings.TrimSpace(cmd))
	if len(words) == 0 {
		return "unknown"
	}

	// Skip common prefixes
	for _, word := range words {
		if !IsStopword(word) && len(word) > 1 {
			return strings.Trim(word, "\"'`")
		}
	}
	return words[0]
}

func extractNameFromMessage(msg string) string {
	// Try to extract a name from messages like "save this as docker-deploy"
	lower := strings.ToLower(msg)
	if idx := strings.Index(lower, "as "); idx >= 0 {
		name := strings.Fields(msg[idx+3:])
		if len(name) > 0 {
			return name[0]
		}
	}
	return "user-skill"
}

func extractCommands(calls []ToolCall) string {
	var cmds []string
	for _, c := range calls {
		if isTerminalTool(c.Tool) {
			cmds = append(cmds, c.Input)
		}
	}
	if len(cmds) == 0 {
		return "N/A"
	}
	return strings.Join(cmds, "\n")
}

func extractCommandLog(calls []ToolCall) []string {
	var cmds []string
	for _, c := range calls {
		if isTerminalTool(c.Tool) {
			cmds = append(cmds, c.Input)
		}
	}
	if cmds == nil {
		return []string{"N/A"}
	}
	return cmds
}

func normalizeCommand(cmd string) string {
	words := strings.Fields(cmd)
	// Remove variable parts: paths, versions, flags with values
	var cleaned []string
	var skipNext bool
	for _, w := range words {
		if skipNext {
			// The previous token was a flag with a value. If this token
			// is also a flag (starts with -), it wasn't really a value —
			// the previous flag was boolean. Fall through to process it.
			if strings.HasPrefix(w, "-") {
				skipNext = false
			} else {
				skipNext = false
				continue
			}
		}
		if strings.HasPrefix(w, "-") {
			// Flag with embedded value (e.g. --count=1): keep token as-is
			if strings.Contains(w, "=") {
				parts := strings.SplitN(w, "=", 2)
				val := parts[1]
				if strings.Contains(val, "/") || strings.Contains(val, "\\") || val == "." || val == ".." {
					val = "<path>"
				}
				cleaned = append(cleaned, parts[0]+"="+val)
				continue
			}
			// Boolean flag — skip the flag itself, and tentatively skip
			// the next token as a value. If the next token is also a flag,
			// the skipNext guard above will correct.
			skipNext = true
			continue
		}
		if strings.Contains(w, "/") || strings.Contains(w, "\\") || w == "." || w == ".." {
			w = "<path>"
		}
		cleaned = append(cleaned, w)
	}
	return strings.Join(cleaned, " ")
}

// ── User-Facing Helpers ──────────────────────────────────────────────

// FormatSuggestion formats a SkillSuggestion for display to the user.
// When preview is true, includes the first 400 chars (or first 8 lines) of the Body.
func FormatSuggestion(s SkillSuggestion, preview bool) string {
	if preview && len(s.Body) > 0 {
		return FormatSuggestionWithPreview(s, true, 400)
	}
	return FormatSuggestionWithPreview(s, false, 0)
}

// FormatSuggestionPreview returns just the body preview string
// (first 400 chars or first 8 lines, whichever is shorter).
func FormatSuggestionPreview(s SkillSuggestion) string {
	return bodyPreview(s.Body)
}

// bodyPreview extracts the first 400 chars or first 8 lines of body,
// whichever is shorter.
func bodyPreview(body string) string {
	if body == "" {
		return ""
	}
	lines := strings.Split(body, "\n")
	if len(lines) > 8 {
		lines = lines[:8]
	}
	result := strings.Join(lines, "\n")
	if len(result) > 400 {
		result = result[:400]
		// Try to break at a newline
		if lastNL := strings.LastIndexByte(result, '\n'); lastNL > 200 {
			result = result[:lastNL]
		}
	}
	return result
}

// ScanSuggestionBody checks a skill suggestion body for prompt-injection patterns.
// If the guard is enabled for skills and detects an issue, it sets the
// suggestion's Provenance.NeedsReview flag and returns true. The caller may
// still save the suggestion; it will be pinned to lazy/manual review.
func ScanSuggestionBody(ctx context.Context, s *SkillSuggestion, g guard.Guard, cfg guard.Config) bool {
	if g == nil || !guard.IsEnabled(cfg.Scan, "skills") {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := guard.ScanContent(ctx, s.Body, g, &cfg); err != nil {
		s.Provenance.NeedsReview = true
		return true
	}
	return false
}

// SaveSuggestionWithGuard is like SaveSuggestion but scans the body before
// writing. Flagged suggestions are saved with Provenance.NeedsReview set so
// they cannot be auto-loaded.
func SaveSuggestionWithGuard(ctx context.Context, dir string, s SkillSuggestion, g guard.Guard, cfg guard.Config) error {
	ScanSuggestionBody(ctx, &s, g, cfg)
	return SaveSuggestion(dir, s)
}

// SaveSuggestion saves a SkillSuggestion as a SKILL.md in the given directory.
func SaveSuggestion(dir string, s SkillSuggestion) error {
	// Untrusted suggestions are never auto-loaded — they must go through
	// explicit user review even after passing the quality gate.
	prov := s.Provenance
	if prov.Untrusted {
		prov.NeedsReview = true
	}
	skill := Skill{
		Name:        s.Name,
		Description: s.Description,
		Version:     "1.0.0",
		Author:      "odek",
		Quality:     QualityDraft,
		AutoLoad:    false,
		Body:        s.Body,
		Provenance:  prov,
	}
	if len(s.CommandLog) > 0 {
		// Derive trigger keywords from body
		topics, actions := DeriveKeywords(s.Body)
		if len(topics) == 0 {
			topics = extractTopicKeywords(s.CommandLog)
		}
		if len(actions) == 0 {
			actions = extractActionKeywords(s.CommandLog)
		}
		skill.Trigger = SkillTrigger{
			TopicKeywords:  topics,
			ActionKeywords: actions,
		}
	}
	return WriteSkill(dir, skill)
}

// extractTopicKeywords extracts topic keywords from command logs.
func extractTopicKeywords(cmds []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, cmd := range cmds {
		t := extractTopic(cmd)
		if t != "" && t != "unknown" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// extractActionKeywords extracts action keywords from command logs.
func extractActionKeywords(cmds []string) []string {
	actionWords := map[string]bool{
		"build": true, "run": true, "test": true, "deploy": true,
		"install": true, "config": true, "setup": true, "create": true,
		"fix": true, "debug": true, "optimize": true, "review": true,
	}
	seen := make(map[string]bool)
	var out []string
	for _, cmd := range cmds {
		words := strings.Fields(cmd)
		for _, w := range words {
			w = strings.ToLower(strings.Trim(w, "\"'`"))
			if actionWords[w] && !seen[w] {
				seen[w] = true
				out = append(out, w)
			}
		}
	}
	return out
}

// ── Quality Gate ──────────────────────────────────────────────────────

// PassesQualityGate checks if a suggestion meets the minimum bar for auto-save.
// Requires: body ≥ 200 chars, has ## Overview section, has ## Common Pitfalls section.
func PassesQualityGate(s SkillSuggestion) bool {
	if len(s.Body) < 200 {
		return false
	}
	if !strings.Contains(s.Body, "## Overview") && !strings.Contains(s.Body, "# Overview") {
		return false
	}
	if !strings.Contains(s.Body, "## Common Pitfalls") {
		return false
	}
	return true
}

// ── Auto-Save ─────────────────────────────────────────────────────────

// ── Provenance derivation ─────────────────────────────────────────────

// DeriveProvenance scans the session's tool calls and returns the trust
// signals appropriate for any skill derived from it. A skill is marked
// Untrusted (with NeedsReview = true) if any of the messages involved
// a tool call that crossed the agent's trust boundary. The sources list
// records which tools triggered the flag so the user can review what to
// inspect.
//
// The per-call decision is delegated to memory.ToolCallTaints — the single
// source of truth shared with episode provenance. That keeps the two systems
// in lockstep and makes both argument-aware: path-scoped reads (read_file,
// search_files, multi_grep) only taint when they touch a sensitive path, while
// network/MCP/audio tools always taint.
func DeriveProvenance(messages []LlmMessage) SkillProvenance {
	prov := SkillProvenance{}
	seen := make(map[string]bool)
	for _, m := range messages {
		for _, tc := range m.ToolCalls {
			if !memory.ToolCallTaints(tc.Function.Name, tc.Function.Arguments) {
				continue
			}
			prov.Untrusted = true
			prov.NeedsReview = true
			name := tc.Function.Name
			if !seen[name] {
				seen[name] = true
				prov.Sources = append(prov.Sources, name)
			}
		}
	}
	return prov
}

// AutoSaveResult reports what auto-save did.
type AutoSaveResult struct {
	Saved       []string          // names of auto-saved skills
	Skipped     int               // count of suggestions filtered by skip list
	Failed      []string          // names that failed quality gate
	Declined    []string          // names declined because they were tainted and allowUntrusted was false
	GuardFlagged []string         // names flagged by the prompt-injection guard
	Heuristics  map[string]string // heuristic labels for saved skills
}

// AutoSaveSuggestions runs auto-save logic on a set of suggestions.
// It filters skipped suggestions, declines tainted suggestions unless
// allowUntrusted is true, then auto-saves those that pass the quality gate
// (up to maxPerRun), recording the rest as Failed. When a guard is provided
// and skills scanning is enabled, each body is scanned; flagged skills are
// saved with Provenance.NeedsReview so they cannot auto-load.
func AutoSaveSuggestions(suggestions []SkillSuggestion, userDir string, cfg SkillsConfig, g guard.Guard, guardCfg guard.Config, allowUntrusted bool) AutoSaveResult {
	result := AutoSaveResult{Heuristics: make(map[string]string)}

	// Load skip list and filter
	sl := LoadSkipList(userDir)
	eligible := make([]SkillSuggestion, 0, len(suggestions))
	for _, s := range suggestions {
		if sl.ShouldSkip(s.Name, cfg.Curation.SkipThreshold, cfg.Curation.SkipResetDays) {
			result.Skipped++
			continue
		}
		eligible = append(eligible, s)
	}

	// Auto-save eligible suggestions that pass quality gate
	saved := 0
	for _, s := range eligible {
		if saved >= cfg.AutoSave.MaxPerRun {
			break
		}
		if !allowUntrusted && s.IsTainted() {
			result.Declined = append(result.Declined, s.Name)
			continue
		}
		if PassesQualityGate(s) {
			if ScanSuggestionBody(context.Background(), &s, g, guardCfg) {
				result.GuardFlagged = append(result.GuardFlagged, s.Name)
			}
			if err := SaveSuggestion(userDir, s); err == nil {
				result.Saved = append(result.Saved, s.Name)
				result.Heuristics[s.Name] = s.Heuristic
				saved++
			}
		} else {
			result.Failed = append(result.Failed, s.Name)
		}
	}

	return result
}

// ── Enhanced Preview ──────────────────────────────────────────────────

// FormatSuggestionWithPreview formats a suggestion with optional body preview.
func FormatSuggestionWithPreview(s SkillSuggestion, preview bool, previewLen int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📝 Skill suggestion: %s\n", s.Name)
	fmt.Fprintf(&b, "   %s\n", s.Description)
	fmt.Fprintf(&b, "   Detected by: %s\n", s.Heuristic)
	if len(s.CommandLog) > 0 {
		b.WriteString("   Commands:\n")
		for _, cmd := range s.CommandLog {
			if len(cmd) > 80 {
				cmd = cmd[:80] + "..."
			}
			fmt.Fprintf(&b, "     • %s\n", cmd)
		}
	}
	if preview && len(s.Body) > 0 {
		body := s.Body
		if previewLen > 0 && len(body) > previewLen {
			body = body[:previewLen]
			// Try to break at a newline
			if lastNL := strings.LastIndexByte(body, '\n'); lastNL > previewLen/2 {
				body = body[:lastNL]
			}
			body += "\n   ... (truncated)"
		}
		b.WriteString("   ── Preview ──\n")
		for _, line := range strings.Split(body, "\n") {
			fmt.Fprintf(&b, "   %s\n", line)
		}
	}
	return b.String()
}
