package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A forbidden prefix must match the requested host path in every spelling:
// the raw path, the symlink-resolved path, and the resolved form of the
// prefix itself. macOS makes /etc and /var symlinks into /private, so a
// check that only compares the resolved host path against the literal
// prefix lets "/etc/secret" through when the working directory is "/".
func TestRED_ForbiddenPrefixMatchesThroughSymlinks(t *testing.T) {
	tmp := t.TempDir()
	real := filepath.Join(tmp, "private", "etc")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "secret"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	forbidden := filepath.Join(tmp, "etc")
	if err := os.Symlink(real, forbidden); err != nil {
		t.Skipf("symlink: %v", err)
	}
	workdir := filepath.Join(tmp, "work")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	saved := ForbiddenMountPrefixes
	ForbiddenMountPrefixes = []string{forbidden}
	t.Cleanup(func() { ForbiddenMountPrefixes = saved })

	// Raw path under the forbidden prefix, resolved path outside it.
	if got, ok := sanitizeVolumeMount(forbidden+"/secret:/c/s", tmp); ok {
		t.Fatalf("mount under symlinked forbidden prefix accepted: %q", got)
	}
	// Resolved path under the resolved prefix, raw path outside it.
	if got, ok := sanitizeVolumeMount(real+"/secret:/c/s", tmp); ok {
		t.Fatalf("mount under the resolved forbidden prefix accepted: %q", got)
	}
	// A sibling that resolves elsewhere stays allowed.
	if _, ok := sanitizeVolumeMount(workdir+":/c/w", tmp); !ok {
		t.Fatalf("unrelated in-workdir mount rejected")
	}
}

// The workdir exemption must also hold in resolved form: a project under
// /var/folders on macOS resolves to /private/var/..., and "/var" is a
// forbidden prefix, so in-workdir mounts must stay accepted either way.
func TestRED_WorkdirExemptionHoldsThroughSymlinks(t *testing.T) {
	tmp := t.TempDir()
	real := filepath.Join(tmp, "private", "var", "proj")
	if err := os.MkdirAll(filepath.Join(real, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(tmp, "private", "var"), filepath.Join(tmp, "var")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	workdir := filepath.Join(tmp, "var", "proj") // raw spelling through the symlink
	saved := ForbiddenMountPrefixes
	ForbiddenMountPrefixes = []string{filepath.Join(tmp, "var")}
	t.Cleanup(func() { ForbiddenMountPrefixes = saved })
	got, ok := sanitizeVolumeMount("data:/c/data", workdir)
	if !ok || !strings.HasSuffix(got, "/data:/c/data") {
		t.Fatalf("in-workdir mount under an exempt forbidden parent rejected: %q ok=%v", got, ok)
	}
}
