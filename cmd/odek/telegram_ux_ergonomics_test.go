package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/llmclient"
	"github.com/BackendStack21/odek/internal/telegram"
)

// ── /stats daily-budget surfacing ─────────────────────────────────────────
// Gap: /stats showed message counts only; users had no visibility of daily
// token consumption against the configured budget.

func TestFormatStats_ShowsDailyTokens(t *testing.T) {
	cs := &telegram.ChatSession{
		ChatID:     1,
		SessionID:  "s1",
		CreatedAt:  time.Now().Add(-time.Hour),
		LastActive: time.Now(),
		TurnCount:  3,
	}
	out := formatStats(cs, 4200, 10000)
	if !strings.Contains(out, "Daily tokens") {
		t.Fatalf("stats should show daily token usage, got:\n%s", out)
	}
	if !strings.Contains(out, "4,200") || !strings.Contains(out, "10,000") {
		t.Fatalf("stats should show used/limit numbers, got:\n%s", out)
	}
	if !strings.Contains(out, "42%") {
		t.Fatalf("stats should show a percentage, got:\n%s", out)
	}
}

func TestFormatStats_UnlimitedBudget(t *testing.T) {
	cs := &telegram.ChatSession{CreatedAt: time.Now(), LastActive: time.Now()}
	out := formatStats(cs, 500, 0)
	if !strings.Contains(out, "unlimited") {
		t.Fatalf("stats with limit 0 should say unlimited, got:\n%s", out)
	}
}

// ── Run-error ergonomics ──────────────────────────────────────────────────
// Gap: provider failures surfaced raw error strings ("Agent error: ..."),
// giving rate-limited or timed-out users no guidance on what happened or
// what to do next.

func TestFriendlyRunError_RateLimit(t *testing.T) {
	err := &llmclient.RateLimitError{}
	err.Attempts = 3
	out := friendlyRunError(err)
	if !strings.Contains(out, "rate-limited") {
		t.Fatalf("rate-limit error should be explained as rate-limited, got:\n%s", out)
	}
	if !strings.Contains(out, "3") {
		t.Fatalf("rate-limit message should mention attempts, got:\n%s", out)
	}
	if !strings.Contains(out, "/stats") {
		t.Fatalf("rate-limit message should point to /stats, got:\n%s", out)
	}
}

func TestFriendlyRunError_Timeout(t *testing.T) {
	out := friendlyRunError(context.DeadlineExceeded)
	if !strings.Contains(out, "timed out") {
		t.Fatalf("deadline error should be explained as a timeout, got:\n%s", out)
	}
	if !strings.Contains(out, "session is intact") {
		t.Fatalf("timeout message must reassure about session state, got:\n%s", out)
	}
}

func TestFriendlyRunError_Generic(t *testing.T) {
	out := friendlyRunError(errors.New("boom"))
	if !strings.Contains(out, "Agent error") || !strings.Contains(out, "boom") {
		t.Fatalf("generic errors keep the raw message, got:\n%s", out)
	}
}

func TestFriendlyRunError_WrappedTimeout(t *testing.T) {
	// Providers wrap deadlines in transport errors; the mapping must
	// traverse the wrap chain, not just match the bare sentinel.
	wrapped := fmt.Errorf("do request: %w", fmt.Errorf("post: %w", context.DeadlineExceeded))
	out := friendlyRunError(wrapped)
	if !strings.Contains(out, "timed out") {
		t.Fatalf("wrapped deadline should map to the timeout message, got:\n%s", out)
	}
}

func TestFormatThousands(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1,000"},
		{4200, "4,200"},
		{1234567, "1,234,567"},
		{-1234, "-1,234"},
	}
	for _, c := range cases {
		if got := formatThousands(c.in); got != c.want {
			t.Errorf("formatThousands(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatStats_OverBudget(t *testing.T) {
	cs := &telegram.ChatSession{CreatedAt: time.Now(), LastActive: time.Now()}
	out := formatStats(cs, 12000, 10000)
	if !strings.Contains(out, "120%") {
		t.Fatalf("over-budget usage should show >100%%, got:\n%s", out)
	}
}
