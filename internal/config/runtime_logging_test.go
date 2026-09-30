package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeLoggingOperatorConfigAndProjectIsolation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(home)
	_ = os.Mkdir(filepath.Join(home, ".odek"), 0700)
	_ = os.WriteFile(filepath.Join(home, ".odek", "config.json"), []byte(`{"logging":{"enabled":true,"level":"debug","file":"/tmp/operator.log","max_file_mb":7,"max_files":5,"max_age_hours":12}}`), 0600)
	_ = os.WriteFile(filepath.Join(home, "odek.json"), []byte(`{"logging":{"enabled":false,"level":"error","file":"/tmp/project.log","max_age_hours":1}}`), 0600)
	got := LoadConfig(CLIFlags{})
	if !got.Logging.Enabled || got.Logging.Level != "debug" || got.Logging.File != "/tmp/operator.log" || got.Logging.MaxFileMB != 7 || got.Logging.MaxFiles != 5 || got.Logging.MaxAgeHours != 12 {
		t.Fatalf("project changed operator logging: %+v %+v", got.Logging, got.Maintenance)
	}
	t.Setenv("ODEK_LOGGING_ENABLED", "false")
	t.Setenv("ODEK_LOGGING_MAX_AGE_HOURS", "0")
	got = LoadConfig(CLIFlags{})
	if got.Logging.Enabled || got.Logging.MaxAgeHours != 0 {
		t.Fatalf("env overrides: %+v %+v", got.Logging, got.Maintenance)
	}
}

func TestRuntimeLoggingDefaultsAndValidation(t *testing.T) {
	if got := resolveLogging(nil); got != (LoggingConfig{Enabled: true, Level: "info", File: "~/.odek/runtime.log", MaxFileMB: 25, MaxFiles: 4, MaxAgeHours: 168}) {
		t.Fatalf("default logging policy: %+v", got)
	}
	badLevel := "verbose"
	blankPath := ""
	oversized := 100
	tooOld := 900000
	negative := int64(-5)
	got := resolveLogging(&FileLoggingConfig{Level: &badLevel, File: &blankPath, MaxFiles: &oversized, MaxAgeHours: &tooOld, MaxFileMB: &negative})
	if got.Level != "info" || got.File != "~/.odek/runtime.log" || got.MaxFiles != 32 || got.MaxAgeHours != 87600 || got.MaxFileMB != 0 {
		t.Fatalf("logging policy wasn't normalized: %+v", got)
	}
	relative := "logs/custom.jsonl"
	if got := resolveLogging(&FileLoggingConfig{File: &relative}); got.File != filepath.Join("~/.odek", relative) {
		t.Fatalf("relative path resolved against process cwd: %q", got.File)
	}
}

func TestTelegramLegacyFileAndEnvironmentLoggingSettingsAreIgnored(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(home)
	if err := os.Mkdir(filepath.Join(home, ".odek"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(GlobalConfigPath(), []byte(`{"telegram":{"log_level":"debug","log_file":"/tmp/telegram.log"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ODEK_TELEGRAM_LOG_LEVEL", "debug")
	t.Setenv("ODEK_TELEGRAM_LOG_FILE", "/tmp/env-telegram.log")
	got := LoadConfig(CLIFlags{}).Telegram
	if got.LogLevel != "" || got.LogFile != "" {
		t.Fatalf("legacy telegram logging setting still resolved: %+v", got)
	}
}

func TestEarlyLoggingPolicyMatchesOperatorPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(home)
	t.Setenv("ODEK_LOGGING_ENABLED", "")
	t.Setenv("ODEK_LOGGING_MAX_FILE_MB", "")
	if err := os.Mkdir(filepath.Join(home, ".odek"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "odek.json"), []byte(`{"logging":{"enabled":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if enabled, maxMB := LoadLoggingSettings(); !enabled || maxMB != 25 {
		t.Fatalf("project or defaults affected bootstrap: %v %d", enabled, maxMB)
	}
	if err := os.WriteFile(GlobalConfigPath(), []byte(`{"logging":{"enabled":true,"max_file_mb":7}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if enabled, maxMB := LoadLoggingSettings(); !enabled || maxMB != 7 {
		t.Fatalf("operator config ignored: %v %d", enabled, maxMB)
	}
	t.Setenv("ODEK_LOGGING_ENABLED", "false")
	t.Setenv("ODEK_LOGGING_MAX_FILE_MB", "0")
	if enabled, maxMB := LoadLoggingSettings(); enabled || maxMB != 0 {
		t.Fatalf("environment ignored: %v %d", enabled, maxMB)
	}
}
