package skills

import (
	"bytes"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/guard"
)

func TestExtractUserMessages_PicksOnlyUserRole(t *testing.T) {
	msgs := []LlmMessage{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "skip me"},
		{Role: "user", Content: "second"},
		{Role: "tool", Content: "tool output"},
		{Role: "system", Content: "ignored"},
	}
	got := ExtractUserMessages(msgs)
	want := []string{"first", "second"}
	if len(got) != len(want) {
		t.Fatalf("got %d messages, want %d (%v)", len(got), len(want), got)
	}
	for i, g := range got {
		if g != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, g, want[i])
		}
	}
}

func TestExtractUserMessages_EmptyInput(t *testing.T) {
	if got := ExtractUserMessages(nil); got != nil {
		t.Errorf("nil input should yield nil, got %v", got)
	}
}

// TestAnalyzeMessages_EmptyConversation guards the trivial input path —
// no user messages, no suggestions, no notifier events.
func TestAnalyzeMessages_EmptyConversation(t *testing.T) {
	sm := NewSkillManager(t.TempDir(), t.TempDir())
	got := AnalyzeMessages(nil, nil, sm, nil, false, true)
	if len(got) != 0 {
		t.Errorf("empty conversation should yield no suggestions, got %d", len(got))
	}
}

// TestRunAutoSaveLoop_DisabledReturnsFalse covers the gate that lets the
// caller fall back to the interactive prompt when auto-save is off.
func TestRunAutoSaveLoop_DisabledReturnsFalse(t *testing.T) {
	cfg := SkillsConfig{} // AutoSave.Enabled defaults to false
	if RunAutoSaveLoop(nil, "", nil, nil, cfg, nil, guard.Config{}, nil) {
		t.Error("RunAutoSaveLoop should return false when AutoSave disabled")
	}
}

// TestRunAutoSaveLoop_RequireLLMWithoutLLMReturnsFalse guards the
// "require LLM enhancement but it's off" gate — the auto-save pipeline
// must defer to the caller rather than save raw heuristic output.
func TestRunAutoSaveLoop_RequireLLMWithoutLLMReturnsFalse(t *testing.T) {
	cfg := SkillsConfig{
		AutoSave: AutoSaveConfig{Enabled: true, RequireLLM: true},
		LLMLearn: false,
	}
	if RunAutoSaveLoop(nil, "", nil, nil, cfg, nil, guard.Config{}, nil) {
		t.Error("RunAutoSaveLoop should return false when RequireLLM is set but LLMLearn is off")
	}
}

// TestRunAutoSaveLoop_EnabledEmptySuggestions runs the path where
// auto-save is allowed and proceeds, but the suggestion list is empty —
// it should still return true (the gate fired) and produce no output.
func TestRunAutoSaveLoop_EnabledEmptySuggestions(t *testing.T) {
	cfg := SkillsConfig{
		AutoSave: AutoSaveConfig{Enabled: true},
	}
	var buf bytes.Buffer
	got := RunAutoSaveLoop(nil, t.TempDir(), nil, nil, cfg, nil, guard.Config{}, &buf)
	if !got {
		t.Error("RunAutoSaveLoop should return true when AutoSave is enabled and the gate passes")
	}
	if buf.Len() != 0 {
		t.Errorf("expected no output for empty suggestions, got: %s", buf.String())
	}
}

// TestRunAutoSaveLoop_DeclinesTaintedSkill covers the path where a tainted
// suggestion reaches the auto-save pipeline and is declined rather than
// persisted.
func TestRunAutoSaveLoop_DeclinesTaintedSkill(t *testing.T) {
	body := "## Overview\n\nEnough body text to pass the quality gate minimum of 200 characters. Keep adding padding so the suggestion does not fail the gate. More text here.\n\n## Step-by-Step\n\n1. Step\n\n## Common Pitfalls\n\n- Pitfall\n\n## Verification\n\n- Run command"
	cfg := SkillsConfig{
		AutoSave: AutoSaveConfig{Enabled: true, MaxPerRun: 5},
	}
	tainted := SkillSuggestion{
		Name:      "tainted-skill",
		Heuristic: "test",
		Body:      body,
		Provenance: SkillProvenance{
			Untrusted: true,
			Sources:   []string{"browser"},
		},
	}
	var buf bytes.Buffer
	got := RunAutoSaveLoop([]SkillSuggestion{tainted}, t.TempDir(), nil, nil, cfg, nil, guard.Config{}, &buf)
	if !got {
		t.Fatal("RunAutoSaveLoop should return true when AutoSave is enabled")
	}
	if !strings.Contains(buf.String(), "Declined to auto-save tainted skill") {
		t.Errorf("verbose output should mention declined tainted skill, got:\n%s", buf.String())
	}
}

// TestRunAutoSaveLoop_VerboseWriterReceivesFailedMessage exercises the
// progress-output branch. We feed a suggestion that will fail the
// quality gate (empty body) and check the verbose stream sees the
// "Quality gate failed" notice.
func TestRunAutoSaveLoop_VerboseWriterReceivesFailedMessage(t *testing.T) {
	cfg := SkillsConfig{
		AutoSave: AutoSaveConfig{Enabled: true, MaxPerRun: 5},
	}
	bad := SkillSuggestion{
		Name:      "needs-review",
		Heuristic: "test",
		// Body intentionally empty so the quality gate rejects it.
	}
	var buf bytes.Buffer
	got := RunAutoSaveLoop([]SkillSuggestion{bad}, t.TempDir(), nil, nil, cfg, nil, guard.Config{}, &buf)
	if !got {
		t.Fatal("RunAutoSaveLoop should return true when AutoSave is enabled")
	}
	if !strings.Contains(buf.String(), "Quality gate failed") {
		t.Errorf("verbose output should mention Quality gate failed for empty-body suggestion, got:\n%s", buf.String())
	}
}
