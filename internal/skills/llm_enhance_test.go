package skills

import (
	"context"
	"strings"
	"testing"
)

// mockLLM is a simple LLMClient mock for testing skill enhancement.
type mockLLM struct {
	responses map[string]string
}

func (m *mockLLM) SimpleCall(ctx context.Context, system, user string) (string, error) {
	for prefix, resp := range m.responses {
		if strings.Contains(system, prefix) || strings.Contains(user, prefix) {
			return resp, nil
		}
	}
	return "", nil
}

func TestParseLLMSuggestion_Full(t *testing.T) {
	text := `NAME: fix-go-mod-tidy
DESCRIPTION: Fix go.mod dependency issues
TOPICS: go, modules, dependencies
ACTIONS: fix, tidy, update
BODY:
## Overview

Fix common go.mod issues.

## Step-by-Step

1. Run go mod tidy
2. Verify build

## Common Pitfalls

- Missing proxy

## Verification

- go build succeeds`

	s := parseLLMSuggestion(text)
	if s == nil {
		t.Fatal("expected non-nil suggestion")
	}
	if s.Name != "fix-go-mod-tidy" {
		t.Errorf("Name = %q, want %q", s.Name, "fix-go-mod-tidy")
	}
	if s.Description != "Fix go.mod dependency issues" {
		t.Errorf("Description = %q, want %q", s.Description, "Fix go.mod dependency issues")
	}
	if !strings.Contains(s.Body, "Step-by-Step") {
		t.Errorf("Body should contain Step-by-Step, got %q", s.Body)
	}
	// Check topic keywords were stored in CommandLog as "topic:X"
	hasTopic := false
	for _, cl := range s.CommandLog {
		if cl == "topic:go" {
			hasTopic = true
		}
	}
	if !hasTopic {
		t.Errorf("expected topic:go in CommandLog, got %v", s.CommandLog)
	}
}

func TestParseLLMSuggestion_Minimal(t *testing.T) {
	text := `NAME: test-skill
DESCRIPTION: A test skill
BODY:
## Overview

Test body`

	s := parseLLMSuggestion(text)
	if s == nil {
		t.Fatal("expected non-nil suggestion")
	}
	if s.Name != "test-skill" {
		t.Errorf("Name = %q", s.Name)
	}
}

func TestParseLLMSuggestion_MissingName(t *testing.T) {
	text := `DESCRIPTION: No name here
BODY:
## Overview

missing name`

	s := parseLLMSuggestion(text)
	if s != nil {
		t.Error("expected nil for missing name")
	}
}

func TestParseLLMSuggestion_MissingBody(t *testing.T) {
	text := `NAME: no-body
DESCRIPTION: No body here`

	s := parseLLMSuggestion(text)
	if s != nil {
		t.Error("expected nil for missing body")
	}
}

func TestParseLLMSuggestion_Keywords(t *testing.T) {
	text := `NAME: docker-cleanup
DESCRIPTION: Clean up Docker resources
TOPICS: docker, containers, cleanup
ACTIONS: clean, remove
BODY:
## Overview

Clean up Docker.

## Step-by-Step

1. docker system prune`

	s := parseLLMSuggestion(text)
	if s == nil {
		t.Fatal("expected non-nil suggestion")
	}

	hasTopic := false
	hasAction := false
	for _, cl := range s.CommandLog {
		if cl == "topic:docker" {
			hasTopic = true
		}
		if cl == "action:clean" {
			hasAction = true
		}
	}
	if !hasTopic {
		t.Errorf("expected topic:docker, got %v", s.CommandLog)
	}
	if !hasAction {
		t.Errorf("expected action:clean, got %v", s.CommandLog)
	}
}

func TestGenerateSkillWithLLM_NilLLM(t *testing.T) {
	s := GenerateSkillWithLLM(nil, nil, nil, "multi-step")
	if s != nil {
		t.Error("expected nil for nil LLM")
	}
}

func TestEnhanceCurationWithLLM_NilReport(t *testing.T) {
	result := EnhanceCurationWithLLM(&mockLLM{}, nil)
	if result != "" {
		t.Errorf("expected empty for nil report, got %q", result)
	}
}

func TestEnhanceCurationWithLLM_NilLLM(t *testing.T) {
	result := EnhanceCurationWithLLM(nil, &CurationReport{TotalSkills: 5})
	if result != "" {
		t.Errorf("expected empty for nil LLM, got %q", result)
	}
}

func TestEnhanceCurationWithLLM_EmptyReport(t *testing.T) {
	result := EnhanceCurationWithLLM(&mockLLM{}, &CurationReport{TotalSkills: 0})
	if result != "" {
		t.Errorf("expected empty for 0 skills, got %q", result)
	}
}

func TestEnhanceCurationWithLLM_WithData(t *testing.T) {
	llm := &mockLLM{responses: map[string]string{
		"Review these skills": "Consider merging the two Go-related skills",
	}}
	report := &CurationReport{
		TotalSkills: 3,
		QualityIssues: []QualityIssue{
			{Name: "go-build", Issues: []string{"missing ## Overview section"}},
		},
		OverlapGroups: []OverlapGroup{
			{Skills: []string{"go-build", "go-test"}, Shared: []string{"go"}},
		},
	}

	result := EnhanceCurationWithLLM(llm, report)
	if result == "" {
		t.Fatal("expected non-empty result")
	}
	if !strings.Contains(result, "merge") {
		t.Logf("curation result: %s", result)
	}
}
