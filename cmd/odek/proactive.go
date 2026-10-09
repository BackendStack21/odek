package main

import (
	"context"
	"fmt"
	"github.com/BackendStack21/odek/internal/session"
	"io"
	"strings"
	"time"

	"github.com/BackendStack21/odek/internal/memory"
)

// ── Proactive Engagement (presentation layer) ─────────────────────────
//
// Shared presentation helpers for the proactive engagement feature:
// return-after-break injection on session resume, and follow-up
// suggestions printed after a completed turn. Both are presentation-only —
// nothing here is appended to the agent response or persisted into session
// transcripts.

// injectReturnAfterBreak loads a concise "where you left off" summary from
// Extended Memory and appends it — wrapped as untrusted content — to the
// resumed history as a user-role message named session.ReturnAfterBreakName.
// The summary is derived from memory, not the principal or the runtime, so it
// never takes the system role; like bg-notice/bg-wake messages it is flagged
// by name, and every consumer that keys on the principal's input (loop hooks,
// verifier, transcript turns, session turn counting and protected head)
// skips it. Appending, rather than inserting after the system head, keeps the
// original task in the protected head. The session store drops the message
// on save, so it lives for the resumed run only and never accumulates. When there is no summary (extended
// memory disabled, no atoms, LLM failure) the messages are returned
// unchanged.
func injectReturnAfterBreak(ctx context.Context, mm *memory.MemoryManager, messages []session.Message) []session.Message {
	if mm == nil {
		return messages
	}
	rbCtx, rbCancel := context.WithTimeout(ctx, 5*time.Second)
	defer rbCancel()
	rb := mm.FormatReturnAfterBreak(rbCtx)
	if rb == "" {
		return messages
	}
	wrapped := wrapUntrusted(rbCtx, "return_after_break", rb)
	return append(messages, session.Message{Role: "user", Name: session.ReturnAfterBreakName, Content: wrapped})
}

// resumeTaskPreview renders the first-message preview for the Telegram
// /resume confirmation, capped at 80 runes-ish. An empty history renders
// as "" — the handler then reports "(empty)" instead of panicking on
// messages[0] (audit 2026-08: a persisted zero-message session crashed the
// update loop on /resume).
func resumeTaskPreview(messages []session.Message) string {
	if len(messages) == 0 {
		return ""
	}
	p := messages[0].Content
	if len(p) > 80 {
		p = p[:80] + "…"
	}
	return p
}

// followUpSuggester is the subset of *memory.MemoryManager used by
// printFollowUpSuggestions; an interface so tests can substitute a fake.
type followUpSuggester interface {
	FollowUpSuggestions() []string
}

// maxFollowUpSuggestions caps the printed suggestion block.
const maxFollowUpSuggestions = 3

// printFollowUpSuggestions prints a compact block of follow-up suggestions
// after a completed turn. Presentation-only: the block is written to w
// (never appended to the agent response), and suppressed only in verbose and
// off modes, which stay machine-clean. Anything else — including empty,
// unknown, or legacy mode strings — behaves like engaging, matching the
// loop's own default-engaging interpretation (internal/loop).
func printFollowUpSuggestions(w io.Writer, mm followUpSuggester, interactionMode string) {
	if mm == nil {
		return
	}
	if interactionMode == "verbose" || interactionMode == "off" {
		return
	}
	fmt.Fprint(w, formatFollowUpSuggestions(mm.FollowUpSuggestions()))
}

// formatFollowUpSuggestions renders the suggestion block, or "" when there
// are no suggestions. At most maxFollowUpSuggestions lines are included.
func formatFollowUpSuggestions(suggestions []string) string {
	if len(suggestions) == 0 {
		return ""
	}
	if len(suggestions) > maxFollowUpSuggestions {
		suggestions = suggestions[:maxFollowUpSuggestions]
	}
	var b strings.Builder
	b.WriteString("── You might also want to ──\n")
	for _, s := range suggestions {
		fmt.Fprintf(&b, "• %s\n", s)
	}
	return b.String()
}
