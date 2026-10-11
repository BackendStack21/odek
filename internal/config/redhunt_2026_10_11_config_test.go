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
