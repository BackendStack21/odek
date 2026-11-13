package config

import (
	"slices"
	"testing"
	"time"

	sdk "github.com/BackendStack21/go-llm-sdk"
)

// The operator turns project AGENTS.md loading off in ~/.odek/config.json
// ("no_agents": true) so an untrusted repository's AGENTS.md never reaches
// the system prompt. The project ./odek.json is untrusted and may only narrow
// settings (docs/CONFIG.md: "Projects can only narrow things ... never widen
// them"), exactly like sandbox=false / sandbox_readonly=false, which the
// loader ignores. But overlayFile copies any non-nil project no_agents, so a
// cloned repo that ships {"no_agents": false} next to its AGENTS.md gets that
// file loaded into the trusted system prompt again. `odek init` writes the
// same "no_agents": false into every fresh ./odek.json, so even a benign
// project config silently re-enables it — contradicting the documented
// "every value is in inherit-neutral form ... adding a fresh ./odek.json
// changes nothing".
func TestRED_ProjectNoAgentsFalseReenablesAgentsMD(t *testing.T) {
	dir := redIsolate(t)
	t.Setenv("ODEK_NO_AGENTS", "")
	writeGlobalConfig(t, dir, `{"no_agents": true}`)
	// Exactly the line odek init's local template writes.
	writeProjectConfig(t, dir, `{"no_agents": false}`)

	cfg := LoadConfig(CLIFlags{})
	if !cfg.NoAgents {
		t.Errorf("NoAgents = false: project ./odek.json re-enabled AGENTS.md loading the operator disabled in ~/.odek/config.json")
	}
}

// docs/CONFIG.md documents ODEK_STREAM_IDLE_TIMEOUT_SECONDS as the env form
// of llm.stream_idle_timeout_seconds (and the project-llm warning tells the
// operator to use it). LoadConfig passes the already-prefixed name to
// envIntPtr, which prepends "ODEK_" again, so the variable actually read is
// ODEK_ODEK_STREAM_IDLE_TIMEOUT_SECONDS and the documented one is ignored:
// the SSE idle watchdog stays at the 300 s default whatever the operator sets.
func TestRED_StreamIdleTimeoutEnvIsHonoured(t *testing.T) {
	redIsolate(t)
	prev := sdk.StreamIdleTimeout()
	t.Cleanup(func() { sdk.SetStreamIdleTimeout(prev) })

	t.Setenv("ODEK_STREAM_IDLE_TIMEOUT_SECONDS", "42")
	LoadConfig(CLIFlags{})
	if got := sdk.StreamIdleTimeout(); got != 42*time.Second {
		t.Errorf("stream idle timeout = %v with ODEK_STREAM_IDLE_TIMEOUT_SECONDS=42, want 42s", got)
	}
}

// docs/CONFIG.md (tools): "`disabled` is merged across layers"; CLI.md:
// --no-tool "Merges with lower-priority disabled lists". The CLI layer
// appends, but the ODEK_TOOLS_DISABLED env layer REPLACES the list, dropping
// every tool the operator's global config and the project config disabled.
// An operator who disabled shell in ~/.odek/config.json and sets
// ODEK_TOOLS_DISABLED=browser for one pipeline gets shell back.
func TestRED_EnvToolsDisabledMergesWithFileLayers(t *testing.T) {
	dir := redIsolate(t)
	writeGlobalConfig(t, dir, `{"tools": {"disabled": ["shell"]}}`)
	writeProjectConfig(t, dir, `{"tools": {"disabled": ["read_file"]}}`)
	t.Setenv("ODEK_TOOLS_DISABLED", "browser")

	cfg := LoadConfig(CLIFlags{})
	for _, want := range []string{"shell", "read_file", "browser"} {
		if !slices.Contains(cfg.Tools.Disabled, want) {
			t.Errorf("Tools.Disabled = %v: %q from a lower layer was dropped by ODEK_TOOLS_DISABLED", cfg.Tools.Disabled, want)
		}
	}
}
