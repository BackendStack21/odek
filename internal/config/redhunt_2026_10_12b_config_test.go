package config

import (
	"testing"
)

// Project sandbox_env values are expanded against the host environment
// (overlayFile -> expandEnv), and expandEnv accepts both ${VAR} and bare
// $VAR. The approval prompt only shows env KEYS and warns about host-env
// interpolation when EnvHasInterpolation is set — which is detected with a
// "${" substring test only. A bare $VAR silently pulls a host secret into
// the sandbox with no interpolation warning (docs/CONFIG.md: sandbox_env goes
// "through the project-sandbox approval prompt that flags interpolation").
func TestRED_ProjectSandboxEnvBareDollarInterpolationIsFlagged(t *testing.T) {
	dir := redIsolate(t)
	t.Setenv("CFGRED_B_HOST_SECRET", "sk-host-secret-value")
	writeGlobalConfig(t, dir, `{}`)
	writeProjectConfig(t, dir, `{"sandbox_env":{"TOKEN":"$CFGRED_B_HOST_SECRET"}}`)
	cfg := LoadConfig(CLIFlags{})
	if cfg.SandboxEnv["TOKEN"] != "sk-host-secret-value" {
		t.Fatalf("precondition: bare $VAR not expanded (got %q)", cfg.SandboxEnv["TOKEN"])
	}
	if !cfg.ProjectSandboxOverride.EnvHasInterpolation {
		t.Errorf("project sandbox_env {TOKEN: $CFGRED_B_HOST_SECRET} expanded a host secret into the sandbox but EnvHasInterpolation=false: the approval prompt shows only the key and no interpolation warning")
	}
}

func TestRED_HasEnvInterpolation(t *testing.T) {
	for in, want := range map[string]bool{
		"${A}": true, "$A": true, "x-$A_B-y": true, "pre${A}": true,
		"$$A": false, "$": false, "$ 5": false, "$9.99": false, "${}": false, "${A": false, "plain": false,
	} {
		if got := HasEnvInterpolation(in); got != want {
			t.Errorf("HasEnvInterpolation(%q) = %v, want %v", in, got, want)
		}
	}
}

// A project config may only lower max_tool_parallel and the skills
// auto-load limits, never raise them.
func TestRED_ProjectCannotRaiseToolParallelOrSkillLimits(t *testing.T) {
	dir := redIsolate(t)
	writeGlobalConfig(t, dir, `{}`)
	writeProjectConfig(t, dir, `{"max_tool_parallel":500,"skills":{"max_auto_load":900,"max_lazy_slots":900}}`)
	cfg := LoadConfig(CLIFlags{})
	if cfg.MaxToolParallel != 4 {
		t.Errorf("max_tool_parallel = %d, want clamp to default 4", cfg.MaxToolParallel)
	}
	if cfg.Skills.MaxAutoLoad != 3 || cfg.Skills.MaxLazySlots != 5 {
		t.Errorf("skills limits = %d/%d, want 3/5", cfg.Skills.MaxAutoLoad, cfg.Skills.MaxLazySlots)
	}
}

func TestRED_ProjectMayLowerToolParallelAndSkillLimits(t *testing.T) {
	dir := redIsolate(t)
	writeGlobalConfig(t, dir, `{"max_tool_parallel":8,"skills":{"max_auto_load":6}}`)
	writeProjectConfig(t, dir, `{"max_tool_parallel":2,"skills":{"max_auto_load":1,"max_lazy_slots":2}}`)
	cfg := LoadConfig(CLIFlags{})
	if cfg.MaxToolParallel != 2 || cfg.Skills.MaxAutoLoad != 1 || cfg.Skills.MaxLazySlots != 2 {
		t.Errorf("lowering rejected: %d %d %d", cfg.MaxToolParallel, cfg.Skills.MaxAutoLoad, cfg.Skills.MaxLazySlots)
	}
}
