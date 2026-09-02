package llm

// Regression test for batch-3 finding B3-LLM-1: CallStream must recover
// from a 400 naming reasoning_effort exactly like buffered Call does —
// learn the constraint once, retry the stream with effort "none", and pin
// "none" for later tool-bearing streams (buildCallParams).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

func TestCallStream_LearnsReasoningEffortNone(t *testing.T) {
	tools := []ToolDef{{
		Type: "function",
		Function: FunctionDef{
			Name:        "echo",
			Description: "echoes input",
			Parameters:  map[string]any{"type": "object"},
		},
	}}

	var mu sync.Mutex
	var efforts []string
	var hits, nonStream int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		effort, _ := parsed["reasoning_effort"].(string)
		if stream, _ := parsed["stream"].(bool); !stream {
			atomic.AddInt32(&nonStream, 1) // a buffered fallback would hide the bug
		}
		mu.Lock()
		efforts = append(efforts, effort)
		mu.Unlock()
		if effort != "none" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"Function tools with reasoning_effort are not supported","type":"invalid_request_error","param":"reasoning_effort"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"choices":[{"index":0,"delta":{"content":"ok"}}]}`)
		fmt.Fprintln(w)
		fmt.Fprintln(w, "data: [DONE]")
		fmt.Fprintln(w)
	}))
	defer srv.Close()

	c := New(srv.URL, "sk-test", "gpt-test", "high", 0, 0)
	msgs := []Message{{Role: "user", Content: "hi"}}

	// First stream: 400 (effort "high") → learned retry with "none" → success.
	res, err := c.CallStream(context.Background(), msgs, nil, tools, func(Delta) error { return nil })
	if err != nil {
		t.Fatalf("BUG B3-LLM-1: CallStream did not recover from reasoning_effort 400: %v", err)
	}
	if res == nil || res.Content != "ok" {
		t.Fatalf("BUG B3-LLM-1: result = %+v, want content %q", res, "ok")
	}
	// Second stream must send "none" immediately — constraint learned once.
	if _, err := c.CallStream(context.Background(), msgs, nil, tools, func(Delta) error { return nil }); err != nil {
		t.Fatalf("second CallStream: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"high", "none", "none"}
	if len(efforts) != len(want) {
		t.Fatalf("BUG B3-LLM-1: reasoning_effort per request = %v, want %v", efforts, want)
	}
	for i := range want {
		if efforts[i] != want[i] {
			t.Fatalf("BUG B3-LLM-1: reasoning_effort per request = %v, want %v", efforts, want)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("BUG B3-LLM-1: hits = %d, want 3 (one 400 + one recovery + one pinned)", got)
	}
	if got := atomic.LoadInt32(&nonStream); got != 0 {
		t.Fatalf("BUG B3-LLM-1: %d buffered-path requests — recovery must stay on the streaming transport", got)
	}
}
