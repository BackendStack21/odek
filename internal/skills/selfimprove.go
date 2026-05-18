package skills

import (
	"fmt"
	"strings"
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
	Name        string   // suggested name
	Description string   // one-line description
	Body        string   // generated markdown body
	Heuristic   string   // which heuristic detected it
	CommandLog  []string // commands that were executed (for context)
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
		if c.Tool == "terminal" && c.ExitCode == 0 {
			current = append(current, c)
		} else if c.Tool == "terminal" && c.ExitCode != 0 {
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

// DetectErrorRecovery detects a terminal failure → retry → success pattern.
func DetectErrorRecovery(calls []ToolCall) []SkillSuggestion {
	for i := 0; i < len(calls)-2; i++ {
		if calls[i].Tool == "terminal" && calls[i].ExitCode != 0 &&
			calls[i+1].Tool == "terminal" && calls[i+1].ExitCode == 0 &&
			calls[i+2].Tool == "terminal" && calls[i+2].ExitCode == 0 {

			// Extract the fix from the retry command
			oldCmd := calls[i].Input
			newCmd := calls[i+1].Input
			summary := extractRelevantChange(oldCmd, newCmd)

			return []SkillSuggestion{
				{
					Name:        "fix-" + extractTopic(oldCmd),
					Description: fmt.Sprintf("Recover from %s errors", extractTopic(oldCmd)),
					Heuristic:   "error-recovery",
					Body:        generateErrorRecoveryBody(oldCmd, newCmd, summary),
					CommandLog:  []string{oldCmd, newCmd, calls[i+2].Input},
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
					if calls[i].Tool == "terminal" && calls[i].ExitCode == 0 &&
						calls[i-1].Tool == "terminal" && calls[i-1].ExitCode == 0 {
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
		if c.Tool == "terminal" && c.ExitCode == 0 {
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
				call := ToolCall{
					Tool:     tc.Function.Name,
					Input:    tc.Function.Arguments,
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
	b.WriteString(fmt.Sprintf("## Overview\n\nProcedure for: %s\n\n", topic))
	b.WriteString("## Step-by-Step\n\n")
	for i, step := range steps {
		b.WriteString(fmt.Sprintf("%d. `%s`\n", i+1, step))
	}
	b.WriteString("\n## Common Pitfalls\n\n")
	b.WriteString("- Verify each step's output before proceeding\n")
	b.WriteString("- Exit code 0 means success\n")
	b.WriteString("\n## Verification\n\n")
	b.WriteString(fmt.Sprintf("- `%s` should exit with code 0\n", steps[len(steps)-1]))
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
		if c.Tool == "terminal" {
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
		if c.Tool == "terminal" {
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
func FormatSuggestion(s SkillSuggestion) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("📝 Skill suggestion: %s\n", s.Name))
	b.WriteString(fmt.Sprintf("   %s\n", s.Description))
	b.WriteString(fmt.Sprintf("   Detected by: %s\n", s.Heuristic))
	if len(s.CommandLog) > 0 {
		b.WriteString("   Commands:\n")
		for _, cmd := range s.CommandLog {
			if len(cmd) > 80 {
				cmd = cmd[:80] + "..."
			}
			b.WriteString(fmt.Sprintf("     • %s\n", cmd))
		}
	}
	return b.String()
}

// SaveSuggestion saves a SkillSuggestion as a SKILL.md in the given directory.
func SaveSuggestion(dir string, s SkillSuggestion) error {
	skill := Skill{
		Name:        s.Name,
		Description: s.Description,
		Version:     "1.0.0",
		Author:      "kode",
		Quality:     QualityDraft,
		AutoLoad:    false,
		Body:        s.Body,
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
