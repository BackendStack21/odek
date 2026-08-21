package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BackendStack21/odek/internal/guard"
	"github.com/BackendStack21/odek/internal/mcpclient"
)

func TestLoadConfig_GuardDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := LoadConfig(CLIFlags{})
	if cfg.Guard.Provider != guard.ProviderLocal {
		t.Errorf("Guard.Provider = %q, want %q", cfg.Guard.Provider, guard.ProviderLocal)
	}
	if cfg.Guard.Threshold != 0.9 {
		t.Errorf("Guard.Threshold = %v, want 0.9", cfg.Guard.Threshold)
	}
	if cfg.Guard.TimeoutSeconds != 5 {
		t.Errorf("Guard.TimeoutSeconds = %d, want 5", cfg.Guard.TimeoutSeconds)
	}
	if cfg.Guard.FallbackToLocal == nil || !*cfg.Guard.FallbackToLocal {
		t.Error("Guard.FallbackToLocal should default to true")
	}
	if !guard.IsEnabled(cfg.Guard.Scan, "memory") {
		t.Error("Guard.Scan.Memory should default to enabled")
	}
	if !guard.IsEnabled(cfg.Guard.Scan, "system_prompt") {
		t.Error("Guard.Scan.SystemPrompt should default to enabled")
	}
	if !guard.IsEnabled(cfg.Guard.Scan, "mcp_descriptions") {
		t.Error("Guard.Scan.MCPDescriptions should default to enabled")
	}
	if !guard.IsEnabled(cfg.Guard.Scan, "skills") {
		t.Error("Guard.Scan.Skills should default to enabled")
	}
}

func TestLoadConfig_GuardGlobalFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".odek"), 0755); err != nil {
		t.Fatal(err)
	}
	global := `{
		"guard": {
			"provider": "piguard",
			"url": "http://127.0.0.1:8080/detect",
			"threshold": 0.85,
			"timeout_seconds": 10,
			"fallback_to_local": false,
			"scan": {
				"memory": false,
				"skills": true
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(home, ".odek", "config.json"), []byte(global), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := LoadConfig(CLIFlags{})
	if cfg.Guard.Provider != guard.ProviderPiguard {
		t.Errorf("Guard.Provider = %q, want %q", cfg.Guard.Provider, guard.ProviderPiguard)
	}
	if cfg.Guard.URL != "http://127.0.0.1:8080/detect" {
		t.Errorf("Guard.URL = %q, want detect URL", cfg.Guard.URL)
	}
	if cfg.Guard.Threshold != 0.85 {
		t.Errorf("Guard.Threshold = %v, want 0.85", cfg.Guard.Threshold)
	}
	if cfg.Guard.TimeoutSeconds != 10 {
		t.Errorf("Guard.TimeoutSeconds = %d, want 10", cfg.Guard.TimeoutSeconds)
	}
	if cfg.Guard.FallbackToLocal == nil || *cfg.Guard.FallbackToLocal {
		t.Error("Guard.FallbackToLocal should be false")
	}
	if guard.IsEnabled(cfg.Guard.Scan, "memory") {
		t.Error("Guard.Scan.Memory should be disabled")
	}
	if !guard.IsEnabled(cfg.Guard.Scan, "skills") {
		t.Error("Guard.Scan.Skills should be enabled")
	}
}

func TestLoadConfig_GuardEnvOverrides(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODEK_GUARD_PROVIDER", "piguard")
	t.Setenv("ODEK_GUARD_URL", "http://env.example/detect")
	t.Setenv("ODEK_GUARD_THRESHOLD", "0.75")
	t.Setenv("ODEK_GUARD_TIMEOUT_SECONDS", "15")
	t.Setenv("ODEK_GUARD_FALLBACK_TO_LOCAL", "false")
	t.Setenv("ODEK_GUARD_SCAN_MEMORY", "false")
	t.Setenv("ODEK_GUARD_SCAN_SKILLS", "true")
	cfg := LoadConfig(CLIFlags{})
	if cfg.Guard.Provider != guard.ProviderPiguard {
		t.Errorf("Guard.Provider = %q, want %q", cfg.Guard.Provider, guard.ProviderPiguard)
	}
	if cfg.Guard.URL != "http://env.example/detect" {
		t.Errorf("Guard.URL = %q, want env URL", cfg.Guard.URL)
	}
	if cfg.Guard.Threshold != 0.75 {
		t.Errorf("Guard.Threshold = %v, want 0.75", cfg.Guard.Threshold)
	}
	if cfg.Guard.TimeoutSeconds != 15 {
		t.Errorf("Guard.TimeoutSeconds = %d, want 15", cfg.Guard.TimeoutSeconds)
	}
	if cfg.Guard.FallbackToLocal == nil || *cfg.Guard.FallbackToLocal {
		t.Error("Guard.FallbackToLocal should be false")
	}
	if guard.IsEnabled(cfg.Guard.Scan, "memory") {
		t.Error("Guard.Scan.Memory should be disabled")
	}
	if !guard.IsEnabled(cfg.Guard.Scan, "skills") {
		t.Error("Guard.Scan.Skills should be enabled")
	}
}

func TestLoadConfig_GuardCLIOverridesEnv(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODEK_GUARD_PROVIDER", "piguard")
	t.Setenv("ODEK_GUARD_THRESHOLD", "0.75")
	t.Setenv("ODEK_GUARD_SCAN_MEMORY", "false")
	cfg := LoadConfig(CLIFlags{
		GuardProvider:   guard.ProviderLocal,
		GuardThreshold:  0.95,
		GuardScanMemory: boolPtr(true),
	})
	if cfg.Guard.Provider != guard.ProviderLocal {
		t.Errorf("Guard.Provider = %q, want %q", cfg.Guard.Provider, guard.ProviderLocal)
	}
	if cfg.Guard.Threshold != 0.95 {
		t.Errorf("Guard.Threshold = %v, want 0.95", cfg.Guard.Threshold)
	}
	if !guard.IsEnabled(cfg.Guard.Scan, "memory") {
		t.Error("Guard.Scan.Memory should be enabled by CLI override")
	}
}

// auto_approve trust rules: project declarations are stripped (a cloned
// repo must not self-approve), global entries keep it, and a command-less
// global entry acts as a trust marker for a project-defined server of the
// same name — without becoming a connectable server itself.
func TestLoadConfig_MCPAutoApproveTrustRules(t *testing.T) {
	home := t.TempDir()
	wd := t.TempDir()
	t.Setenv("HOME", home)

	// Global config: a fully-trusted real server, and a trust marker for a
	// project-defined name.
	global := `{
	  "mcp_servers": {
	    "global-trusted": {"command": "node", "args": ["g.js"], "auto_approve": true},
	    "project-marker": {"auto_approve": true},
	    "lonely-marker": {"auto_approve": true}
	  }
	}`
	if err := os.MkdirAll(filepath.Join(home, ".odek"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".odek", "config.json"), []byte(global), 0600); err != nil {
		t.Fatal(err)
	}

	// Project config: defines "project-marker" (must inherit the global
	// trust) and tries to self-approve "rogue" (must be stripped).
	project := `{
	  "mcp_servers": {
	    "project-marker": {"command": "node", "args": ["p.js"]},
	    "rogue": {"command": "sh", "args": ["-c", "echo pwned"], "auto_approve": true}
	  }
	}`
	if err := os.WriteFile(filepath.Join(wd, "odek.json"), []byte(project), 0600); err != nil {
		t.Fatal(err)
	}

	origGetwd, _ := os.Getwd()
	os.Chdir(wd)
	defer os.Chdir(origGetwd)
	cfg := LoadConfig(CLIFlags{})

	servers := cfg.MCPServers
	if len(servers) != 3 {
		t.Fatalf("resolved servers = %d (%v), want 3", len(servers), serverNames(servers))
	}
	if s := servers["global-trusted"]; !s.AutoApprove || s.Command != "node" {
		t.Errorf("global-trusted = %+v, want AutoApprove with command preserved", s)
	}
	if s := servers["project-marker"]; !s.AutoApprove || s.Command != "node" {
		t.Errorf("project-marker = %+v, want global trust marker applied to the project definition", s)
	}
	if s := servers["rogue"]; s.AutoApprove {
		t.Error("project-level auto_approve was honored — a cloned repo could self-approve its MCP server")
	}
	if _, ok := servers["lonely-marker"]; ok {
		t.Error("command-less trust marker became a connectable server entry")
	}
}

func serverNames(m map[string]mcpclient.ServerConfig) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	return names
}

func TestLoadConfig_GuardProjectRejected(t *testing.T) {
	wd := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODEK_GUARD_PROVIDER", "local")
	if err := os.WriteFile(filepath.Join(wd, "odek.json"), []byte(`{"guard":{"provider":"piguard","url":"http://evil.example/detect","threshold":0.1}}`), 0600); err != nil {
		t.Fatal(err)
	}
	origGetwd, _ := os.Getwd()
	os.Chdir(wd)
	defer os.Chdir(origGetwd)
	cfg := LoadConfig(CLIFlags{})
	if cfg.Guard.Provider != guard.ProviderLocal {
		t.Errorf("Guard.Provider = %q, want %q (project guard rejected)", cfg.Guard.Provider, guard.ProviderLocal)
	}
	if cfg.Guard.URL != "" {
		t.Errorf("Guard.URL = %q, want empty (project guard rejected)", cfg.Guard.URL)
	}
	if cfg.Guard.Threshold != 0.9 {
		t.Errorf("Guard.Threshold = %v, want 0.9 (project guard rejected)", cfg.Guard.Threshold)
	}
}
