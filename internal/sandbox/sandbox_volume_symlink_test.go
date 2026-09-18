package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSanitizeVolumeMountRejectsMissingPathBehindOutsideSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink tests skipped on windows")
	}
	workdir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workdir, "link")); err != nil {
		t.Fatal(err)
	}
	if _, ok := sanitizeVolumeMount("link/new-dir/subdir:/workspace/new-dir", workdir); ok {
		t.Fatal("volume through outside symlink with missing child was accepted")
	}
}

func TestSanitizeVolumeMountAllowsMissingPathInsideWorkdir(t *testing.T) {
	workdir := t.TempDir()
	if _, ok := sanitizeVolumeMount("new-dir/subdir:/workspace/new-dir", workdir); !ok {
		t.Fatal("normal missing path inside workdir was rejected")
	}
}
