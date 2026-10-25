package config

import "testing"

func composeFixture(t *testing.T, global, project string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODEK_SANDBOX_COMPOSE_PROJECT", "")
	proj := t.TempDir()
	t.Chdir(proj)
	if global != "" {
		writeGlobalConfig(t, home, global)
	}
	if project != "" {
		writeProjectConfig(t, proj, project)
	}
}

func TestSandboxComposeProject_DefaultEmpty(t *testing.T) {
	composeFixture(t, "", "")
	cfg := LoadConfig(CLIFlags{})
	if cfg.SandboxComposeProject != "" {
		t.Errorf("SandboxComposeProject = %q, want empty", cfg.SandboxComposeProject)
	}
	if cfg.ProjectSandboxOverride.HasComposeProject {
		t.Error("no project config: HasComposeProject must be false")
	}
}

func TestSandboxComposeProject_FileEnvCLIPriority(t *testing.T) {
	composeFixture(t, `{"sandbox_compose_project": "file_p"}`, "")
	if got := LoadConfig(CLIFlags{}).SandboxComposeProject; got != "file_p" {
		t.Errorf("file: SandboxComposeProject = %q", got)
	}
	t.Setenv("ODEK_SANDBOX_COMPOSE_PROJECT", "env_p")
	if got := LoadConfig(CLIFlags{}).SandboxComposeProject; got != "env_p" {
		t.Errorf("env over file: SandboxComposeProject = %q", got)
	}
	if got := LoadConfig(CLIFlags{SandboxComposeProject: "cli_p"}).SandboxComposeProject; got != "cli_p" {
		t.Errorf("cli over env: SandboxComposeProject = %q", got)
	}
}

func TestSandboxComposeProject_ProjectOverridesAndNeedsApproval(t *testing.T) {
	composeFixture(t, `{"sandbox_compose_project": "global_p"}`, `{"sandbox_compose_project": "project_p"}`)
	cfg := LoadConfig(CLIFlags{})
	if cfg.SandboxComposeProject != "project_p" {
		t.Errorf("project value must be applied (pending approval), got %q", cfg.SandboxComposeProject)
	}
	o := cfg.ProjectSandboxOverride
	if !o.HasComposeProject || o.ComposeProject != "project_p" {
		t.Errorf("override = %+v, want HasComposeProject with project_p", o)
	}
}

func TestSandboxComposeProject_ProjectOnlyIsNotStripped(t *testing.T) {
	composeFixture(t, "", `{"sandbox_compose_project": "only_p"}`)
	cfg := LoadConfig(CLIFlags{})
	if cfg.SandboxComposeProject != "only_p" || !cfg.ProjectSandboxOverride.HasComposeProject {
		t.Errorf("project compose project must flow to approval, not be stripped: %q %+v",
			cfg.SandboxComposeProject, cfg.ProjectSandboxOverride)
	}
}

func TestSandboxComposeProject_GlobalOnlyIsNotAnOverride(t *testing.T) {
	composeFixture(t, `{"sandbox_compose_project": "g"}`, "")
	if LoadConfig(CLIFlags{}).ProjectSandboxOverride.HasComposeProject {
		t.Error("a global compose project is operator-set and needs no approval")
	}
}

func TestSandboxComposeProject_EnvAndCLIReplaceProjectValue(t *testing.T) {
	composeFixture(t, "", `{"sandbox_compose_project": "project_p"}`)
	t.Setenv("ODEK_SANDBOX_COMPOSE_PROJECT", "env_p")
	if got := LoadConfig(CLIFlags{}).SandboxComposeProject; got != "env_p" {
		t.Errorf("env replaces file layers: got %q", got)
	}
}
