package main

import (
	"context"
	"strings"
	"testing"
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
