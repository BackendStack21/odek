package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	if d := parseRetryAfter("2"); d != 2*time.Second {
		t.Errorf("parseRetryAfter(\"2\") = %v, want 2s", d)
	}
	if d := parseRetryAfter("  5 "); d != 5*time.Second {
		t.Errorf("parseRetryAfter trims and parses, got %v", d)
	}
	if d := parseRetryAfter(""); d != 0 {
		t.Errorf("empty header → 0, got %v", d)
	}
	if d := parseRetryAfter("garbage"); d != 0 {
		t.Errorf("unparseable → 0, got %v", d)
	}
	if d := parseRetryAfter("0"); d != 0 {
		t.Errorf("zero/negative → 0, got %v", d)
	}
	// Capped at maxRetryAfter.
	if d := parseRetryAfter("100000"); d != maxRetryAfter {
		t.Errorf("huge value should cap at %v, got %v", maxRetryAfter, d)
	}
}

// TestClient_Call_HonorsRetryAfter verifies a 429 with a Retry-After header is
// retried (rather than failed) and ultimately succeeds. The 1s value keeps the
// test fast while exercising the header path.
func TestClient_Call_HonorsRetryAfter(t *testing.T) {
	var callCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if int(callCount.Add(1)) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":"slow down"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer ts.Close()

	c := New(ts.URL, "key", "model", "", 0, 10*time.Second)
	start := time.Now()
	result, err := c.Call(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "ok" {
		t.Errorf("content = %q, want ok", result.Content)
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Errorf("expected to wait ~1s for Retry-After, only waited %v", elapsed)
	}
}

// TestClient_SimpleCall_RetryOn429 verifies the lightweight secondary calls
// share the main loop's retry resilience: a transient 429 no longer aborts a
// skill-match / memory / title call on the first failure.
func TestClient_SimpleCall_RetryOn429(t *testing.T) {
	var callCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := int(callCount.Add(1))
		if count <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"error":{"message":"Rate limited"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"assessed"}}]}`))
	}))
	defer ts.Close()

	c := New(ts.URL, "key", "model", "", 0, 10*time.Second)
	out, err := c.SimpleCall(context.Background(), "sys", "user")
	if err != nil {
		t.Fatalf("unexpected error after retries: %v", err)
	}
	if out != "assessed" {
		t.Errorf("content = %q, want %q", out, "assessed")
	}
	if callCount.Load() != 3 {
		t.Errorf("call count = %d, want 3 (SimpleCall should retry)", callCount.Load())
	}
}

func TestClient_Call_RetryOn429(t *testing.T) {
	var callCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := int(callCount.Add(1))
		if count <= 2 {
			// First two calls return 429
			w.WriteHeader(http.StatusTooManyRequests)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"error":{"message":"Rate limited"}}`))
			return
		}
		// Third call succeeds
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"hello"}}]}`))
	}))
	defer ts.Close()

	c := New(ts.URL, "key", "model", "", 0, 10*time.Second)
	result, err := c.Call(context.Background(), []Message{
		{Role: "user", Content: "hi"},
	}, nil, nil)

	if err != nil {
		t.Fatalf("unexpected error after retries: %v", err)
	}
	if result.Content != "hello" {
		t.Errorf("content = %q, want %q", result.Content, "hello")
	}
	if callCount.Load() != 3 {
		t.Errorf("call count = %d, want 3", callCount.Load())
	}
}

func TestClient_Call_RetryOn503(t *testing.T) {
	var callCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := int(callCount.Add(1))
		if count <= 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer ts.Close()

	c := New(ts.URL, "key", "model", "", 0, 10*time.Second)
	result, err := c.Call(context.Background(), []Message{
		{Role: "user", Content: "hi"},
	}, nil, nil)

	if err != nil {
		t.Fatalf("unexpected error after retry: %v", err)
	}
	if result.Content != "ok" {
		t.Errorf("content = %q, want %q", result.Content, "ok")
	}
}

func TestClient_Call_NoRetryOn400(t *testing.T) {
	var callCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"error":{"message":"bad request"}}`))
	}))
	defer ts.Close()

	c := New(ts.URL, "key", "model", "", 0, 10*time.Second)
	_, err := c.Call(context.Background(), []Message{
		{Role: "user", Content: "hi"},
	}, nil, nil)

	if err == nil {
		t.Fatal("expected error for 400, got nil")
	}
	if callCount.Load() != 1 {
		t.Errorf("call count = %d, want 1 (no retry on 400)", callCount.Load())
	}
}

func TestClient_Call_RetryExhausted(t *testing.T) {
	var callCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"error":{"message":"always rate limited"}}`))
	}))
	defer ts.Close()

	c := New(ts.URL, "key", "model", "", 0, 10*time.Second)
	_, err := c.Call(context.Background(), []Message{
		{Role: "user", Content: "hi"},
	}, nil, nil)

	if err == nil {
		t.Fatal("expected error after exhausting retries, got nil")
	}
	errStr := err.Error()
	if errStr == "" || errStr == "expected error after exhausting retries, got nil" {
		t.Error("expected non-empty error message")
	}
	// Should have tried: initial + 3 retries = 4 total
	if callCount.Load() != 4 {
		t.Errorf("call count = %d, want 4 (1 initial + 3 retries)", callCount.Load())
	}
}

func TestClient_Call_RetryOnNetworkError(t *testing.T) {
	var callCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := int(callCount.Add(1))
		if count <= 2 {
			// Simulate network error by closing the connection
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"recovered"}}]}`))
	}))
	defer ts.Close()

	c := New(ts.URL, "key", "model", "", 0, 10*time.Second)
	result, err := c.Call(context.Background(), []Message{
		{Role: "user", Content: "hi"},
	}, nil, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "recovered" {
		t.Errorf("content = %q, want %q", result.Content, "recovered")
	}
}
