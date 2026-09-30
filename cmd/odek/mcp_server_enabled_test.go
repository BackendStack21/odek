package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/mcpclient"
)

// mcpServerEnabledDefault: an entry without "enabled" must stay enabled
// (back-compat) and a "enabled": false entry must resolve as disabled.
func TestMCPServerEnabledResolution(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".odek"), 0755); err != nil {
		t.Fatal(err)
	}
	cfgJSON := `{
	  "mcp_servers": {
	    "on":  {"command": "echo"},
	    "off": {"command": "echo", "enabled": false}
	  }
	}`
	if err := os.WriteFile(filepath.Join(home, ".odek", "config.json"), []byte(cfgJSON), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	resolved := config.LoadConfig(config.CLIFlags{})
	on, ok := resolved.MCPServers["on"]
	if !ok {
		t.Fatal("enabled-by-default server missing from resolved config")
	}
	if on.IsDisabled() {
		t.Error("server without enabled field must default to enabled")
	}
	off, ok := resolved.MCPServers["off"]
	if !ok {
		t.Fatal("disabled server missing from resolved config (should stay configured, just not run)")
	}
	if !off.IsDisabled() {
		t.Error("enabled:false server must resolve as disabled")
	}
}

// buildMCPServersView must surface every configured server with its
// enabled state, so operators can see configured-but-off servers.
func TestBuildMCPServersViewMarksDisabled(t *testing.T) {
	disabled := false
	resolved := config.ResolvedConfig{
		MCPServers: map[string]mcpclient.ServerConfig{
			"on":  {Command: "echo"},
			"off": {Command: "echo", Enabled: &disabled},
		},
	}
	view := buildMCPServersView(resolved)
	if len(view) != 2 {
		t.Fatalf("want 2 entries, got %d", len(view))
	}
	for _, e := range view {
		switch e.Name {
		case "on":
			if !e.Enabled {
				t.Error("default server must report enabled:true")
			}
		case "off":
			if e.Enabled {
				t.Error("disabled server must report enabled:false")
			}
		}
	}
}

// A disabled project-level MCP server must not trigger the approval
// prompt — it never runs, so there is nothing to approve.
func TestApproveMCPServersSkipsDisabled(t *testing.T) {
	disabled := false
	resolved := config.ResolvedConfig{
		MCPServers: map[string]mcpclient.ServerConfig{
			"proj": {Command: "definitely-not-a-real-binary", Enabled: &disabled},
		},
		ProjectMCPServerNames: []string{"proj"},
	}
	var out bytes.Buffer
	// tty=true with an empty stdin: an enabled project server would block
	// on the prompt read; a disabled one must return nil immediately.
	err := approveMCPServersWithTTY(resolved, &bytes.Buffer{}, &out, true)
	if err != nil {
		t.Fatalf("disabled project server must not prompt: %v", err)
	}
	if bytes.Contains(out.Bytes(), []byte("Approve?")) {
		t.Error("approval prompt shown for a disabled server")
	}
}
