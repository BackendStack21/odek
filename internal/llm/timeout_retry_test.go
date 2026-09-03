package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// http.Client timeouts produce
// "context deadline exceeded (Client.Timeout exceeded while awaiting headers)"
// — which never matches the lowercase "timeout" substring in
// isRetryableNetworkError. The 8-attempt retry budget (built exactly for
// transient failures like this) was bypassed: a single timed-out request
// killed the turn on attempt 0.
func TestCall_RetriesClientTimeout(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			// First attempt: exceed the client's 100ms timeout.
			time.Sleep(300 * time.Millisecond)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"recovered"}}]}`)
	}))
	defer server.Close()

	client := New(server.URL, "sk-test", "test-model", "", 0, 100*time.Millisecond)
	res, err := client.Call(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("Call after client-timeout retry: %v", err)
	}
	if res.Content != "recovered" {
		t.Errorf("content = %q, want %q", res.Content, "recovered")
	}
	if n := hits.Load(); n < 2 {
		t.Errorf("server hit %d times, want >= 2 (timeout must be retried)", n)
	}
}
