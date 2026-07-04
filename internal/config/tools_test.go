package config

import (
	"os"
	"path/filepath"
	"testing"
)

// RED tests for the proposed ToolConfig contract.
// These tests will fail until the ToolConfig feature is implemented.

func TestToolConfig_Defaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := LoadConfig(CLIFlags{})
	if cfg.Tools.Enabled != nil {
		t.Errorf("Tools.Enabled should default to nil, got %v", cfg.Tools.Enabled)
	}
	if cfg.Tools.Disabled != nil {
		t.Errorf("Tools.Disabled should default to nil, got %v", cfg.Tools.Disabled)
	}
}

func TestToolConfig_EnabledWhitelist(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	writeConfig(t, filepath.Join(dir, ".odek", "config.json"), `{
		"tools": {
			"enabled": ["web_search", "transcribe", "vision"]
		}
	}`)

	cfg := LoadConfig(CLIFlags{})
	want := []string{"web_search", "transcribe", "vision"}
	if !stringSlicesEqual(cfg.Tools.Enabled, want) {
		t.Errorf("Tools.Enabled = %v, want %v", cfg.Tools.Enabled, want)
	}
}

func TestToolConfig_DisabledBlacklist(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	writeConfig(t, filepath.Join(dir, ".odek", "config.json"), `{
		"tools": {
			"disabled": ["shell", "write_file"]
		}
	}`)

	cfg := LoadConfig(CLIFlags{})
	want := []string{"shell", "write_file"}
	if !stringSlicesEqual(cfg.Tools.Disabled, want) {
		t.Errorf("Tools.Disabled = %v, want %v", cfg.Tools.Disabled, want)
	}
}

func TestToolConfig_EnvVarOverrides(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("ODEK_TOOLS_ENABLED", "session_search,web_search")
	t.Setenv("ODEK_TOOLS_DISABLED", "shell,delegate_tasks")

	cfg := LoadConfig(CLIFlags{})
	if !stringSlicesEqual(cfg.Tools.Enabled, []string{"session_search", "web_search"}) {
		t.Errorf("Tools.Enabled = %v, want [session_search web_search]", cfg.Tools.Enabled)
	}
	if !stringSlicesEqual(cfg.Tools.Disabled, []string{"shell", "delegate_tasks"}) {
		t.Errorf("Tools.Disabled = %v, want [shell delegate_tasks]", cfg.Tools.Disabled)
	}
}

func TestToolConfig_CLIOverridesEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("ODEK_TOOLS_ENABLED", "web_search")
	t.Setenv("ODEK_TOOLS_DISABLED", "shell")

	cfg := LoadConfig(CLIFlags{
		ToolsEnabled:  []string{"vision", "transcribe"},
		ToolsDisabled: []string{"delegate_tasks"},
	})
	if !stringSlicesEqual(cfg.Tools.Enabled, []string{"vision", "transcribe"}) {
		t.Errorf("Tools.Enabled = %v, want CLI value [vision transcribe]", cfg.Tools.Enabled)
	}
	if !stringSlicesEqual(cfg.Tools.Disabled, []string{"shell", "delegate_tasks"}) {
		t.Errorf("Tools.Disabled = %v, want merged env+CLI [shell delegate_tasks]", cfg.Tools.Disabled)
	}
}

func TestToolConfig_ProjectConfigCanOnlyDisable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	writeConfig(t, filepath.Join(dir, ".odek", "config.json"), `{
		"tools": {
			"enabled": ["web_search", "vision"],
			"disabled": ["delegate_tasks"]
		}
	}`)

	wd := t.TempDir()
	writeConfig(t, filepath.Join(wd, "odek.json"), `{
		"tools": {
			"enabled": ["shell"],
			"disabled": ["read_file"]
		}
	}`)

	origWd, _ := os.Getwd()
	os.Chdir(wd)
	defer os.Chdir(origWd)

	cfg := LoadConfig(CLIFlags{})
	// Global whitelist was present, project tried to override it; project
	// enablement is ignored so the global whitelist should still stand.
	if cfg.Tools.Enabled == nil || !stringSlicesEqual(cfg.Tools.Enabled, []string{"web_search", "vision"}) {
		t.Errorf("Tools.Enabled = %v, want global value [web_search vision]", cfg.Tools.Enabled)
	}
	wantDisabled := []string{"delegate_tasks", "read_file"}
	if !stringSlicesEqual(cfg.Tools.Disabled, wantDisabled) {
		t.Errorf("Tools.Disabled = %v, want merged global+project %v", cfg.Tools.Disabled, wantDisabled)
	}
}

func TestToolConfig_CLIEnabledOverridesProjectAndGlobal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	writeConfig(t, filepath.Join(dir, ".odek", "config.json"), `{
		"tools": {
			"enabled": ["web_search", "vision"]
		}
	}`)

	wd := t.TempDir()
	writeConfig(t, filepath.Join(wd, "odek.json"), `{
		"tools": {
			"disabled": ["read_file"]
		}
	}`)

	origWd, _ := os.Getwd()
	os.Chdir(wd)
	defer os.Chdir(origWd)

	cfg := LoadConfig(CLIFlags{
		ToolsEnabled: []string{"transcribe"},
	})
	if !stringSlicesEqual(cfg.Tools.Enabled, []string{"transcribe"}) {
		t.Errorf("CLI Tools.Enabled = %v, want [transcribe]", cfg.Tools.Enabled)
	}
}

func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
