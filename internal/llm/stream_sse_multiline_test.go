package llm

// Regression test for batch-3 finding B3-LLM-2: readSSE used to unmarshal
// each data: line standalone, so a spec-valid SSE event whose JSON is split
// across multiple data: lines (the SSE spec joins same-event data lines
// with "\n") failed to parse and — being pre-emission — burned the full
// transient retry budget before failing. Events must be assembled per the
// SSE spec.

import (
	"context"
	"testing"
)

func TestStream_MultiDataLineEventParsed(t *testing.T) {
	// One event, two data lines — joined with "\n" this is valid JSON:
	//   {"choices":\n[{"delta":{"content":"Hi"}}]}
	lines := []string{
		`data: {"choices":`,
		`data: [{"delta":{"content":"Hi"}}]}`,
		``,
		`data: [DONE]`,
	}
	srv := sseServer(t, lines)
	defer srv.Close()

	c := New(srv.URL, "sk-test", "gpt-test", "", 0, 0)
	res, err := c.CallStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil, func(Delta) error { return nil })
	if err != nil {
		t.Fatalf("BUG B3-LLM-2: spec-valid multi-data-line SSE event failed: %v", err)
	}
	if res == nil || res.Content != "Hi" {
		t.Fatalf("BUG B3-LLM-2: result = %+v, want content %q", res, "Hi")
	}
}
