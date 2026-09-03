package llm

import (
	"context"
	"testing"
)

// Spec-legal SSE streams may set the event type with an "event:" field
// line before the first data line (SSE spec §5.2; gateways like
// LiteLLM/CF Workers emit "event: message"). The reader treated any
// non-data line before the first data as errNotSSE — which permanently
// downgraded the client to buffered mode. Field lines are metadata: they
// must be tolerated.
func TestStream_ToleratesEventFieldLineBeforeData(t *testing.T) {
	lines := []string{
		`event: message`,
		`data: {"choices":[{"delta":{"content":"Hi"}}]}`,
		``,
		`data: [DONE]`,
	}
	srv := sseServer(t, lines)
	defer srv.Close()

	c := New(srv.URL, "sk-test", "gpt-test", "", 0, 0)
	res, err := c.CallStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil, func(Delta) error { return nil })
	if err != nil {
		t.Fatalf("spec-valid event:-prefixed SSE stream failed: %v", err)
	}
	if res == nil || res.Content != "Hi" {
		t.Fatalf("result = %+v, want content %q", res, "Hi")
	}
}
