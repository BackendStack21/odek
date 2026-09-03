package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// A 429 followed by persistent network failures exhausted into
// RateLimitError: the Do-error path updates lastErr but never clears
// lastStatus, and the exhaustion path reports RateLimitError whenever the
// LAST STATUS was 429. A connection outage was rendered as "provider
// throttled" — misleading for users and for 429-aware retry UX.
func TestCall_NetworkExhaustionNotMaskedAsRateLimit(t *testing.T) {
	stubRetrySleep(t)
	var calls atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"slow down"}}`)
			return
		}
		// Every later attempt: kill the connection (network failure).
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				conn.Close()
			}
		}
	}))
	defer ts.Close()

	c := New(ts.URL, "key", "model", "", 0, 10*time.Second)
	_, err := c.Call(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	var rl *RateLimitError
	if errors.As(err, &rl) {
		t.Fatalf("network-error exhaustion masked as RateLimitError: %v", err)
	}
}

// Cloudflare-fronted providers (Z.ai, OpenRouter, Groq) answer 520-524
// during origin hiccups — the same incident class as the retried 529.
// A single 52x killed the turn on attempt 0.
func TestCall_RetriesCloudflare52x(t *testing.T) {
	stubRetrySleep(t)
	var calls atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(520)
			fmt.Fprint(w, `<html><body>Web server is returning an unknown error</body></html>`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"recovered after 520"}}]}`)
	}))
	defer ts.Close()

	c := New(ts.URL, "key", "model", "", 0, 10*time.Second)
	res, err := c.Call(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("Call after 520 retry: %v", err)
	}
	if res.Content != "recovered after 520" {
		t.Errorf("content = %q, want retried final answer", res.Content)
	}
	if calls.Load() < 2 {
		t.Errorf("server hit %d times, want >= 2 (520 must be retried)", calls.Load())
	}
}
