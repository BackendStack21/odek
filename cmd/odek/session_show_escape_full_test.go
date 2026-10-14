package main

import (
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
)

// session show prints whole messages: the terminal escaper used there must
// not apply the approval-prompt length cap.
func TestSessionShowEscaperKeepsFullLength(t *testing.T) {
	body := strings.Repeat("x", danger.DisplayMaxBytes+5000)
	if got := danger.EscapeTerminalText(body); got != body {
		t.Fatalf("transcript text was shortened: len %d -> %d", len(body), len(got))
	}
}
