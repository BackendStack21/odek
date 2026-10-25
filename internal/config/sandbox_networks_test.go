package config

import (
	"reflect"
	"testing"
)

func networksFixture(t *testing.T, global, project string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODEK_SANDBOX_NETWORKS", "")
	proj := t.TempDir()
	t.Chdir(proj)
	if global != "" {
		writeGlobalConfig(t, home, global)
	}
	if project != "" {
		writeProjectConfig(t, proj, project)
	}
}

func TestSandboxNetworks_DefaultEmpty(t *testing.T) {
	networksFixture(t, "", "")
	cfg := LoadConfig(CLIFlags{})
	if len(cfg.SandboxNetworks) != 0 {
		t.Errorf("SandboxNetworks = %v, want empty", cfg.SandboxNetworks)
	}
	if cfg.ProjectSandboxOverride.HasNetworks {
		t.Error("no project config: HasNetworks must be false")
	}
}

func TestSandboxNetworks_FileEnvCLIPriority(t *testing.T) {
	networksFixture(t, `{"sandbox_networks": ["file_a", "file_b"]}`, "")
	if got := LoadConfig(CLIFlags{}).SandboxNetworks; !reflect.DeepEqual(got, []string{"file_a", "file_b"}) {
		t.Errorf("file: SandboxNetworks = %v", got)
	}
	t.Setenv("ODEK_SANDBOX_NETWORKS", "env_a, env_b ,,env_c")
	if got := LoadConfig(CLIFlags{}).SandboxNetworks; !reflect.DeepEqual(got, []string{"env_a", "env_b", "env_c"}) {
		t.Errorf("env over file (comma split, trimmed, empties dropped): SandboxNetworks = %v", got)
	}
	got := LoadConfig(CLIFlags{SandboxNetworks: []string{"cli_a"}}).SandboxNetworks
	if !reflect.DeepEqual(got, []string{"cli_a"}) {
		t.Errorf("cli over env: SandboxNetworks = %v", got)
	}
}

func TestSandboxNetworks_ProjectUnionsWithGlobalAndNeedsApproval(t *testing.T) {
	networksFixture(t, `{"sandbox_networks": ["global_net"]}`, `{"sandbox_networks": ["project_net"]}`)
	cfg := LoadConfig(CLIFlags{})
	if !reflect.DeepEqual(cfg.SandboxNetworks, []string{"global_net", "project_net"}) {
		t.Errorf("project networks must union-merge after the global ones, got %v", cfg.SandboxNetworks)
	}
	o := cfg.ProjectSandboxOverride
	if !o.HasNetworks || !reflect.DeepEqual(o.Networks, []string{"project_net"}) {
		t.Errorf("override = %+v, want HasNetworks with the project's own list", o)
	}
}

func TestSandboxNetworks_ProjectOnly(t *testing.T) {
	networksFixture(t, "", `{"sandbox_networks": ["only_net"]}`)
	cfg := LoadConfig(CLIFlags{})
	if !reflect.DeepEqual(cfg.SandboxNetworks, []string{"only_net"}) {
		t.Errorf("SandboxNetworks = %v", cfg.SandboxNetworks)
	}
	if !cfg.ProjectSandboxOverride.HasNetworks {
		t.Error("a project sandbox_networks must be reported for operator approval, not stripped")
	}
}

func TestSandboxNetworks_GlobalOnlyIsNotAnOverride(t *testing.T) {
	networksFixture(t, `{"sandbox_networks": ["g"]}`, "")
	if LoadConfig(CLIFlags{}).ProjectSandboxOverride.HasNetworks {
		t.Error("global networks are operator-set and need no approval")
	}
}

func TestSandboxNetworks_EnvAndCLIReplaceProjectList(t *testing.T) {
	networksFixture(t, "", `{"sandbox_networks": ["project_net"]}`)
	t.Setenv("ODEK_SANDBOX_NETWORKS", "env_net")
	if got := LoadConfig(CLIFlags{}).SandboxNetworks; !reflect.DeepEqual(got, []string{"env_net"}) {
		t.Errorf("env replaces file layers: got %v", got)
	}
}
