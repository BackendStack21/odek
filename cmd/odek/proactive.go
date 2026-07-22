package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/BackendStack21/odek/internal/llm"
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
// Extended Memory and inserts it — wrapped as untrusted content — into the
// message history immediately after the last system message. When there is
// no summary (extended memory disabled, no atoms, LLM failure) the messages
// are returned unchanged.
func injectReturnAfterBreak(ctx context.Context, mm *memory.MemoryManager, messages []llm.Message) []llm.Message {
	if mm == nil {
		return messages
	}
	rbCtx, rbCancel := context.WithTimeout(ctx, 5*time.Second)
	defer rbCancel()
	rb := mm.FormatReturnAfterBreak(rbCtx)
	if rb == "" {
		return messages
	}
	insertIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "system" {
			insertIdx = i
			break
		}
	}
	wrapped := wrapUntrusted(rbCtx, "return_after_break", rb)
	rbMsg := llm.Message{Role: "system", Content: wrapped}
	if insertIdx >= 0 {
		messages = append(messages[:insertIdx+1], append([]llm.Message{rbMsg}, messages[insertIdx+1:]...)...)
	} else {
		messages = append([]llm.Message{rbMsg}, messages...)
	}
	return messages
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
