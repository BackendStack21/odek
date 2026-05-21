package skills

import (
	"fmt"
	"testing"
)

func TestRunAllHeuristics(t *testing.T) {
	calls := []ToolCall{
		{Tool: "terminal", Input: "git clone repo", ExitCode: 0, Turn: 0},
		{Tool: "terminal", Input: "cd repo", ExitCode: 0, Turn: 1},
		{Tool: "terminal", Input: "npm install", ExitCode: 0, Turn: 2},
		{Tool: "terminal", Input: "npm test", ExitCode: 0, Turn: 3},
	}
	userMsgs := []string{"build the project"}
	msgs := make([]LlmMessage, 0)
	for _, c := range calls {
		msgs = append(msgs, LlmMessage{
			Role: "assistant",
			ToolCalls: []LlmToolCall{{
				ID: "call_1",
				Function: struct {
					Name      string
					Arguments string
				}{Name: c.Tool, Arguments: c.Input},
			}},
		})
		msgs = append(msgs, LlmMessage{Role: "tool", Content: "ok", ToolCallID: "call_1"})
	}
	suggestions := RunAllHeuristics(msgs, userMsgs)
	if len(suggestions) == 0 {
		t.Error("RunAllHeuristics should return suggestions")
	}
}

func TestRunAllHeuristics_Empty(t *testing.T) {
	suggestions := RunAllHeuristics(nil, nil)
	if len(suggestions) != 0 {
		t.Errorf("expected 0 suggestions for empty input, got %d", len(suggestions))
	}
}

func TestDefaultSkillsConfig(t *testing.T) {
	cfg := DefaultSkillsConfig()
	if cfg.MaxAutoLoad != 3 {
		t.Errorf("MaxAutoLoad = %d", cfg.MaxAutoLoad)
	}
	if cfg.MaxLazySlots != 5 {
		t.Errorf("MaxLazySlots = %d", cfg.MaxLazySlots)
	}
	if !cfg.Learn {
		t.Error("Learn should default to true")
	}
	if cfg.Import.MaxSizeBytes != 1048576 {
		t.Errorf("Import.MaxSizeBytes = %d", cfg.Import.MaxSizeBytes)
	}
	if cfg.Curation.StalenessDays != 90 {
		t.Errorf("Curation.StalenessDays = %d", cfg.Curation.StalenessDays)
	}
}

func TestUserSkillsDir(t *testing.T) {
	dir := UserSkillsDir()
	if dir != "~/.odek/skills" {
		t.Errorf("UserSkillsDir = %q", dir)
	}
}

func TestProjectSkillsDir(t *testing.T) {
	dir := ProjectSkillsDir()
	if dir != "./.odek/skills" {
		t.Errorf("ProjectSkillsDir = %q", dir)
	}
}

func TestActiveQualities(t *testing.T) {
	active := ActiveQualities()
	if !active[QualityDraft] || !active[QualityVerified] || !active[QualityImported] || !active[QualityManual] {
		t.Error("ActiveQualities missing expected quality states")
	}
	if active[QualityStale] {
		t.Error("QualityStale should not be active")
	}
}

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
	output := FormatSuggestion(s, false)
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

func TestExtractRelevantChange(t *testing.T) {
	tests := []struct {
		oldCmd, newCmd, expected string
	}{
		{"docker run --name app nginx", "docker run --name app alpine", "   Key change: 'nginx' → 'alpine'"},
		{"go build -o bin", "go build -o dist", "   Key change: 'bin' → 'dist'"},
		{"short", "short too", ""}, // too few words
		{"a b c d", "a b c d", ""}, // no changes
	}
	for _, tt := range tests {
		got := extractRelevantChange(tt.oldCmd, tt.newCmd)
		if got != tt.expected {
			t.Errorf("extractRelevantChange(%q, %q) = %q, want %q", tt.oldCmd, tt.newCmd, got, tt.expected)
		}
	}
}

func TestFormatSuggestion_WithPreview(t *testing.T) {
	s := SkillSuggestion{
		Name:        "test-skill",
		Description: "A test skill for preview",
		Heuristic:   "multi-step",
		CommandLog:  []string{"cmd1"},
		Body:        "## Overview\n\nTest body content.\n\n## Common Pitfalls\n\n- None\n\n## Verification\n\n- Check output",
	}
	output := FormatSuggestion(s, true)
	if !contains(output, "test-skill") {
		t.Errorf("expected skill name in output: %s", output)
	}
	if !contains(output, "Overview") {
		t.Errorf("expected body preview in output: %s", output)
	}
	if !contains(output, "Preview") {
		t.Errorf("expected 'Preview' section: %s", output)
	}

	// Without preview
	outputNoPreview := FormatSuggestion(s, false)
	if contains(outputNoPreview, "Preview") || contains(outputNoPreview, "Overview") {
		t.Errorf("unexpected preview content: %s", outputNoPreview)
	}
}

func TestPassesQualityGate(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"too short", "## Overview\n\nToo short.", false},
		{"missing overview", "## Common Pitfalls\n\n- None\n\nSome more text to reach the 200 char minimum because we need more content here. Let me keep typing to ensure we cross that threshold. Almost there now.", false},
		{"missing pitfalls", "## Overview\n\nThis has overview but no pitfalls section. Let me add enough text to reach the 200 character minimum which requires quite a bit of padding actually. Still going. Almost there now yes.", false},
		{"passes", "## Overview\n\nThis is a good skill body with proper structure.\n\n## Step-by-Step\n\n1. Do this\n2. Do that\n\n## Common Pitfalls\n\n- Watch out for X\n\n## Verification\n\n- Run the command. Adding more text to reach the 200 char minimum. Still more text needed for the threshold check.", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := SkillSuggestion{Body: tt.body}
			got := PassesQualityGate(s)
			if got != tt.want {
				t.Errorf("PassesQualityGate() = %v, want %v (body len=%d)", got, tt.want, len(tt.body))
			}
		})
	}
}

func TestAutoSaveSuggestions_WithHeuristics(t *testing.T) {
	dir := t.TempDir()
	body := "## Overview\n\nTest with enough body text to pass the quality gate minimum of 200 characters. Adding more padding here to ensure we cross that threshold. Still going with more text content for the body length requirement.\n\n## Step-by-Step\n\n1. Step one\n\n## Common Pitfalls\n\n- Pitfall\n\n## Verification\n\n- Run command"
	suggestions := []SkillSuggestion{
		{Name: "skill-a", Heuristic: "multi-step", Body: body},
		{Name: "skill-b", Heuristic: "error-recovery", Body: body},
	}

	cfg := DefaultSkillsConfig()
	cfg.AutoSave.MaxPerRun = 5
	result := AutoSaveSuggestions(suggestions, dir, cfg)

	if len(result.Saved) != 2 {
		t.Fatalf("expected 2 saved, got %d", len(result.Saved))
	}
	if result.Heuristics["skill-a"] != "multi-step" {
		t.Errorf("Heuristics[skill-a] = %q, want multi-step", result.Heuristics["skill-a"])
	}
	if result.Heuristics["skill-b"] != "error-recovery" {
		t.Errorf("Heuristics[skill-b] = %q, want error-recovery", result.Heuristics["skill-b"])
	}
}

func TestAutoSaveSuggestions_QualityGateFails(t *testing.T) {
	dir := t.TempDir()
	suggestions := []SkillSuggestion{
		{Name: "bad-skill", Body: "Too short body"},
	}

	cfg := DefaultSkillsConfig()
	result := AutoSaveSuggestions(suggestions, dir, cfg)

	if len(result.Saved) != 0 {
		t.Errorf("expected 0 saved, got %d", len(result.Saved))
	}
	if len(result.Failed) != 1 {
		t.Errorf("expected 1 failed, got %d", len(result.Failed))
	}
}

func TestAutoSaveSuggestions_MaxPerRun(t *testing.T) {
	dir := t.TempDir()
	body := "## Overview\n\nTest with enough body text to pass the quality gate minimum of 200 characters. Adding more padding here to ensure we cross that threshold. Still going with more text content for the body length requirement.\n\n## Step-by-Step\n\n1. Step\n\n## Common Pitfalls\n\n- Pitfall\n\n## Verification\n\n- Run command"
	var suggestions []SkillSuggestion
	for i := 0; i < 5; i++ {
		suggestions = append(suggestions, SkillSuggestion{
			Name: fmt.Sprintf("skill-%d", i), Body: body,
		})
	}

	cfg := DefaultSkillsConfig()
	cfg.AutoSave.MaxPerRun = 2
	result := AutoSaveSuggestions(suggestions, dir, cfg)

	if len(result.Saved) != 2 {
		t.Errorf("expected 2 saved (max per run), got %d", len(result.Saved))
	}
}

func TestDefaultSkipThreshold(t *testing.T) {
	cfg := DefaultSkillsConfig()
	if cfg.Curation.SkipThreshold != 1 {
		t.Errorf("SkipThreshold = %d, want 1 (one skip should be enough)", cfg.Curation.SkipThreshold)
	}
	if !cfg.AutoSave.Enabled {
		t.Error("AutoSave.Enabled should default to true")
	}
	if !cfg.Curation.AutoCurate {
		t.Error("AutoCurate should default to true")
	}
}

func TestFormatSuggestionPreview(t *testing.T) {
	s := SkillSuggestion{
		Body: "## Overview\n\nTest body.\n\n## Step-by-Step\n\n1. Do this.\n\n## Common Pitfalls\n\n- None\n\n## Verification\n\n- Check.",
	}
	preview := FormatSuggestionPreview(s)
	if preview == "" {
		t.Error("expected non-empty preview")
	}
	if !contains(preview, "Overview") {
		t.Errorf("expected 'Overview' in preview: %s", preview)
	}

	// Empty body
	s2 := SkillSuggestion{Body: ""}
	if FormatSuggestionPreview(s2) != "" {
		t.Error("expected empty preview for empty body")
	}
}
