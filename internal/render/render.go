// Package render provides emoji-driven terminal rendering for the odek agent loop.
//
// It produces structured output for each phase of the ReAct cycle:
// thinking, tool calls, tool results, and the final answer. When a Renderer
// is nil or disabled, no output is produced — this keeps the programmatic API
// silent and the CLI colorful.
//
// # Design
//
//   - Zero dependencies. Uses ANSI escape codes directly.
//   - Emoji icons as visual anchors (🧠 🔧 ✅ ❌).
//   - Color detection: respects NO_COLOR env var and tty detection.
//   - Truncation: tool output is collapsed to one line to keep the
//     terminal scannable during multi-step tool chains.
//   - Maintainable: each rendering method is self-contained; adding a new
//     event type requires one constant + one method.
package render

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// ── Events ────────────────────────────────────────────────────────────

// Event identifies a point in the agent loop lifecycle.
// Programmatic consumers can type-switch on Event values.
type Event int

const (
	// IterStart marks the beginning of an iteration cycle.
	IterStart Event = iota
	// Thinking is the model's reasoning text before tool calls.
	Thinking
	// ToolCall is a tool invocation requested by the model.
	ToolCall
	// ToolResult is the output from a completed tool call.
	ToolResult
	// FinalAnswer is the model's final response (no tool calls).
	FinalAnswer
	// Error is a non-fatal error during the loop.
	Error
)

func (e Event) String() string {
	switch e {
	case IterStart:
		return "iter"
	case Thinking:
		return "thinking"
	case ToolCall:
		return "tool_call"
	case ToolResult:
		return "tool_result"
	case FinalAnswer:
		return "answer"
	case Error:
		return "error"
	default:
		return "unknown"
	}
}

// ── ANSI Styles ───────────────────────────────────────────────────────

const (
	reset   = "\033[0m"
	dim     = "\033[2m"
	italic  = "\033[3m"
	red     = "\033[31m"
	green   = "\033[32m"
	yellow  = "\033[33m"
	blue    = "\033[34m"
	magenta = "\033[35m"
	cyan    = "\033[36m"
	gray    = "\033[90m"
)

// ── Renderer ──────────────────────────────────────────────────────────

// Renderer writes formatted agent loop output to an io.Writer.
// The zero value is usable but won't produce any output — call New()
// to create a properly initialized Renderer.
type Renderer struct {
	w           io.Writer
	color       bool
	model       string
	skillVerbose bool // show skill notifications (auto-load, save, suggest, etc.)
}

// New creates a Renderer that writes to w. If color is false, ANSI escape
// codes are stripped from the output.
func New(w io.Writer, color bool) *Renderer {
	return &Renderer{
		w:     w,
		color: color,
	}
}

// WithModel sets the model name displayed in iteration headers.
func (r *Renderer) WithModel(name string) *Renderer {
	r.model = name
	return r
}

// WithSkillVerbose controls whether skill lifecycle notifications
// (auto-load, save, suggest, delete) are shown. Disabled by default.
func (r *Renderer) WithSkillVerbose(verbose bool) *Renderer {
	r.skillVerbose = verbose
	return r
}

// disable returns true when the renderer should produce no output.
func (r *Renderer) disable() bool {
	if r == nil {
		return true
	}
	return r.w == nil
}

// ── Rendering methods ─────────────────────────────────────────────────

// Start prints the session header with task preview.
func (r *Renderer) Start(task string) {
	if r.disable() {
		return
	}
	preview := r.truncate(strings.ReplaceAll(task, "\n", " "), 80)
	prefix := "⚡ odek"
	if r.model != "" {
		prefix += " · " + r.model
	}
	fmt.Fprintln(r.w, r.style(blue, prefix))
	fmt.Fprintln(r.w, r.style(gray, "   "+preview))
	fmt.Fprintln(r.w)
}

// Iteration prints the cycle header with optional turn statistics and
// turn number. When turn > 0, shows "Turn N" in the header.
// When latency > 0 or tokens are reported, a compact stats suffix
// appears on the same line: [1,247 in · 342 out · 4.1s]
func (r *Renderer) Iteration(n, maxN int, latency time.Duration, inTokens, outTokens int, turn int) {
	if r.disable() {
		return
	}
	var prefix string
	if r.model != "" {
		prefix = fmt.Sprintf("Iter %d/%d · %s", n, maxN, r.model)
	} else {
		prefix = fmt.Sprintf("Iter %d/%d", n, maxN)
	}
	// Turn counter when in multi-turn session mode
	if turn > 0 {
		prefix += fmt.Sprintf(" · Turn %d", turn)
	}
	// Build stats suffix only when data is available
	stats := ""
	if inTokens > 0 || outTokens > 0 || latency > 0 {
		stats = fmt.Sprintf("  [%d in · %d out · %.1fs]", inTokens, outTokens, latency.Seconds())
	}
	// Double-line rule framing
	rule := strings.Repeat("═", 3)
	line := fmt.Sprintf("%s %s %s%s", rule, prefix, rule, stats)
	fmt.Fprintln(r.w)
	fmt.Fprintln(r.w, r.style(blue, line))
}

// Thinking prints the model's reasoning text with a brain emoji.
func (r *Renderer) Thinking(text string) {
	if r.disable() || text == "" {
		return
	}
	fmt.Fprintln(r.w, r.style(dim+italic, "🧠 "+text))
}

// NarratorMessage prints an engaging, human-friendly narration line.
// Used in "engaging" interaction mode instead of raw tool call output.
func (r *Renderer) NarratorMessage(msg string) {
	if r.disable() || msg == "" {
		return
	}
	fmt.Fprintf(r.w, "%s\n", r.style(magenta, "💬 "+msg))
}

// toolEmoji returns an emoji that visually signals the tool category.
// Each tool gets an icon matching its domain so users can scan tool traces
// at a glance without reading every label.
// ToolEmoji returns an emoji for the given tool name based on its category.
// Exported so non-renderer consumers (e.g., Telegram bot) can use the same mapping.
func ToolEmoji(name string) string {
	return toolEmoji(name)
}

// ToolPreview extracts a meaningful short preview from a tool call's JSON args.
// Returns a human-readable snippet like "main.go" for read_file, or
// "go test ./..." for shell. Falls back to a truncated command description.
func ToolPreview(name, args string) string {
	switch name {
	case "read_file", "write_file", "patch", "file_info", "glob":
		if p := extractJSONField(args, "path"); p != "" {
			if lastSlash := strings.LastIndex(p, "/"); lastSlash >= 0 {
				return p[lastSlash+1:]
			}
			return p
		}
		return "file"
	case "search_files":
		if p := extractJSONField(args, "pattern"); p != "" {
			if len(p) > 40 {
				p = p[:37] + "..."
			}
			return p
		}
		return ""
	case "shell", "terminal":
		if cmd := extractJSONField(args, "command"); cmd != "" {
			if len(cmd) > 60 {
				cmd = cmd[:57] + "..."
			}
			return cmd
		}
		return ""
	case "batch_read", "batch_patch", "parallel_shell":
		return ""
	case "http_batch", "browser":
		if u := extractJSONField(args, "url"); u != "" {
			if len(u) > 60 {
				u = u[:57] + "..."
			}
			return u
		}
		return ""
	case "memory":
		if q := extractJSONField(args, "query"); q != "" {
			if len(q) > 40 {
				q = q[:37] + "..."
			}
			return q
		}
		return ""
	case "transcribe":
		if p := extractJSONField(args, "path"); p != "" {
			if lastSlash := strings.LastIndex(p, "/"); lastSlash >= 0 {
				return p[lastSlash+1:]
			}
			return p
		}
		return ""
	case "send_message":
		if t := extractJSONField(args, "text"); t != "" {
			if len(t) > 60 {
				t = t[:57] + "..."
			}
			return t
		}
		return ""
	case "session_search":
		if q := extractJSONField(args, "query"); q != "" {
			if len(q) > 40 {
				q = q[:37] + "..."
			}
			return q
		}
		return ""
	}
	return ""
}

// FirstSentence extracts the first sentence from reasoning/thinking text.
// Returns a user-facing preview under 20 words. Falls back to truncation
// if no sentence boundary is found. Returns empty string for empty input.
// Handles standard punctuation (. ! ?) followed by space or newline, and
// also handles ellipsis (...) and end-of-input as boundaries.
func FirstSentence(text string) string {
	if text == "" {
		return ""
	}

	// Clean leading whitespace and reasoning markers
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "I'll")
	text = strings.TrimPrefix(text, "I will")
	text = strings.TrimSpace(text)

	// Try standard sentence boundaries
	for _, sep := range []string{". ", "! ", "? ", ".\n", "!\n", "?\n", "...\n"} {
		if idx := strings.Index(text, sep); idx > 0 {
			sentence := strings.TrimSpace(text[:idx+1])
			if sentence != "" {
				return truncateWords(sentence, 20)
			}
		}
	}

	// No boundary found — check if the whole thing is short enough
	if wordCount(text) <= 20 {
		return text
	}

	// Truncate to 20 words
	return truncateWords(text, 20)
}

// wordCount returns the number of whitespace-delimited words in s.
func wordCount(s string) int {
	if strings.TrimSpace(s) == "" {
		return 0
	}
	return len(strings.Fields(s))
}

// truncateWords limits s to maxWords, appending "…" if trimmed.
func truncateWords(s string, maxWords int) string {
	words := strings.Fields(s)
	if len(words) <= maxWords {
		return s
	}
	return strings.Join(words[:maxWords], " ") + "…"
}

// extractJSONField extracts the value of a top-level string field from a JSON blob.
func extractJSONField(jsonStr, field string) string {
	prefix := `"` + field + `": "`
	if idx := strings.Index(jsonStr, prefix); idx >= 0 {
		rest := jsonStr[idx+len(prefix):]
		if end := strings.Index(rest, `"`); end >= 0 {
			return rest[:end]
		}
	}
	return ""
}

// toolEmoji returns an emoji that visually signals the tool category.
func toolEmoji(name string) string {
	switch {
	// File / code operations
	case name == "read_file" || name == "write_file" || name == "search_files" ||
		name == "patch" || name == "execute_code":
		return "📝"
	// Shell / process operations
	case name == "shell" || name == "terminal" || name == "process":
		return "💻"
	// Web / browser operations
	case name == "web_search" || name == "web_extract" ||
		strings.HasPrefix(name, "browser_"):
		return "🌐"
	// Memory / knowledge
	case name == "memory" || name == "session_search":
		return "🧠"
	// Vision
	case name == "vision_analyze":
		return "👁️"
	// Messaging
	case name == "send_message":
		return "💬"
	// Delegation / subagents
	case name == "delegate_task" || name == "delegate_tasks":
		return "👥"
	// Cron / scheduling
	case name == "cronjob":
		return "⏰"
	// Skills / meta
	case name == "todo" || name == "skill_view" || name == "skill_manage" ||
		name == "skills_list" || name == "clarify":
		return "➕"
	default:
		return "🔧"
	}
}

// ToolCall prints a tool invocation with a category emoji, name, and compact args.
func (r *Renderer) ToolCall(name, args string) {
	if r.disable() {
		return
	}
	header := r.style(cyan, toolEmoji(name)+" "+name)
	argStr := r.style(gray, "─── "+r.truncate(args, 100))
	fmt.Fprintf(r.w, "%s %s\n", header, argStr)
}

// ToolResult prints a one-line summary of the tool output in gray.
// Long output is collapsed to the first line + ellipsis to keep the
// terminal readable during multi-step tool chains.
func (r *Renderer) ToolResult(output string) {
	if r.disable() || output == "" {
		return
	}
	// Take first line only, truncate, add ellipsis if there's more.
	line, _, _ := strings.Cut(output, "\n")
	summary := r.truncate(line, 120)
	if len(output) > len(line) || len(line) > 120 {
		summary += " …"
	}
	fmt.Fprintf(r.w, "%s\n", r.style(gray, "   "+summary))
}

// FinalAnswer prints the model's concluding response with a checkmark emoji.
func (r *Renderer) FinalAnswer(text string) {
	if r.disable() || text == "" {
		return
	}
	fmt.Fprintln(r.w)
	fmt.Fprintln(r.w, r.style(green, "✅ "+text))
	fmt.Fprintln(r.w)
}

// Summary prints a run summary line with total token and cache statistics.
// Emitted after the final answer when at least one stat is non-zero.
// Shows: total input/output tokens, cache creation/read/cached tokens.
// Uses plain text labels (no symbols) for cross-terminal compatibility.
func (r *Renderer) Summary(inTokens, outTokens, cacheCreate, cacheRead, cached int) {
	if r.disable() {
		return
	}
	if inTokens == 0 && outTokens == 0 {
		return
	}
	parts := []string{
		fmt.Sprintf("%d in", inTokens),
		fmt.Sprintf("%d out", outTokens),
	}
	if cacheCreate > 0 {
		parts = append(parts, fmt.Sprintf("%d stored", cacheCreate))
	}
	if cacheRead > 0 {
		parts = append(parts, fmt.Sprintf("%d read", cacheRead))
	}
	if cached > 0 {
		parts = append(parts, fmt.Sprintf("%d cached", cached))
	}
	fmt.Fprintln(r.w, r.style(gray, "── "+strings.Join(parts, " · ")))
	fmt.Fprintln(r.w)
}

// Error prints a non-fatal loop error with a cross emoji.
func (r *Renderer) Error(err error) {
	if r.disable() || err == nil {
		return
	}
	fmt.Fprintln(r.w, r.style(red, "❌ "+err.Error()))
}

// ── Skill Events ──────────────────────────────────────────────────────

// SkillLoaded prints a notification about lazy-loaded skills.
func (r *Renderer) SkillLoaded(names []string) {
	if r.disable() || len(names) == 0 || !r.skillVerbose {
		return
	}
	joined := strings.Join(names, ", ")
	fmt.Fprintln(r.w, r.style(cyan, "📚 Loaded skill: "+joined))
}

// SkillAutoLoaded prints a notification about auto-loaded skills at startup.
func (r *Renderer) SkillAutoLoaded(names []string) {
	if r.disable() || len(names) == 0 || !r.skillVerbose {
		return
	}
	joined := strings.Join(names, ", ")
	fmt.Fprintln(r.w, r.style(dim, fmt.Sprintf("📚 Auto-loaded %d skill(s): %s", len(names), joined)))
}

// SkillSuggested prints a skill suggestion from the learning system.
func (r *Renderer) SkillSuggested(name, heuristic string) {
	if r.disable() || name == "" || !r.skillVerbose {
		return
	}
	fmt.Fprintln(r.w, r.style(yellow, "🔍 Skill suggestion: "+name+" ("+heuristic+")"))
}

// SkillSaved prints confirmation of a saved skill.
func (r *Renderer) SkillSaved(name string) {
	if r.disable() || name == "" || !r.skillVerbose {
		return
	}
	fmt.Fprintln(r.w, r.style(green, "✓ Saved skill \""+name+"\""))
}

// SkillDeleted prints confirmation of a deleted skill.
func (r *Renderer) SkillDeleted(name string) {
	if r.disable() || name == "" || !r.skillVerbose {
		return
	}
	fmt.Fprintln(r.w, r.style(red, "✗ Deleted skill \""+name+"\""))
}

// ── Helpers ───────────────────────────────────────────────────────────

// style wraps text in ANSI codes. Returns plain text when color is off.
func (r *Renderer) style(code, text string) string {
	if !r.color {
		return text
	}
	return code + text + reset
}

// truncate limits s to n characters (not bytes), adding "…" if truncated.
func (r *Renderer) truncate(s string, n int) string {
	// Convert to runes once, check length on the slice
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// ── Auto-detection ────────────────────────────────────────────────────

// ColorEnabled returns true when the terminal supports ANSI colors and
// the user hasn't set NO_COLOR.
func ColorEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	// Terminals that aren't character devices (pipes, redirects) get no color.
	return (fi.Mode() & os.ModeCharDevice) != 0
}
