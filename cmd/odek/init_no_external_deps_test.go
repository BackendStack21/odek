package main

import (
	"io"
	"testing"

	"github.com/BackendStack21/odek/internal/config"
)

// initScratch runs `odek init --global` and `odek init` in a fresh HOME and
// project directory, so tests observe exactly what a new user gets.
func initScratch(t *testing.T) (home, proj string) {
	t.Helper()
	home, proj = t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(proj)
	if err := initConfig([]string{"--global"}); err != nil {
		t.Fatalf("init --global: %v", err)
	}
	if err := initConfig(nil); err != nil {
		t.Fatalf("init: %v", err)
	}
	return home, proj
}

// TestRED_FreshLocalInitNeedsNoSandboxApproval: a freshly generated
// ./odek.json must not count as a project sandbox override. It used to pin
// "sandbox_network": "none", which made every non-interactive run in that
// project fail with "requires explicit approval".
func TestRED_FreshLocalInitNeedsNoSandboxApproval(t *testing.T) {
	initScratch(t)
	t.Setenv("ODEK_APPROVE_PROJECT_SANDBOX", "")
	resolved := config.LoadConfig(config.CLIFlags{})
	if o := resolved.ProjectSandboxOverride; o.HasEnv || o.HasImage || o.HasNetwork || o.HasVolumes || o.HasUser || o.HasMemory || o.HasCPUs {
		t.Errorf("fresh odek init produced a project sandbox override: %+v", o)
	}
	if err := approveProjectSandboxWithTTY(resolved, nil, io.Discard, false); err != nil {
		t.Errorf("fresh odek init needs sandbox approval without a TTY: %v", err)
	}
}

// TestRED_FreshLocalInitInheritsGlobalValues: the local template documents
// knobs in inherit-neutral form only, so adding it must not change the
// resolved planning or skills settings (zeros used to clamp planning down to
// its minimum, and pinned skills values overrode the operator's).
func TestRED_FreshLocalInitInheritsGlobalValues(t *testing.T) {
	initScratch(t)
	withLocal := config.LoadConfig(config.CLIFlags{})

	t.Chdir(t.TempDir()) // same global config, no ./odek.json
	globalOnly := config.LoadConfig(config.CLIFlags{})

	if withLocal.Planning.MaxSteps != globalOnly.Planning.MaxSteps ||
		withLocal.Planning.MaxRenderChars != globalOnly.Planning.MaxRenderChars {
		t.Errorf("local template changed planning: steps %d→%d, render %d→%d",
			globalOnly.Planning.MaxSteps, withLocal.Planning.MaxSteps,
			globalOnly.Planning.MaxRenderChars, withLocal.Planning.MaxRenderChars)
	}
	if withLocal.Skills.MaxAutoLoad != globalOnly.Skills.MaxAutoLoad ||
		withLocal.Skills.MaxLazySlots != globalOnly.Skills.MaxLazySlots {
		t.Errorf("local template changed skills: auto_load %d→%d, lazy %d→%d",
			globalOnly.Skills.MaxAutoLoad, withLocal.Skills.MaxAutoLoad,
			globalOnly.Skills.MaxLazySlots, withLocal.Skills.MaxLazySlots)
	}
}
