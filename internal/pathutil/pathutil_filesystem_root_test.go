package pathutil

import (
	"path/filepath"
	"testing"
)

// TestRED_WithinRootFilesystemRoot: the prefix check appends a separator to
// the root, so with root "/" every candidate is reported outside it.
func TestRED_WithinRootFilesystemRoot(t *testing.T) {
	cand, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !WithinRoot("/", cand) {
		t.Fatalf("WithinRoot(\"/\", %q) = false, want true", cand)
	}
}

func TestWithinRoot_FilesystemRootContainsItself(t *testing.T) {
	if !WithinRoot("/", "/") {
		t.Fatal("WithinRoot(\"/\", \"/\") = false, want true")
	}
}
