package skills

import (
	"testing"
)

func TestDetectMultiStepProcedure_EnoughCalls(t *testing.T) {
	calls := []ToolCall{
		{Tool: "terminal", Input: "git clone repo", ExitCode: 0, Turn: 0},
		{Tool: "terminal", Input: "cd repo && npm install", ExitCode: 0, Turn: 1},
		{Tool: "terminal", Input: "npm run build", ExitCode: 0, Turn: 2},
		{Tool: "terminal", Input: "npm test", ExitCode: 0, Turn: 3},
	}
	suggestions := DetectMultiStepProcedure(calls)
	if len(suggestions) == 0 {
		t.Fatal("expected suggestions for 4+ sequential terminal calls")
	}
	if suggestions[0].Heuristic != "multi-step" {
		t.Errorf("Heuristic = %q", suggestions[0].Heuristic)
	}
}

func TestDetectMultiStepProcedure_TooFew(t *testing.T) {
	calls := []ToolCall{
		{Tool: "terminal", Input: "npm install", ExitCode: 0, Turn: 0},
		{Tool: "terminal", Input: "npm test", ExitCode: 0, Turn: 1},
	}
	suggestions := DetectMultiStepProcedure(calls)
	if len(suggestions) != 0 {
		t.Error("expected no suggestions for < 4 calls")
	}
}

func TestDetectMultiStepProcedure_FailureBreaksSequence(t *testing.T) {
	calls := []ToolCall{
		{Tool: "terminal", Input: "step1", ExitCode: 0, Turn: 0},
		{Tool: "terminal", Input: "step2", ExitCode: 0, Turn: 1},
		{Tool: "terminal", Input: "step3", ExitCode: 0, Turn: 2},
		{Tool: "terminal", Input: "step4", ExitCode: 0, Turn: 3},
		{Tool: "terminal", Input: "step5", ExitCode: 1, Turn: 4}, // breaks
		{Tool: "terminal", Input: "step6", ExitCode: 0, Turn: 5},
		{Tool: "terminal", Input: "step7", ExitCode: 0, Turn: 6},
		{Tool: "terminal", Input: "step8", ExitCode: 0, Turn: 7},
		{Tool: "terminal", Input: "step9", ExitCode: 0, Turn: 8},
	}
	suggestions := DetectMultiStepProcedure(calls)
	if len(suggestions) != 2 {
		t.Fatalf("expected 2 sequences (before and after failure), got %d", len(suggestions))
	}
}

func TestDetectErrorRecovery_Found(t *testing.T) {
	calls := []ToolCall{
		{Tool: "terminal", Input: "docker build .", ExitCode: 1, Turn: 0},
		{Tool: "terminal", Input: "docker build --cache-from .", ExitCode: 0, Turn: 1},
		{Tool: "terminal", Input: "docker push", ExitCode: 0, Turn: 2},
	}
	suggestions := DetectErrorRecovery(calls)
	if len(suggestions) == 0 {
		t.Fatal("expected error recovery suggestion")
	}
}

func TestDetectErrorRecovery_NoRecovery(t *testing.T) {
	calls := []ToolCall{
		{Tool: "terminal", Input: "failing", ExitCode: 1, Turn: 0},
	}
	suggestions := DetectErrorRecovery(calls)
	if len(suggestions) != 0 {
		t.Error("expected no suggestion (not enough calls)")
	}
}

func TestDetectRepeatedAction_Found(t *testing.T) {
	calls := []ToolCall{
		{Tool: "terminal", Input: "go test ./...", ExitCode: 0, Turn: 0},
		{Tool: "terminal", Input: "npm run lint", ExitCode: 0, Turn: 1},
		{Tool: "terminal", Input: "go test ./...", ExitCode: 0, Turn: 2},
		{Tool: "terminal", Input: "npm run build", ExitCode: 0, Turn: 3},
		{Tool: "terminal", Input: "go test ./...", ExitCode: 0, Turn: 4},
		{Tool: "terminal", Input: "npm test", ExitCode: 0, Turn: 5},
	}
	suggestions := DetectRepeatedAction(calls)
	if len(suggestions) == 0 {
		t.Fatal("expected repeated action suggestion")
	}
}

func TestDetectRepeatedAction_NotEnough(t *testing.T) {
	calls := []ToolCall{
		{Tool: "terminal", Input: "cmd", ExitCode: 0, Turn: 0},
		{Tool: "terminal", Input: "other", ExitCode: 0, Turn: 1},
	}
	suggestions := DetectRepeatedAction(calls)
	if len(suggestions) != 0 {
		t.Error("expected no suggestion")
	}
}

func TestDetectExplicitInstruction(t *testing.T) {
	msgs := []string{"save this as a skill please"}
	calls := []ToolCall{
		{Tool: "terminal", Input: "some command", ExitCode: 0, Turn: 0},
	}
	suggestions := DetectExplicitInstruction(msgs, calls)
	if len(suggestions) == 0 {
		t.Fatal("expected suggestion for explicit instruction")
	}
	if suggestions[0].Heuristic != "explicit-instruction" {
		t.Errorf("Heuristic = %q", suggestions[0].Heuristic)
	}
}

func TestDetectExplicitInstruction_NoMatch(t *testing.T) {
	msgs := []string{"how are you"}
	suggestions := DetectExplicitInstruction(msgs, nil)
	if len(suggestions) != 0 {
		t.Error("expected no suggestion")
	}
}

func TestDetectCorrection_Found(t *testing.T) {
	msgs := []string{"no, do it differently"}
	calls := []ToolCall{
		{Tool: "terminal", Input: "wrong-approach", ExitCode: 0, Turn: 0},
		{Tool: "terminal", Input: "correct-approach", ExitCode: 0, Turn: 1},
		{Tool: "terminal", Input: "verify-result", ExitCode: 0, Turn: 2},
	}
	suggestions := DetectCorrection(calls, msgs)
	if len(suggestions) == 0 {
		t.Fatal("expected correction suggestion")
	}
}

func TestExtractTopic(t *testing.T) {
	tests := []struct {
		cmd  string
		want string
	}{
		{"docker build .", "docker"},
		{"npm test", "npm"},
		{"", "unknown"},
		{"'quoted' command", "quoted"},
	}
	for _, tt := range tests {
		got := extractTopic(tt.cmd)
		if got != tt.want {
			t.Errorf("extractTopic(%q) = %q, want %q", tt.cmd, got, tt.want)
		}
	}
}

func TestNormalizeCommand(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"npm test", "npm test"},
		{"docker build -t myimage .", "docker build <path>"},
		{"go test ./...", "go test <path>"},
	}
	for _, tt := range tests {
		got := normalizeCommand(tt.input)
		if got != tt.want {
			t.Errorf("normalizeCommand(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
