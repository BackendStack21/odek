package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// legacyLocalInitTemplate is the ./odek.json that `odek init` wrote up to
// v2.34.9. Those files are still in users' projects.
const legacyLocalInitTemplate = `{
  "model": "",
  "thinking": "",
  "max_iterations": 0,
  "max_tool_parallel": 0,
  "interaction_mode": "",
  "prompt_caching": null,
  "stream": null,
  "compaction": null,
  "announce_budget": null,
  "no_color": false,
  "no_agents": false,
  "planning": {
    "enabled": null,
    "max_steps": 0,
    "max_render_chars": 0
  },
  "limits": {
    "max_runtime_seconds": 0,
    "max_tool_calls": 0,
    "max_input_tokens": 0,
    "max_output_tokens": 0,
    "max_cost_usd": 0
  },
  "sandbox_image": "",
  "sandbox_network": "none",
  "sandbox_memory": "",
  "sandbox_cpus": "",
  "sandbox_user": "",
  "sandbox_env": {},
  "sandbox_volumes": [],
  "tools": {
    "disabled": []
  },
  "skills": {
    "max_auto_load": 3,
    "max_lazy_slots": 5,
    "verbose": false
  },
  "mcp_servers": {}
}`

// TestRED_LegacyLocalInitFileIsNeutral: a ./odek.json written by an older
// `odek init` must load like no project file at all for the sandbox and
// planning settings: "sandbox_network": "none" (the strictest mode, the
// default) is not a sandbox override needing approval, and planning 0
// placeholders are not clamped down to the minimum.
func TestRED_LegacyLocalInitFileIsNeutral(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)
	writeGlobalConfig(t, dir, `{"planning":{"max_steps":12,"max_render_chars":2000}}`)
	writeProjectConfig(t, dir, legacyLocalInitTemplate)
	for _, f := range []string{filepath.Join(dir, ".odek", "config.json"), filepath.Join(dir, "odek.json")} {
		if err := os.Chmod(f, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var cfg ResolvedConfig
	stderr := captureLoaderStderr(t, func() { cfg = LoadConfig(CLIFlags{}) })
	if cfg.ProjectSandboxOverride.HasNetwork {
		t.Error(`legacy "sandbox_network": "none" counts as a project sandbox override (needs approval)`)
	}
	if cfg.SandboxNetwork != DefaultSandboxNetwork {
		t.Errorf("sandbox_network = %q, want %q", cfg.SandboxNetwork, DefaultSandboxNetwork)
	}
	if cfg.Planning.MaxSteps != 12 || cfg.Planning.MaxRenderChars != 2000 {
		t.Errorf("legacy planning zeros changed planning to steps=%d render=%d, want 12/2000", cfg.Planning.MaxSteps, cfg.Planning.MaxRenderChars)
	}
	if strings.Contains(stderr, "WARNING") {
		t.Errorf("legacy local init file produced warnings:\n%s", stderr)
	}
}

// TestRED_ProjectNetworkOtherThanNoneStillNeedsApproval: only the strictest
// mode is exempt; any other project network value is still an override.
func TestRED_ProjectNetworkOtherThanNoneStillNeedsApproval(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)
	writeProjectConfig(t, dir, `{"sandbox_network":"bridge"}`)
	if !LoadConfig(CLIFlags{}).ProjectSandboxOverride.HasNetwork {
		t.Error(`project "sandbox_network": "bridge" is not flagged as a sandbox override`)
	}
}
