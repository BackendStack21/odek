package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Persistent provider rate-limiting (HTTP 429) that exhausts the retry budget
// must surface as a typed *RateLimitError on BOTH chat paths (buffered Call
// and CallStream), so callers — the serve turn handler in particular — can
// render a precise "provider throttled" failure instead of an opaque llm
// error string. Motivated by the 2026-08-29 subagent 429-saturation incidents
// where throttled turns vanished without an explainable error.

func alwaysRateLimitedServer(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"throttled","code":"1302"}}`))
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestClient_Call_RateLimitExhausted_TypedError(t *testing.T) {
	stubRetrySleep(t)
	ts := alwaysRateLimitedServer(t)

	c := New(ts.URL, "key", "model", "", 0, 10*time.Second)
	_, err := c.Call(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("expected error after exhausted retries")
	}
	var rle *RateLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("error %v (%T) is not a *RateLimitError", err, err)
	}
	if rle.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode = %d, want 429", rle.StatusCode)
	}
	if rle.Attempts != 8 {
		t.Errorf("Attempts = %d, want 8 (maxRetries+1)", rle.Attempts)
	}
}

func TestClient_CallStream_RateLimitExhausted_TypedError(t *testing.T) {
	stubRetrySleep(t)
	ts := alwaysRateLimitedServer(t)

	c := New(ts.URL, "key", "model", "", 0, 10*time.Second)
	_, err := c.CallStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil, func(Delta) error { return nil })
	if err == nil {
		t.Fatal("expected error after exhausted retries")
	}
	var rle *RateLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("error %v (%T) is not a *RateLimitError", err, err)
	}
	if rle.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode = %d, want 429", rle.StatusCode)
	}
}
