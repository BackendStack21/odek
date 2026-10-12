package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// The scheduled-delivery path (sendTelegramResult) retries a rejected
// MarkdownV2 chunk by re-sending the SAME escaped chunk without parse_mode, so
// the user receives backslash-littered text ("1\.2 \(beta\)"). The live
// handler uses SendWithPlainFallback, which strips the markup first; the
// scheduler's sibling path was not converted.
func TestRED_TgSchedDeliveryPlainFallbackStripsEscapes(t *testing.T) {
	rec := &sendRecorder{reply: func(i int) bool { return i != 0 }} // MarkdownV2 send rejected
	bot := newRecorderBot(t, rec)

	if err := sendTelegramResult(context.Background(), bot, 7, "Release 1.2 (beta) is out!"); err != nil {
		t.Fatalf("sendTelegramResult: %v", err)
	}
	calls := rec.snapshot()
	if len(calls) != 2 {
		t.Fatalf("want MarkdownV2 send + plain retry, got %d sends", len(calls))
	}
	if calls[1].parseMode != "" {
		t.Fatalf("retry parse_mode = %q, want plain", calls[1].parseMode)
	}
	if strings.Contains(calls[1].text, `\`) {
		t.Errorf("plain-text retry still carries MarkdownV2 escapes: %q", calls[1].text)
	}
}

// /plan_resume mutates and saves the chat session; while an agent run holds the
// per-chat lock it must not touch the session (the run's end-of-turn save would
// overwrite it, or the two would race).
func TestRED_TgPlanResumeRespectsChatLock(t *testing.T) {
	const chatID = int64(88120211)
	sm, home := tgrh12Env(t, chatID)
	dir := filepath.Join(home, ".odek", "plans", fmt.Sprintf("chat%d", chatID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "p1.md"), []byte("# Plan\nstep"), 0o600); err != nil {
		t.Fatal(err)
	}
	slot := pinChat(chatID)
	slot.mu.Lock() // an agent run is in progress
	defer unpinChat(chatID, slot)

	reply := telegramPlanResume(chatID, sm)
	cs, err := sm.GetOrCreate(chatID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Messages) != 0 {
		t.Errorf("plan injected into a session whose chat lock is held (reply %q)", reply)
	}
}

// /plan_view truncation must cut on a rune boundary.
func TestRED_TgPlanViewTruncateKeepsRunes(t *testing.T) {
	content := strings.Repeat("é", 3000) // 2 bytes each
	out := telegramPlanViewTruncate(content)
	if !utf8.ValidString(out) {
		t.Errorf("truncated plan view is not valid UTF-8")
	}
	if len(out) > 4096 {
		t.Errorf("truncated plan view too long: %d", len(out))
	}
}
