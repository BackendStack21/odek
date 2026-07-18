package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/config"
)

func TestApproveProjectSandbox_NoOverride(t *testing.T) {
	resolved := config.ResolvedConfig{}
	if err := approveProjectSandboxWithTTY(resolved, strings.NewReader(""), &bytes.Buffer{}, false); err != nil {
		t.Fatalf("expected no approval needed when no override, got: %v", err)
	}
}

func TestApproveProjectSandbox_EnvBypass(t *testing.T) {
	resolved := config.ResolvedConfig{
		ProjectSandboxOverride: config.ProjectSandboxOverride{
			HasEnv: true,
			EnvKeys: []string{"X"},
		},
	}
	t.Setenv("ODEK_APPROVE_PROJECT_SANDBOX", "1")
	if err := approveProjectSandboxWithTTY(resolved, strings.NewReader(""), &bytes.Buffer{}, false); err != nil {
		t.Fatalf("expected env approval, got: %v", err)
	}
}

func TestApproveProjectSandbox_NonTTYRequiresEnv(t *testing.T) {
	resolved := config.ResolvedConfig{
		ProjectSandboxOverride: config.ProjectSandboxOverride{
			HasEnv: true,
			EnvKeys: []string{"X"},
		},
	}
	os.Unsetenv("ODEK_APPROVE_PROJECT_SANDBOX")
	err := approveProjectSandboxWithTTY(resolved, strings.NewReader(""), &bytes.Buffer{}, false)
	if err == nil {
		t.Fatal("expected error for non-interactive unapproved project sandbox")
	}
	if !strings.Contains(err.Error(), "ODEK_APPROVE_PROJECT_SANDBOX") {
		t.Errorf("error = %q, want ODEK_APPROVE_PROJECT_SANDBOX hint", err.Error())
	}
}

func TestApproveProjectSandbox_TTYDeny(t *testing.T) {
	resolved := config.ResolvedConfig{
		ProjectSandboxOverride: config.ProjectSandboxOverride{
			HasEnv: true,
			EnvKeys: []string{"X"},
		},
	}
	var out bytes.Buffer
	err := approveProjectSandboxWithTTY(resolved, strings.NewReader("\n"), &out, true)
	if err == nil {
		t.Fatal("expected error when user denies approval")
	}
	if !strings.Contains(err.Error(), "not approved") {
		t.Errorf("error = %q, want 'not approved'", err.Error())
	}
	if !strings.Contains(out.String(), "WARNING") {
		t.Errorf("prompt = %q, want WARNING header", out.String())
	}
}

func TestApproveProjectSandbox_TTYApproveOnce(t *testing.T) {
	resolved := config.ResolvedConfig{
		ProjectSandboxOverride: config.ProjectSandboxOverride{
			HasEnv: true,
			EnvKeys: []string{"X"},
		},
	}
	var out bytes.Buffer
	err := approveProjectSandboxWithTTY(resolved, strings.NewReader("y\n"), &out, true)
	if err != nil {
		t.Fatalf("expected approval, got: %v", err)
	}
}

func TestApproveProjectSandbox_TTYTrustPersists(t *testing.T) {
	homeDir := setupTestHome(t)
	resolved := config.ResolvedConfig{
		ProjectSandboxOverride: config.ProjectSandboxOverride{
			HasEnv: true,
			EnvKeys: []string{"X"},
		},
	}

	var out bytes.Buffer
	err := approveProjectSandboxWithTTY(resolved, strings.NewReader("t\n"), &out, true)
	if err != nil {
		t.Fatalf("expected trust approval, got: %v", err)
	}

	// Second call with same key and no input should succeed because of persisted trust.
	err = approveProjectSandboxWithTTY(resolved, strings.NewReader(""), &bytes.Buffer{}, false)
	if err != nil {
		t.Fatalf("expected persisted approval, got: %v", err)
	}

	approvalPath := filepath.Join(homeDir, ".odek", projectSandboxApprovalsFile)
	if _, err := os.Stat(approvalPath); err != nil {
		t.Fatalf("approval file not created: %v", err)
	}
}

func TestApproveProjectSandbox_KeyChanges(t *testing.T) {
	setupTestHome(t)
	resolved := config.ResolvedConfig{
		ProjectSandboxOverride: config.ProjectSandboxOverride{
			HasEnv: true,
			EnvKeys: []string{"X"},
		},
	}

	var out bytes.Buffer
	if err := approveProjectSandboxWithTTY(resolved, strings.NewReader("t\n"), &out, true); err != nil {
		t.Fatalf("expected trust approval, got: %v", err)
	}

	// Add a new env key: previous trust should be invalidated.
	resolved.ProjectSandboxOverride.EnvKeys = []string{"X", "Y"}
	err := approveProjectSandboxWithTTY(resolved, strings.NewReader(""), &bytes.Buffer{}, false)
	if err == nil {
		t.Fatal("expected error after key change invalidated trust")
	}
}

func TestApproveProjectSandbox_PromptHidesValues(t *testing.T) {
	resolved := config.ResolvedConfig{
		ProjectSandboxOverride: config.ProjectSandboxOverride{
			HasEnv:              true,
			EnvKeys:             []string{"X"},
			EnvHasInterpolation: true,
		},
	}
	var out bytes.Buffer
	_ = approveProjectSandboxWithTTY(resolved, strings.NewReader("\n"), &out, true)
	prompt := out.String()
	if !strings.Contains(prompt, "X") {
		t.Errorf("prompt = %q, want env key X", prompt)
	}
	if strings.Contains(prompt, "${HOME}") || strings.Contains(prompt, "secret-value") {
		t.Errorf("prompt should not contain env values; got %q", prompt)
	}
	if !strings.Contains(prompt, "${...}") {
		t.Errorf("prompt = %q, want interpolation warning", prompt)
	}
}
