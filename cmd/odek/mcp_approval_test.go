package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/mcpclient"
)

func TestApproveMCPServers_NoProjectServers(t *testing.T) {
	resolved := config.ResolvedConfig{
		MCPServers: map[string]mcpclient.ServerConfig{
			"global": {Command: "node", Args: []string{"global.js"}},
		},
	}
	if err := approveMCPServersWithTTY(resolved, strings.NewReader(""), &bytes.Buffer{}, false); err != nil {
		t.Fatalf("expected no approval needed for global servers, got: %v", err)
	}
}

func TestApproveMCPServers_ProjectServerRequiresApproval(t *testing.T) {
	resolved := config.ResolvedConfig{
		MCPServers: map[string]mcpclient.ServerConfig{
			"project": {Command: "sh", Args: []string{"-c", "echo pwned"}},
		},
		ProjectMCPServerNames: []string{"project"},
	}

	var out bytes.Buffer
	err := approveMCPServersWithTTY(resolved, strings.NewReader("\n"), &out, true)
	if err == nil {
		t.Fatal("expected error when user denies approval, got nil")
	}
	if !strings.Contains(err.Error(), "was not approved") {
		t.Errorf("error = %q, want 'was not approved'", err)
	}
	if !strings.Contains(out.String(), "Project-level MCP server") {
		t.Errorf("prompt = %q, want project-level prompt", out.String())
	}
}

func TestApproveMCPServers_ApprovalViaTTY(t *testing.T) {
	resolved := config.ResolvedConfig{
		MCPServers: map[string]mcpclient.ServerConfig{
			"project": {Command: "node", Args: []string{"server.js"}},
		},
		ProjectMCPServerNames: []string{"project"},
	}

	var out bytes.Buffer
	err := approveMCPServersWithTTY(resolved, strings.NewReader("yes\n"), &out, true)
	if err != nil {
		t.Fatalf("expected approval, got: %v", err)
	}
}

func TestApproveMCPServers_ApprovalViaEnv(t *testing.T) {
	resolved := config.ResolvedConfig{
		MCPServers: map[string]mcpclient.ServerConfig{
			"project": {Command: "sh", Args: []string{"-c", "echo pwned"}},
		},
		ProjectMCPServerNames: []string{"project"},
	}

	t.Setenv("ODEK_APPROVE_MCP", "1")
	if err := approveMCPServersWithTTY(resolved, strings.NewReader(""), &bytes.Buffer{}, false); err != nil {
		t.Fatalf("expected env approval, got: %v", err)
	}
}

func TestApproveMCPServers_NonTTYRequiresEnv(t *testing.T) {
	resolved := config.ResolvedConfig{
		MCPServers: map[string]mcpclient.ServerConfig{
			"project": {Command: "sh", Args: []string{"-c", "echo pwned"}},
		},
		ProjectMCPServerNames: []string{"project"},
	}

	// Ensure env is not set.
	os.Unsetenv("ODEK_APPROVE_MCP")
	err := approveMCPServersWithTTY(resolved, strings.NewReader(""), &bytes.Buffer{}, false)
	if err == nil {
		t.Fatal("expected error for non-interactive unapproved project server")
	}
	if !strings.Contains(err.Error(), "ODEK_APPROVE_MCP") {
		t.Errorf("error = %q, want ODEK_APPROVE_MCP hint", err)
	}
}

func TestMCPApprovalKey_Stability(t *testing.T) {
	cfg := mcpclient.ServerConfig{Command: "node", Args: []string{"a.js", "b.js"}, Env: map[string]string{"X": "1"}}
	k1 := mcpApprovalKey("/proj", "srv", cfg)
	k2 := mcpApprovalKey("/proj", "srv", cfg)
	if k1 != k2 {
		t.Fatalf("approval key not stable: %q vs %q", k1, k2)
	}

	cfg2 := mcpclient.ServerConfig{Command: "node", Args: []string{"a.js", "c.js"}, Env: map[string]string{"X": "1"}}
	k3 := mcpApprovalKey("/proj", "srv", cfg2)
	if k1 == k3 {
		t.Fatal("approval key did not change when args changed")
	}
}
