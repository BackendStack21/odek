package redact

import (
	"strings"
	"testing"
)

// The sk- pattern had no left boundary: "sk-" occurring mid-word (prose,
// slugs, URLs — "task-notes-...", "risk-assessment-...") matched and the
// surrounding text was corrupted to [REDACTED] in sessions, logs, and the
// event stream. A provider key must start at a word boundary.
func TestRedactSecrets_SkPatternNotMidWord(t *testing.T) {
	inputs := []string{
		"task-notes-for-the-release-branch-are-ready-now",        // prose slug
		"the risk-assessment-workflow-uses-these-checklists-here", // prose
		"see desk-level-planning-notes-and-assumptions-sections",  // another word ending in -sk
	}
	for _, in := range inputs {
		if out := RedactSecrets(in); out != in {
			t.Errorf("over-redaction: %q -> %q", in, out)
		}
	}

	// Real keys at a boundary are still redacted.
	leaked := "token: sk-abcdefghijklmnopqrstuvwxyz0123456789"
	if out := RedactSecrets(leaked); strings.Contains(out, "sk-abcdefghij") {
		t.Errorf("real sk- key survived: %q", out)
	}
	if prefix := "key sk-abcdefghijklmnopqrstuvwxyz0123456789 end"; RedactSecrets(prefix) == prefix {
		t.Error("space-delimited sk- key survived")
	}
}
