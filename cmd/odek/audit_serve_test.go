package main

// Regression tests for the 2026-08 security audit — serve/API quick wins.

import (
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/session"
)

// TestAudit_ExportMarkdown_FenceBreakout audits the finding that the
// markdown export used a fixed 4-backtick fence: any transcript line of
// exactly ```` closed it early, letting model/tool output forge document
// structure in the "human-shareable" export. The fence must grow longer
// than the longest backtick run in the fenced content.
func TestAudit_ExportMarkdown_FenceBreakout(t *testing.T) {
	sess := &session.Session{
		ID:   "audit-fence-test",
		Task: "fence test",
		Messages: []llm.Message{
			{Role: "user", Content: "check this"},
			{Role: "assistant", Content: "````\n# FORGED HEADING\n```normal```\n````"},
			{Role: "tool", Name: "browser", Content: "````\n## forged tool section\n````"},
		},
	}
	out := exportSessionMarkdown(sess)
	// The assistant content's longest run is 4 backticks → fence must be ≥5.
	// A 5-backtick opener cannot be closed by any 4-backtick line in the
	// body, so the forged structure stays inside the code block.
	if !strings.Contains(out, "`````markdown") {
		t.Errorf("expected a 5-backtick fence around assistant content:\n%s", out)
	}
	if !strings.Contains(out, "`````text") {
		t.Errorf("expected a 5-backtick fence around tool content:\n%s", out)
	}
}

// TestAudit_CodeFence pins the fence-length computation directly.
func TestAudit_CodeFence(t *testing.T) {
	cases := []struct {
		body string
		want int
	}{
		{"plain text", 4},
		{"has `one` backtick", 4},
		{"has ```three```", 4},
		{"has ````four````", 5},
		{"has ```````seven```````", 8},
	}
	for _, c := range cases {
		if got := len(codeFence(c.body)); got != c.want {
			t.Errorf("codeFence(%q) length = %d, want %d", c.body, got, c.want)
		}
	}
}
