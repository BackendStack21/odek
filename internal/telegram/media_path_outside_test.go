package telegram

import (
	"os"
	"path/filepath"
	"testing"
)

func mediaPathTestDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// A document in a "secrets" folder outside the bot's working directory is not
// refused by the directory-name rule.
func TestCheckMediaPathSensitivity_DirRuleOnlyBelowWorkingDir(t *testing.T) {
	cwd := mediaPathTestDir(t)
	other := mediaPathTestDir(t)
	t.Chdir(cwd)

	outside := filepath.Join(other, "secrets", "report.pdf")
	if err := checkMediaPathSensitivity(outside); err != nil {
		t.Errorf("outside working dir refused: %v", err)
	}

	inside := filepath.Join(cwd, "secrets", "report.pdf")
	if err := os.MkdirAll(filepath.Dir(inside), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := checkMediaPathSensitivity(inside); err == nil {
		t.Error("document under a secrets folder inside the working dir was allowed")
	}

	// Name-based rules still apply everywhere.
	if err := checkMediaPathSensitivity(filepath.Join(other, ".env")); err == nil {
		t.Error(".env outside the working dir was allowed")
	}
}
