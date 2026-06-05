package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/BackendStack21/odek/internal/telegram"
)

// capturedSend records one sendMessage request the test server received.
type capturedSend struct {
	text      string
	parseMode string
}

// sendRecorder is a Telegram mock server that records every sendMessage call
// and lets the test decide, per call index, whether to succeed or fail.
type sendRecorder struct {
	mu    sync.Mutex
	calls []capturedSend
	// reply is consulted for each call (0-based index). Returning ok=false
	// makes the server respond with a 400 "can't parse entities" error, which
	// the Bot treats as a non-retryable client error.
	reply func(index int) (ok bool)
}

func (s *sendRecorder) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var payload struct {
		Text      string `json:"text"`
		ParseMode string `json:"parse_mode"`
	}
	_ = json.Unmarshal(body, &payload)

	s.mu.Lock()
	idx := len(s.calls)
	s.calls = append(s.calls, capturedSend{text: payload.Text, parseMode: payload.ParseMode})
	s.mu.Unlock()

	ok := true
	if s.reply != nil {
		ok = s.reply(idx)
	}

	w.Header().Set("Content-Type", "application/json")
	if !ok {
		// Telegram always returns HTTP 200 even on API errors.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":          false,
			"error_code":  400,
			"description": "Bad Request: can't parse entities",
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":     true,
		"result": map[string]any{"message_id": 1, "chat": map[string]any{"id": 1}},
	})
}

func (s *sendRecorder) snapshot() []capturedSend {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]capturedSend, len(s.calls))
	copy(out, s.calls)
	return out
}

// newRecorderBot spins up a mock server and a Bot pointed at it.
func newRecorderBot(t *testing.T, rec *sendRecorder) *telegram.Bot {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(rec.handler))
	t.Cleanup(ts.Close)
	bot := telegram.NewBot("testtoken")
	bot.BaseURL = ts.URL
	return bot
}

// A scheduled task's result is odek markdown; sendTelegramResult must convert
// it to Telegram MarkdownV2 (italic `*x*` → `_x_`, reserved chars escaped) and
// set parse_mode, instead of shipping the raw text as plain.
func TestSendTelegramResult_ConvertsAndSetsParseMode(t *testing.T) {
	rec := &sendRecorder{}
	bot := newRecorderBot(t, rec)

	result := "The temperature in **Berlin** is *mild* at +20°C."
	if err := sendTelegramResult(context.Background(), bot, 555, result); err != nil {
		t.Fatalf("sendTelegramResult: %v", err)
	}

	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("want 1 send, got %d", len(calls))
	}
	got := calls[0]
	if got.parseMode != telegram.ParseModeMarkdownV2 {
		t.Errorf("parse_mode = %q, want %q", got.parseMode, telegram.ParseModeMarkdownV2)
	}
	// Italic single-asterisk is converted to underscore form.
	if !strings.Contains(got.text, "_mild_") {
		t.Errorf("italic not converted, text = %q", got.text)
	}
	// The reserved '+' is escaped so MarkdownV2 won't reject the message.
	if !strings.Contains(got.text, `\+20`) {
		t.Errorf("reserved '+' not escaped, text = %q", got.text)
	}
	// The text must differ from the raw input (i.e. it was actually formatted).
	if got.text == result {
		t.Errorf("text was sent unformatted: %q", got.text)
	}
}

// If Telegram rejects the MarkdownV2 formatting, the message is re-sent as plain
// text (no parse_mode) so the user still receives the content.
func TestSendTelegramResult_FallsBackToPlainText(t *testing.T) {
	rec := &sendRecorder{
		reply: func(index int) bool { return index != 0 }, // first (MarkdownV2) fails
	}
	bot := newRecorderBot(t, rec)

	if err := sendTelegramResult(context.Background(), bot, 7, "**hi** there"); err != nil {
		t.Fatalf("sendTelegramResult should recover via plain text: %v", err)
	}

	calls := rec.snapshot()
	if len(calls) != 2 {
		t.Fatalf("want 2 sends (markdown + plain retry), got %d", len(calls))
	}
	if calls[0].parseMode != telegram.ParseModeMarkdownV2 {
		t.Errorf("first send parse_mode = %q, want MarkdownV2", calls[0].parseMode)
	}
	if calls[1].parseMode != "" {
		t.Errorf("retry should be plain text, got parse_mode = %q", calls[1].parseMode)
	}
}

// When both the MarkdownV2 send and the plain-text retry fail, the error is
// surfaced to the scheduler (which records it as a failed run).
func TestSendTelegramResult_ReturnsErrorWhenBothFail(t *testing.T) {
	rec := &sendRecorder{
		reply: func(int) bool { return false }, // every send fails
	}
	bot := newRecorderBot(t, rec)

	err := sendTelegramResult(context.Background(), bot, 7, "**boom**")
	if err == nil {
		t.Fatal("expected an error when both sends fail")
	}
	if n := len(rec.snapshot()); n != 2 {
		t.Errorf("want 2 attempts, got %d", n)
	}
}

// A result larger than Telegram's 4096-byte limit is split into multiple chunks,
// each sent as its own message.
func TestSendTelegramResult_ChunksLargeResult(t *testing.T) {
	rec := &sendRecorder{}
	bot := newRecorderBot(t, rec)

	// Two paragraphs, each ~3000 bytes, separated by a blank line so they split
	// at the paragraph boundary into two chunks under the 4096 limit.
	para := strings.Repeat("word ", 600)
	result := para + "\n\n" + para
	if err := sendTelegramResult(context.Background(), bot, 1, result); err != nil {
		t.Fatalf("sendTelegramResult: %v", err)
	}
	if n := len(rec.snapshot()); n != 2 {
		t.Fatalf("want 2 chunked sends, got %d", n)
	}
}

// Empty/whitespace-only chunks are skipped (no empty messages are sent).
func TestSendTelegramResult_SkipsEmpty(t *testing.T) {
	rec := &sendRecorder{}
	bot := newRecorderBot(t, rec)

	if err := sendTelegramResult(context.Background(), bot, 1, ""); err != nil {
		t.Fatalf("sendTelegramResult: %v", err)
	}
	if n := len(rec.snapshot()); n != 0 {
		t.Errorf("empty result should send nothing, got %d sends", n)
	}
}

// If the context is cancelled, a failed MarkdownV2 send must NOT trigger a
// plain-text retry — the scheduler is shutting down and the error propagates.
func TestSendTelegramResult_NoRetryAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rec := &sendRecorder{
		reply: func(index int) bool {
			if index == 0 {
				cancel() // cancel before the fallback decision
				return false
			}
			return true
		},
	}
	bot := newRecorderBot(t, rec)

	err := sendTelegramResult(ctx, bot, 7, "**hi**")
	if err == nil {
		t.Fatal("expected an error when the context is cancelled")
	}
	if n := len(rec.snapshot()); n != 1 {
		t.Errorf("cancelled context must skip the plain-text retry, got %d sends", n)
	}
}
