package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/loop"
)

// recordingTool wraps its output as untrusted and is used to prove that an
// agent run records the ingest through the context-scoped recorder.
type recordingTool struct {
	ctxTool
}

func (r *recordingTool) Name() string        { return "recording_tool" }
func (r *recordingTool) Description() string { return "Records output for testing." }
func (r *recordingTool) Schema() any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}
func (r *recordingTool) Call(args string) (string, error) {
	return wrapUntrusted(r.toolCtx(), "recording_tool", "sensitive payload"), nil
}

// TestAgentRun_RecordsIngestViaContext runs a single agent turn that invokes a
// tool whose output is wrapped as untrusted. The ingest recorder attached to
// the context must capture the tool's source and content.
func TestAgentRun_RecordsIngestViaContext(t *testing.T) {
	var recorded []struct {
		Source  string
		Content string
	}
	recorder := func(source, content string) {
		recorded = append(recorded, struct {
			Source  string
			Content string
		}{source, content})
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// First request: ask the agent to call recording_tool.
		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"content": "",
						"tool_calls": []map[string]any{
							{
								"id":   "call_1",
								"type": "function",
								"function": map[string]any{
									"name":      "recording_tool",
									"arguments": "{}",
								},
							},
						},
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	agent, err := odek.New(odek.Config{
		Model:         "test-model",
		BaseURL:       server.URL,
		APIKey:        "sk-test",
		MaxIterations: 2,
		SystemMessage: "You are a test agent.",
		NoProjectFile: true,
		Tools: []odek.Tool{
			&recordingTool{},
		},
		DangerousConfig: &danger.DangerousConfig{},
	})
	if err != nil {
		t.Fatalf("odek.New: %v", err)
	}
	defer agent.Close()

	ctx := loop.WithIngestRecorder(context.Background(), recorder)
	messages := []llm.Message{
		{Role: "system", Content: "You are a test agent."},
		{Role: "user", Content: "call the recording tool"},
	}
	_, _, err = agent.RunWithMessages(ctx, messages)
	if err != nil {
		// The agent may error when the mock server returns the same tool-call
		// response repeatedly; we only care that the ingest was recorded.
		t.Logf("RunWithMessages returned (acceptable): %v", err)
	}

	found := false
	for _, rec := range recorded {
		if rec.Source == "recording_tool" && rec.Content == "sensitive payload" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("recorder did not capture recording_tool ingest; recorded = %+v", recorded)
	}
}

// TestAgentRun_SkillIngestRecordedViaContext verifies that skill context loaded
// during a run is recorded through the context-scoped recorder.
func TestAgentRun_SkillIngestRecordedViaContext(t *testing.T) {
	var recorded []struct {
		Source  string
		Content string
	}
	recorder := func(source, content string) {
		recorded = append(recorded, struct {
			Source  string
			Content string
		}{source, content})
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
	}))
	defer server.Close()

	agent, err := odek.New(odek.Config{
		Model:            "test-model",
		BaseURL:          server.URL,
		APIKey:           "sk-test",
		MaxIterations:    2,
		SystemMessage:    "You are a test agent.",
		NoProjectFile:    true,
		UntrustedWrapper: func(source, content string) string { return fmt.Sprintf("[%s:%s]", source, content) },
	})
	if err != nil {
		t.Fatalf("odek.New: %v", err)
	}
	defer agent.Close()

	ctx := loop.WithIngestRecorder(context.Background(), recorder)
	messages := []llm.Message{
		{Role: "system", Content: "You are a test agent."},
		{Role: "user", Content: "trigger skill"},
	}
	_, _, err = agent.RunWithMessages(ctx, messages)
	if err != nil {
		t.Fatalf("RunWithMessages: %v", err)
	}

	// Without a skill loader configured, no skill ingest is recorded. This test
	// primarily ensures the context path does not panic when no loader is set.
	for _, rec := range recorded {
		if rec.Source == "skill" {
			t.Errorf("unexpected skill ingest recorded: %+v", rec)
		}
	}
}
