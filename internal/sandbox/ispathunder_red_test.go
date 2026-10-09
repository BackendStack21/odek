package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// A legitimate in-workdir directory whose name merely starts with two dots
// must not be treated as outside the root.
func TestRED_VolumeMountDotPrefixedDirRejected(t *testing.T) {
	workdir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workdir, "..cache"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, ok := sanitizeVolumeMount("..cache:/cache", workdir); !ok {
		t.Fatal("in-workdir mount of ..cache was rejected")
	}
}

func TestIsPathUnder_Table(t *testing.T) {
	root := filepath.Clean("/a/b")
	cases := map[string]bool{
		"/a/b":          true,
		"/a/b/c":        true,
		"/a/b/..c":      true,
		"/a/b/..c/d":    true,
		"/a":            false,
		"/a/bc":         false,
		"/a/other":      false,
		"/x":            false,
		"/a/b/../other": false,
	}
	for p, want := range cases {
		if got := isPathUnder(filepath.Clean(p), root); got != want {
			t.Errorf("isPathUnder(%q) = %v, want %v", p, got, want)
		}
	}
}
