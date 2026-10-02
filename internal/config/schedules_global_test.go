package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProjectSchedulesIgnoredEntirely pins that schedules are operator-only:
// the whole `schedules` section from a project ./odek.json is ignored with a
// warning — a project must not enable/disable scheduling or change the
// timezone the operator's scheduler daemon runs under.
func TestProjectSchedulesIgnoredEntirely(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()
	t.Chdir(wd)

	if err := os.WriteFile(filepath.Join(wd, "odek.json"), []byte(`{
		"schedules": {"enabled": false, "timezone": "America/New_York", "max_concurrent": 5}
	}`), 0600); err != nil {
		t.Fatal(err)
	}

	stderr := captureEnvStderr(t, func() {
		_ = LoadConfig(CLIFlags{})
	})
	if !strings.Contains(stderr, "ignoring schedules from project config") {
		t.Errorf("expected whole-section schedules warning, got stderr: %s", stderr)
	}
}
