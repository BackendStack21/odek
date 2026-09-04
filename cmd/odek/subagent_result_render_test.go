package main

// TDD RED phase — sub-agent result artifacts, M0 (SUBAGENT_RESULT_ARTIFACTS_PLAN.md).
//
// Two fixes under test:
//  1. extractSummary hard-cut the child's final answer at 500 runes — the
//     primary data-loss point. The headline cap is now 2048 runes
//     (subagentHeadlineMaxRunes); the bulk-report channel arrives in M1.
//  2. The parent collated the child's RAW JSON envelope into its context.
//     formatTaskResult renders parsed fields (status/headline/files/denials)
//     as compact text and falls back to the raw payload only when the JSON
//     cannot be parsed.

import (
	"encoding/json"
	"github.com/BackendStack21/odek/internal/session"
	"strings"
	"testing"
)

func TestExtractSummary_HeadlineCap(t *testing.T) {
	long := strings.Repeat("a", 3000)
	msgs := []session.Message{
		{Role: "user", Content: "do the thing"},
		{Role: "assistant", Content: long},
	}
	got := extractSummary(msgs)
	if n := len([]rune(got)); n != subagentHeadlineMaxRunes+1 { // cap + ellipsis
		t.Errorf("headline cap: got %d runes, want %d (cap + ellipsis)", n, subagentHeadlineMaxRunes+1)
	}
	if !strings.HasPrefix(got, long[:subagentHeadlineMaxRunes]) {
		t.Error("headline must preserve the answer prefix")
	}
}

func TestExtractSummary_ShortAnswerUnchanged(t *testing.T) {
	msgs := []session.Message{{Role: "assistant", Content: "done: 3 files"}}
	if got := extractSummary(msgs); got != "done: 3 files" {
		t.Errorf("short answers must pass through unchanged, got %q", got)
	}
}

func TestFormatTaskResult_ParsedRender(t *testing.T) {
	raw := `{"status":"success","summary":"Built the auth middleware with tests.","files_changed":["cmd/auth.go","cmd/auth_test.go"],"tokens_used":800,"iterations":3,"duration_seconds":12.5}`
	got := formatTaskResult(raw)

	for _, want := range []string{
		"status: success",
		"3 iterations",
		"12.5s",
		"~800 tokens",
		"summary: Built the auth middleware with tests.",
		"files changed: cmd/auth.go, cmd/auth_test.go",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("parsed render missing %q\n--- render ---\n%s", want, got)
		}
	}
	for _, banned := range []string{`"files_changed"`, `"status":`} {
		if strings.Contains(got, banned) {
			t.Errorf("parsed render leaked raw JSON %q:\n%s", banned, got)
		}
	}
}

func TestFormatTaskResult_PartialAndDenials(t *testing.T) {
	raw := `{"status":"partial","partial_reason":"time_budget","summary":"Half done.","denials":[{"tool":"shell","class":"system_write","reason":"untrusted task"}],"denials_total":4}`
	got := formatTaskResult(raw)
	for _, want := range []string{
		"status: partial (time_budget)",
		"summary: Half done.",
		"denials (1 of 4):",
		"shell/system_write",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("render missing %q\n--- render ---\n%s", want, got)
		}
	}
}

func TestFormatTaskResult_ErrorOnlyEnvelope(t *testing.T) {
	raw := `{"error":"spawn failed: no such binary"}`
	got := formatTaskResult(raw)
	if !strings.Contains(got, "error: spawn failed: no such binary") {
		t.Errorf("error envelope must render the error:\n%s", got)
	}
}

func TestFormatTaskResult_UnparseableFallsBackToRaw(t *testing.T) {
	raw := "some plain-text child output\nline two"
	got := formatTaskResult(raw)
	if !strings.Contains(got, "some plain-text child output") {
		t.Errorf("unparseable output must fall back to raw text:\n%s", got)
	}
	if got != raw {
		t.Errorf("small raw fallback must be verbatim, got %q", got)
	}
}

// TestFormatTaskResult_LongSummaryHeadlineCapped keeps a hostile/oversized
// summary inside the headline budget even when the child side did not cap it
// (version skew).
func TestFormatTaskResult_LongSummaryHeadlineCapped(t *testing.T) {
	big := strings.Repeat("x", 5000)
	b, err := json.Marshal(map[string]any{"status": "success", "summary": big})
	if err != nil {
		t.Fatal(err)
	}
	got := formatTaskResult(string(b))
	idx := strings.Index(got, "summary: ")
	if idx < 0 {
		t.Fatalf("summary line missing:\n%s", got)
	}
	summary := strings.TrimRight(got[idx+len("summary: "):], "\n")
	if n := len([]rune(summary)); n > subagentHeadlineMaxRunes+1 {
		t.Errorf("summary headline not capped: %d runes (max %d+ellipsis)", n, subagentHeadlineMaxRunes)
	}
}
