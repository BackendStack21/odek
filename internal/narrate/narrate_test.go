package narrate

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNarrator_ToolCallMessage_Offline(t *testing.T) {
	n := New(true) // enabled, template mode
	msg := n.ToolCallMessage("read_file", `{"path": "main.go"}`)
	if msg == "" {
		t.Error("expected non-empty fallback message")
	}
	if !strings.Contains(msg, "main.go") {
		t.Errorf("expected message to contain filename, got: %q", msg)
	}
}

func TestNarrator_Disabled(t *testing.T) {
	n := New(false) // explicitly disabled
	msg := n.ToolCallMessage("shell", `{"command": "go test ./..."}`)
	if msg != "" {
		t.Errorf("expected empty when disabled, got: %q", msg)
	}
}

func TestNarrator_ThinkingMessage_Offline(t *testing.T) {
	n := New(true)
	msg := n.ThinkingMessage("I should read the config file first")
	if msg == "" {
		t.Error("expected non-empty thinking fallback")
	}
}

func TestNarrator_AllFallbackTools(t *testing.T) {
	n := New(true)
	tools := []string{"read_file", "write_file", "shell", "search_files", "delegate_tasks", "browser", "memory", "unknown_tool_xyz"}
	for _, name := range tools {
		msg := n.ToolCallMessage(name, `{}`)
		if msg == "" {
			t.Errorf("tool %q: expected non-empty fallback", name)
		}
	}
}

func TestNarrator_ThinkingMessage_WithContent(t *testing.T) {
	n := New(true)
	msg := n.ThinkingMessage("I should read the config first")
	if msg != "🤔 Thinking..." {
		t.Errorf("expected thinking message, got: %q", msg)
	}
}

func TestNarrator_ThinkingMessage_EmptyContent(t *testing.T) {
	n := New(true)
	if msg := n.ThinkingMessage(""); msg != "" {
		t.Errorf("expected empty for empty thought, got: %q", msg)
	}
}

func TestNarrator_Truncate_UnderLimit(t *testing.T) {
	if s := truncate("hello", 10); s != "hello" {
		t.Errorf("expected no truncation, got: %q", s)
	}
}

func TestNarrator_Truncate_OverLimit(t *testing.T) {
	if s := truncate("hello world this is long", 10); s != "hello worl..." {
		t.Errorf("expected truncated, got: %q", s)
	}
}

func TestNarrator_Truncate_AtLimit(t *testing.T) {
	if s := truncate("hello", 5); s != "hello" {
		t.Errorf("expected no truncation at exact length, got: %q", s)
	}
}

func TestNarrator_ExtractShell_Found(t *testing.T) {
	if s := extractShell(`{"command": "go test ./..."}`); s != "go test ./..." {
		t.Errorf("expected command, got: %q", s)
	}
}

func TestNarrator_ExtractShell_NotFound(t *testing.T) {
	if s := extractShell(`{"path": "main.go"}`); s != "command" {
		t.Errorf("expected fallback 'command', got: %q", s)
	}
}

func TestNarrator_ExtractShell_Malformed(t *testing.T) {
	if s := extractShell(`{"command": }`); s != "command" {
		t.Errorf("expected fallback for malformed args, got: %q", s)
	}
}

// A command containing quotes must be extracted in full — the old substring
// scan stopped at the first embedded quote and showed a truncated command.
func TestNarrator_ExtractShell_EmbeddedQuotes(t *testing.T) {
	args := `{"command": "git commit -m \"fix: the thing\""}`
	want := `git commit -m "fix: the thing"`
	if s := extractShell(args); s != want {
		t.Errorf("extractShell = %q, want %q", s, want)
	}
}

func TestNarrator_ExtractShell_DescriptionFieldBefore(t *testing.T) {
	// "description" appears before "command"; the JSON decode must still pick
	// the command rather than getting confused by the earlier field.
	args := `{"description": "run the tests", "command": "go test ./..."}`
	if s := extractShell(args); s != "go test ./..." {
		t.Errorf("extractShell = %q, want %q", s, "go test ./...")
	}
}

// truncate measures runes, not bytes, so it never splits a multi-byte char.
func TestNarrator_Truncate_RuneSafe(t *testing.T) {
	s := truncate("héllo wörld", 5) // é and ö are multi-byte
	if s != "héllo..." {
		t.Errorf("truncate = %q, want %q", s, "héllo...")
	}
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			t.Fatalf("truncate produced invalid UTF-8 at byte %d: %q", i, s)
		}
		i += size
	}
}

func TestNarrator_ExtractPath_Found(t *testing.T) {
	if s := extractPath(`{"path": "/root/projects/main.go"}`); s != "main.go" {
		t.Errorf("expected basename, got: %q", s)
	}
}

func TestNarrator_ExtractPath_FileKey(t *testing.T) {
	if s := extractPath(`{"file": "config.json"}`); s != "config.json" {
		t.Errorf("expected filename, got: %q", s)
	}
}

func TestNarrator_ExtractPath_NotFound(t *testing.T) {
	if s := extractPath(`{"command": "echo"}`); s != "file" {
		t.Errorf("expected fallback 'file', got: %q", s)
	}
}

func TestNarrator_ToolEmoji_AllKnown(t *testing.T) {
	tests := map[string]string{
		"read_file":      "📖",
		"write_file":     "✏️",
		"patch":          "✏️",
		"shell":          "⚙️",
		"search_files":   "🔍",
		"delegate_task":  "👥",
		"delegate_tasks": "👥",
		"browser":        "🌐",
		"memory":         "🧠",
		"clarify":        "❓",
		"unknown_tool":   "🔧",
	}
	for name, want := range tests {
		if got := toolEmoji(name); got != want {
			t.Errorf("toolEmoji(%q) = %q, want %q", name, got, want)
		}
	}
}
