package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/tool"
)

func TestWithIngestRecorder_RoundTrip(t *testing.T) {
	var gotSource, gotContent string
	rec := func(source, content string) {
		gotSource = source
		gotContent = content
	}

	ctx := WithIngestRecorder(context.Background(), rec)
	fn := IngestRecorderFrom(ctx)
	if fn == nil {
		t.Fatal("IngestRecorderFrom returned nil")
	}
	fn("https://example.com", "body")

	if gotSource != "https://example.com" {
		t.Errorf("source = %q, want %q", gotSource, "https://example.com")
	}
	if gotContent != "body" {
		t.Errorf("content = %q, want %q", gotContent, "body")
	}
}

func TestIngestRecorderFrom_Missing(t *testing.T) {
	if fn := IngestRecorderFrom(context.Background()); fn != nil {
		t.Errorf("IngestRecorderFrom without recorder = non-nil, want nil")
	}
}

func TestIngestRecorderFrom_NilContext(t *testing.T) {
	// IngestRecorderFrom should not panic on nil context; it simply returns nil.
	if fn := IngestRecorderFrom(nil); fn != nil {
		t.Errorf("IngestRecorderFrom(nil) = non-nil, want nil")
	}
}

func TestWithIngestRecorder_Overwrite(t *testing.T) {
	var order []string

	ctx := WithIngestRecorder(context.Background(), func(source, content string) {
		order = append(order, "first")
	})
	ctx = WithIngestRecorder(ctx, func(source, content string) {
		order = append(order, "second")
	})

	IngestRecorderFrom(ctx)("s", "c")
	if len(order) != 1 || order[0] != "second" {
		t.Errorf("recorder order = %v, want [second]", order)
	}
}

// TestEngine_RecordsSkillIngest runs the loop with a skill loader and verifies
// the ingest recorder captures the skill context source.
func TestEngine_RecordsSkillIngest(t *testing.T) {
	var sources []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
	}))
	defer server.Close()

	client := llm.New(server.URL, "sk-test", "test-model", "", 0, 0)
	registry := tool.NewRegistry(nil)
	engine := New(client, registry, 10, "", nil, 0)
	engine.SetSkillLoader(func(userInput string) string {
		return "skill context for " + userInput
	})

	ctx := WithIngestRecorder(context.Background(), func(source, content string) {
		sources = append(sources, source)
	})

	_, _, err := engine.RunWithMessages(ctx, []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hello"},
	})
	if err != nil {
		t.Fatalf("RunWithMessages error: %v", err)
	}

	found := false
	for _, s := range sources {
		if s == "skill" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("recorder did not capture skill ingest; sources = %v", sources)
	}
}

// TestEngine_RecordsEpisodeIngest runs the loop with an episode context
// function and verifies the ingest recorder captures the episode source.
func TestEngine_RecordsEpisodeIngest(t *testing.T) {
	var sources []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
	}))
	defer server.Close()

	client := llm.New(server.URL, "sk-test", "test-model", "", 0, 0)
	registry := tool.NewRegistry(nil)
	engine := New(client, registry, 10, "", nil, 0)
	engine.SetEpisodeContextFunc(func(userInput string) string {
		return "episode context for " + userInput
	})

	ctx := WithIngestRecorder(context.Background(), func(source, content string) {
		sources = append(sources, source)
	})

	_, _, err := engine.RunWithMessages(ctx, []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hello"},
	})
	if err != nil {
		t.Fatalf("RunWithMessages error: %v", err)
	}

	found := false
	for _, s := range sources {
		if s == "episode" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("recorder did not capture episode ingest; sources = %v", sources)
	}
}

// TestEngine_RecordsToolIngest runs the loop with a context-aware tool and
// verifies the ingest recorder attached to the run context receives the tool's
// untrusted output.
func TestEngine_RecordsToolIngest(t *testing.T) {
	var sources, contents []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
									"name":      "recorder",
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

	client := llm.New(server.URL, "sk-test", "test-model", "", 0, 0)
	rec := &recorderTool{}
	registry := tool.NewRegistry([]tool.Tool{rec})
	engine := New(client, registry, 2, "", nil, 0)

	ctx := WithIngestRecorder(context.Background(), func(source, content string) {
		sources = append(sources, source)
		contents = append(contents, content)
	})

	_, _, _ = engine.RunWithMessages(ctx, []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "call recorder"},
	})

	found := false
	for i, s := range sources {
		if s == "recorder" && contents[i] == "sensitive output" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("recorder did not capture tool ingest; sources=%v contents=%v", sources, contents)
	}
}

// recorderTool is a tool that reports its output through the context-scoped
// ingest recorder by wrapping it as untrusted.
type recorderTool struct {
	ctx context.Context
}

func (r *recorderTool) Name() string        { return "recorder" }
func (r *recorderTool) Description() string { return "records output" }
func (r *recorderTool) Schema() any         { return map[string]any{"type": "object", "properties": map[string]any{}} }
func (r *recorderTool) Call(args string) (string, error) {
	if fn := IngestRecorderFrom(r.ctx); fn != nil {
		fn("recorder", "sensitive output")
	}
	return "wrapped output", nil
}
func (r *recorderTool) SetContext(ctx context.Context) { r.ctx = ctx }
