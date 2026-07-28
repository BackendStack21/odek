package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// stubRetrySleep replaces the real backoff sleep with a no-op for the
// duration of the test so exhaustion paths (8 attempts, ~91s of real
// backoff) stay fast. Tests in this package don't run in parallel, so the
// package var swap is race-safe.
func stubRetrySleep(t *testing.T) {
	t.Helper()
	orig := retrySleep
	retrySleep = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { retrySleep = orig })
}

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
	stubRetrySleep(t)
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
	// The exhaustion error must name the attempt count.
	if !strings.Contains(err.Error(), "retry exhausted (8 attempts)") {
		t.Errorf("error = %q, want it to mention %q", err.Error(), "retry exhausted (8 attempts)")
	}
	// Should have tried: initial + 7 retries = 8 total
	if callCount.Load() != 8 {
		t.Errorf("call count = %d, want 8 (1 initial + 7 retries)", callCount.Load())
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

// TestClient_Call_RetryOn529ThenSuccess verifies Anthropic's 529 Overloaded
// response — the most common signal during capacity incidents — is retried
// and the call ultimately succeeds.
func TestClient_Call_RetryOn529ThenSuccess(t *testing.T) {
	stubRetrySleep(t)
	var callCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if int(callCount.Add(1)) <= 2 {
			w.WriteHeader(529)
			w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer ts.Close()

	c := New(ts.URL, "key", "model", "", 0, 10*time.Second)
	result, err := c.Call(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error after 529 retries: %v", err)
	}
	if result.Content != "ok" {
		t.Errorf("content = %q, want ok", result.Content)
	}
	if callCount.Load() != 3 {
		t.Errorf("call count = %d, want 3 (529 should be retried)", callCount.Load())
	}
}

// TestClient_Call_RetryOn500 verifies a 500 Internal Server Error — common
// from gateways and providers mid-incident — is retried rather than aborting
// the turn.
func TestClient_Call_RetryOn500(t *testing.T) {
	stubRetrySleep(t)
	var callCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if int(callCount.Add(1)) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"internal"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer ts.Close()

	c := New(ts.URL, "key", "model", "", 0, 10*time.Second)
	result, err := c.Call(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error after 500 retry: %v", err)
	}
	if result.Content != "ok" {
		t.Errorf("content = %q, want ok", result.Content)
	}
	if callCount.Load() != 2 {
		t.Errorf("call count = %d, want 2 (500 should be retried)", callCount.Load())
	}
}

// TestClient_Call_RetryOnParseError verifies a 200 with an unparseable body
// (a transient gateway/proxy artifact, e.g. an HTML error page) is retried
// through the same budget instead of aborting the turn.
func TestClient_Call_RetryOnParseError(t *testing.T) {
	stubRetrySleep(t)
	var callCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if int(callCount.Add(1)) == 1 {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<html><body>502 Bad Gateway</body></html>`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer ts.Close()

	c := New(ts.URL, "key", "model", "", 0, 10*time.Second)
	result, err := c.Call(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error after parse-error retry: %v", err)
	}
	if result.Content != "ok" {
		t.Errorf("content = %q, want ok", result.Content)
	}
	if callCount.Load() != 2 {
		t.Errorf("call count = %d, want 2 (malformed 200 should be retried)", callCount.Load())
	}
}

// TestClient_Call_RetryOnZeroChoices verifies a 200 with a valid JSON body
// but zero choices (another transient gateway artifact) is retried.
func TestClient_Call_RetryOnZeroChoices(t *testing.T) {
	stubRetrySleep(t)
	var callCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if int(callCount.Add(1)) == 1 {
			w.Write([]byte(`{"choices":[]}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer ts.Close()

	c := New(ts.URL, "key", "model", "", 0, 10*time.Second)
	result, err := c.Call(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error after zero-choices retry: %v", err)
	}
	if result.Content != "ok" {
		t.Errorf("content = %q, want ok", result.Content)
	}
	if callCount.Load() != 2 {
		t.Errorf("call count = %d, want 2 (zero-choices 200 should be retried)", callCount.Load())
	}
}

// TestClient_Call_ParseErrorExhausted verifies a persistently malformed body
// surfaces the parse error (wrapped in the attempt-count message) only after
// the full retry budget is spent.
func TestClient_Call_ParseErrorExhausted(t *testing.T) {
	stubRetrySleep(t)
	var callCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`not json`))
	}))
	defer ts.Close()

	c := New(ts.URL, "key", "model", "", 0, 10*time.Second)
	_, err := c.Call(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("expected error after exhausting retries on malformed body, got nil")
	}
	if !strings.Contains(err.Error(), "retry exhausted (8 attempts)") {
		t.Errorf("error = %q, want it to mention %q", err.Error(), "retry exhausted (8 attempts)")
	}
	if !strings.Contains(err.Error(), "parse response") {
		t.Errorf("error = %q, want it to wrap the parse error", err.Error())
	}
	if callCount.Load() != 8 {
		t.Errorf("call count = %d, want 8", callCount.Load())
	}
}

// TestJitterBackoffBounds verifies jitter stays within ±25% of the base and
// never returns a negative/zero duration for the smallest base.
func TestJitterBackoffBounds(t *testing.T) {
	base := 16 * time.Second
	for i := 0; i < 200; i++ {
		d := jitterBackoff(base)
		if d < base*3/4 || d >= base*5/4 {
			t.Fatalf("jitterBackoff(%v) = %v, outside ±25%% bounds", base, d)
		}
	}
	if d := jitterBackoff(time.Second); d < 750*time.Millisecond {
		t.Fatalf("jitterBackoff(1s) = %v, below lower bound", d)
	}
}
