package telegram

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	cases := map[string]time.Duration{
		"":            0,
		"3":           3 * time.Second,
		"2.5":         2500 * time.Millisecond,
		"0":           0,
		"-7":          0,
		"abc":         0,
		"1e300":       maxRetryBackoff,
		"99999999999": maxRetryBackoff,
	}
	for in, want := range cases {
		if got := parseRetryAfter(json.Number(in)); got != want {
			t.Errorf("parseRetryAfter(%q)=%v want %v", in, got, want)
		}
	}
}

func TestRetryBackoff(t *testing.T) {
	cases := []struct {
		attempt    int
		retryAfter time.Duration
		want       time.Duration
	}{
		{1, 0, time.Second},
		{4, 0, 8 * time.Second},
		{4, time.Second, time.Second},
		{1, 3 * time.Second, 3 * time.Second},
		{1, time.Hour, maxRetryBackoff},
	}
	for _, c := range cases {
		if got := retryBackoff(c.attempt, c.retryAfter); got != c.want {
			t.Errorf("retryBackoff(%d,%v)=%v want %v", c.attempt, c.retryAfter, got, c.want)
		}
	}
}

// A 429 carrying parameters.retry_after must be retried after that wait, not
// after the exponential schedule (1s+2s+4s = 7s for three 429s).
func TestRED_Telegram_RetryAfterHonoured(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) <= 3 {
			_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 1","parameters":{"retry_after":1}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL
	start := time.Now()
	if err := bot.doJSON("editMessageText", map[string]any{"a": 1}, nil); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("three retry_after=1 responses took %v, want about 3s", elapsed)
	}
	if calls.Load() != 4 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

// Telegram documents retry_after as an integer, but the decode must not fail
// the whole response on a fractional value: every API path retries it.
func TestRED_Telegram_FractionalRetryAfterStillRetried(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) <= 2 {
			_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":0.2}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer ts.Close()
	bot := NewBot("x")
	bot.BaseURL = ts.URL
	start := time.Now()
	if err := bot.doJSON("sendMessage", map[string]any{"a": 1}, nil); err != nil {
		t.Fatalf("fractional retry_after broke the retry: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("two retry_after=0.2 responses took %v, want well under the 1s+2s schedule", elapsed)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls=%d, want 3", calls.Load())
	}
}
