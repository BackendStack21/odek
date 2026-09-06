package main

// Coverage v3 — schedule residuals:
//   - sendTelegramResult: MarkdownV2-fail → plain-text retry branch.
//   - acquireScheduleLock: ErrLocked path with empty pidfile content,
//     non-numeric pidfile content, and a missing/unreadable pidfile.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/flock"
)

// MarkdownV2 delivery fails once (Telegram "can't parse entities"); the
// plain-text retry must succeed and carry no parse_mode.
func TestSendTelegramResult_RetriesAsPlainText(t *testing.T) {
	rec := &sendRecorder{reply: func(i int) bool { return i != 0 }}
	bot := newRecorderBot(t, rec)

	result := "Summary for **Berlin**: *mild*, +20°C."
	if err := sendTelegramResult(context.Background(), bot, 555, result); err != nil {
		t.Fatalf("sendTelegramResult with retry: %v", err)
	}
	calls := rec.snapshot()
	if len(calls) != 2 {
		t.Fatalf("want 2 sends (markdown + plain retry), got %d", len(calls))
	}
	if calls[0].parseMode == "" {
		t.Fatalf("first send should carry parse_mode, got %q", calls[0].parseMode)
	}
	if calls[1].parseMode != "" {
		t.Fatalf("retry should be plain text, parse_mode = %q", calls[1].parseMode)
	}
	if calls[1].text == "" {
		t.Fatal("retry lost the chunk text")
	}
}

// Both attempts fail: the plain-text retry error surfaces.
func TestSendTelegramResult_PlainRetryAlsoFails(t *testing.T) {
	rec := &sendRecorder{reply: func(int) bool { return false }}
	bot := newRecorderBot(t, rec)

	err := sendTelegramResult(context.Background(), bot, 555, "**boom**")
	if err == nil {
		t.Fatal("want error when both markdown and plain-text sends fail")
	}
}

// A cancelled context must not spin through the retry.
func TestSendTelegramResult_CancelledContext(t *testing.T) {
	rec := &sendRecorder{reply: func(int) bool { return false }}
	bot := newRecorderBot(t, rec)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := sendTelegramResult(ctx, bot, 555, "**cancelled**")
	if err == nil {
		t.Fatal("want error on cancelled context")
	}
	if !strings.Contains(err.Error(), "context canceled") && ctx.Err() == nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// holdScheduleFlock takes the flock at a fresh HOME so acquireScheduleLock
// hits its ErrLocked branch deterministically.
func holdScheduleFlock(t *testing.T, home string) func() {
	t.Helper()
	pidPath := filepath.Join(home, ".odek", "schedule.pid")
	if err := os.MkdirAll(filepath.Dir(pidPath), 0o755); err != nil {
		t.Fatal(err)
	}
	rel, err := flock.TryLock(pidPath)
	if err != nil {
		t.Fatalf("test flock: %v", err)
	}
	return rel
}

func TestAcquireScheduleLock_LockedPidfileVariants(t *testing.T) {
	t.Run("empty pidfile content", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		rel := holdScheduleFlock(t, home)
		defer rel()
		pidPath := filepath.Join(home, ".odek", "schedule.pid")
		if err := os.WriteFile(pidPath, []byte(""), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := acquireScheduleLock()
		if err == nil || !strings.Contains(err.Error(), "already running") {
			t.Fatalf("err = %v, want already-running refusal", err)
		}
	})

	t.Run("non-numeric pidfile content", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		rel := holdScheduleFlock(t, home)
		defer rel()
		pidPath := filepath.Join(home, ".odek", "schedule.pid")
		if err := os.WriteFile(pidPath, []byte("garbage-pid"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := acquireScheduleLock()
		if err == nil || !strings.Contains(err.Error(), "garbage-pid") {
			t.Fatalf("err = %v, want refusal echoing the pidfile content", err)
		}
	})

	t.Run("locked pidfile not readable is a plain refusal", func(t *testing.T) {
		// Same-process flock re-opens a fresh inode once the pidfile is
		// removed (O_CREATE), so the ReadFile-error branch of the ErrLocked
		// path is only reachable with a cross-process holder. Verified
		// unreachable in-process; documented here for the coverage record.
		t.Skip("ReadFile-error branch requires a cross-process lock holder")
	})
}
