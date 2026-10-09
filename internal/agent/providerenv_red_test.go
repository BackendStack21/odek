package agent

import (
	"testing"
)

// Config.APIKey doc: "Empty falls back to the provider's env key". The
// error text also advertises "the provider env key". A non-default provider
// with its env key set must construct.
func TestRED_ProviderEnvKeyFallbackNonDefaultProvider(t *testing.T) {
	t.Setenv("ODEK_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "sk-openai-env")
	t.Setenv("ANTHROPIC_API_KEY", "sk-anth-env")
	for _, p := range []string{"openai", "anthropic"} {
		a, err := New(Config{Provider: p, Model: "m", NoProjectFile: true, MemoryDir: t.TempDir()})
		if err != nil {
			t.Fatalf("provider %s with its env key set: New failed: %v", p, err)
		}
		a.Close()
	}
}

func TestProviderKeyEnv_Table(t *testing.T) {
	for p, want := range map[string]string{
		"deepseek": "DEEPSEEK_API_KEY", "openai": "OPENAI_API_KEY", "anthropic": "ANTHROPIC_API_KEY",
		"gemini": "GEMINI_API_KEY", "zai": "ZAI_API_KEY", "kimi": "KIMI_API_KEY", "legacy": "DEEPSEEK_API_KEY",
	} {
		got := providerKeyEnv(p)
		if len(got) == 0 || got[0] != want {
			t.Errorf("%s: %v, want first %s", p, got, want)
		}
	}
	if providerKeyEnv("nope") != nil {
		t.Error("unknown provider must have no env key")
	}
}

func TestNew_UnknownProviderWithoutKeyStillFails(t *testing.T) {
	t.Setenv("ODEK_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "sk-x")
	t.Setenv("ANTHROPIC_API_KEY", "")
	if _, err := New(Config{Provider: "anthropic", Model: "m", NoProjectFile: true, MemoryDir: t.TempDir()}); err == nil {
		t.Fatal("anthropic must not borrow OPENAI_API_KEY")
	}
}
