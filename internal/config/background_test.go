package config

import (
	"os"
	"testing"
)

// ── background section (docs/CONFIG.md) ────────────────────────────────

func intPtr(i int) *int { return &i }

func TestBackground_Defaults(t *testing.T) {
	// No files, no env, no CLI — the background section applies defaults:
	// enabled, 8 jobs, 1 MiB output, no timeout cap, observe notices,
	// kill-on-session-end.
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())

	cfg := LoadConfig(CLIFlags{})
	bg := cfg.Background
	if !bg.Enabled {
		t.Error("Background.Enabled should default to true")
	}
	if bg.MaxJobs != 8 {
		t.Errorf("Background.MaxJobs = %d, want 8", bg.MaxJobs)
	}
	if bg.MaxOutputBytes != 1048576 {
		t.Errorf("Background.MaxOutputBytes = %d, want 1048576 (1 MiB)", bg.MaxOutputBytes)
	}
	if bg.MaxTimeoutSeconds != 0 {
		t.Errorf("Background.MaxTimeoutSeconds = %d, want 0 (uncapped)", bg.MaxTimeoutSeconds)
	}
	if bg.Notify != "observe" {
		t.Errorf("Background.Notify = %q, want %q", bg.Notify, "observe")
	}
	if bg.OnSessionEnd != "kill" {
		t.Errorf("Background.OnSessionEnd = %q, want %q", bg.OnSessionEnd, "kill")
	}
}

func TestBackground_GlobalSection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	writeGlobalConfig(t, os.Getenv("HOME"), `{
		"background": {
			"enabled": true,
			"max_jobs": 4,
			"max_output_bytes": 262144,
			"max_timeout_seconds": 600,
			"notify": "off",
			"on_session_end": "kill"
		}
	}`)

	cfg := LoadConfig(CLIFlags{})
	bg := cfg.Background
	if !bg.Enabled {
		t.Error("Background.Enabled = false, want true")
	}
	if bg.MaxJobs != 4 {
		t.Errorf("Background.MaxJobs = %d, want 4", bg.MaxJobs)
	}
	if bg.MaxOutputBytes != 262144 {
		t.Errorf("Background.MaxOutputBytes = %d, want 262144", bg.MaxOutputBytes)
	}
	if bg.MaxTimeoutSeconds != 600 {
		t.Errorf("Background.MaxTimeoutSeconds = %d, want 600", bg.MaxTimeoutSeconds)
	}
	if bg.Notify != "off" {
		t.Errorf("Background.Notify = %q, want %q", bg.Notify, "off")
	}
	if bg.OnSessionEnd != "kill" {
		t.Errorf("Background.OnSessionEnd = %q, want %q", bg.OnSessionEnd, "kill")
	}
}

func TestBackground_Disabled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	writeGlobalConfig(t, os.Getenv("HOME"), `{"background": {"enabled": false}}`)

	cfg := LoadConfig(CLIFlags{})
	if cfg.Background.Enabled {
		t.Error("Background.Enabled = true, want false (explicit disable)")
	}
	// Numeric defaults still apply so the section stays coherent.
	if cfg.Background.MaxJobs != 8 {
		t.Errorf("Background.MaxJobs = %d, want 8", cfg.Background.MaxJobs)
	}
}

func TestBackground_InvalidNotifyFallsBackToObserve(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	writeGlobalConfig(t, os.Getenv("HOME"), `{"background": {"notify": "shout"}}`)

	cfg := LoadConfig(CLIFlags{})
	if cfg.Background.Notify != "observe" {
		t.Errorf("Background.Notify = %q, want %q (invalid value falls back)", cfg.Background.Notify, "observe")
	}
}

func TestBackground_OnSessionEndKillOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	writeGlobalConfig(t, os.Getenv("HOME"), `{"background": {"on_session_end": "detach"}}`)

	cfg := LoadConfig(CLIFlags{})
	if cfg.Background.OnSessionEnd != "kill" {
		t.Errorf("Background.OnSessionEnd = %q, want %q (kill is the only supported value in v1)", cfg.Background.OnSessionEnd, "kill")
	}
}

func TestBackground_InvalidNumericsFallBackToDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	writeGlobalConfig(t, os.Getenv("HOME"), `{
		"background": {
			"max_jobs": -3,
			"max_output_bytes": 0,
			"max_timeout_seconds": -10
		}
	}`)

	cfg := LoadConfig(CLIFlags{})
	bg := cfg.Background
	if bg.MaxJobs != 8 {
		t.Errorf("Background.MaxJobs = %d, want 8 (non-positive falls back to default)", bg.MaxJobs)
	}
	if bg.MaxOutputBytes != 1048576 {
		t.Errorf("Background.MaxOutputBytes = %d, want 1048576 (non-positive falls back to default)", bg.MaxOutputBytes)
	}
	if bg.MaxTimeoutSeconds != 0 {
		t.Errorf("Background.MaxTimeoutSeconds = %d, want 0 (negative means uncapped)", bg.MaxTimeoutSeconds)
	}
}

func TestBackground_ProjectClamp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	writeGlobalConfig(t, os.Getenv("HOME"), `{
		"background": {
			"enabled": true,
			"max_jobs": 4,
			"max_output_bytes": 524288,
			"max_timeout_seconds": 300,
			"notify": "observe"
		}
	}`)

	// Project may lower every numeric cap.
	t.Chdir(t.TempDir())
	if err := os.WriteFile("odek.json", []byte(`{
		"background": {
			"max_jobs": 2,
			"max_output_bytes": 65536,
			"max_timeout_seconds": 60,
			"notify": "off"
		}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	bg := cfg.Background
	if bg.MaxJobs != 2 {
		t.Errorf("Background.MaxJobs = %d, want 2 (project lowered)", bg.MaxJobs)
	}
	if bg.MaxOutputBytes != 65536 {
		t.Errorf("Background.MaxOutputBytes = %d, want 65536 (project lowered)", bg.MaxOutputBytes)
	}
	if bg.MaxTimeoutSeconds != 60 {
		t.Errorf("Background.MaxTimeoutSeconds = %d, want 60 (project lowered)", bg.MaxTimeoutSeconds)
	}
	if bg.Notify != "off" {
		t.Errorf("Background.Notify = %q, want %q (project may tighten)", bg.Notify, "off")
	}

	// Project may not RAISE a globally-set cap.
	if err := os.WriteFile("odek.json", []byte(`{
		"background": {
			"max_jobs": 16,
			"max_output_bytes": 10485760,
			"max_timeout_seconds": 3600
		}
	}`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg = LoadConfig(CLIFlags{})
	bg = cfg.Background
	if bg.MaxJobs != 4 {
		t.Errorf("Background.MaxJobs = %d, want 4 (project may only lower)", bg.MaxJobs)
	}
	if bg.MaxOutputBytes != 524288 {
		t.Errorf("Background.MaxOutputBytes = %d, want 524288 (project may only lower)", bg.MaxOutputBytes)
	}
	if bg.MaxTimeoutSeconds != 300 {
		t.Errorf("Background.MaxTimeoutSeconds = %d, want 300 (project may only lower)", bg.MaxTimeoutSeconds)
	}

	// Global-off wins over a project enable attempt.
	writeGlobalConfig(t, os.Getenv("HOME"), `{"background": {"enabled": false}}`)
	if err := os.WriteFile("odek.json", []byte(`{"background": {"enabled": true}}`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg = LoadConfig(CLIFlags{})
	if cfg.Background.Enabled {
		t.Error("project must not re-enable globally-disabled background commands")
	}
}

func TestBackground_ProjectWithoutGlobalSection(t *testing.T) {
	// No global background section: the project may set values freely —
	// deviation from defaults can only tighten or observe, never loosen
	// an operator decision (none exists).
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	if err := os.WriteFile("odek.json", []byte(`{
		"background": {"max_jobs": 2, "notify": "off"}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if cfg.Background.MaxJobs != 2 {
		t.Errorf("Background.MaxJobs = %d, want 2", cfg.Background.MaxJobs)
	}
	if cfg.Background.Notify != "off" {
		t.Errorf("Background.Notify = %q, want %q", cfg.Background.Notify, "off")
	}
	if !cfg.Background.Enabled {
		t.Error("Background.Enabled should stay true (default)")
	}
}
