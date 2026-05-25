package skills

import (
	"context"
	"errors"
	"testing"
)

// mockLLMClient is a test helper that returns predefined responses.
type mockLLMClient struct {
	resp string
	err  error
}

func (m *mockLLMClient) SimpleCall(_ context.Context, _, _ string) (string, error) {
	return m.resp, m.err
}

func TestParseLLMSuggestion_FullOutput(t *testing.T) {
	input := `NAME: test-skill
DESCRIPTION: A test skill for unit testing
TOPICS: docker, build, ci
ACTIONS: create, optimize
BODY:
## Overview
This is a test skill body.

## Step-by-Step
1. Do the thing
2. Verify it works

## Common Pitfalls
- Nothing

## Verification
Check the output.`

	s := parseLLMSuggestion(input)
	if s == nil {
		t.Fatal("parseLLMSuggestion returned nil for valid input")
	}
	if s.Name != "test-skill" {
		t.Errorf("Name = %q, want %q", s.Name, "test-skill")
	}
	if s.Description != "A test skill for unit testing" {
		t.Errorf("Description = %q, want %q", s.Description, "A test skill for unit testing")
	}
	if s.Heuristic != "llm-enhanced" {
		t.Errorf("Heuristic = %q, want %q", s.Heuristic, "llm-enhanced")
	}
	if len(s.CommandLog) != 5 {
		t.Fatalf("expected 5 CommandLog entries (3 topic + 2 action), got %d", len(s.CommandLog))
	}
	expectedTopics := []string{"topic:docker", "topic:build", "topic:ci"}
	for i, et := range expectedTopics {
		if s.CommandLog[i] != et {
			t.Errorf("CommandLog[%d] = %q, want %q", i, s.CommandLog[i], et)
		}
	}
	expectedActions := []string{"action:create", "action:optimize"}
	for i, ea := range expectedActions {
		if s.CommandLog[3+i] != ea {
			t.Errorf("CommandLog[%d] = %q, want %q", 3+i, s.CommandLog[3+i], ea)
		}
	}
	if s.Body == "" {
		t.Error("Body should not be empty")
	}
}

func TestParseLLMSuggestion_MissingName(t *testing.T) {
	input := `DESCRIPTION: No name here
BODY:
## Overview
Missing name test`

	s := parseLLMSuggestion(input)
	if s != nil {
		t.Error("expected nil for missing NAME")
	}
}

func TestParseLLMSuggestion_MissingBody(t *testing.T) {
	input := `NAME: no-body
DESCRIPTION: This has no body`

	s := parseLLMSuggestion(input)
	if s != nil {
		t.Error("expected nil for missing BODY section")
	}
}

func TestParseLLMSuggestion_EmptyInput(t *testing.T) {
	s := parseLLMSuggestion("")
	if s != nil {
		t.Error("expected nil for empty input")
	}
}

func TestParseLLMSuggestion_EmptyTopicsAndActions(t *testing.T) {
	input := `NAME: empty-fields
DESCRIPTION: Has empty topics and actions
TOPICS:
ACTIONS:
BODY:
## Overview
Some body content`

	s := parseLLMSuggestion(input)
	if s == nil {
		t.Fatal("expected non-nil for valid input with empty topics/actions")
	}
	if len(s.CommandLog) != 0 {
		t.Errorf("expected empty CommandLog for empty topics/actions, got %d entries", len(s.CommandLog))
	}
}

func TestParseLLMSuggestion_TopicsActionsWithExtraWhitespace(t *testing.T) {
	input := `NAME: whitespace-test
DESCRIPTION: Test with extra whitespace
TOPICS:  docker ,  build ,ci  
ACTIONS: deploy , verify
BODY:
## Overview
Body here`

	s := parseLLMSuggestion(input)
	if s == nil {
		t.Fatal("expected non-nil")
	}
	if len(s.CommandLog) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(s.CommandLog))
	}
	if s.CommandLog[0] != "topic:docker" {
		t.Errorf("expected topic:docker, got %q", s.CommandLog[0])
	}
	if s.CommandLog[3] != "action:deploy" {
		t.Errorf("expected action:deploy, got %q", s.CommandLog[3])
	}
}

func TestParseLLMSuggestion_BodyWithMultipleLines(t *testing.T) {
	input := `NAME: multi-line-body
DESCRIPTION: Test multiline body
TOPICS: test
ACTIONS: check
BODY:
Line 1
Line 2
Line 3

Line 5 after blank`

	s := parseLLMSuggestion(input)
	if s == nil {
		t.Fatal("expected non-nil")
	}
	expectedBody := "Line 1\nLine 2\nLine 3\n\nLine 5 after blank"
	if s.Body != expectedBody {
		t.Errorf("Body = %q, want %q", s.Body, expectedBody)
	}
}

func TestParseLLMSuggestion_OnlyNameAndBody(t *testing.T) {
	input := `NAME: minimal
DESCRIPTION: Minimal test
BODY:
Just a single line body`

	s := parseLLMSuggestion(input)
	if s == nil {
		t.Fatal("expected non-nil for minimal valid input")
	}
	if s.Name != "minimal" {
		t.Errorf("Name = %q, want %q", s.Name, "minimal")
	}
	if s.Body != "Just a single line body" {
		t.Errorf("Body = %q, want %q", s.Body, "Just a single line body")
	}
	if len(s.CommandLog) != 0 {
		t.Errorf("expected empty CommandLog, got %d", len(s.CommandLog))
	}
}

func TestGenerateSkillWithLLM_NilLLM(t *testing.T) {
	s := GenerateSkillWithLLM(nil, nil, nil, "test")
	if s != nil {
		t.Error("expected nil for nil LLM")
	}
}

func TestGenerateSkillWithLLM_LLMError(t *testing.T) {
	mock := &mockLLMClient{err: errors.New("LLM unavailable")}
	s := GenerateSkillWithLLM(mock, nil, nil, "test")
	if s != nil {
		t.Error("expected nil on LLM error")
	}
}

func TestGenerateSkillWithLLM_EmptyResponse(t *testing.T) {
	mock := &mockLLMClient{resp: ""}
	s := GenerateSkillWithLLM(mock, nil, nil, "test")
	if s != nil {
		t.Error("expected nil on empty response")
	}
}

func TestGenerateSkillWithLLM_Success(t *testing.T) {
	mock := &mockLLMClient{
		resp: `NAME: docker-build
DESCRIPTION: Build Docker images with caching
TOPICS: docker, build
ACTIONS: build, cache
BODY:
## Overview
Guide for building Docker images.

## Step-by-Step
1. Run docker build
2. Tag the image`,
	}
	s := GenerateSkillWithLLM(mock, nil, nil, "multi-step")
	if s == nil {
		t.Fatal("expected non-nil on success")
	}
	if s.Name != "docker-build" {
		t.Errorf("Name = %q, want %q", s.Name, "docker-build")
	}
	if s.Heuristic != "llm-enhanced" {
		t.Errorf("Heuristic = %q, want %q", s.Heuristic, "llm-enhanced")
	}
	if len(s.CommandLog) != 4 {
		t.Errorf("expected 4 CommandLog entries, got %d", len(s.CommandLog))
	}
}

func TestGenerateSkillWithLLM_UserMessagesAndToolCalls(t *testing.T) {
	mock := &mockLLMClient{
		resp: `NAME: go-test
DESCRIPTION: Run Go tests with coverage
TOPICS: go, test
ACTIONS: test, coverage
BODY:
## Overview
Testing Go code with coverage.`,
	}
	calls := []ToolCall{
		{Tool: "shell", Input: "go test ./...", ExitCode: 0},
		{Tool: "shell", Input: "go test -cover ./...", ExitCode: 0},
	}
	msgs := []string{"test the code", "check coverage"}
	s := GenerateSkillWithLLM(mock, calls, msgs, "tool-sequence")
	if s == nil {
		t.Fatal("expected non-nil")
	}
	if s.Name != "go-test" {
		t.Errorf("Name = %q, want %q", s.Name, "go-test")
	}
}

func TestGenerateSkillWithLLM_LongInputTruncation(t *testing.T) {
	longInput := ""
	for i := 0; i < 300; i++ {
		longInput += "x"
	}
	longMsg := ""
	for i := 0; i < 300; i++ {
		longMsg += "y"
	}

	mock := &mockLLMClient{
		resp: `NAME: truncation-test
DESCRIPTION: Test input truncation
TOPICS: test
ACTIONS: verify
BODY:
## Overview
Truncation body.`,
	}
	calls := []ToolCall{
		{Tool: "shell", Input: longInput, ExitCode: 0},
	}
	msgs := []string{longMsg}
	s := GenerateSkillWithLLM(mock, calls, msgs, "multi-step")
	if s == nil {
		t.Fatal("expected non-nil — truncation shouldn't affect parsing")
	}
	if s.Name != "truncation-test" {
		t.Errorf("Name = %q, want %q", s.Name, "truncation-test")
	}
}

func TestEnhanceCurationWithLLM_NilLLM(t *testing.T) {
	s := EnhanceCurationWithLLM(nil, &CurationReport{TotalSkills: 5})
	if s != "" {
		t.Error("expected empty string for nil LLM")
	}
}

func TestEnhanceCurationWithLLM_NilReport(t *testing.T) {
	mock := &mockLLMClient{}
	s := EnhanceCurationWithLLM(mock, nil)
	if s != "" {
		t.Error("expected empty string for nil report")
	}
}

func TestEnhanceCurationWithLLM_EmptyReport(t *testing.T) {
	mock := &mockLLMClient{resp: "should not be called"}
	s := EnhanceCurationWithLLM(mock, &CurationReport{TotalSkills: 0})
	if s != "" {
		t.Error("expected empty string for report with 0 skills")
	}
}

func TestEnhanceCurationWithLLM_LLMFails(t *testing.T) {
	mock := &mockLLMClient{err: errors.New("LLM failure")}
	s := EnhanceCurationWithLLM(mock, &CurationReport{
		TotalSkills: 5,
		QualityIssues: []QualityIssue{
			{Name: "test-skill", Issues: []string{"missing body"}},
		},
	})
	if s != "" {
		t.Error("expected empty string on LLM error")
	}
}

func TestEnhanceCurationWithLLM_Success(t *testing.T) {
	mock := &mockLLMClient{
		resp: "The skills look good overall. Consider merging docker-build and docker-push.",
	}
	s := EnhanceCurationWithLLM(mock, &CurationReport{
		TotalSkills: 10,
		QualityIssues: []QualityIssue{
			{Name: "old-skill", Issues: []string{"stale: not used in 120 days"}},
		},
		OverlapGroups: []OverlapGroup{
			{Skills: []string{"docker-build", "docker-push"}, Shared: []string{"docker"}},
		},
	})
	if s == "" {
		t.Fatal("expected non-empty response")
	}
	if s != "The skills look good overall. Consider merging docker-build and docker-push." {
		t.Errorf("unexpected response: %q", s)
	}
}
