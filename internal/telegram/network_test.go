package telegram

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// RetryWithBackoff
// ---------------------------------------------------------------------------

func TestRetryWithBackoff_SuccessFirstTry(t *testing.T) {
	var callCount int
	err := RetryWithBackoff(func() error {
		callCount++
		return nil
	}, 3, time.Microsecond)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if callCount != 1 {
		t.Errorf("expected 1 call, got %d", callCount)
	}
}

func TestRetryWithBackoff_SuccessOnRetry(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	err := RetryWithBackoff(func() error {
		mu.Lock()
		attempts++
		count := attempts
		mu.Unlock()
		if count < 3 {
			return fmt.Errorf("attempt %d failed", count)
		}
		return nil
	}, 5, time.Microsecond)
	if err != nil {
		t.Fatalf("expected nil error after retries, got: %v", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestRetryWithBackoff_FailExhausted(t *testing.T) {
	attempts := 0
	maxAtt := 4
	wantErr := errors.New("always fails")
	err := RetryWithBackoff(func() error {
		attempts++
		return wantErr
	}, maxAtt, time.Microsecond)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if attempts != maxAtt {
		t.Errorf("expected %d attempts, got %d", maxAtt, attempts)
	}
	if !strings.Contains(err.Error(), "retry exhausted") {
		t.Errorf("error message = %q, want substring %q", err, "retry exhausted")
	}
	if !strings.Contains(err.Error(), "always fails") {
		t.Errorf("error message = %q, want substring %q", err, "always fails")
	}
}

func TestRetryWithBackoff_SingleAttempt(t *testing.T) {
	// maxAttempts = 1 means no retry.
	wantErr := errors.New("single fail")
	err := RetryWithBackoff(func() error {
		return wantErr
	}, 1, time.Microsecond)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "retry exhausted") {
		t.Errorf("error message = %q, want substring %q", err, "retry exhausted")
	}
}

// ---------------------------------------------------------------------------
// NewFallbackTransport
// ---------------------------------------------------------------------------

func TestNewFallbackTransport(t *testing.T) {
	ft := NewFallbackTransport([]string{"https://fallback1.example.com", "https://fallback2.example.com"})
	if ft == nil {
		t.Fatal("NewFallbackTransport returned nil")
	}
	if ft.PrimaryURL != "https://api.telegram.org" {
		t.Errorf("PrimaryURL = %q, want %q", ft.PrimaryURL, "https://api.telegram.org")
	}
	if len(ft.FallbackURLs) != 2 {
		t.Errorf("len(FallbackURLs) = %d, want 2", len(ft.FallbackURLs))
	}
	if ft.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v, want 30s", ft.Timeout)
	}
	if ft.Client == nil {
		t.Fatal("Client is nil")
	}
	if ft.Client.Timeout != 30*time.Second {
		t.Errorf("Client.Timeout = %v, want 30s", ft.Client.Timeout)
	}
}

func TestNewFallbackTransport_EmptyFallbacks(t *testing.T) {
	ft := NewFallbackTransport(nil)
	if ft == nil {
		t.Fatal("NewFallbackTransport returned nil")
	}
	if ft.PrimaryURL != "https://api.telegram.org" {
		t.Errorf("PrimaryURL = %q, want %q", ft.PrimaryURL, "https://api.telegram.org")
	}
	if len(ft.FallbackURLs) != 0 {
		t.Errorf("len(FallbackURLs) = %d, want 0", len(ft.FallbackURLs))
	}
	// allURLs should return just the primary.
	urls := ft.allURLs()
	if len(urls) != 1 || urls[0] != "https://api.telegram.org" {
		t.Errorf("allURLs() = %v, want [https://api.telegram.org]", urls)
	}
}

// ---------------------------------------------------------------------------
// TestEndpoints — running server
// ---------------------------------------------------------------------------

func TestEndpoints_RunningServer(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/getMe") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"Test"}}`))
	}))
	defer ts.Close()

	ft := NewFallbackTransport(nil)
	ft.PrimaryURL = ts.URL

	results := ft.TestEndpoints()
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d: %v", len(results), results)
	}
	status, ok := results[ts.URL]
	if !ok {
		t.Fatalf("missing result for URL %q", ts.URL)
	}
	if status != "ok" {
		t.Errorf("status = %q, want %q", status, "ok")
	}
}

func TestEndpoints_RunningServerWithFallback(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer primary.Close()

	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer fallback.Close()

	ft := NewFallbackTransport([]string{fallback.URL})
	ft.PrimaryURL = primary.URL

	results := ft.TestEndpoints()
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d: %v", len(results), results)
	}
	if results[primary.URL] != "ok" {
		t.Errorf("primary status = %q, want %q", results[primary.URL], "ok")
	}
	if results[fallback.URL] != "ok" {
		t.Errorf("fallback status = %q, want %q", results[fallback.URL], "ok")
	}
}

// TestEndpoints_StoppedServer tests that a closed server produces an error status.
func TestEndpoints_StoppedServer(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not receive requests after Close")
	}))
	ts.Close()

	ft := NewFallbackTransport(nil)
	ft.PrimaryURL = ts.URL

	results := ft.TestEndpoints()
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d: %v", len(results), results)
	}
	status := results[ts.URL]
	if status == "ok" {
		t.Fatal("expected error status for closed server, got 'ok'")
	}
	if !strings.HasPrefix(status, "error:") {
		t.Errorf("status = %q, want prefix 'error:'", status)
	}
}

func TestEndpoints_MixedOneUpOneDown(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer up.Close()

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	down.Close() // server is dead

	ft := NewFallbackTransport([]string{down.URL, up.URL})
	ft.PrimaryURL = down.URL // test the down one first

	results := ft.TestEndpoints()
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d: %v", len(results), results)
	}
	if results[up.URL] != "ok" {
		t.Errorf("up server status = %q, want %q", results[up.URL], "ok")
	}
	// Down server may or may not report as error (port may be reused).
	// The important thing is both endpoints were checked.
	_ = results[down.URL]
}

func TestEndpoints_NonOKStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return a non-200 status such as 403.
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"ok":false}`))
	}))
	defer ts.Close()

	ft := NewFallbackTransport(nil)
	ft.PrimaryURL = ts.URL

	results := ft.TestEndpoints()
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d: %v", len(results), results)
	}
	status := results[ts.URL]
	if strings.HasPrefix(status, "ok") {
		t.Fatalf("expected error for non-200 status, got %q", status)
	}
	if !strings.Contains(status, "status 403") {
		t.Errorf("status = %q, want substring %q", status, "status 403")
	}
}

// ---------------------------------------------------------------------------
// FallbackTransport — RoundTrip / Do
// ---------------------------------------------------------------------------

func TestFallbackTransport_RoundTrip_PrimaryWorks(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("primary response"))
	}))
	defer primary.Close()

	ft := NewFallbackTransport(nil)
	ft.PrimaryURL = primary.URL

	req, err := http.NewRequest(http.MethodGet, primary.URL+"/getMe", nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	resp, err := ft.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "primary response" {
		t.Errorf("body = %q, want %q", string(body), "primary response")
	}
}

func TestFallbackTransport_RoundTrip_FallbackWorks(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	primary.Close() // primary is dead

	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("fallback response"))
	}))
	defer fallback.Close()

	ft := NewFallbackTransport([]string{fallback.URL})
	ft.PrimaryURL = primary.URL

	req, err := http.NewRequest(http.MethodGet, primary.URL+"/getMe", nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	resp, err := ft.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "fallback response" {
		t.Errorf("body = %q, want %q", string(body), "fallback response")
	}
}

func TestFallbackTransport_RoundTrip_AllFail(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	primary.Close()

	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	fallback.Close()

	ft := NewFallbackTransport([]string{fallback.URL})
	ft.PrimaryURL = primary.URL

	req, err := http.NewRequest(http.MethodGet, primary.URL+"/getMe", nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	_, err = ft.RoundTrip(req)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "all endpoints failed") {
		t.Errorf("error = %q, want substring %q", err, "all endpoints failed")
	}
}

func TestFallbackTransport_Do_UsesTryURLs(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("do works"))
	}))
	defer ts.Close()

	ft := NewFallbackTransport(nil)
	ft.PrimaryURL = ts.URL

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/sendMessage", nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	resp, err := ft.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "do works" {
		t.Errorf("body = %q, want %q", string(body), "do works")
	}
}

func TestFallbackTransport_QueryParametersPreserved(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "foo=bar&baz=1" {
			t.Errorf("RawQuery = %q, want %q", r.URL.RawQuery, "foo=bar&baz=1")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer primary.Close()

	ft := NewFallbackTransport(nil)
	ft.PrimaryURL = primary.URL

	req, err := http.NewRequest(http.MethodGet, primary.URL+"/test?foo=bar&baz=1", nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	resp, err := ft.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()
}

// ---------------------------------------------------------------------------
// FallbackTransport — invalid base URL handling
// ---------------------------------------------------------------------------

func TestFallbackTransport_InvalidPrimaryURL(t *testing.T) {
	ft := NewFallbackTransport(nil)
	ft.PrimaryURL = "://invalid-url"

	req, err := http.NewRequest(http.MethodGet, "http://example.com/test", nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	_, err = ft.RoundTrip(req)
	if err == nil {
		t.Fatal("expected error for invalid primary URL, got nil")
	}
	if !strings.Contains(err.Error(), "invalid base URL") {
		t.Errorf("error = %q, want substring %q", err, "invalid base URL")
	}
}

func TestFallbackTransport_InvalidFallbackURL(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("primary ok"))
	}))
	defer primary.Close()

	ft := NewFallbackTransport([]string{":bad"})
	ft.PrimaryURL = primary.URL

	req, err := http.NewRequest(http.MethodGet, primary.URL+"/test", nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	resp, err := ft.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip should fall through to primary: %v", err)
	}
	resp.Body.Close()
}

// ---------------------------------------------------------------------------
// FallbackTransport — TestEndpoints with invalid URLs
// ---------------------------------------------------------------------------

func TestEndpoints_InvalidURL(t *testing.T) {
	ft := NewFallbackTransport(nil)
	ft.PrimaryURL = "://bad"

	results := ft.TestEndpoints()
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	status := results["://bad"]
	if !strings.Contains(status, "error: invalid URL") {
		t.Errorf("status = %q, want substring %q", status, "error: invalid URL")
	}
}

// ---------------------------------------------------------------------------
// FallbackTransport — WrapBot
// ---------------------------------------------------------------------------

func TestFallbackTransport_WrapBot(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The bot sends POST to /sendMessage.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"text":"hi"}}`))
	}))
	defer ts.Close()

	bot := NewBot("testtoken")
	// Point the bot at our primary URL.
	ft := NewFallbackTransport(nil)
	ft.PrimaryURL = ts.URL

	// WrapBot must replace bot.Client with the transport's client.
	wrapped := ft.WrapBot(bot)
	if wrapped != bot {
		t.Error("WrapBot should return the same bot pointer")
	}
	if bot.Client == nil {
		t.Fatal("bot.Client is nil after WrapBot")
	}
	if bot.Client.Transport == nil {
		t.Fatal("bot.Client.Transport is nil after WrapBot")
	}
	// The Transport should be the FallbackTransport.
	_, ok := bot.Client.Transport.(*FallbackTransport)
	if !ok {
		t.Fatalf("bot.Client.Transport is %T, want *FallbackTransport", bot.Client.Transport)
	}
}

func TestFallbackTransport_WrapBot_SendsRequest(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"text":"hi"}}`))
	}))
	defer ts.Close()

	bot := NewBot("testtoken")
	ft := NewFallbackTransport(nil)
	ft.PrimaryURL = ts.URL
	ft.WrapBot(bot)

	// Use the bot normally — the request should go through the transport.
	msg, err := bot.SendMessage(123, "hello", nil)
	if err != nil {
		t.Fatalf("SendMessage after WrapBot: %v", err)
	}
	if msg == nil || msg.ID != 1 {
		t.Errorf("msg = %+v, want message_id=1", msg)
	}
	if !strings.HasSuffix(gotPath, "/sendMessage") {
		t.Errorf("got path = %q, want suffix %q", gotPath, "/sendMessage")
	}
}

// ---------------------------------------------------------------------------
// allURLs (unexported, tested indirectly via other tests)
// ---------------------------------------------------------------------------

func TestNewFallbackTransport_allURLs(t *testing.T) {
	ft := NewFallbackTransport([]string{"fb1", "fb2"})
	urls := ft.allURLs()
	want := []string{"https://api.telegram.org", "fb1", "fb2"}
	if len(urls) != len(want) {
		t.Fatalf("len = %d, want %d: %v", len(urls), len(want), urls)
	}
	for i := range want {
		if urls[i] != want[i] {
			t.Errorf("urls[%d] = %q, want %q", i, urls[i], want[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Compile-time check: ensure FallbackTransport implements http.RoundTripper
// (already in network.go, but verify in tests too for completeness)
// ---------------------------------------------------------------------------

func TestFallbackTransport_ImplementsRoundTripper(t *testing.T) {
	var _ http.RoundTripper = (*FallbackTransport)(nil)
}
