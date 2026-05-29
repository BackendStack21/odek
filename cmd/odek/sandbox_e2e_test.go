package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/sandbox"
)

// ─────────────────────────────────────────────────────────────────────
// E2E Tests: Sandbox file injection
// ─────────────────────────────────────────────────────────────────────
//
// These tests create a real Docker sandbox container and verify that
// ctx files are correctly injected into it via docker cp.
//
// Gated by ODEK_E2E=true.
// ─────────────────────────────────────────────────────────────────────

// TestE2E_SandboxFileInjection verifies that --ctx files are copied
// into the sandbox container and accessible via the container's shell.
func TestE2E_SandboxFileInjection(t *testing.T) {
	skipIfNoE2E(t)

	// Create temp file to inject
	workDir := t.TempDir()
	testContent := "sandbox-injection-test-content"
	testFile := filepath.Join(workDir, "injected.txt")
	if err := os.WriteFile(testFile, []byte(testContent), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	// Create a sandbox container using the same machinery as odek run
	containerName := fmt.Sprintf("odek-test-inject-%d", time.Now().UnixNano())
	args := sandbox.BuildRunArgs(sandboxConfig{
		Image:   "alpine:latest",
		Network: "none",
	}, containerName, workDir, "alpine:latest")

	createCmd := exec.Command("docker", args...)
	createCmd.Stderr = os.Stderr
	if err := createCmd.Run(); err != nil {
		t.Fatalf("create sandbox container: %v", err)
	}
	defer func() {
		exec.Command("docker", "rm", "-f", containerName).Run()
	}()

	// Wait for container to be ready
	time.Sleep(500 * time.Millisecond)

	// Inject the file
	count, err := sandbox.InjectFiles(containerName, []string{"injected.txt"}, workDir)
	if err != nil {
		t.Fatalf("injectFilesToSandbox: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 file injected, got %d", count)
	}

	// Verify the file exists in the container at /workspace/injected.txt
	verifyCmd := exec.Command("docker", "exec", "-w", "/workspace", containerName, "cat", "injected.txt")
	output, err := verifyCmd.Output()
	if err != nil {
		t.Fatalf("docker exec cat: %v", err)
	}
	if strings.TrimSpace(string(output)) != testContent {
		t.Errorf("file content = %q, want %q", strings.TrimSpace(string(output)), testContent)
	}
}

// TestE2E_SandboxFileInjection_NestedPath verifies that --ctx files
// in subdirectories preserve their relative path in the container.
func TestE2E_SandboxFileInjection_NestedPath(t *testing.T) {
	skipIfNoE2E(t)

	workDir := t.TempDir()
	subDir := filepath.Join(workDir, "subdir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}
	testContent := "nested-file-content"
	nestedFile := filepath.Join(subDir, "nested.txt")
	if err := os.WriteFile(nestedFile, []byte(testContent), 0644); err != nil {
		t.Fatalf("write nested file: %v", err)
	}

	containerName := fmt.Sprintf("odek-test-nested-%d", time.Now().UnixNano())
	args := sandbox.BuildRunArgs(sandboxConfig{
		Image:   "alpine:latest",
		Network: "none",
	}, containerName, workDir, "alpine:latest")

	createCmd := exec.Command("docker", args...)
	createCmd.Stderr = os.Stderr
	if err := createCmd.Run(); err != nil {
		t.Fatalf("create container: %v", err)
	}
	defer exec.Command("docker", "rm", "-f", containerName).Run()

	time.Sleep(500 * time.Millisecond)

	// Inject with relative path from cwd: subdir/nested.txt
	count, err := sandbox.InjectFiles(containerName, []string{"subdir/nested.txt"}, workDir)
	if err != nil {
		t.Fatalf("injectFilesToSandbox: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 file injected, got %d", count)
	}

	// Verify file exists at /workspace/subdir/nested.txt
	verifyCmd := exec.Command("docker", "exec", "-w", "/workspace", containerName, "cat", "subdir/nested.txt")
	output, err := verifyCmd.Output()
	if err != nil {
		t.Fatalf("docker exec cat nested: %v", err)
	}
	if strings.TrimSpace(string(output)) != testContent {
		t.Errorf("nested file content = %q, want %q", strings.TrimSpace(string(output)), testContent)
	}
}

// TestE2E_SandboxFileInjection_AbsolutePath verifies that absolute
// path files outside cwd are injected by basename into /workspace/.
func TestE2E_SandboxFileInjection_AbsolutePath(t *testing.T) {
	skipIfNoE2E(t)

	// Create a file outside the working directory
	externalDir := t.TempDir()
	absContent := "absolute-path-content"
	absFile := filepath.Join(externalDir, "external.txt")
	if err := os.WriteFile(absFile, []byte(absContent), 0644); err != nil {
		t.Fatalf("write external file: %v", err)
	}

	workDir := t.TempDir()

	containerName := fmt.Sprintf("odek-test-abs-%d", time.Now().UnixNano())
	args := sandbox.BuildRunArgs(sandboxConfig{
		Image:   "alpine:latest",
		Network: "none",
	}, containerName, workDir, "alpine:latest")

	createCmd := exec.Command("docker", args...)
	createCmd.Stderr = os.Stderr
	if err := createCmd.Run(); err != nil {
		t.Fatalf("create container: %v", err)
	}
	defer exec.Command("docker", "rm", "-f", containerName).Run()

	time.Sleep(500 * time.Millisecond)

	// Inject with absolute path (outside cwd) — should use basename
	count, err := sandbox.InjectFiles(containerName, []string{absFile}, workDir)
	if err != nil {
		t.Fatalf("injectFilesToSandbox: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 file injected, got %d", count)
	}

	// Should exist at /workspace/external.txt (basename)
	verifyCmd := exec.Command("docker", "exec", "-w", "/workspace", containerName, "cat", "external.txt")
	output, err := verifyCmd.Output()
	if err != nil {
		t.Fatalf("docker exec cat external: %v", err)
	}
	if strings.TrimSpace(string(output)) != absContent {
		t.Errorf("external file content = %q, want %q", strings.TrimSpace(string(output)), absContent)
	}
}

// TestE2E_SandboxFileInjection_MultipleFiles verifies injecting
// multiple files at once.
func TestE2E_SandboxFileInjection_MultipleFiles(t *testing.T) {
	skipIfNoE2E(t)

	workDir := t.TempDir()
	os.WriteFile(filepath.Join(workDir, "a.txt"), []byte("file A"), 0644)
	os.WriteFile(filepath.Join(workDir, "b.txt"), []byte("file B"), 0644)

	containerName := fmt.Sprintf("odek-test-multi-%d", time.Now().UnixNano())
	args := sandbox.BuildRunArgs(sandboxConfig{
		Image:   "alpine:latest",
		Network: "none",
	}, containerName, workDir, "alpine:latest")

	createCmd := exec.Command("docker", args...)
	createCmd.Stderr = os.Stderr
	if err := createCmd.Run(); err != nil {
		t.Fatalf("create container: %v", err)
	}
	defer exec.Command("docker", "rm", "-f", containerName).Run()

	time.Sleep(500 * time.Millisecond)

	count, err := sandbox.InjectFiles(containerName, []string{"a.txt", "b.txt"}, workDir)
	if err != nil {
		t.Fatalf("injectFilesToSandbox: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 files injected, got %d", count)
	}

	// Verify both files
	for _, name := range []string{"a.txt", "b.txt"} {
		out, err := exec.Command("docker", "exec", "-w", "/workspace", containerName, "cat", name).Output()
		if err != nil {
			t.Errorf("cat %s: %v", name, err)
		}
		if strings.TrimSpace(string(out)) == "" {
			t.Errorf("file %s is empty", name)
		}
	}
}
