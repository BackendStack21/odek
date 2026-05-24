package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestGetUpdates_ContextCancelDuringRetry verifies that the retry
// loop in doJSON respects context cancellation between retry attempts.
func TestGetUpdates_ContextCancelDuringRetry(t *testing.T) {
	var callCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests"}`))
	}))
	defer ts.Close()

	bot := NewBot("test:token")
	bot.BaseURL = ts.URL
	bot.Client = &http.Client{Timeout: 30 * time.Second}

	// Context with very short deadline so only 1-2 retries happen.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := bot.GetUpdatesContext(ctx, 0, 30)
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}

	count := callCount.Load()
	if count >= 5 {
		t.Errorf("expected retries to be cut short by context, got %d calls", count)
	}
	t.Logf("retries cut short: %d/5 attempts before cancellation", count)
}

// TestGetUpdates_NormalOperation verifies that GetUpdates works
// normally when context is not cancelled.
func TestGetUpdates_NormalOperation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true,"result":[{"update_id":1,"message":{"message_id":1,"chat":{"id":1},"text":"hi"}}]}`))
	}))
	defer ts.Close()

	bot := NewBot("test:token")
	bot.BaseURL = ts.URL
	bot.Client = &http.Client{Timeout: 30 * time.Second}

	ctx := context.Background()
	updates, err := bot.GetUpdatesContext(ctx, 0, 30)
	if err != nil {
		t.Fatalf("GetUpdatesContext failed: %v", err)
	}
	if len(updates) != 1 {
		t.Errorf("got %d updates, want 1", len(updates))
	}
	if updates[0].ID != 1 {
		t.Errorf("update ID = %d, want 1", updates[0].ID)
	}
}

// TestPoller_ContextCancelDuringRetry verifies the poller respects
// context cancellation during the retry backoff between polls.
func TestPoller_ContextCancelDuringRetry(t *testing.T) {
	var callCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		// Return errors to trigger backoff in the poller.
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests"}`))
	}))
	defer ts.Close()

	bot := NewBot("test:token")
	bot.BaseURL = ts.URL
	bot.Client = &http.Client{Timeout: 10 * time.Second}

	poller := NewPoller(bot)
	poller.Timeout = 5
	poller.Interval = 1 * time.Second
	updates := make(chan Update, 10)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := poller.Start(ctx, updates)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}

	// Should return quickly (before the poller's exponential backoff completes).
	if elapsed > 5*time.Second {
		t.Errorf("poller.Start took %v, expected quick cancellation", elapsed)
	}

	// Should NOT have made many calls because backoff is cut short.
	// With 300ms deadline and 1s base interval, exponential backoff
	// means only the first call might complete before cancellation.
	count := callCount.Load()
	t.Logf("poller made %d calls in %v before cancellation", count, elapsed)
}

// TestPoller_ShutdownOnContext verifies the poller stops when
// its context is cancelled after a successful long-poll.
func TestPoller_ShutdownOnContext(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wg.Done()
		// Write an empty update list to simulate a timeout (no updates).
		w.Write([]byte(`{"ok":true,"result":[]}`))
	}))
	defer ts.Close()

	bot := NewBot("test:token")
	bot.BaseURL = ts.URL
	bot.Client = &http.Client{Timeout: 10 * time.Second}

	poller := NewPoller(bot)
	poller.Timeout = 5
	poller.Interval = 1 * time.Second
	updates := make(chan Update, 10)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		wg.Wait()
		// Cancel after first poll completes.
		cancel()
	}()

	err := poller.Start(ctx, updates)
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
