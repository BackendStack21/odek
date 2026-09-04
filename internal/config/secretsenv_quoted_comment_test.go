package config

import (
	"os"
	"testing"
)

// Bug-sweep 2026-09-04: quote-strip and comment-strip were exclusive
// branches — a QUOTED value followed by an inline comment kept its quotes:
// KEY="val" # comment  →  env value `"val"` (with quotes) → silent auth
// 401s. Also: `export<TAB>KEY=v` was not recognized as an export line.
func TestSecretsEnv_QuotedValueWithInlineComment(t *testing.T) {
	t.Setenv("SE_MIX_Q", "")
	t.Setenv("SE_MIX_S", "")
	t.Setenv("SE_MIX_TAB", "")
	writeSecretsAndLoad(t, "SE_MIX_Q=\"val\" # comment\n"+
		"SE_MIX_S='single' # comment\n"+
		"export\tSE_MIX_TAB=tabbed\n")

	if got := envOrEmpty("SE_MIX_Q"); got != "val" {
		t.Errorf("SE_MIX_Q = %q, want %q (quotes must not survive a trailing comment)", got, "val")
	}
	if got := envOrEmpty("SE_MIX_S"); got != "single" {
		t.Errorf("SE_MIX_S = %q, want %q", got, "single")
	}
	if got := envOrEmpty("SE_MIX_TAB"); got != "tabbed" {
		t.Errorf("SE_MIX_TAB = %q, want %q (export<TAB> must be recognized)", got, "tabbed")
	}
}

func envOrEmpty(k string) string {
	return os.Getenv(k)
}
