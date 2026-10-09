package loop

import (
	"strings"
	"testing"
)

// The candidate answer is rendered into the verifier prompt; it must be bounded
// like every other evidence field.
func TestRED_VerifyPromptAnswerUnbounded(t *testing.T) {
	p := verifyPrompt("t", "p", "tr", strings.Repeat("x", 5<<20))
	if len(p) > 1<<20 {
		t.Fatalf("verifier prompt is %d bytes for a 5MiB answer", len(p))
	}
}

// A short answer is rendered whole, and an oversized one keeps its head and
// tail with an explicit cut marker.
func TestVerifyPromptAnswerBoundKeepsHeadAndTail(t *testing.T) {
	short := verifyPrompt("t", "p", "tr", "short answer")
	if !strings.Contains(short, "short answer") {
		t.Fatalf("short answer missing from prompt")
	}
	long := "HEAD" + strings.Repeat("x", 1<<20) + "TAIL"
	p := verifyPrompt("t", "p", "tr", long)
	if !strings.Contains(p, "HEAD") || !strings.Contains(p, "TAIL") {
		t.Fatalf("bounded answer lost its head or tail")
	}
	if !strings.Contains(p, "omitted") {
		t.Fatalf("bounded answer does not mark the cut")
	}
}
