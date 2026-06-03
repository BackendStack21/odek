// Package narrate produces human-friendly, emoji-rich transition messages
// describing what the agent is doing. Uses template-based descriptions;
// LLM-powered enrichment can be layered on as a future enhancement.
package narrate

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Narrator produces engaging progress messages for tool calls and thinking.
type Narrator struct {
	enabled bool
}

// New creates a Narrator. When enabled, produces emoji-rich template messages
// describing what the agent is doing in human-friendly language.
// If enabled is false, all methods return empty strings (verbose mode).
func New(enabled bool) *Narrator {
	return &Narrator{enabled: enabled}
}

// ToolCallMessage returns a human-friendly description of a tool invocation.
func (n *Narrator) ToolCallMessage(name, args string) string {
	if !n.enabled {
		return ""
	}
	emoji := toolEmoji(name)
	switch name {
	case "read_file":
		return fmt.Sprintf("%s Reading `%s`...", emoji, extractPath(args))
	case "write_file", "patch":
		return fmt.Sprintf("%s Editing `%s`...", emoji, extractPath(args))
	case "shell":
		return fmt.Sprintf("%s Running `%s`...", emoji, truncate(extractShell(args), 120))
	case "search_files":
		return fmt.Sprintf("%s Searching the codebase...", emoji)
	case "delegate_task", "delegate_tasks":
		return "👥 Spawning sub-agents to work in parallel..."
	case "browser":
		return "🌐 Browsing the web..."
	case "memory":
		return "🧠 Checking memory for relevant context..."
	case "clarify":
		return "❓ Asking a clarifying question..."
	default:
		return fmt.Sprintf("%s Working on `%s`...", emoji, name)
	}
}

// ThinkingMessage returns a narration of the thinking/reasoning phase.
func (n *Narrator) ThinkingMessage(thought string) string {
	if !n.enabled || thought == "" {
		return ""
	}
	return "🤔 Thinking..."
}

// ── Helpers ──

func toolEmoji(name string) string {
	switch name {
	case "read_file":
		return "📖"
	case "write_file", "patch":
		return "✏️"
	case "shell":
		return "⚙️"
	case "search_files":
		return "🔍"
	case "delegate_task", "delegate_tasks":
		return "👥"
	case "browser":
		return "🌐"
	case "memory":
		return "🧠"
	case "clarify":
		return "❓"
	default:
		return "🔧"
	}
}

// truncate shortens s to at most n runes, appending an ellipsis when it cuts.
// It measures in runes (not bytes) so it never splits a multi-byte character.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i] + "..."
		}
		count++
	}
	return s + "..."
}

func extractPath(args string) string {
	for _, key := range []string{`"path"`, `"file"`} {
		if idx := strings.Index(args, key); idx >= 0 {
			rest := args[idx+len(key):]
			if start := strings.Index(rest, `"`); start >= 0 {
				rest = rest[start+1:]
				if end := strings.Index(rest, `"`); end >= 0 {
					path := rest[:end]
					if lastSlash := strings.LastIndex(path, "/"); lastSlash >= 0 {
						path = path[lastSlash+1:]
					}
					return path
				}
			}
		}
	}
	return "file"
}

// extractShell pulls the shell command out of the tool-call JSON args. It
// decodes the JSON properly rather than scanning for the first quote pair:
// commands routinely contain quotes (git commit -m "msg", python -c "code"),
// and the old substring approach stopped at the first embedded quote, showing
// a truncated command. Falls back to a best-effort scan only if the args are
// not valid JSON.
func extractShell(args string) string {
	var parsed struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(args), &parsed); err == nil && parsed.Command != "" {
		return parsed.Command
	}
	if idx := strings.Index(args, `"command"`); idx >= 0 {
		rest := args[idx+len(`"command"`):]
		if start := strings.Index(rest, `"`); start >= 0 {
			rest = rest[start+1:]
			if end := strings.Index(rest, `"`); end >= 0 {
				return rest[:end]
			}
		}
	}
	return "command"
}
