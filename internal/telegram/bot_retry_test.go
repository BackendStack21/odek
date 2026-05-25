package telegram

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ── Red test: doJSON retry ignores cancellation ──────────────────────────

// TestBot_doJSON_RetryRespectsStop tests that doJSON's retry backoff
// respects the Bot's stop signal instead of blocking with time.Sleep.
// BUG: time.Sleep(backoff) ignores context/stop, delaying shutdown by
// up to 8s during retry windows.
func TestBot_doJSON_RetryRespectsStop(t *testing.T) {
	var attemptCount int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attemptCount++
		// Always return 429 (rate limit) so doJSON keeps retrying.
		failResponse(w, 429, "Too Many Requests: retry after 1")
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	// Signal stop after a short delay so the retry loop starts.
	go func() {
		time.Sleep(50 * time.Millisecond)
		bot.StopRetries()
	}()

	// Call a method that uses doJSON — should NOT block for 15s.
	start := time.Now()
	err := bot.EditMessageText(999, 1, "text", nil)
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Errorf("doJSON blocked for %v after StopRetries, expected quick abort", elapsed)
	}
	if err == nil {
		t.Fatal("expected error after StopRetries, got nil")
	}
	if !strings.Contains(err.Error(), "cancelled") {
		t.Logf("error = %v (expected cancelled)", err)
	}
	t.Logf("attempts = %d, elapsed = %v", attemptCount, elapsed)
}
