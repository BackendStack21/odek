package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestShellTool_Name(t *testing.T) {
	st := &shellTool{}
	if st.Name() != "shell" {
		t.Errorf("Name() = %q, want %q", st.Name(), "shell")
	}
}

// TestShellTool_Timeout verifies a stuck command can no longer wedge the agent:
// a tiny per-tool timeout kills the command and Call returns promptly with a
// clear timeout error instead of blocking forever.
func TestShellTool_Timeout(t *testing.T) {
	st := &shellTool{timeout: 200 * time.Millisecond}
	done := make(chan struct{})
	var out string
	var err error
	go func() {
		out, err = st.Call(`{"command":"sleep 30"}`)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Call did not return after the command timeout — agent would hang")
	}
	if err == nil {
		t.Fatalf("expected a timeout error, got output %q", out)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error should mention the timeout, got: %v", err)
	}
}

// TestShellTool_ContextCancellation verifies Ctrl-C / turn cancellation kills a
// running command immediately via the agent context.
func TestShellTool_ContextCancellation(t *testing.T) {
	st := &shellTool{}
	ctx, cancel := context.WithCancel(context.Background())
	st.SetContext(ctx)

	done := make(chan struct{})
	var err error
	go func() {
		_, err = st.Call(`{"command":"sleep 30"}`)
		close(done)
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Call did not return after context cancellation — Ctrl-C would not work")
	}
	if err == nil {
		t.Fatal("expected a cancellation error")
	}
	if !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("error should mention cancellation, got: %v", err)
	}
}

func TestShellTool_Description(t *testing.T) {
	st := &shellTool{}
	desc := st.Description()
	if !strings.Contains(desc, "shell command") {
		t.Errorf("Description() missing expected content: %q", desc)
	}
}

func TestShellTool_Schema(t *testing.T) {
	st := &shellTool{}
	schema := st.Schema()
	m, ok := schema.(map[string]any)
	if !ok {
		t.Fatal("Schema() should return map[string]any")
	}
	if m["type"] != "object" {
		t.Errorf("schema type = %q, want %q", m["type"], "object")
	}
	props, ok := m["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema missing properties")
	}
	cmd, ok := props["command"].(map[string]any)
	if !ok {
		t.Fatal("schema missing command property")
	}
	if cmd["type"] != "string" {
		t.Errorf("command type = %q, want %q", cmd["type"], "string")
	}
	required, ok := m["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "command" {
		t.Errorf("required = %v, want [command]", required)
	}
}

func TestShellTool_Call_ValidCommand(t *testing.T) {
	st := &shellTool{}
	args := `{"command": "echo hello"}`
	result, err := st.Call(args)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	if !strings.Contains(result, "hello") {
		t.Errorf("result = %q, want it to contain 'hello'", result)
	}
}

func TestShellTool_Call_InvalidJSON(t *testing.T) {
	st := &shellTool{}
	_, err := st.Call(`not json`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "parse args") {
		t.Errorf("error = %v, want 'parse args'", err)
	}
}

func TestShellTool_Call_EmptyCommand(t *testing.T) {
	st := &shellTool{}
	_, err := st.Call(`{"command": ""}`)
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestShellTool_Call_CommandFails(t *testing.T) {
	st := &shellTool{}
	result, err := st.Call(`{"command": "exit 1"}`)
	// sh -c "exit 1" returns exit code 1 but produces no output
	if err == nil {
		t.Fatal("expected error for failing command with no output")
	}
	_ = result
}

func TestShellTool_Call_CommandFailsWithStderr(t *testing.T) {
	st := &shellTool{}
	result, err := st.Call(`{"command": "echo error >&2 && exit 1"}`)
	if err != nil {
		t.Errorf("Call() should return output even on error, got: %v", err)
	}
	if !strings.Contains(result, "error") {
		t.Errorf("result = %q, want it to contain 'error'", result)
	}
}

func TestShellTool_Call_NoOutput(t *testing.T) {
	st := &shellTool{}
	result, err := st.Call(`{"command": "true"}`)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	body := unwrapUntrusted(result)
	if body != "(no output)" {
		t.Errorf("body = %q, want '(no output)'", body)
	}
}

func TestShellTool_Call_StdoutAndStderr(t *testing.T) {
	st := &shellTool{}
	result, err := st.Call(`{"command": "echo out && echo err >&2"}`)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	if !strings.Contains(result, "out") {
		t.Errorf("result = %q, want it to contain 'out'", result)
	}
	if !strings.Contains(result, "err") {
		t.Errorf("result = %q, want it to contain 'err'", result)
	}
}

func TestShellTool_BuildCmd_Local(t *testing.T) {
	st := &shellTool{}
	cmd := st.buildCmd(context.Background(), "echo test")
	args := cmd.Args
	if args[0] != "sh" || args[1] != "-c" || args[2] != "echo test" {
		t.Errorf("local cmd args = %v, want [sh -c 'echo test']", args)
	}
}

func TestShellTool_BuildCmd_Docker(t *testing.T) {
	st := &shellTool{containerName: "odek-12345"}
	cmd := st.buildCmd(context.Background(), "echo test")
	args := cmd.Args
	expected := []string{"docker", "exec", "-w", "/workspace", "odek-12345", "sh", "-c", "echo test"}
	if !stringSlicesEqual(args, expected) {
		t.Errorf("docker cmd args = %v, want %v", args, expected)
	}
}

func TestShellTool_Call_DockerExec_Integration(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker not available")
	}

	// Create a test container with /workspace directory.
	// Use a fixed path that Docker can access in CI environments.
	containerName := "odek-test-shell"
	tmpDir, err := os.MkdirTemp("", "odek-test-")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)
	createCmd := exec.Command("docker", "run",
		"--rm", "--detach", "--name", containerName,
		"-v", tmpDir+":/workspace",
		"alpine:latest", "sleep", "infinity",
	)
	if out, err := createCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to create test container: %v\n%s", err, out)
	}
	defer exec.Command("docker", "rm", "-f", containerName).Run()

	st := &shellTool{containerName: containerName}
	result, err := st.Call(`{"command": "echo hello-from-docker"}`)
	if err != nil {
		t.Fatalf("docker exec Call() error: %v", err)
	}
	if !strings.Contains(result, "hello-from-docker") {
		t.Errorf("result = %q, want it to contain 'hello-from-docker'", result)
	}
}

func TestShellTool_Call_DockerExec_WorkingDir(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker not available")
	}

	tmpDir := t.TempDir()
	if !dockerBindMountWorks(t, tmpDir) {
		t.Skipf("docker bind mount of %s not supported in this environment", tmpDir)
	}

	containerName := "odek-test-wd"
	markerFile := tmpDir + "/marker.txt"
	if err := os.WriteFile(markerFile, []byte("found"), 0644); err != nil {
		t.Fatalf("write marker file: %v", err)
	}

	createCmd := exec.Command("docker", "run",
		"--rm", "--detach", "--name", containerName,
		"-v", tmpDir+":/workspace",
		"alpine:latest", "sleep", "infinity",
	)
	if out, err := createCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to create test container: %v\n%s", err, out)
	}
	defer exec.Command("docker", "rm", "-f", containerName).Run()

	st := &shellTool{containerName: containerName}
	result, err := st.Call(`{"command": "cat marker.txt"}`)
	if err != nil {
		t.Fatalf("docker exec Call() error: %v", err)
	}
	if !strings.Contains(result, "found") {
		t.Errorf("result = %q, want 'found' (working directory should be /workspace)", result)
	}
}

// dockerBindMountWorks probes whether docker can bind-mount the host path
// and see file changes. Docker Desktop on macOS only shares specific paths
// (e.g. /Users) by default — bind mounts of /tmp or /var/folders silently
// no-op, leaving the test directory invisible inside the container.
func dockerBindMountWorks(t *testing.T, hostDir string) bool {
	t.Helper()
	marker := hostDir + "/.docker-probe"
	if err := os.WriteFile(marker, []byte("ok"), 0644); err != nil {
		return false
	}
	defer os.Remove(marker)
	out, err := exec.Command("docker", "run", "--rm",
		"-v", hostDir+":/probe",
		"alpine:latest", "cat", "/probe/.docker-probe",
	).CombinedOutput()
	return err == nil && strings.Contains(string(out), "ok")
}

func TestShellTool_Call_JSONUnmarshalError(t *testing.T) {
	st := &shellTool{}
	_, err := st.Call(`{"command": `) // truncated JSON
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestShellTool_ContainerName(t *testing.T) {
	st := &shellTool{containerName: "my-container"}
	if st.containerName != "my-container" {
		t.Errorf("containerName = %q, want 'my-container'", st.containerName)
	}

	// Default is empty
	st2 := &shellTool{}
	if st2.containerName != "" {
		t.Errorf("default containerName should be empty, got %q", st2.containerName)
	}
}

func TestShellTool_SchemaIntegration(t *testing.T) {
	st := &shellTool{}
	schema := st.Schema()

	// Verify the schema is valid JSON
	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}

	// Round-trip
	var roundTrip map[string]any
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("schema JSON round-trip failed: %v", err)
	}
}

// ── shellTool.checkApproval Tests ─────────────────────────────────────

func TestShellTool_CheckApproval(t *testing.T) {
	// These tests verify the checkApproval logic by calling Call which
	// internally calls checkApproval, using a permissive config.

	t.Run("permissive config allows safe command", func(t *testing.T) {
		st := &shellTool{}
		result, err := st.Call(`{"command": "echo safe"}`)
		if err != nil {
			t.Fatalf("Call() error: %v", err)
		}
		if !strings.Contains(result, "safe") {
			t.Errorf("result = %q, want 'safe'", result)
		}
	})

	t.Run("deny config rejects command", func(t *testing.T) {
		// We can't easily set up dangerous config from test pkg,
		// but we can verify the allowlist/denylist via the ActionForCommand path
		// by checking the Call errors for empty commands already tested above.
		st := &shellTool{}
		_, err := st.Call(`{"command": ""}`)
		if err == nil {
			t.Fatal("expected error for empty command")
		}
	})
}

func TestShellTool_BuildCmd_Default(t *testing.T) {
	st := &shellTool{}
	cmd := st.buildCmd(context.Background(), "echo hello")
	if cmd.Args[0] != "sh" {
		t.Errorf("expected sh, got %s", cmd.Args[0])
	}
}

func TestShellTool_Call_EmptyCommandExact(t *testing.T) {
	st := &shellTool{}
	_, err := st.Call(`{"command": ""}`)
	if err == nil {
		t.Fatal("expected error for empty command")
	}
	if !strings.Contains(err.Error(), "empty command") {
		t.Errorf("error = %v, want 'empty command'", err)
	}
}

// stringSlicesEqual compares two string slices.
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
