package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestGetVersion_LdFlagsOverride(t *testing.T) {
	orig := version
	version = "v9.9.9"
	defer func() { version = orig }()

	v := getVersion()
	if v != "v9.9.9" {
		t.Errorf("getVersion() = %q, want %q", v, "v9.9.9")
	}
}

func TestGetVersion_DevFallback(t *testing.T) {
	orig := version
	version = ""
	defer func() { version = orig }()

	v := getVersion()
	if v == "" {
		t.Error("getVersion() returned empty string")
	}
}

func TestGetVersion_NotEmpty(t *testing.T) {
	v := getVersion()
	if v == "" {
		t.Error("getVersion() returned empty string")
	}
}

func TestParseRunFlags_Defaults(t *testing.T) {
	f, err := parseRunFlags([]string{"hello world"})
	if err != nil {
		t.Fatalf("parseRunFlags error: %v", err)
	}
	if f.Task != "hello world" {
		t.Errorf("Task = %q, want %q", f.Task, "hello world")
	}
	if f.MaxIter != 90 {
		t.Errorf("MaxIter = %d, want 90", f.MaxIter)
	}
	if f.Sandbox {
		t.Error("Sandbox should default to false")
	}
}

func TestParseRunFlags_AllFlags(t *testing.T) {
	f, err := parseRunFlags([]string{
		"--model", "gpt-4o",
		"--base-url", "https://api.openai.com/v1",
		"--max-iter", "42",
		"--system", "You are a bot.",
		"--thinking", "enabled",
		"--sandbox",
		"do the thing",
	})
	if err != nil {
		t.Fatalf("parseRunFlags error: %v", err)
	}
	if f.Model != "gpt-4o" {
		t.Errorf("Model = %q", f.Model)
	}
	if f.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("BaseURL = %q", f.BaseURL)
	}
	if f.MaxIter != 42 {
		t.Errorf("MaxIter = %d", f.MaxIter)
	}
	if f.System != "You are a bot." {
		t.Errorf("System = %q", f.System)
	}
	if f.Thinking != "enabled" {
		t.Errorf("Thinking = %q", f.Thinking)
	}
	if !f.Sandbox {
		t.Error("Sandbox should be true")
	}
	if f.Task != "do the thing" {
		t.Errorf("Task = %q", f.Task)
	}
}

func TestParseRunFlags_NoTask(t *testing.T) {
	_, err := parseRunFlags([]string{})
	if err == nil {
		t.Fatal("expected error for no task")
	}
}

func TestParseRunFlags_SandboxFlagOnly(t *testing.T) {
	f, err := parseRunFlags([]string{"--sandbox", "list files"})
	if err != nil {
		t.Fatalf("parseRunFlags error: %v", err)
	}
	if !f.Sandbox {
		t.Error("Sandbox should be true")
	}
	if f.Task != "list files" {
		t.Errorf("Task = %q", f.Task)
	}
}

func TestParseRunFlags_MultiWordTask(t *testing.T) {
	f, err := parseRunFlags([]string{"fix", "the", "oom", "bug"})
	if err != nil {
		t.Fatalf("parseRunFlags error: %v", err)
	}
	if f.Task != "fix the oom bug" {
		t.Errorf("Task = %q, want %q", f.Task, "fix the oom bug")
	}
}

func TestParseRunFlags_FlagValueWithoutPair(t *testing.T) {
	// --model without value: the flag parser sees --model at i, args[i+1] is --sandbox
	// This is a weird edge case but shouldn't crash
	args := []string{"--model", "--sandbox", "task"}
	f, err := parseRunFlags(args)
	if err != nil {
		t.Fatalf("parseRunFlags error: %v", err)
	}
	// --model consumed --sandbox as its value
	if f.Model != "--sandbox" {
		t.Errorf("Model = %q (consumed next arg as value)", f.Model)
	}
}

func TestRun_NoTask(t *testing.T) {
	err := run([]string{})
	if err == nil {
		t.Fatal("expected error for no task")
	}
}

func TestRun_NoAPIKey(t *testing.T) {
	// Save env
	origDS := os.Getenv("DEEPSEEK_API_KEY")
	origOAI := os.Getenv("OPENAI_API_KEY")
	os.Unsetenv("DEEPSEEK_API_KEY")
	os.Unsetenv("OPENAI_API_KEY")
	defer func() {
		os.Setenv("DEEPSEEK_API_KEY", origDS)
		os.Setenv("OPENAI_API_KEY", origOAI)
	}()

	err := run([]string{"test task"})
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
}

func TestBuiltinTools(t *testing.T) {
	tools := builtinTools()
	if len(tools) == 0 {
		t.Fatal("builtinTools() returned empty slice")
	}
	if tools[0].Name() != "shell" {
		t.Errorf("first tool name = %q, want 'shell'", tools[0].Name())
	}
}

func TestDefaultSystemPrompt(t *testing.T) {
	if !strings.Contains(defaultSystem, "kode") {
		t.Error("defaultSystem should contain agent instructions")
	}
}

func TestPrintUsage(t *testing.T) {
	orig := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	printUsage()

	w.Close()
	os.Stdout = orig

	data, _ := io.ReadAll(r)
	output := string(data)

	required := []string{
		"kode run",
		"kode version",
		"--model",
		"Known profiles",
		"deepseek-v4-flash",
		"deepseek-v4-pro",
		"--base-url",
		"--max-iter",
		"--thinking",
		"profile default",
		"--sandbox",
		"--no-color",
		"--no-agents",
		"--system",
	}
	for _, req := range required {
		if !strings.Contains(output, req) {
			t.Errorf("usage missing %q", req)
		}
	}
}

func TestSetupSandbox_CommandFlags(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker not available")
	}

	containerName := "kode-test-setup"
	wd := "/tmp"

	cmd := exec.Command("docker", "run",
		"--rm", "--detach", "--name", containerName,
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--network", "none",
		"--tmpfs", "/tmp:noexec",
		"-v", wd+":/workspace",
		"alpine:latest",
		"sleep", "infinity",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker run failed: %v\n%s", err, out)
	}
	defer exec.Command("docker", "rm", "-f", containerName).Run()

	// Verify container security settings
	inspectCmd := exec.Command("docker", "inspect", containerName,
		"--format", "{{.HostConfig.NetworkMode}}")
	inspectOut, err := inspectCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker inspect failed: %v\n%s", err, inspectOut)
	}
	output := strings.TrimSpace(string(inspectOut))
	if output != "none" {
		t.Errorf("network mode = %q, want 'none'", output)
	}
}

func TestSetupSandbox_ExecWorks(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker not available")
	}

	containerName := "kode-test-exec"
	wd := "/tmp"

	cmd := exec.Command("docker", "run",
		"--rm", "--detach", "--name", containerName,
		"--network", "none",
		"-v", wd+":/workspace",
		"alpine:latest",
		"sleep", "infinity",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker run failed: %v\n%s", err, out)
	}
	defer exec.Command("docker", "rm", "-f", containerName).Run()

	execCmd := exec.Command("docker", "exec", "-w", "/workspace", containerName,
		"sh", "-c", "echo sandbox-ok")
	execOut, err := execCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker exec failed: %v\n%s", err, execOut)
	}
	if !strings.Contains(string(execOut), "sandbox-ok") {
		t.Errorf("exec output = %q, want to contain 'sandbox-ok'", string(execOut))
	}
}

func TestSetupSandbox_NetworkBlocked(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker not available")
	}

	containerName := "kode-test-net"
	wd := "/tmp"

	cmd := exec.Command("docker", "run",
		"--rm", "--detach", "--name", containerName,
		"--network", "none",
		"-v", wd+":/workspace",
		"alpine:latest",
		"sleep", "infinity",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker run failed: %v\n%s", err, out)
	}
	defer exec.Command("docker", "rm", "-f", containerName).Run()

	pingCmd := exec.Command("docker", "exec", containerName,
		"sh", "-c", "ping -c 1 -W 1 8.8.8.8 2>&1")
	pingOut, _ := pingCmd.CombinedOutput()
	output := string(pingOut)
	if !strings.Contains(output, "unreachable") && !strings.Contains(output, "Network") {
		t.Errorf("network should be blocked, got: %s", output)
	}
}

func TestCaptureStdout(t *testing.T) {
	output := captureStdout(func() {
		printUsage()
	})
	if !strings.Contains(output, "kode run") {
		t.Error("captured output should contain 'kode run'")
	}
}

// captureStdout captures stdout during fn execution.
func captureStdout(fn func()) string {
	orig := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = orig
	data, _ := io.ReadAll(r)
	return string(data)
}

// Test parseRunFlags with a non-numeric --max-iter value.
func TestParseRunFlags_MaxIterNonNumeric(t *testing.T) {
	f, err := parseRunFlags([]string{"--max-iter", "abc", "task"})
	if err != nil {
		t.Fatalf("parseRunFlags error: %v", err)
	}
	// fmt.Sscanf with non-numeric keeps the default (90)
	if f.MaxIter != 90 {
		t.Errorf("MaxIter = %d, want 90 (non-numeric should leave default)", f.MaxIter)
	}
	if f.Task != "task" {
		t.Errorf("Task = %q, want %q", f.Task, "task")
	}
}

// Test run() with --sandbox when Docker is not available — tests the
// sandbox error path (main.go lines 129-135).
func TestRun_SandboxNoDocker(t *testing.T) {
	if dockerAvailable() {
		t.Skip("docker is available, cannot test sandbox error path")
	}
	origDS := os.Getenv("DEEPSEEK_API_KEY")
	origOAI := os.Getenv("OPENAI_API_KEY")
	os.Setenv("DEEPSEEK_API_KEY", "sk-test")
	defer func() {
		os.Setenv("DEEPSEEK_API_KEY", origDS)
		os.Setenv("OPENAI_API_KEY", origOAI)
	}()

	err := run([]string{"--sandbox", "test task"})
	if err == nil {
		t.Fatal("expected error when sandbox requested but docker unavailable")
	}
}

// Test run() with a mocked LLM endpoint — no real API calls.
func TestRun_WithMockModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"mocked response"}}]}`))
	}))
	defer server.Close()

	origDS := os.Getenv("DEEPSEEK_API_KEY")
	origOAI := os.Getenv("OPENAI_API_KEY")
	os.Setenv("DEEPSEEK_API_KEY", "sk-mock")
	os.Unsetenv("OPENAI_API_KEY")
	defer func() {
		os.Setenv("DEEPSEEK_API_KEY", origDS)
		os.Setenv("OPENAI_API_KEY", origOAI)
	}()

	// Use the mock server as the API endpoint
	err := run([]string{"--base-url", server.URL, "test task"})
	if err != nil {
		t.Fatalf("run() with mock model should succeed, got: %v", err)
	}
}

// Test getVersion() when version is empty — verifies it doesn't panic
// and returns a sensible fallback (dev, tag, or revision).
func TestGetVersion_EmptyVersionNoPanic(t *testing.T) {
	orig := version
	version = ""
	defer func() { version = orig }()

	// Must not panic; must return non-empty string
	v := getVersion()
	if v == "" {
		t.Error("getVersion() with empty version should never return empty")
	}
}

// Test that getVersion always returns something meaningful even without VCS settings.
func TestGetVersion_ReturnsNonEmpty(t *testing.T) {
	// Multiple calls should be consistent
	v1 := getVersion()
	v2 := getVersion()
	if v1 != v2 {
		t.Errorf("getVersion() not idempotent: %q vs %q", v1, v2)
	}
	if v1 == "v0.2.0" && version == "" {
		// Old hardcoded value shouldn't appear unless ldflags set it
		t.Log("version appears to be v0.2.0 (may be ldflags override)")
	}
}

// Test parseRunFlags with edge cases.
func TestParseRunFlags_EdgeCases(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		check func(*testing.T, runFlags)
	}{
		{
			name: "only sandbox flag",
			args: []string{"--sandbox", "do work"},
			check: func(t *testing.T, f runFlags) {
				if !f.Sandbox {
					t.Error("Sandbox should be true")
				}
			},
		},
		{
			name: "flags after task",
			args: []string{"my task", "--sandbox"},
			check: func(t *testing.T, f runFlags) {
				if f.Task != "my task --sandbox" {
					t.Errorf("task = %q", f.Task)
				}
			},
		},
		{
			name: "all flags with values",
			args: []string{
				"--model", "gpt-4",
				"--base-url", "http://localhost:8080/v1",
				"--max-iter", "10",
				"--thinking", "high",
				"--system", "be helpful",
				"--sandbox",
				"explain code",
			},
			check: func(t *testing.T, f runFlags) {
				if f.Model != "gpt-4" {
					t.Errorf("Model = %q", f.Model)
				}
				if f.BaseURL != "http://localhost:8080/v1" {
					t.Errorf("BaseURL = %q", f.BaseURL)
				}
				if f.MaxIter != 10 {
					t.Errorf("MaxIter = %d", f.MaxIter)
				}
				if f.Thinking != "high" {
					t.Errorf("Thinking = %q", f.Thinking)
				}
				if f.System != "be helpful" {
					t.Errorf("System = %q", f.System)
				}
				if !f.Sandbox {
					t.Error("Sandbox should be true")
				}
				if f.Task != "explain code" {
					t.Errorf("Task = %q", f.Task)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := parseRunFlags(tt.args)
			if err != nil {
				t.Fatalf("parseRunFlags error: %v", err)
			}
			tt.check(t, f)
		})
	}
}

// dockerAvailable returns true if the docker CLI is available.
func dockerAvailable() bool {
	_, err := exec.LookPath("docker")
	return err == nil
}
