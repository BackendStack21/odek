package config

import (
	"os"
	"testing"
)

// unsetEnvForTest removes name for the test (t.Setenv restores it afterwards).
func unsetEnvForTest(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	os.Unsetenv(name)
}

func TestScrubbedProviderEnvIsBoundToHome(t *testing.T) {
	redIsolate(t)
	t.Setenv("ODEK_API_KEY", "sk-home-a")
	if got := LoadConfig(CLIFlags{}).APIKey; got != "sk-home-a" {
		t.Fatalf("first load: %q", got)
	}
	// Another HOME must not inherit the remembered key.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODEK_API_KEY", "")
	if got := providerEnv("ODEK_API_KEY"); got != "" {
		t.Fatalf("explicitly empty variable resolved %q", got)
	}
	unsetEnvForTest(t, "ODEK_API_KEY")
	if got := providerEnv("ODEK_API_KEY"); got != "" {
		t.Fatalf("remembered key leaked across HOME: %q", got)
	}
}

func TestScrubbedProviderEnvRefreshedByNewValue(t *testing.T) {
	redIsolate(t)
	t.Setenv("ODEK_API_KEY", "old")
	_ = LoadConfig(CLIFlags{})
	t.Setenv("ODEK_API_KEY", "new")
	_ = LoadConfig(CLIFlags{})
	if got := LoadConfig(CLIFlags{}).APIKey; got != "new" {
		t.Fatalf("got %q, want the most recent scrubbed value", got)
	}
}
