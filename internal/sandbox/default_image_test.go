package sandbox

import (
	"regexp"
	"strings"
	"testing"
)

var pinnedRef = regexp.MustCompile(`^alpine:3\.\d+(\.\d+)?@sha256:[0-9a-f]{64}$`)

func dockerfileFrom(t *testing.T) string {
	t.Helper()
	for _, line := range strings.Split(defaultDockerfile, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && strings.EqualFold(f[0], "FROM") {
			return f[1]
		}
	}
	t.Fatal("embedded default.Dockerfile has no FROM line")
	return ""
}

func TestDefaultDockerfile_FromIsDigestPinned(t *testing.T) {
	if from := dockerfileFrom(t); !pinnedRef.MatchString(from) {
		t.Fatalf("default.Dockerfile FROM %q is not alpine:<ver>@sha256:<64 hex>", from)
	}
}

func TestDefaultImageConstantMatchesDockerfile(t *testing.T) {
	if DefaultImage != dockerfileFrom(t) {
		t.Fatalf("DefaultImage %q != Dockerfile FROM %q", DefaultImage, dockerfileFrom(t))
	}
}

func TestResolveImage_FallsThroughToPinnedDefault(t *testing.T) {
	t.Chdir(t.TempDir()) // no Dockerfile.odek here
	got, err := ResolveImage(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if got == "alpine:latest" || strings.HasSuffix(got, ":latest") {
		t.Fatalf("ResolveImage returned floating ref %q", got)
	}
	if !pinnedRef.MatchString(got) {
		t.Fatalf("ResolveImage returned %q, want alpine:<ver>@sha256:<64 hex>", got)
	}
	if got != dockerfileFrom(t) {
		t.Fatalf("ResolveImage %q disagrees with Dockerfile FROM %q", got, dockerfileFrom(t))
	}
}

func TestResolveImage_ExplicitImageUnchanged(t *testing.T) {
	got, err := ResolveImage(Config{Image: "node:20-alpine"})
	if err != nil || got != "node:20-alpine" {
		t.Fatalf("got %q, %v", got, err)
	}
}
