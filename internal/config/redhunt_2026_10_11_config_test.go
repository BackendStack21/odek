package config

import (
	"os"
	"testing"
)

// A global planning.remind:true is the documented way to opt into the soft
// plan reminder; the merge must carry Remind like the other planning fields.
func TestRED_PlanningRemindFromGlobalConfigIsApplied(t *testing.T) {
	dir := redIsolate(t)
	writeGlobalConfig(t, dir, `{"planning":{"remind":true}}`)
	cfg := LoadConfig(CLIFlags{})
	if !cfg.Planning.Remind {
		t.Errorf("planning.remind=true in ~/.odek/config.json resolved to Remind=%v, want true", cfg.Planning.Remind)
	}
}

// A project planning.remind:true applies when the operator set nothing, and
// the global-off rule still beats a project opt-in.
func TestRED_PlanningRemindProjectMergeAndClamp(t *testing.T) {
	dir := redIsolate(t)
	if err := os.WriteFile("odek.json", []byte(`{"planning":{"remind":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if cfg := LoadConfig(CLIFlags{}); !cfg.Planning.Remind {
		t.Errorf("project planning.remind=true without a global setting resolved to Remind=false")
	}
	writeGlobalConfig(t, dir, `{"planning":{"remind":false}}`)
	if cfg := LoadConfig(CLIFlags{}); cfg.Planning.Remind {
		t.Errorf("project planning.remind=true overrode a global remind=false")
	}
}

// A tab before an inline comment is whitespace exactly like a space; the
// comment text must not become part of the secret.
func TestRED_SecretsEnvTabInlineCommentStripped(t *testing.T) {
	t.Setenv("SE_RED_TABCOMMENT", "")
	writeSecretsAndLoad(t, "SE_RED_TABCOMMENT=sk-live-123\t# rotated 2026-10\n")
	if got := os.Getenv("SE_RED_TABCOMMENT"); got != "sk-live-123" {
		t.Errorf("SE_RED_TABCOMMENT = %q, want %q", got, "sk-live-123")
	}
	t.Setenv("SE_RED_HASHINVALUE", "")
	writeSecretsAndLoad(t, "SE_RED_HASHINVALUE=abc#def\n")
	if got := os.Getenv("SE_RED_HASHINVALUE"); got != "abc#def" {
		t.Errorf("SE_RED_HASHINVALUE = %q, want %q ('#' without preceding whitespace is part of the value)", got, "abc#def")
	}
}
