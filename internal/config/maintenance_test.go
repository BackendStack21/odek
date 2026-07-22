package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BackendStack21/odek/internal/maintenance"
)

func TestLoadConfig_MaintenanceDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := LoadConfig(CLIFlags{})
	if cfg.Maintenance != maintenance.DefaultConfig() {
		t.Errorf("Maintenance = %+v, want defaults %+v", cfg.Maintenance, maintenance.DefaultConfig())
	}
}

func TestLoadConfig_MaintenanceGlobalFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	globalDir := filepath.Join(dir, ".odek")
	os.MkdirAll(globalDir, 0755)
	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"maintenance": {
			"enabled": false,
			"interval_minutes": 15,
			"sessions_max_age_days": 90,
			"audit_max_age_days": 7,
			"log_max_mb": 100,
			"plans_max_age_days": 60,
			"skills_skip_max_age_days": 30
		}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	want := maintenance.Config{
		Enabled:              false,
		IntervalMinutes:      15,
		SessionsMaxAgeDays:   90,
		AuditMaxAgeDays:      7,
		LogMaxMB:             100,
		PlansMaxAgeDays:      60,
		SkillsSkipMaxAgeDays: 30,
	}
	if cfg.Maintenance != want {
		t.Errorf("Maintenance = %+v, want %+v", cfg.Maintenance, want)
	}
}

// TestLoadConfig_MaintenanceExplicitZero verifies that an explicit 0 in the
// file config means "keep forever / disable", not "inherit the default".
func TestLoadConfig_MaintenanceExplicitZero(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	globalDir := filepath.Join(dir, ".odek")
	os.MkdirAll(globalDir, 0755)
	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"maintenance": {"sessions_max_age_days": 0}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if cfg.Maintenance.SessionsMaxAgeDays != 0 {
		t.Errorf("SessionsMaxAgeDays = %d, want 0 (explicit keep-forever)", cfg.Maintenance.SessionsMaxAgeDays)
	}
	// Unset fields still inherit defaults.
	if cfg.Maintenance.AuditMaxAgeDays != 14 {
		t.Errorf("AuditMaxAgeDays = %d, want default 14", cfg.Maintenance.AuditMaxAgeDays)
	}
}

// TestLoadConfig_MaintenanceProjectIgnored verifies that the project-level
// ./odek.json cannot configure deletion of user data.
func TestLoadConfig_MaintenanceProjectIgnored(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	globalDir := filepath.Join(dir, ".odek")
	os.MkdirAll(globalDir, 0755)
	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"maintenance": {"sessions_max_age_days": 90}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(`{
		"maintenance": {"enabled": true, "sessions_max_age_days": 1, "audit_max_age_days": 1}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if cfg.Maintenance.SessionsMaxAgeDays != 90 {
		t.Errorf("SessionsMaxAgeDays = %d, want 90 (project maintenance must be ignored)", cfg.Maintenance.SessionsMaxAgeDays)
	}
	if cfg.Maintenance.AuditMaxAgeDays != 14 {
		t.Errorf("AuditMaxAgeDays = %d, want default 14 (project maintenance must be ignored)", cfg.Maintenance.AuditMaxAgeDays)
	}
}

// TestLoadConfig_MaintenanceProjectIgnored_EnvStillOverrides verifies that
// ODEK_MAINTENANCE_* env vars still apply when a project config tries (and
// fails) to set the same section.
func TestLoadConfig_MaintenanceProjectIgnored_EnvStillOverrides(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(`{
		"maintenance": {"sessions_max_age_days": 1}
	}`), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ODEK_MAINTENANCE_SESSIONS_MAX_AGE_DAYS", "45")

	cfg := LoadConfig(CLIFlags{})
	if cfg.Maintenance.SessionsMaxAgeDays != 45 {
		t.Errorf("SessionsMaxAgeDays = %d, want 45 (env override)", cfg.Maintenance.SessionsMaxAgeDays)
	}
}

func TestLoadConfig_MaintenanceEnvVars(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODEK_MAINTENANCE_ENABLED", "false")
	t.Setenv("ODEK_MAINTENANCE_INTERVAL_MINUTES", "5")
	t.Setenv("ODEK_MAINTENANCE_SESSIONS_MAX_AGE_DAYS", "7")
	t.Setenv("ODEK_MAINTENANCE_AUDIT_MAX_AGE_DAYS", "3")
	t.Setenv("ODEK_MAINTENANCE_LOG_MAX_MB", "10")
	t.Setenv("ODEK_MAINTENANCE_PLANS_MAX_AGE_DAYS", "15")
	t.Setenv("ODEK_MAINTENANCE_SKILLS_SKIP_MAX_AGE_DAYS", "20")

	cfg := LoadConfig(CLIFlags{})
	want := maintenance.Config{
		Enabled:              false,
		IntervalMinutes:      5,
		SessionsMaxAgeDays:   7,
		AuditMaxAgeDays:      3,
		LogMaxMB:             10,
		PlansMaxAgeDays:      15,
		SkillsSkipMaxAgeDays: 20,
	}
	if cfg.Maintenance != want {
		t.Errorf("Maintenance = %+v, want %+v", cfg.Maintenance, want)
	}
}

// TestLoadConfig_MaintenanceEnvExplicitZero verifies that an env var set to 0
// is honored (keep forever), not treated as unset.
func TestLoadConfig_MaintenanceEnvExplicitZero(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODEK_MAINTENANCE_AUDIT_MAX_AGE_DAYS", "0")

	cfg := LoadConfig(CLIFlags{})
	if cfg.Maintenance.AuditMaxAgeDays != 0 {
		t.Errorf("AuditMaxAgeDays = %d, want 0 (explicit env zero)", cfg.Maintenance.AuditMaxAgeDays)
	}
}

// TestEnvIntPtr covers the unset / valid / unparseable branches of the
// ODEK_* integer env helper.
func TestEnvIntPtr(t *testing.T) {
	os.Unsetenv("ODEK_TEST_ENV_INT_PTR")
	if got := envIntPtr("TEST_ENV_INT_PTR"); got != nil {
		t.Errorf("envIntPtr(unset) = %v, want nil", *got)
	}
	t.Setenv("ODEK_TEST_ENV_INT_PTR", "42")
	if got := envIntPtr("TEST_ENV_INT_PTR"); got == nil || *got != 42 {
		t.Errorf("envIntPtr(42) = %v, want 42", got)
	}
	t.Setenv("ODEK_TEST_ENV_INT_PTR", "notanint")
	if got := envIntPtr("TEST_ENV_INT_PTR"); got != nil {
		t.Errorf("envIntPtr(notanint) = %v, want nil", *got)
	}
}

// TestEnvInt64Ptr covers the unset / valid / unparseable branches of the
// ODEK_* int64 env helper.
func TestEnvInt64Ptr(t *testing.T) {
	os.Unsetenv("ODEK_TEST_ENV_INT64_PTR")
	if got := envInt64Ptr("TEST_ENV_INT64_PTR"); got != nil {
		t.Errorf("envInt64Ptr(unset) = %v, want nil", *got)
	}
	t.Setenv("ODEK_TEST_ENV_INT64_PTR", "9876543210")
	if got := envInt64Ptr("TEST_ENV_INT64_PTR"); got == nil || *got != 9876543210 {
		t.Errorf("envInt64Ptr(9876543210) = %v, want 9876543210", got)
	}
	t.Setenv("ODEK_TEST_ENV_INT64_PTR", "12x")
	if got := envInt64Ptr("TEST_ENV_INT64_PTR"); got != nil {
		t.Errorf("envInt64Ptr(12x) = %v, want nil", *got)
	}
}
