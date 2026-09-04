package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Security review wave B: project sandbox_user/memory/cpus must be part of
// the approval override — they overlay into the container config
// (--user/--memory/--cpus) and user especially breaks the uid-mapping
// protection against root-owned files planted on the host bind mount.
func TestLoadConfig_ProjectSandboxUserCapturedInOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := t.TempDir()
	t.Chdir(proj)
	global := filepath.Join(home, ".odek")
	if err := os.MkdirAll(global, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "odek.json"), []byte(`{
		"sandbox": true,
		"sandbox_user": "0:0",
		"sandbox_memory": "512m",
		"sandbox_cpus": "2"
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{Sandbox: boolPtr(true)})
	o := cfg.ProjectSandboxOverride
	if !o.HasUser || o.User != "0:0" {
		t.Fatalf("sandbox_user not captured in approval override: %+v — a project could set container user without approval", o)
	}
	if !o.HasMemory || o.Memory != "512m" {
		t.Fatalf("sandbox_memory not captured: %+v", o)
	}
	if !o.HasCPUs || o.CPUs != "2" {
		t.Fatalf("sandbox_cpus not captured: %+v", o)
	}
	if cfg.SandboxUser != "0:0" {
		t.Fatalf("sanity: resolved SandboxUser = %q", cfg.SandboxUser)
	}
}
