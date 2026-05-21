package tool

import (
	"errors"
	"strings"
	"testing"
)

func TestClarifyTool_Name(t *testing.T) {
	tool := &ClarifyTool{}
	if got := tool.Name(); got != "clarify" {
		t.Errorf("Name() = %q, want %q", got, "clarify")
	}
}

func TestClarifyTool_Description(t *testing.T) {
	tool := &ClarifyTool{}
	desc := tool.Description()
	if desc == "" {
		t.Error("Description() returned empty string")
	}
	if !strings.Contains(desc, "question") {
		t.Error("Description() should mention 'question'")
	}
}

func TestClarifyTool_Schema(t *testing.T) {
	tool := &ClarifyTool{}
	schema := tool.Schema()
	m, ok := schema.(map[string]any)
	if !ok {
		t.Fatalf("Schema() returned %T, want map[string]any", schema)
	}
	if m["type"] != "object" {
		t.Errorf("schema.type = %v, want 'object'", m["type"])
	}
	props, ok := m["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema.properties is not a map")
	}
	if _, ok := props["question"]; !ok {
		t.Error("schema.properties missing 'question' key")
	}
	required, ok := m["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "question" {
		t.Errorf("schema.required = %v, want ['question']", required)
	}
}

func TestClarifyTool_Call_Success(t *testing.T) {
	tool := &ClarifyTool{
		Answer: func(question string) (string, error) {
			if question != "What is your name?" {
				t.Errorf("question = %q, want %q", question, "What is your name?")
			}
			return "Alice", nil
		},
	}
	result, err := tool.Call(`{"question": "What is your name?"}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if result != "Alice" {
		t.Errorf("Call() = %q, want %q", result, "Alice")
	}
}

func TestClarifyTool_Call_EmptyQuestion(t *testing.T) {
	tool := &ClarifyTool{
		Answer: func(question string) (string, error) {
			return "answer", nil
		},
	}
	_, err := tool.Call(`{"question": ""}`)
	if err == nil {
		t.Fatal("expected error for empty question")
	}
	if !strings.Contains(err.Error(), "question is required") {
		t.Errorf("error = %q, want 'question is required'", err)
	}
}

func TestClarifyTool_Call_MissingQuestion(t *testing.T) {
	tool := &ClarifyTool{
		Answer: func(question string) (string, error) {
			return "answer", nil
		},
	}
	_, err := tool.Call(`{}`)
	if err == nil {
		t.Fatal("expected error for missing question")
	}
	if !strings.Contains(err.Error(), "question is required") {
		t.Errorf("error = %q, want 'question is required'", err)
	}
}

func TestClarifyTool_Call_InvalidJSON(t *testing.T) {
	tool := &ClarifyTool{}
	_, err := tool.Call(`{invalid}`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "parse args") {
		t.Errorf("error = %q, want 'parse args'", err)
	}
}

func TestClarifyTool_Call_NilAnswer(t *testing.T) {
	tool := &ClarifyTool{} // Answer is nil
	_, err := tool.Call(`{"question": "test"}`)
	if err == nil {
		t.Fatal("expected error when Answer is nil")
	}
	if !strings.Contains(err.Error(), "Answer function not set") {
		t.Errorf("error = %q, want 'Answer function not set'", err)
	}
}

func TestClarifyTool_Call_AnswerError(t *testing.T) {
	expectedErr := errors.New("user cancelled")
	tool := &ClarifyTool{
		Answer: func(question string) (string, error) {
			return "", expectedErr
		},
	}
	_, err := tool.Call(`{"question": "continue?"}`)
	if err == nil {
		t.Fatal("expected error from Answer")
	}
	if !strings.Contains(err.Error(), "user cancelled") {
		t.Errorf("error = %q, want 'user cancelled'", err)
	}
}

func TestNewClarifyTool(t *testing.T) {
	fn := func(question string) (string, error) { return "yes", nil }
	tool := NewClarifyTool(fn)
	if tool == nil {
		t.Fatal("NewClarifyTool returned nil")
	}
	if tool.Answer == nil {
		t.Error("Answer function not set")
	}
	result, err := tool.Answer("test")
	if err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
	if result != "yes" {
		t.Errorf("Answer() = %q, want %q", result, "yes")
	}
}
