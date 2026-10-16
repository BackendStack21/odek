package agent

import (
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/llmclient"
)

// TestRED_MissingKeyErrorNamesProviderEnvAndSecretsFile: the missing-key
// error names the provider's own env variable and ~/.odek/secrets.env, the
// file the CLI loads secrets from.
func TestRED_MissingKeyErrorNamesProviderEnvAndSecretsFile(t *testing.T) {
	for _, k := range []string{"ODEK_API_KEY", "DEEPSEEK_API_KEY", "OPENAI_API_KEY"} {
		t.Setenv(k, "")
	}
	_, err := New(Config{Provider: "deepseek"})
	if err == nil {
		t.Fatal("New succeeded without an API key")
	}
	msg := err.Error()
	for _, want := range []string{"DEEPSEEK_API_KEY", "~/.odek/secrets.env", "providers.deepseek.api_key"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing-key error does not mention %s: %q", want, msg)
		}
	}
}

// TestRED_CheckAPIKeyMatchesNew: CheckAPIKey reports exactly New's
// missing-key error, so the CLI can fail fast before starting a sandbox.
func TestRED_CheckAPIKeyMatchesNew(t *testing.T) {
	for _, k := range []string{"ODEK_API_KEY", "DEEPSEEK_API_KEY", "OPENAI_API_KEY"} {
		t.Setenv(k, "")
	}
	err := CheckAPIKey(Config{Provider: "deepseek"})
	_, newErr := New(Config{Provider: "deepseek"})
	if err == nil || newErr == nil || err.Error() != newErr.Error() {
		t.Errorf("CheckAPIKey = %v, New = %v; want the same missing-key error", err, newErr)
	}
	t.Setenv("DEEPSEEK_API_KEY", "sk-test")
	if err := CheckAPIKey(Config{}); err != nil {
		t.Errorf("CheckAPIKey with DEEPSEEK_API_KEY set: %v", err)
	}
}

// TestRED_CheckAPIKeyParityCases pins CheckAPIKey against New for the
// provider-override, unknown-provider and default-provider cases.
func TestRED_CheckAPIKeyParityCases(t *testing.T) {
	for _, k := range []string{"ODEK_API_KEY", "DEEPSEEK_API_KEY", "OPENAI_API_KEY"} {
		t.Setenv(k, "")
	}
	cases := []Config{
		{Provider: "openai", Providers: map[string]llmclient.ProviderOverride{"openai": {APIKey: "sk-override"}}},
		{Provider: "custom-unknown"},
		{},
	}
	for _, c := range cases {
		checkErr := CheckAPIKey(c)
		c.NoProjectFile, c.MemoryDir, c.Model = true, t.TempDir(), "m"
		a, newErr := New(c)
		if a != nil {
			a.Close()
		}
		newKeyErr := newErr != nil && strings.Contains(newErr.Error(), "no API key")
		if (checkErr != nil) != newKeyErr {
			t.Errorf("provider %q: CheckAPIKey=%v, New=%v", c.Provider, checkErr, newErr)
		}
	}
}
