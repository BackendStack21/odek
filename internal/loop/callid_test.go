package loop

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/tool"
)

// ── P0-3: tool_call events must carry a stable call_id ──────────────────
//
// started,started,…,completed,completed,… with no correlation id cannot be
// paired by audit/replay consumers; args_sha256 only works when arguments
// happen to be unique.

// newBatchedToolServer returns a fake LLM server whose first response
// requests two tool calls in one batch.
func newBatchedToolServer() *httptest.Server {
	callCount := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			fmt.Fprint(w, `{
				"choices":[{
					"message":{
						"content":"Calling two tools.",
						"tool_calls":[
							{"id":"call_x1","function":{"name":"echo","arguments":"{\"text\":\"first\"}"}},
							{"id":"call_x2","function":{"name":"echo","arguments":"{\"text\":\"second\"}"}}
						]
					}
				}],
				"usage":{"prompt_tokens":11,"completion_tokens":7}
			}`)
		} else {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}],"usage":{"prompt_tokens":23,"completion_tokens":5}}`)
		}
	}))
}

func TestEngine_Events_CallIDCorrelatesBatchedCalls(t *testing.T) {
	server := newBatchedToolServer()
	defer server.Close()

	registry := tool.NewRegistry([]tool.Tool{
		&fakeTool{name: "echo", description: "echoes input", output: "out"},
	})
	client := llm.New(server.URL, "sk-test", "test-model", "", 0, 0)
	engine := New(client, registry, 10, "", nil, 0)

	col := &eventCollector{}
	engine.SetEventHandler(col.handle)

	if _, err := engine.Run(context.Background(), "Echo twice"); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	var startIDs, doneIDs []string
	for _, ev := range col.all() {
		switch ev.Type {
		case events.TypeToolCallStarted:
			id, _ := ev.Data["call_id"].(string)
			startIDs = append(startIDs, id)
		case events.TypeToolCallCompleted:
			id, _ := ev.Data["call_id"].(string)
			doneIDs = append(doneIDs, id)
		}
	}

	if len(startIDs) != 2 || len(doneIDs) != 2 {
		t.Fatalf("start ids = %v, done ids = %v; want 2 each", startIDs, doneIDs)
	}
	for i, id := range startIDs {
		if id == "" {
			t.Errorf("started event %d missing call_id", i)
		}
		if doneIDs[i] != id {
			t.Errorf("completed event %d call_id = %q, want matching started %q", i, doneIDs[i], id)
		}
	}
	if startIDs[0] == startIDs[1] {
		t.Errorf("distinct calls in one batch must have distinct call_ids, got %q twice", startIDs[0])
	}
	if startIDs[0] != "call_x1" || startIDs[1] != "call_x2" {
		t.Errorf("provider tool-call IDs should be preserved verbatim, got %v", startIDs)
	}
}

// Same scenario, but the provider omits call IDs entirely — the synthetic
// fallback must still be stable and distinct.
func TestEngine_Events_CallIDSyntheticWhenProviderOmits(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			fmt.Fprint(w, `{
				"choices":[{
					"message":{
						"content":"Calling two tools.",
						"tool_calls":[
							{"id":"","function":{"name":"echo","arguments":"{}"}},
							{"id":"","function":{"name":"echo","arguments":"{}"}}
						]
					}
				}],
				"usage":{"prompt_tokens":11,"completion_tokens":7}
			}`)
		} else {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}],"usage":{"prompt_tokens":23,"completion_tokens":5}}`)
		}
	}))
	defer server.Close()

	registry := tool.NewRegistry([]tool.Tool{
		&fakeTool{name: "echo", description: "echoes input", output: "out"},
	})
	client := llm.New(server.URL, "sk-test", "test-model", "", 0, 0)
	engine := New(client, registry, 10, "", nil, 0)

	col := &eventCollector{}
	engine.SetEventHandler(col.handle)

	if _, err := engine.Run(context.Background(), "Echo twice"); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	var startIDs, doneIDs []string
	for _, ev := range col.all() {
		switch ev.Type {
		case events.TypeToolCallStarted:
			id, _ := ev.Data["call_id"].(string)
			startIDs = append(startIDs, id)
		case events.TypeToolCallCompleted:
			id, _ := ev.Data["call_id"].(string)
			doneIDs = append(doneIDs, id)
		}
	}
	want := []string{"it1-call0", "it1-call1"}
	for i, id := range want {
		if startIDs[i] != id {
			t.Errorf("started[%d] call_id = %q, want %q", i, startIDs[i], id)
		}
		if doneIDs[i] != id {
			t.Errorf("completed[%d] call_id = %q, want %q", i, doneIDs[i], id)
		}
	}
}
