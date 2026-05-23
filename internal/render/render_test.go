package render

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRenderer_Start(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true).WithModel("deepseek-chat")

	r.Start("list all files in this directory")

	out := buf.String()
	if !strings.Contains(out, "odek") {
		t.Errorf("Start() missing odek brand: %q", out)
	}
	if !strings.Contains(out, "deepseek-chat") {
		t.Errorf("Start() missing model name: %q", out)
	}
	if !strings.Contains(out, "list all files") {
		t.Errorf("Start() missing task preview: %q", out)
	}
}

func TestRenderer_Start_LongTask(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true).WithModel("deepseek-chat")

	longTask := strings.Repeat("explain this code in great detail ", 10)
	r.Start(longTask)

	out := buf.String()
	// Task preview should be truncated to ~80 chars
	if strings.Count(out, "explain") > 6 {
		t.Errorf("Start() should truncate long task: %q", out)
	}
}

func TestRenderer_Iteration(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true).WithModel("deepseek-chat")

	r.Iteration(3, 90, 0, 0, 0, 0)

	out := buf.String()
	if !strings.Contains(out, "Iter 3/90") {
		t.Errorf("Iteration() missing iteration info: %q", out)
	}
	if !strings.Contains(out, "deepseek-chat") {
		t.Errorf("Iteration() missing model name: %q", out)
	}
}

func TestRenderer_Iteration_NoModel(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true)

	r.Iteration(1, 10, 0, 0, 0, 0)

	out := buf.String()
	if !strings.Contains(out, "Iter 1/10") {
		t.Errorf("Iteration() missing iteration info: %q", out)
	}
}

func TestRenderer_Thinking(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true)

	r.Thinking("Let me check the file contents first.")

	out := buf.String()
	if !strings.Contains(out, "🧠") {
		t.Errorf("Thinking() missing brain emoji: %q", out)
	}
	if !strings.Contains(out, "Let me check the file contents first.") {
		t.Errorf("Thinking() missing content: %q", out)
	}
}

func TestRenderer_Thinking_Empty(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true)

	r.Thinking("")

	if buf.Len() != 0 {
		t.Errorf("Thinking(empty) should produce no output, got %q", buf.String())
	}
}

func TestRenderer_ToolCall(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true)

	r.ToolCall("shell", `{"command": "ls -la"}`)

	out := buf.String()
	if !strings.Contains(out, "💻") {
		t.Errorf("ToolCall() missing shell emoji (shell): %q", out)
	}
	if !strings.Contains(out, "shell") {
		t.Errorf("ToolCall() missing tool name: %q", out)
	}
	if !strings.Contains(out, `"command"`) {
		t.Errorf("ToolCall() missing args: %q", out)
	}
}

func TestRenderer_ToolCall_TruncatedArgs(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true)

	longArgs := strings.Repeat("x", 200)
	r.ToolCall("read", longArgs)

	out := buf.String()
	// Args truncated to 100 chars
	if strings.Count(out, "x") >= 200 {
		t.Errorf("ToolCall() should truncate long args, got %d x chars", strings.Count(out, "x"))
	}
	if !strings.Contains(out, "…") {
		t.Errorf("ToolCall() missing truncation ellipsis: %q", out)
	}
}

func TestRenderer_ToolResult(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true)

	r.ToolResult("file1.txt\nfile2.txt\nfile3.txt")

	out := buf.String()
	if !strings.Contains(out, "file1.txt") {
		t.Errorf("ToolResult() missing output: %q", out)
	}
	// Multi-line output should include ellipsis
	if !strings.Contains(out, "…") {
		t.Errorf("ToolResult() missing ellipsis for multi-line output: %q", out)
	}
	// Should NOT contain lines beyond the first
	if strings.Contains(out, "file2.txt") {
		t.Errorf("ToolResult() should only show first line, got: %q", out)
	}
}

func TestRenderer_ToolResult_SingleLine(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true)

	r.ToolResult("short output")

	out := buf.String()
	if !strings.Contains(out, "short output") {
		t.Errorf("ToolResult() missing output: %q", out)
	}
	// Single short line — no ellipsis
	if strings.Contains(out, "…") {
		t.Errorf("ToolResult() should not have ellipsis for short single line: %q", out)
	}
}

func TestRenderer_ToolResult_GrayColor(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true)

	r.ToolResult("result text")

	out := buf.String()
	// Should use gray, not green
	if strings.Contains(out, green) {
		t.Errorf("ToolResult() should use gray, not green: %q", out)
	}
}

func TestRenderer_ToolResult_Truncation(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true)

	// A single line longer than 120 chars should be truncated.
	longLine := strings.Repeat("x", 200)
	r.ToolResult(longLine)

	out := buf.String()
	if strings.Count(out, "x") >= 200 {
		t.Errorf("ToolResult() should truncate long line, got %d x chars (input was %d)", strings.Count(out, "x"), 200)
	}
	if !strings.Contains(out, "…") {
		t.Errorf("ToolResult() missing truncation ellipsis: %q", out)
	}
}

func TestRenderer_ToolResult_Empty(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true)

	r.ToolResult("")

	if buf.Len() != 0 {
		t.Errorf("ToolResult(empty) should produce no output, got %q", buf.String())
	}
}

func TestRenderer_FinalAnswer(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true)

	r.FinalAnswer("The answer is 42.")

	out := buf.String()
	if !strings.Contains(out, "✅") {
		t.Errorf("FinalAnswer() missing check emoji: %q", out)
	}
	if !strings.Contains(out, "The answer is 42.") {
		t.Errorf("FinalAnswer() missing content: %q", out)
	}
}

func TestRenderer_FinalAnswer_Empty(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true)

	r.FinalAnswer("")

	if buf.Len() != 0 {
		t.Errorf("FinalAnswer(empty) should produce no output, got %q", buf.String())
	}
}

func TestRenderer_Error(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true)

	r.Error(errors.New("something went wrong"))

	out := buf.String()
	if !strings.Contains(out, "❌") {
		t.Errorf("Error() missing cross emoji: %q", out)
	}
	if !strings.Contains(out, "something went wrong") {
		t.Errorf("Error() missing message: %q", out)
	}
}

func TestRenderer_Error_Nil(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true)

	r.Error(nil)

	if buf.Len() != 0 {
		t.Errorf("Error(nil) should produce no output, got %q", buf.String())
	}
}

func TestRenderer_NoColor(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, false)

	r.Iteration(1, 5, 0, 0, 0, 0)

	out := buf.String()
	if strings.Contains(out, "\033[") {
		t.Errorf("NoColor should strip ANSI codes, got: %q", out)
	}
	if !strings.Contains(out, "Iter 1/5") {
		t.Errorf("NoColor should still render text: %q", out)
	}
}

func TestRenderer_NilWriter(t *testing.T) {
	r := New(nil, true)

	// None of these should panic
	r.Start("task")
	r.Iteration(1, 5, 0, 0, 0, 0)
	r.Thinking("hello")
	r.ToolCall("shell", "{}")
	r.ToolResult("output")
	r.FinalAnswer("answer")
	r.Error(errors.New("err"))
}

func TestRenderer_NilRenderer(t *testing.T) {
	var r *Renderer

	// None of these should panic on nil receiver
	r.Start("task")
	r.Iteration(1, 5, 0, 0, 0, 0)
	r.Thinking("hello")
	r.ToolCall("shell", "{}")
	r.ToolResult("output")
	r.FinalAnswer("answer")
	r.Error(errors.New("err"))
}

func TestRenderer_FullCycle(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true).WithModel("deepseek-chat")

	// Simulate one full session
	r.Start("what files are here?")
	r.Iteration(1, 90, 0, 0, 0, 0)
	r.Thinking("I need to read the file to understand its contents.")
	r.ToolCall("shell", `{"command": "cat main.go"}`)
	r.ToolResult("package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}")
	r.Iteration(2, 90, 0, 0, 0, 0)
	r.FinalAnswer("The file contains a simple Go program that prints 'hello'.")

	out := buf.String()

	// Verify each phase is present via its emoji
	emojis := []string{"🧠", "💻", "✅"}
	for _, emoji := range emojis {
		if !strings.Contains(out, emoji) {
			t.Errorf("FullCycle missing emoji %q in output:\n%s", emoji, out)
		}
	}

	// Verify key text
	texts := []string{"Iter 1/90", "shell", "package main", "Iter 2/90"}
	for _, text := range texts {
		if !strings.Contains(out, text) {
			t.Errorf("FullCycle missing text %q in output:\n%s", text, out)
		}
	}

	// Verify ANSI codes present
	if !strings.Contains(out, "\033[") {
		t.Error("FullCycle should contain ANSI color codes")
	}
}

func TestToolEmoji(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		// File / code
		{"read_file", "📝"},
		{"write_file", "📝"},
		{"search_files", "📝"},
		{"patch", "📝"},
		{"execute_code", "📝"},
		// Shell / process
		{"shell", "💻"},
		{"terminal", "💻"},
		{"process", "💻"},
		// Web / browser
		{"web_search", "🌐"},
		{"web_extract", "🌐"},
		{"browser_navigate", "🌐"},
		{"browser_click", "🌐"},
		{"browser_snapshot", "🌐"},
		// Memory
		{"memory", "🧠"},
		{"session_search", "🧠"},
		// Vision
		{"vision_analyze", "👁️"},
		// Messaging
		{"send_message", "💬"},
		// Delegation
		{"delegate_task", "👥"},
		{"delegate_tasks", "👥"},
		// Cron
		{"cronjob", "⏰"},
		// Skills / meta
		{"todo", "➕"},
		{"skill_view", "➕"},
		{"skill_manage", "➕"},
		{"skills_list", "➕"},
		{"clarify", "➕"},
		// Default fallback
		{"unknown_tool", "🔧"},
		{"random_name", "🔧"},
		{"", "🔧"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toolEmoji(tt.name)
			if got != tt.want {
				t.Errorf("toolEmoji(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestEvent_String(t *testing.T) {
	tests := []struct {
		e    Event
		want string
	}{
		{IterStart, "iter"},
		{Thinking, "thinking"},
		{ToolCall, "tool_call"},
		{ToolResult, "tool_result"},
		{FinalAnswer, "answer"},
		{Error, "error"},
		{Event(99), "unknown"},
	}

	for _, tt := range tests {
		if got := tt.e.String(); got != tt.want {
			t.Errorf("Event(%d).String() = %q, want %q", tt.e, tt.want, got)
		}
	}
}

func TestRenderer_Iteration_WithStats(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true).WithModel("deepseek-v4-pro")

	r.Iteration(3, 90, 5*time.Second, 1247, 342, 0)

	out := buf.String()
	if !strings.Contains(out, "Iter 3/90") {
		t.Errorf("missing iteration info: %q", out)
	}
	if !strings.Contains(out, "deepseek-v4-pro") {
		t.Errorf("missing model name: %q", out)
	}
	if !strings.Contains(out, "1247 in") {
		t.Errorf("missing input tokens: %q", out)
	}
	if !strings.Contains(out, "342 out") {
		t.Errorf("missing output tokens: %q", out)
	}
	if !strings.Contains(out, "5.0s") {
		t.Errorf("missing latency: %q", out)
	}
}

func TestRenderer_Iteration_StatsSuppressedWhenZero(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true).WithModel("test")

	r.Iteration(1, 10, 0, 0, 0, 0)

	out := buf.String()
	// Check that the stats pattern (with "in ·") doesn't appear.
	// Don't check for "[" which is part of ANSI escape codes.
	if strings.Contains(out, "0 in") {
		t.Errorf("stats should not appear when all values are zero: %q", out)
	}
}

func TestRenderer_Iteration_WithoutModel(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true)

	r.Iteration(1, 10, time.Second, 100, 50, 0)

	out := buf.String()
	if !strings.Contains(out, "Iter 1/10") {
		t.Errorf("missing iteration info: %q", out)
	}
	if !strings.Contains(out, "100 in") {
		t.Errorf("missing input tokens: %q", out)
	}
}

func TestRenderer_Iteration_NilSafe(t *testing.T) {
	var r *Renderer
	r.Iteration(1, 10, time.Second, 100, 50, 0) // should not panic
}

func TestRenderer_Summary_TokensOnly(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true)

	r.Summary(1500, 340, 0, 0, 0)

	out := buf.String()
	if !strings.Contains(out, "1500 in") {
		t.Errorf("missing input tokens: %q", out)
	}
	if !strings.Contains(out, "340 out") {
		t.Errorf("missing output tokens: %q", out)
	}
	// No cache metrics
	if strings.Contains(out, "stored") {
		t.Errorf("should not show 'stored' when cache creation is 0: %q", out)
	}
	if strings.Contains(out, "read") {
		t.Errorf("should not show cache read: %q", out)
	}
	if strings.Contains(out, "cached") {
		t.Errorf("should not show cached: %q", out)
	}
}

func TestRenderer_Summary_AnthropicCache(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true)

	r.Summary(2000, 500, 100, 200, 0)

	out := buf.String()
	if !strings.Contains(out, "2000 in") {
		t.Errorf("missing input tokens: %q", out)
	}
	if !strings.Contains(out, "500 out") {
		t.Errorf("missing output tokens: %q", out)
	}
	if !strings.Contains(out, "100 stored") {
		t.Errorf("missing cache creation: %q", out)
	}
	if !strings.Contains(out, "200 read") {
		t.Errorf("missing cache read: %q", out)
	}
	if strings.Contains(out, "cached") {
		t.Errorf("should not show OpenAI cached: %q", out)
	}
}

func TestRenderer_Summary_OpenAICache(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true)

	r.Summary(300, 50, 0, 0, 75)

	out := buf.String()
	if !strings.Contains(out, "300 in") {
		t.Errorf("missing input tokens: %q", out)
	}
	if !strings.Contains(out, "50 out") {
		t.Errorf("missing output tokens: %q", out)
	}
	if !strings.Contains(out, "75 cached") {
		t.Errorf("missing cached tokens: %q", out)
	}
	if strings.Contains(out, "stored") {
		t.Errorf("should not show 'stored' when cache creation is 0: %q", out)
	}
	if strings.Contains(out, "read") {
		t.Errorf("should not show cache read: %q", out)
	}
}

func TestRenderer_Summary_AllZero(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true)

	r.Summary(0, 0, 0, 0, 0)

	if buf.Len() != 0 {
		t.Errorf("Summary(all zero) should produce no output, got %q", buf.String())
	}
}

func TestRenderer_Summary_NilRenderer(t *testing.T) {
	var r *Renderer
	r.Summary(100, 20, 0, 0, 0) // should not panic
}

func TestRenderer_Summary_NilWriter(t *testing.T) {
	r := New(nil, true)
	r.Summary(100, 20, 0, 0, 0) // should not panic
}

func TestRenderer_Summary_NoColor(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, false)

	r.Summary(100, 20, 10, 5, 0)

	out := buf.String()
	if strings.Contains(out, "\033[") {
		t.Errorf("NoColor should strip ANSI codes, got: %q", out)
	}
	if !strings.Contains(out, "100 in") {
		t.Errorf("missing input tokens: %q", out)
	}
	if !strings.Contains(out, "10 stored") {
		t.Errorf("missing cache creation: %q", out)
	}
	if !strings.Contains(out, "5 read") {
		t.Errorf("missing cache read: %q", out)
	}
}

// ── Skill Event Tests ──────────────────────────────────────────────────

func TestRenderer_SkillLoaded(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true).WithSkillVerbose(true)

	r.SkillLoaded([]string{"docker-build", "go-test"})
	out := buf.String()
	if !strings.Contains(out, "📚") {
		t.Errorf("missing book emoji: %q", out)
	}
	if !strings.Contains(out, "Loaded skill") {
		t.Errorf("missing 'Loaded skill': %q", out)
	}
	if !strings.Contains(out, "docker-build") {
		t.Errorf("missing skill name: %q", out)
	}
	if !strings.Contains(out, "go-test") {
		t.Errorf("missing second skill name: %q", out)
	}
}

func TestRenderer_SkillLoaded_Empty(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true)
	r.SkillLoaded(nil)
	if buf.Len() != 0 {
		t.Errorf("expected no output for empty names, got: %q", buf.String())
	}
	r.SkillLoaded([]string{})
	if buf.Len() != 0 {
		t.Errorf("expected no output for empty slice, got: %q", buf.String())
	}
}

func TestRenderer_SkillAutoLoaded(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true).WithSkillVerbose(true)

	r.SkillAutoLoaded([]string{"ci-pipeline", "git-workflow"})
	out := buf.String()
	if !strings.Contains(out, "Auto-loaded 2 skill(s)") {
		t.Errorf("missing count: %q", out)
	}
	if !strings.Contains(out, "ci-pipeline") {
		t.Errorf("missing skill name: %q", out)
	}
}

func TestRenderer_SkillSuggested(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true).WithSkillVerbose(true)

	r.SkillSuggested("procedure-docker", "multi-step")
	out := buf.String()
	if !strings.Contains(out, "🔍") {
		t.Errorf("missing magnifying glass: %q", out)
	}
	if !strings.Contains(out, "Skill suggestion") {
		t.Errorf("missing 'Skill suggestion': %q", out)
	}
	if !strings.Contains(out, "procedure-docker") {
		t.Errorf("missing skill name: %q", out)
	}
	if !strings.Contains(out, "multi-step") {
		t.Errorf("missing heuristic name: %q", out)
	}
}

func TestRenderer_SkillSaved(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true).WithSkillVerbose(true)

	r.SkillSaved("my-skill")
	out := buf.String()
	if !strings.Contains(out, "✓") {
		t.Errorf("missing checkmark: %q", out)
	}
	if !strings.Contains(out, "Saved skill") {
		t.Errorf("missing 'Saved skill': %q", out)
	}
	if !strings.Contains(out, "my-skill") {
		t.Errorf("missing skill name: %q", out)
	}
}

func TestRenderer_SkillDeleted(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true).WithSkillVerbose(true)

	r.SkillDeleted("old-skill")
	out := buf.String()
	if !strings.Contains(out, "✗") {
		t.Errorf("missing cross: %q", out)
	}
	if !strings.Contains(out, "Deleted skill") {
		t.Errorf("missing 'Deleted skill': %q", out)
	}
	if !strings.Contains(out, "old-skill") {
		t.Errorf("missing skill name: %q", out)
	}
}

func TestRenderer_SkillEvents_NilSafe(t *testing.T) {
	var r *Renderer // nil
	r.SkillLoaded([]string{"test"})   // should not panic
	r.SkillAutoLoaded([]string{"test"}) // should not panic
	r.SkillSuggested("x", "h")       // should not panic
	r.SkillSaved("x")                // should not panic
	r.SkillDeleted("x")              // should not panic
}

func TestRenderer_SkillEvents_NoColor(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, false).WithSkillVerbose(true)

	r.SkillLoaded([]string{"docker-build"})
	out := buf.String()
	if strings.Contains(out, "\033[") {
		t.Errorf("NoColor should strip ANSI codes, got: %q", out)
	}
	if !strings.Contains(out, "docker-build") {
		t.Errorf("missing skill name in no-color: %q", out)
	}
}

func TestRenderer_NarratorMessage(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, false)

	r.NarratorMessage("📖 Reading `main.go`...")
	out := buf.String()
	if !strings.Contains(out, "📖 Reading `main.go`...") {
		t.Errorf("expected narrator message, got: %q", out)
	}
}

func TestRenderer_NarratorMessage_Empty(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, false)

	r.NarratorMessage("")
	out := buf.String()
	if out != "" {
		t.Errorf("expected empty output for empty message, got: %q", out)
	}
}

func TestRenderer_NarratorMessage_NilRenderer(t *testing.T) {
	var r *Renderer
	// Should not panic.
	r.NarratorMessage("test")
}

func TestRenderer_NarratorMessage_NoColor(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, false) // color = false = noColor

	r.NarratorMessage("📖 Reading")
	out := buf.String()
	if strings.Contains(out, "\033[") {
		t.Errorf("NoColor should strip ANSI codes, got: %q", out)
	}
	if !strings.Contains(out, "📖 Reading") {
		t.Errorf("missing message in no-color output: %q", out)
	}
}
