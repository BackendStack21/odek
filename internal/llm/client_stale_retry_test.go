package llm

// Bug-sweep batch 2 — stale retry state regression test.
//
// RED-first: lastStatus/lastBody were set on non-200 responses and never
// cleared, so a 429 early in the retry loop masked the REAL final failure:
// a later malformed-200 exhaustion was wrapped in RateLimitError — the
// exact type the serve turn handler reads as "provider throttled".

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient_Call_Stale429DoesNotMaskMalformed200(t *testing.T) {
	stubRetrySleep(t) // full retry budget without real backoff sleeps

	n := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		// 200 with a body that fails completion-body validation: the final
		// failure is NOT a rate limit.
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"unexpected":true}`))
	}))
	defer server.Close()

	c := New(server.URL, "sk-test", "test-model", "", 0, 0)
	_, err := c.Call(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("expected an error (malformed completion body)")
	}
	var rle *RateLimitError
	if errors.As(err, &rle) {
		t.Fatalf("final malformed-200 exhaustion misreported as RateLimitError (stale 429 state): %v", err)
	}
}
