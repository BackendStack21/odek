package config

import "testing"

func redIsolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)
	for _, k := range []string{"ODEK_API_KEY", "OPENAI_API_KEY", "DEEPSEEK_API_KEY", "ANTHROPIC_API_KEY"} {
		t.Setenv(k, "")
	}
	return dir
}

// A second LoadConfig in the same process (LoadLoggingConfig runs before the
// command's own LoadConfig) must still see an env-supplied API key.
func TestRED_SecondLoadConfigKeepsEnvAPIKey(t *testing.T) {
	redIsolate(t)
	t.Setenv("ODEK_API_KEY", "sk-env-only-key")
	first := LoadConfig(CLIFlags{})
	if first.APIKey != "sk-env-only-key" {
		t.Fatalf("first load: %q", first.APIKey)
	}
	second := LoadConfig(CLIFlags{})
	if second.APIKey != "sk-env-only-key" {
		t.Errorf("second LoadConfig lost env API key: got %q", second.APIKey)
	}
}

func TestRED_SecondLoadConfigKeepsProviderEnvKey(t *testing.T) {
	redIsolate(t)
	t.Setenv("DEEPSEEK_API_KEY", "ds-env-key")
	_ = LoadConfig(CLIFlags{})
	second := LoadConfig(CLIFlags{})
	if second.APIKey != "ds-env-key" {
		t.Errorf("second LoadConfig lost DEEPSEEK_API_KEY: got %q", second.APIKey)
	}
}
