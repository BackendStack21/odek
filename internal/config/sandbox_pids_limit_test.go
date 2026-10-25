package config

import "testing"

func pidsFixture(t *testing.T, global, project string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODEK_SANDBOX_PIDS_LIMIT", "")
	proj := t.TempDir()
	t.Chdir(proj)
	if global != "" {
		writeGlobalConfig(t, home, global)
	}
	if project != "" {
		writeProjectConfig(t, proj, project)
	}
}

func TestSandboxPidsLimit_DefaultEmpty(t *testing.T) {
	pidsFixture(t, "", "")
	if got := LoadConfig(CLIFlags{}).SandboxPidsLimit; got != "" {
		t.Errorf("SandboxPidsLimit = %q, want empty (default applied downstream)", got)
	}
}

func TestSandboxPidsLimit_FileEnvCLIPriority(t *testing.T) {
	pidsFixture(t, `{"sandbox_pids_limit": "300"}`, "")
	if got := LoadConfig(CLIFlags{}).SandboxPidsLimit; got != "300" {
		t.Errorf("file: SandboxPidsLimit = %q, want 300", got)
	}
	t.Setenv("ODEK_SANDBOX_PIDS_LIMIT", "400")
	if got := LoadConfig(CLIFlags{}).SandboxPidsLimit; got != "400" {
		t.Errorf("env over file: SandboxPidsLimit = %q, want 400", got)
	}
	if got := LoadConfig(CLIFlags{SandboxPidsLimit: "500"}).SandboxPidsLimit; got != "500" {
		t.Errorf("cli over env: SandboxPidsLimit = %q, want 500", got)
	}
}

func TestSandboxPidsLimit_InvalidValueKeptForDownstreamRejection(t *testing.T) {
	pidsFixture(t, "", "")
	t.Setenv("ODEK_SANDBOX_PIDS_LIMIT", "abc")
	if got := LoadConfig(CLIFlags{}).SandboxPidsLimit; got != "abc" {
		t.Errorf("SandboxPidsLimit = %q, want abc preserved so the sandbox start rejects it", got)
	}
}

func TestSandboxPidsLimit_ProjectMayOnlyLower(t *testing.T) {
	cases := []struct {
		name, global, project, want string
		wantOverride                bool
	}{
		{"project lower than global", `{"sandbox_pids_limit": "1000"}`, `{"sandbox_pids_limit": "200"}`, "200", true},
		{"project higher than global", `{"sandbox_pids_limit": "200"}`, `{"sandbox_pids_limit": "1000"}`, "200", true},
		{"project equal to global", `{"sandbox_pids_limit": "200"}`, `{"sandbox_pids_limit": "200"}`, "200", true},
		{"project lower than built-in default", ``, `{"sandbox_pids_limit": "100"}`, "100", true},
		{"project above built-in default", ``, `{"sandbox_pids_limit": "9999"}`, "4096", true},
		{"global only", `{"sandbox_pids_limit": "700"}`, ``, "700", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pidsFixture(t, tc.global, tc.project)
			cfg := LoadConfig(CLIFlags{})
			if cfg.SandboxPidsLimit != tc.want {
				t.Errorf("SandboxPidsLimit = %q, want %q", cfg.SandboxPidsLimit, tc.want)
			}
			o := cfg.ProjectSandboxOverride
			if o.HasPidsLimit != tc.wantOverride {
				t.Errorf("HasPidsLimit = %v, want %v", o.HasPidsLimit, tc.wantOverride)
			}
			if tc.wantOverride && o.PidsLimit != tc.want {
				t.Errorf("override PidsLimit = %q, want clamped %q", o.PidsLimit, tc.want)
			}
		})
	}
}

func TestSandboxPidsLimit_EnvAndCLIAreOperatorSurfaces(t *testing.T) {
	pidsFixture(t, `{"sandbox_pids_limit": "200"}`, `{"sandbox_pids_limit": "100"}`)
	t.Setenv("ODEK_SANDBOX_PIDS_LIMIT", "8000")
	if got := LoadConfig(CLIFlags{}).SandboxPidsLimit; got != "8000" {
		t.Errorf("env must be able to raise above file values: got %q", got)
	}
}
