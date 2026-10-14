package sandbox

import "testing"

func TestRED_ResolveImageRejectsOptionLikeNames(t *testing.T) {
	for _, img := range []string{"-v=/:/host", "--privileged", "-", "--network=host", "alp ine", "a\nb", "img\x00"} {
		if got, err := ResolveImage(Config{Image: img}); err == nil {
			t.Errorf("ResolveImage(%q) = %q, want error", img, got)
		}
	}
	for _, img := range []string{"alpine:latest", "node:20-alpine", "ghcr.io/org/img@sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "localhost:5000/a/b:1.0"} {
		if _, err := ResolveImage(Config{Image: img}); err != nil {
			t.Errorf("ResolveImage(%q) rejected: %v", img, err)
		}
	}
}
