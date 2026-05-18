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
		// Boolean flags: path after boolean flag is still eaten
		// (fundamental limitation — can't distinguish flag values from paths)
		{"go test -v ./...", "go test"},
		// But flag=value tokens survive (this was the regression fix)
		{"go test -v -count=1 ./...", "go test -count=1 <path>"},
		// Combined flag=value preserved
		{"docker run --memory=512m image", "docker run --memory=512m image"},
		// Boolean flags followed by path — path normalizes but may be eaten
		{"cat /etc/hosts", "cat <path>"},
	}
	for _, tt := range tests {
		got := normalizeCommand(tt.input)
		if got != tt.want {
			t.Errorf("normalizeCommand(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExtractToolCalls_Empty(t *testing.T) {
	calls := ExtractToolCalls(nil)
	if len(calls) != 0 {
		t.Errorf("expected 0 calls, got %d", len(calls))
	}
}

func TestExtractToolCalls_NoToolCalls(t *testing.T) {
	msgs := []LlmMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
	}
	calls := ExtractToolCalls(msgs)
	if len(calls) != 0 {
		t.Errorf("expected 0 calls, got %d", len(calls))
	}
}

func TestExtractToolCalls_WithToolCalls(t *testing.T) {
	msgs := []LlmMessage{
		{Role: "user", Content: "list files"},
		{
			Role: "assistant",
			ToolCalls: []LlmToolCall{
				{
					ID: "call1",
					Function: struct {
						Name      string
						Arguments string
					}{Name: "shell", Arguments: `{"command": "ls -la"}`},
				},
			},
		},
		{Role: "tool", ToolCallID: "call1", Name: "shell", Content: "file1.txt\nfile2.txt"},
	}

	calls := ExtractToolCalls(msgs)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Tool != "shell" {
		t.Errorf("Tool = %q, want shell", calls[0].Tool)
	}
	if calls[0].ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", calls[0].ExitCode)
	}
}

func TestExtractToolCalls_ErrorExitCode(t *testing.T) {
	msgs := []LlmMessage{
		{Role: "user", Content: "run"},
		{
			Role: "assistant",
			ToolCalls: []LlmToolCall{
				{
					ID: "call1",
					Function: struct {
						Name      string
						Arguments string
					}{Name: "shell", Arguments: `{"command": "bad-command"}`},
				},
			},
		},
		{Role: "tool", ToolCallID: "call1", Name: "shell", Content: "error: command not found"},
	}

	calls := ExtractToolCalls(msgs)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", calls[0].ExitCode)
	}
}

func TestFormatSuggestion(t *testing.T) {
	s := SkillSuggestion{
		Name:        "test-skill",
		Description: "A test",
		Heuristic:   "multi-step",
		CommandLog:  []string{"cmd1", "cmd2"},
	}
	output := FormatSuggestion(s)
	if !contains(output, "test-skill") {
		t.Errorf("expected skill name in output: %s", output)
	}
	if !contains(output, "multi-step") {
		t.Errorf("expected heuristic in output")
	}
	if !contains(output, "cmd1") {
		t.Errorf("expected command in output")
	}
}

func TestSaveSuggestion(t *testing.T) {
	dir := t.TempDir()
	s := SkillSuggestion{
		Name:        "test-skill",
		Description: "A test skill",
		Heuristic:   "multi-step",
		CommandLog:  []string{"docker build .", "docker push"},
		Body:        "## Overview\n\nTest\n\n## Common Pitfalls\n\n- None\n\n## Verification\n\n- Check. This needs to be long enough to pass the 300 char threshold. Adding more text. And more. And still more. Almost there. Just a bit more now. Yes this should be more than enough.",
	}

	err := SaveSuggestion(dir, s)
	if err != nil {
		t.Fatal(err)
	}

	// Verify the skill was saved
	sm := NewSkillManager(dir, "")
	found := false
	for _, sk := range sm.Result.Lazy {
		if sk.Name == "test-skill" {
			found = true
			break
		}
	}
	if !found {
		t.Error("saved skill not found in scan")
	}
}

func TestExtractTopicKeywords(t *testing.T) {
	cmds := []string{"docker build .", "docker push image"}
	result := extractTopicKeywords(cmds)
	if len(result) == 0 {
		t.Fatal("expected keywords")
	}
	// "docker" should be one of the topics
	found := false
	for _, k := range result {
		if k == "docker" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'docker' in topics, got %v", result)
	}
}

func TestExtractActionKeywords(t *testing.T) {
	cmds := []string{"npm build", "docker push"}
	result := extractActionKeywords(cmds)
	if len(result) == 0 {
		t.Fatal("expected action keywords")
	}
	found := false
	for _, a := range result {
		if a == "build" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'build' in actions, got %v", result)
	}
}
