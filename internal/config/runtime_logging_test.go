package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeLoggingOperatorConfigAndExpiration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(home)
	_ = os.Mkdir(filepath.Join(home, ".odek"), 0700)
	_ = os.WriteFile(filepath.Join(home, ".odek", "config.json"), []byte(`{"logging":{"enabled":true},"maintenance":{"runtime_log_max_age_hours":12}}`), 0600)
	_ = os.WriteFile(filepath.Join(home, "odek.json"), []byte(`{"logging":{"enabled":false},"maintenance":{"runtime_log_max_age_hours":1}}`), 0600)
	got := LoadConfig(CLIFlags{})
	if !got.Logging.Enabled || got.Maintenance.RuntimeLogMaxAgeHours != 12 {
		t.Fatalf("project changed operator logging: %+v %+v", got.Logging, got.Maintenance)
	}
	t.Setenv("ODEK_LOGGING_ENABLED", "false")
	t.Setenv("ODEK_MAINTENANCE_RUNTIME_LOG_MAX_AGE_HOURS", "0")
	got = LoadConfig(CLIFlags{})
	if got.Logging.Enabled || got.Maintenance.RuntimeLogMaxAgeHours != 0 {
		t.Fatalf("env overrides: %+v %+v", got.Logging, got.Maintenance)
	}
}
func TestRuntimeLogExpirationClamps(t *testing.T) {
	for _, n := range []int{-1, 999999999, 0, 24} {
		got := resolveMaintenance(&MaintenanceConfig{RuntimeLogMaxAgeHours: &n}).RuntimeLogMaxAgeHours
		if got < 0 || got > 36500 {
			t.Fatalf("unbounded retention: %d", got)
		}
		if n == 0 && got != 0 {
			t.Fatal("zero must retain forever")
		}
	}
}

func TestEarlyLoggingPolicyMatchesOperatorPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(home)
	t.Setenv("ODEK_LOGGING_ENABLED", "")
	t.Setenv("ODEK_MAINTENANCE_LOG_MAX_MB", "")
	if err := os.Mkdir(filepath.Join(home, ".odek"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "odek.json"), []byte(`{"logging":{"enabled":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if enabled, maxMB := LoadLoggingSettings(); enabled || maxMB != 50 {
		t.Fatalf("project or defaults affected bootstrap: %v %d", enabled, maxMB)
	}
	if err := os.WriteFile(GlobalConfigPath(), []byte(`{"logging":{"enabled":true},"maintenance":{"log_max_mb":7}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if enabled, maxMB := LoadLoggingSettings(); !enabled || maxMB != 7 {
		t.Fatalf("operator config ignored: %v %d", enabled, maxMB)
	}
	t.Setenv("ODEK_LOGGING_ENABLED", "false")
	t.Setenv("ODEK_MAINTENANCE_LOG_MAX_MB", "0")
	if enabled, maxMB := LoadLoggingSettings(); enabled || maxMB != 0 {
		t.Fatalf("environment ignored: %v %d", enabled, maxMB)
	}
}
