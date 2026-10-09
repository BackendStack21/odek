package skills

import (
	"strings"
	"testing"
)

// The provenance source written for an imported skill must not carry a
// secret from the import URL: it lands in SKILL.md and in promote output.
func TestRED_ImportSourceMarkerRedactsSecrets(t *testing.T) {
	got := importSourceMarker("https://host.example/skill.md?token=ghp_abcdefghijklmnopqrstuvwxyz1234567890")
	if strings.Contains(got, "ghp_abcdefghijklmnopqrstuvwxyz") {
		t.Fatalf("token survived in the source marker: %q", got)
	}
	if !strings.HasPrefix(got, "https://host.example/skill.md") {
		t.Fatalf("marker lost the URL: %q", got)
	}
	if got := importSourceMarker("  https://host.example/a b  "); got != "https://host.example/ab" {
		t.Fatalf("whitespace handling changed: %q", got)
	}
}
