package main

import (
	"fmt"
	"strings"
	"testing"
)

// The batch_patch preview hunk must be truthful: it shows the region around
// the actual match, not a hardcoded first-100-bytes diff that renders
// identical -/+ lines when old_string sits past byte 100.
func TestPatchPreviewDiff_OffsetMatch(t *testing.T) {
	padding := strings.Repeat("x", 300)
	original := padding + "\nold line\n" + strings.Repeat("y", 100)
	modified := padding + "\nnew line\n" + strings.Repeat("y", 100)
	diff := patchPreviewDiff("f.txt", original, modified, "old line", "new line")
	if strings.Contains(diff, "@@ -1 +1 @@") {
		t.Fatalf("preview still uses hardcoded hunk header: %s", diff)
	}
	if !strings.Contains(diff, "-old line") || !strings.Contains(diff, "+new line") {
		t.Fatalf("preview hunk does not show the changed lines:\n%s", diff)
	}
}

func TestPatchPreviewDiff_HeadMatch(t *testing.T) {
	original := "alpha\nbeta\ngamma"
	modified := "alpha\nBETA\ngamma"
	diff := patchPreviewDiff("f.txt", original, modified, "beta", "BETA")
	if !strings.Contains(diff, "-beta") || !strings.Contains(diff, "+BETA") {
		t.Fatalf("head-match preview wrong:\n%s", diff)
	}
}

func TestPatchPreviewDiff_OldNotFound(t *testing.T) {
	diff := patchPreviewDiff("f.txt", "abc", "abd", "zzz", "q")
	if diff == "" || !strings.Contains(diff, "f.txt") {
		t.Fatalf("expected fallback diff naming the file, got %q", diff)
	}
	fmt.Print()
}
