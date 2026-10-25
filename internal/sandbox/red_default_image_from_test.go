package sandbox

import (
	"strings"
	"testing"
)

// DefaultImage is read from the embedded default.Dockerfile, so a Dependabot
// bump of that file changes the image without a second hand edit.
func TestRED_DefaultImageFrom_ReadsTheFromLine(t *testing.T) {
	pin := "alpine:3.23@sha256:" + strings.Repeat("e", 64)
	cases := map[string]string{
		"FROM " + pin + "\n":                             pin,
		"# syntax comment\n\nfrom " + pin + " AS base\n": pin,
		"":           "",
		"RUN true\n": "",
	}
	for in, want := range cases {
		if got := defaultImageFrom(in); got != want {
			t.Errorf("defaultImageFrom(%q) = %q, want %q", in, got, want)
		}
	}
	if DefaultImage != defaultImageFrom(defaultDockerfile) || DefaultImage == "" {
		t.Errorf("DefaultImage %q must come from the embedded Dockerfile", DefaultImage)
	}
}
