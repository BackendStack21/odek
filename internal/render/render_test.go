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
	if !strings.Contains(out, "kode") {
		t.Errorf("Start() missing kode brand: %q", out)
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
	if !strings.Contains(out, "🔧") {
		t.Errorf("ToolCall() missing wrench emoji: %q", out)
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
	emojis := []string{"🧠", "🔧", "✅"}
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
