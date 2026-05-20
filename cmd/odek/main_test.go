package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/kode/internal/config"
	"github.com/BackendStack21/kode/internal/danger"
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
	if f.MaxIter != 0 {
		t.Errorf("MaxIter = %d, want 0 (default handled by config layer)", f.MaxIter)
	}
	if f.Sandbox != nil {
		t.Error("Sandbox should default to nil (not set)")
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
		"--sandbox-image", "node:20-alpine",
		"--sandbox-network", "bridge",
		"--sandbox-readonly",
		"--sandbox-memory", "512m",
		"--sandbox-cpus", "2",
		"--sandbox-user", "1000:1000",
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
	if f.Sandbox == nil || !*f.Sandbox {
		t.Error("Sandbox should be true")
	}
	if f.SandboxImage != "node:20-alpine" {
		t.Errorf("SandboxImage = %q", f.SandboxImage)
	}
	if f.SandboxNetwork != "bridge" {
		t.Errorf("SandboxNetwork = %q", f.SandboxNetwork)
	}
	if f.SandboxReadonly == nil || !*f.SandboxReadonly {
		t.Error("SandboxReadonly should be true")
	}
	if f.SandboxMemory != "512m" {
		t.Errorf("SandboxMemory = %q", f.SandboxMemory)
	}
	if f.SandboxCPUs != "2" {
		t.Errorf("SandboxCPUs = %q", f.SandboxCPUs)
	}
	if f.SandboxUser != "1000:1000" {
		t.Errorf("SandboxUser = %q", f.SandboxUser)
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
	if f.Sandbox == nil || !*f.Sandbox {
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
	origKODE := os.Getenv("KODE_API_KEY")
	origHome := os.Getenv("HOME")
	os.Unsetenv("DEEPSEEK_API_KEY")
	os.Unsetenv("OPENAI_API_KEY")
	os.Unsetenv("KODE_API_KEY")
	os.Setenv("HOME", t.TempDir()) // isolate from any ~/kode/config.json
	defer func() {
		os.Setenv("DEEPSEEK_API_KEY", origDS)
		os.Setenv("OPENAI_API_KEY", origOAI)
		os.Setenv("KODE_API_KEY", origKODE)
		os.Setenv("HOME", origHome)
	}()

	err := run([]string{"test task"})
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
}

func TestBuiltinTools(t *testing.T) {
	tools := builtinTools(danger.DangerousConfig{}, nil, nil)
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
		"odek run",
		"odek continue",
		"odek repl",
		"odek session",
		"trim",
		"cleanup",
		"odek init",
		"odek version",
		"Commands:",
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
		"--session",
		"--system",
		"--global",
		"--force",
		"~/kode/config.json",
		"KODE_MODEL",
		"KODE_API_KEY",
		"KODE_SANDBOX",
		"SANDBOX_IMAGE",
		"SANDBOX_NETWORK",
		"SANDBOX_READONLY",
		"SANDBOX_MEMORY",
		"SANDBOX_CPUS",
		"SANDBOX_USER",
		"--sandbox-image",
		"--sandbox-network",
		"--sandbox-readonly",
		"--sandbox-memory",
		"--sandbox-cpus",
		"--sandbox-user",
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
	if !strings.Contains(output, "odek run") {
		t.Error("captured output should contain 'odek run'")
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
	// fmt.Sscanf with non-numeric leaves the zero value (not set)
	if f.MaxIter != 0 {
		t.Errorf("MaxIter = %d, want 0 (non-numeric should leave zero)", f.MaxIter)
	}
	if f.Task != "task" {
		t.Errorf("Task = %q, want %q", f.Task, "task")
	}
}

// Test run() with --sandbox when Docker is not available — tests the
// sandbox error path.
func TestRun_SandboxNoDocker(t *testing.T) {
	if dockerAvailable() {
		t.Skip("docker is available, cannot test sandbox error path")
	}
	origDS := os.Getenv("DEEPSEEK_API_KEY")
	origOAI := os.Getenv("OPENAI_API_KEY")
	origHome := os.Getenv("HOME")
	os.Setenv("DEEPSEEK_API_KEY", "sk-test")
	os.Setenv("HOME", t.TempDir())
	defer func() {
		os.Setenv("DEEPSEEK_API_KEY", origDS)
		os.Setenv("OPENAI_API_KEY", origOAI)
		os.Setenv("HOME", origHome)
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
	origHome := os.Getenv("HOME")
	os.Setenv("DEEPSEEK_API_KEY", "sk-mock")
	os.Unsetenv("OPENAI_API_KEY")
	os.Setenv("HOME", t.TempDir())
	defer func() {
		os.Setenv("DEEPSEEK_API_KEY", origDS)
		os.Setenv("OPENAI_API_KEY", origOAI)
		os.Setenv("HOME", origHome)
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
				if f.Sandbox == nil || !*f.Sandbox {
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
				"--sandbox-image", "python:3.12",
				"--sandbox-network", "none",
				"--sandbox-readonly",
				"--sandbox-memory", "1g",
				"--sandbox-cpus", "4",
				"--sandbox-user", "1001:1001",
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
				if f.Sandbox == nil || !*f.Sandbox {
					t.Error("Sandbox should be true")
				}
				if f.SandboxImage != "python:3.12" {
					t.Errorf("SandboxImage = %q", f.SandboxImage)
				}
				if f.SandboxNetwork != "none" {
					t.Errorf("SandboxNetwork = %q", f.SandboxNetwork)
				}
				if f.SandboxReadonly == nil || !*f.SandboxReadonly {
					t.Error("SandboxReadonly should be true")
				}
				if f.SandboxMemory != "1g" {
					t.Errorf("SandboxMemory = %q", f.SandboxMemory)
				}
				if f.SandboxCPUs != "4" {
					t.Errorf("SandboxCPUs = %q", f.SandboxCPUs)
				}
				if f.SandboxUser != "1001:1001" {
					t.Errorf("SandboxUser = %q", f.SandboxUser)
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

// Test that KODE_* env vars flow through run() to kode.New().
func TestRun_WithKODEEnvVars(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"mocked"}}]}`))
	}))
	defer server.Close()

	origDS := os.Getenv("DEEPSEEK_API_KEY")
	origOAI := os.Getenv("OPENAI_API_KEY")
	origHome := os.Getenv("HOME")
	os.Unsetenv("DEEPSEEK_API_KEY")
	os.Unsetenv("OPENAI_API_KEY")
	os.Setenv("HOME", t.TempDir())
	// Also isolate KODE_API_KEY (should not be set)
	origKODE := os.Getenv("KODE_API_KEY")
	os.Unsetenv("KODE_API_KEY")
	defer func() {
		os.Setenv("DEEPSEEK_API_KEY", origDS)
		os.Setenv("OPENAI_API_KEY", origOAI)
		os.Setenv("HOME", origHome)
		os.Setenv("KODE_API_KEY", origKODE)
	}()

	// Set KODE_API_KEY so run() can find it (no env is set otherwise)
	os.Setenv("KODE_API_KEY", "sk-kode-env")

	err := run([]string{"--base-url", server.URL, "test task"})
	if err != nil {
		t.Fatalf("run() with KODE_API_KEY should succeed, got: %v", err)
	}
}

// Test that ~/kode/config.json flows through run().
func TestRun_WithGlobalConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"mocked"}}]}`))
	}))
	defer server.Close()

	origDS := os.Getenv("DEEPSEEK_API_KEY")
	origOAI := os.Getenv("OPENAI_API_KEY")
	origHome := os.Getenv("HOME")
	os.Unsetenv("DEEPSEEK_API_KEY")
	os.Unsetenv("OPENAI_API_KEY")
	os.Unsetenv("KODE_API_KEY")
	defer func() {
		os.Setenv("DEEPSEEK_API_KEY", origDS)
		os.Setenv("OPENAI_API_KEY", origOAI)
		os.Setenv("HOME", origHome)
	}()

	// Create a global config with an API key
	homeDir := t.TempDir()
	os.Setenv("HOME", homeDir)
	os.MkdirAll(homeDir+"/kode", 0755)
	if err := os.WriteFile(homeDir+"/kode/config.json", []byte(`{
		"api_key": "sk-global-config"
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	err := run([]string{"--base-url", server.URL, "test task"})
	if err != nil {
		t.Fatalf("run() with global config should succeed, got: %v", err)
	}
}

// Test that ./kode.json flows through run().
func TestRun_WithProjectConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"mocked"}}]}`))
	}))
	defer server.Close()

	origDS := os.Getenv("DEEPSEEK_API_KEY")
	origOAI := os.Getenv("OPENAI_API_KEY")
	origHome := os.Getenv("HOME")
	origCwd, _ := os.Getwd()
	os.Unsetenv("DEEPSEEK_API_KEY")
	os.Unsetenv("OPENAI_API_KEY")
	os.Unsetenv("KODE_API_KEY")
	defer func() {
		os.Setenv("DEEPSEEK_API_KEY", origDS)
		os.Setenv("OPENAI_API_KEY", origOAI)
		os.Setenv("HOME", origHome)
		os.Chdir(origCwd)
	}()

	// Isolate from any global config
	os.Setenv("HOME", t.TempDir())

	// Create project-level config in a temp directory
	projectDir := t.TempDir()
	os.Chdir(projectDir)
	if err := os.WriteFile(projectDir+"/kode.json", []byte(`{
		"api_key": "sk-project-config"
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	err := run([]string{"--base-url", server.URL, "test task"})
	if err != nil {
		t.Fatalf("run() with project config should succeed, got: %v", err)
	}
}

// dockerAvailable returns true if the docker CLI is available.
func dockerAvailable() bool {
	_, err := exec.LookPath("docker")
	return err == nil
}

// ── Init Config Tests ─────────────────────────────────────────────────

func TestInitConfig_Local(t *testing.T) {
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(cwd)

	origHome := os.Getenv("HOME")
	os.Setenv("HOME", t.TempDir()) // isolate from any global config
	defer os.Setenv("HOME", origHome)

	if err := initConfig([]string{}); err != nil {
		t.Fatalf("initConfig() error: %v", err)
	}

	// Verify the file was created
	data, err := os.ReadFile("kode.json")
	if err != nil {
		t.Fatalf("kode.json not created: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "deepseek-v4-flash") {
		t.Errorf("config should contain deepseek-v4-flash, got: %s", content)
	}
	if !strings.Contains(content, "api_key") {
		t.Errorf("config should contain api_key field, got: %s", content)
	}
}

func TestInitConfig_Global(t *testing.T) {
	dir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", origHome)

	if err := initConfig([]string{"--global"}); err != nil {
		t.Fatalf("initConfig() --global error: %v", err)
	}

	// Verify the file was created
	data, err := os.ReadFile(dir + "/kode/config.json")
	if err != nil {
		t.Fatalf("global config not created: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "deepseek-v4-flash") {
		t.Errorf("config should contain deepseek-v4-flash, got: %s", content)
	}
}

func TestInitConfig_LocalExists(t *testing.T) {
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(cwd)

	origHome := os.Getenv("HOME")
	os.Setenv("HOME", t.TempDir())
	defer os.Setenv("HOME", origHome)

	// Create existing config
	os.WriteFile("kode.json", []byte(`{"model": "existing"}`), 0644)

	// Should warn, not overwrite
	if err := initConfig([]string{}); err != nil {
		t.Fatalf("initConfig() error: %v", err)
	}

	// Content should be unchanged
	data, _ := os.ReadFile("kode.json")
	if !strings.Contains(string(data), "existing") {
		t.Error("config should not have been overwritten")
	}
}

func TestInitConfig_LocalForce(t *testing.T) {
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(cwd)

	origHome := os.Getenv("HOME")
	os.Setenv("HOME", t.TempDir())
	defer os.Setenv("HOME", origHome)

	// Create existing config
	os.WriteFile("kode.json", []byte(`{"model": "old"}`), 0644)

	// Force overwrite
	if err := initConfig([]string{"--force"}); err != nil {
		t.Fatalf("initConfig() --force error: %v", err)
	}

	// Content should be the template
	data, _ := os.ReadFile("kode.json")
	if strings.Contains(string(data), "old") {
		t.Error("config should have been overwritten")
	}
	if !strings.Contains(string(data), "deepseek-v4-flash") {
		t.Errorf("config should contain template, got: %s", string(data))
	}
}

func TestInitConfig_UnknownFlag(t *testing.T) {
	err := initConfig([]string{"--unknown"})
	if err == nil {
		t.Fatal("expected error for unknown flag")
	}
	if !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("error should mention unknown flag, got: %v", err)
	}
}

func TestInitConfig_ShortFlags(t *testing.T) {
	// Test -g and -f short flags
	dir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", origHome)

	if err := initConfig([]string{"-g", "-f"}); err != nil {
		t.Fatalf("initConfig() -g -f error: %v", err)
	}

	// Verify
	if _, err := os.Stat(dir + "/kode/config.json"); err != nil {
		t.Errorf("global config should exist after -g -f: %v", err)
	}
}

// TestInitConfig_RestrictivePermissions verifies that config files
// containing API keys are created with 0600 (owner read/write only),
// not 0644 (world-readable).
func TestInitConfig_RestrictivePermissions(t *testing.T) {
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(cwd)

	origHome := os.Getenv("HOME")
	os.Setenv("HOME", t.TempDir())
	defer os.Setenv("HOME", origHome)

	if err := initConfig([]string{}); err != nil {
		t.Fatalf("initConfig() error: %v", err)
	}

	// Check file permissions
	info, err := os.Stat("kode.json")
	if err != nil {
		t.Fatalf("kode.json not found: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("config file permissions = %04o, want 0600 (owner read/write only, no world/group read)", perm)
	}
}

// TestJsonMarshalName verifies that skill names with special characters
// (quotes, backslashes) are properly escaped in JSON output, preventing
// JSON injection in kode skill view/delete commands.
func TestJsonMarshalName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains string
	}{
		{"plain", "my-skill", `"name":"my-skill"`},
		{"with_quote", `skill"with"quotes`, `"name":"skill\"with\"quotes"`},
		{"with_backslash", `path\to\skill`, `"name":"path\\to\\skill"`},
		{"injection_attempt", `","evil":"true","x":"`, `"name":"\",\"evil\":\"true\",\"x\":\""`},
	}
	for _, tt := range tests {
		result := jsonMarshalName(tt.input)
		// Must be valid JSON (json.Unmarshal succeeds)
		var m map[string]string
		if err := json.Unmarshal([]byte(result), &m); err != nil {
			t.Errorf("jsonMarshalName(%q) produced invalid JSON: %v\nOutput: %s", tt.input, err, result)
			continue
		}
		if m["name"] != tt.input {
			t.Errorf("jsonMarshalName(%q): name = %q, want original input", tt.input, m["name"])
		}
	}
}

// ── Sandbox Tests ────────────────────────────────────────────────────

func TestResolveSandboxImage_Default(t *testing.T) {
	// No image configured, no Dockerfile.kode → alpine:latest
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(cwd)

	image, err := resolveSandboxImage(sandboxConfig{})
	if err != nil {
		t.Fatalf("resolveSandboxImage error: %v", err)
	}
	if image != "alpine:latest" {
		t.Errorf("image = %q, want %q", image, "alpine:latest")
	}
}

func TestResolveSandboxImage_Explicit(t *testing.T) {
	// Explicit image set → use it directly, ignore any Dockerfile.kode
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(cwd)

	// Even with a Dockerfile.kode, explicit should win
	os.WriteFile("Dockerfile.kode", []byte("FROM alpine"), 0644)

	image, err := resolveSandboxImage(sandboxConfig{Image: "node:20-alpine"})
	if err != nil {
		t.Fatalf("resolveSandboxImage error: %v", err)
	}
	if image != "node:20-alpine" {
		t.Errorf("image = %q, want %q", image, "node:20-alpine")
	}
}

func TestResolveSandboxImage_DockerfileKode(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker not available")
	}

	// No explicit image, Dockerfile.kode exists → build it
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(cwd)

	// Create a minimal Dockerfile.kode that doesn't need to pull
	if err := os.WriteFile("Dockerfile.kode", []byte("FROM scratch\nCMD []"), 0644); err != nil {
		t.Fatal(err)
	}

	image, err := resolveSandboxImage(sandboxConfig{})
	if err != nil {
		t.Fatalf("resolveSandboxImage error: %v", err)
	}

	// Should return a kode-sandbox:<hash> tag
	if !strings.HasPrefix(image, "kode-sandbox:") {
		t.Errorf("image = %q, want prefix 'kode-sandbox:'", image)
	}
}

func TestResolveSandboxImage_DockerfileKodeCached(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker not available")
	}

	// Same content → same hash → cached build
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(cwd)

	content := "FROM scratch\nCMD []"
	os.WriteFile("Dockerfile.kode", []byte(content), 0644)

	img1, err := resolveSandboxImage(sandboxConfig{})
	if err != nil {
		t.Fatal(err)
	}

	// Recreate with same content
	os.Remove("Dockerfile.kode")
	os.WriteFile("Dockerfile.kode", []byte(content), 0644)

	img2, err := resolveSandboxImage(sandboxConfig{})
	if err != nil {
		t.Fatal(err)
	}

	// Provided docker is available, the image was built and cached.
	// The hash is deterministic based on content.
	if img1 != img2 {
		t.Errorf("same Dockerfile.kode content should produce same hash, got %q vs %q", img1, img2)
	}
}

// Test that sandbox env vars flow through config.LoadConfig
func TestLoadConfig_SandboxEnvVars(t *testing.T) {
	os.Setenv("KODE_SANDBOX_IMAGE", "python:3.12-slim")
	os.Setenv("KODE_SANDBOX_NETWORK", "bridge")
	os.Setenv("KODE_SANDBOX_READONLY", "true")
	os.Setenv("KODE_SANDBOX_MEMORY", "1g")
	os.Setenv("KODE_SANDBOX_CPUS", "4")
	os.Setenv("KODE_SANDBOX_USER", "1000:1000")
	defer func() {
		os.Unsetenv("KODE_SANDBOX_IMAGE")
		os.Unsetenv("KODE_SANDBOX_NETWORK")
		os.Unsetenv("KODE_SANDBOX_READONLY")
		os.Unsetenv("KODE_SANDBOX_MEMORY")
		os.Unsetenv("KODE_SANDBOX_CPUS")
		os.Unsetenv("KODE_SANDBOX_USER")
	}()

	cfg := config.LoadConfig(config.CLIFlags{})
	if cfg.SandboxImage != "python:3.12-slim" {
		t.Errorf("SandboxImage = %q", cfg.SandboxImage)
	}
	if cfg.SandboxNetwork != "bridge" {
		t.Errorf("SandboxNetwork = %q", cfg.SandboxNetwork)
	}
	if !cfg.SandboxReadonly {
		t.Error("SandboxReadonly should be true")
	}
	if cfg.SandboxMemory != "1g" {
		t.Errorf("SandboxMemory = %q", cfg.SandboxMemory)
	}
	if cfg.SandboxCPUs != "4" {
		t.Errorf("SandboxCPUs = %q", cfg.SandboxCPUs)
	}
	if cfg.SandboxUser != "1000:1000" {
		t.Errorf("SandboxUser = %q", cfg.SandboxUser)
	}
}

// Test that sandbox config file fields flow through LoadConfig
func TestLoadConfig_SandboxFileConfig(t *testing.T) {
	dir := t.TempDir()
	prevHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", prevHome)

	// Create ~/kode/config.json with sandbox settings
	cfgDir := filepath.Join(dir, "kode")
	os.MkdirAll(cfgDir, 0755)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(`{
		"sandbox_image": "golang:1.24-alpine",
		"sandbox_network": "none",
		"sandbox_readonly": true,
		"sandbox_memory": "2g",
		"sandbox_cpus": "8",
		"sandbox_user": "1001:1001",
		"sandbox_env": {"GOCACHE": "/tmp/cache"},
		"sandbox_volumes": ["/cache:/cache"]
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := config.LoadConfig(config.CLIFlags{})
	if cfg.SandboxImage != "golang:1.24-alpine" {
		t.Errorf("SandboxImage = %q", cfg.SandboxImage)
	}
	if cfg.SandboxNetwork != "none" {
		t.Errorf("SandboxNetwork = %q", cfg.SandboxNetwork)
	}
	if !cfg.SandboxReadonly {
		t.Error("SandboxReadonly should be true")
	}
	if cfg.SandboxMemory != "2g" {
		t.Errorf("SandboxMemory = %q", cfg.SandboxMemory)
	}
	if cfg.SandboxCPUs != "8" {
		t.Errorf("SandboxCPUs = %q", cfg.SandboxCPUs)
	}
	if cfg.SandboxUser != "1001:1001" {
		t.Errorf("SandboxUser = %q", cfg.SandboxUser)
	}
	if cfg.SandboxEnv == nil || cfg.SandboxEnv["GOCACHE"] != "/tmp/cache" {
		t.Errorf("SandboxEnv = %v", cfg.SandboxEnv)
	}
	vols := cfg.SandboxVolumes
	if len(vols) != 1 || vols[0] != "/cache:/cache" {
		t.Errorf("SandboxVolumes = %v", vols)
	}
}

func TestBuildSandboxArgs_EnvAndVolumes(t *testing.T) {
	// Regression: continueCmd was missing Env and Volumes from sandboxConfig.
	// Verify that buildSandboxArgs correctly includes both.
	cfg := sandboxConfig{
		Image:   "alpine:latest",
		Network: "bridge",
		Env: map[string]string{
			"GOCACHE":   "/tmp/gocache",
			"NODE_ENV":  "test",
		},
		Volumes: []string{"/host/cache:/container/cache", "/host/data:/data:ro"},
	}
	args := buildSandboxArgs(cfg, "kode-test", "/tmp/workdir", cfg.Image)

	// Must contain env vars as "-e KEY=VALUE" pairs
	if !hasArgPair(args, "-e", "GOCACHE=/tmp/gocache") {
		t.Error("missing env var GOCACHE=/tmp/gocache in docker args")
	}
	if !hasArgPair(args, "-e", "NODE_ENV=test") {
		t.Error("missing env var NODE_ENV=test in docker args")
	}

	// Must contain volume mounts as "-v HOST:CONTAINER" pairs
	if !hasArgPair(args, "-v", "/host/cache:/container/cache") {
		t.Error("missing volume /host/cache:/container/cache in docker args")
	}
	if !hasArgPair(args, "-v", "/host/data:/data:ro") {
		t.Error("missing volume /host/data:/data:ro in docker args")
	}
}

func TestBuildSandboxArgs_EmptyEnvAndVolumes(t *testing.T) {
	// Verify buildSandboxArgs works with empty Env/Volumes (nil maps/slices).
	cfg := sandboxConfig{
		Image:   "alpine:latest",
		Network: "bridge",
	}
	args := buildSandboxArgs(cfg, "kode-test", "/tmp/workdir", cfg.Image)

	// Should not contain any -e or extra -v beyond the workspace mount
	for i, a := range args {
		if a == "-e" {
			t.Errorf("unexpected -e at position %d with empty env", i)
		}
	}
	// Count -v occurrences (should be exactly 1: workspace mount)
	vCount := 0
	for _, a := range args {
		if a == "-v" {
			vCount++
		}
	}
	if vCount != 1 {
		t.Errorf("expected 1 -v flag (workspace mount), got %d", vCount)
	}
}

// hasArgPair checks that args contains flag followed by expected value.
func hasArgPair(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

// ── REPL Flag Parsing Tests ───────────────────────────────────────────

func TestParseReplFlags_Defaults(t *testing.T) {
	f, err := parseReplFlags([]string{})
	if err != nil {
		t.Fatalf("parseReplFlags error: %v", err)
	}
	if f.ID != "" {
		t.Errorf("ID = %q, want empty", f.ID)
	}
	if f.Model != "" {
		t.Errorf("Model = %q, want empty (no CLI default)", f.Model)
	}
	if f.Thinking != "" {
		t.Errorf("Thinking = %q, want empty", f.Thinking)
	}
	if f.Sandbox != nil {
		t.Errorf("Sandbox = %v, want nil (not set via CLI)", *f.Sandbox)
	}
}

func TestParseReplFlags_SessionID(t *testing.T) {
	f, err := parseReplFlags([]string{"--id", "abc123"})
	if err != nil {
		t.Fatalf("parseReplFlags error: %v", err)
	}
	if f.ID != "abc123" {
		t.Errorf("ID = %q, want %q", f.ID, "abc123")
	}
}

func TestParseReplFlags_SandboxOnly(t *testing.T) {
	f, err := parseReplFlags([]string{"--sandbox"})
	if err != nil {
		t.Fatalf("parseReplFlags error: %v", err)
	}
	if f.Sandbox == nil || !*f.Sandbox {
		t.Error("Sandbox should be true when --sandbox flag present")
	}
}

func TestParseReplFlags_AllSandboxFlags(t *testing.T) {
	f, err := parseReplFlags([]string{
		"--sandbox",
		"--sandbox-image", "node:20-alpine",
		"--sandbox-network", "bridge",
		"--sandbox-readonly",
		"--sandbox-memory", "512m",
		"--sandbox-cpus", "2",
		"--sandbox-user", "1000:1000",
	})
	if err != nil {
		t.Fatalf("parseReplFlags error: %v", err)
	}
	if f.Sandbox == nil || !*f.Sandbox {
		t.Error("Sandbox should be true")
	}
	if f.SandboxImage != "node:20-alpine" {
		t.Errorf("SandboxImage = %q", f.SandboxImage)
	}
	if f.SandboxNetwork != "bridge" {
		t.Errorf("SandboxNetwork = %q", f.SandboxNetwork)
	}
	if f.SandboxReadonly == nil || !*f.SandboxReadonly {
		t.Error("SandboxReadonly should be true")
	}
	if f.SandboxMemory != "512m" {
		t.Errorf("SandboxMemory = %q", f.SandboxMemory)
	}
	if f.SandboxCPUs != "2" {
		t.Errorf("SandboxCPUs = %q", f.SandboxCPUs)
	}
	if f.SandboxUser != "1000:1000" {
		t.Errorf("SandboxUser = %q", f.SandboxUser)
	}
}

func TestParseReplFlags_ModelAndThinking(t *testing.T) {
	f, err := parseReplFlags([]string{
		"--model", "deepseek-v4-pro",
		"--thinking", "enabled",
	})
	if err != nil {
		t.Fatalf("parseReplFlags error: %v", err)
	}
	if f.Model != "deepseek-v4-pro" {
		t.Errorf("Model = %q, want %q", f.Model, "deepseek-v4-pro")
	}
	if f.Thinking != "enabled" {
		t.Errorf("Thinking = %q, want %q", f.Thinking, "enabled")
	}
}

func TestParseReplFlags_Combined(t *testing.T) {
	f, err := parseReplFlags([]string{
		"--id", "sess-001",
		"--sandbox",
		"--sandbox-image", "alpine:3.19",
		"--model", "deepseek-v4-flash",
		"--thinking", "disabled",
	})
	if err != nil {
		t.Fatalf("parseReplFlags error: %v", err)
	}
	if f.ID != "sess-001" {
		t.Errorf("ID = %q", f.ID)
	}
	if f.Sandbox == nil || !*f.Sandbox {
		t.Error("Sandbox should be true")
	}
	if f.SandboxImage != "alpine:3.19" {
		t.Errorf("SandboxImage = %q", f.SandboxImage)
	}
	if f.Model != "deepseek-v4-flash" {
		t.Errorf("Model = %q", f.Model)
	}
	if f.Thinking != "disabled" {
		t.Errorf("Thinking = %q", f.Thinking)
	}
}

func TestParseReplFlags_ExtraArgsIgnored(t *testing.T) {
	// Extra unrecognized arguments should not cause errors
	f, err := parseReplFlags([]string{"--sandbox", "extra", "positional", "args"})
	if err != nil {
		t.Fatalf("parseReplFlags should not error on extra args: %v", err)
	}
	if f.Sandbox == nil || !*f.Sandbox {
		t.Error("Sandbox should be true even with extra args")
	}
}

// ── Self-Learning E2E Tests ───────────────────────────────────────────

// multiTurnServer returns an httptest server that simulates a multi-turn
// conversation: n terminal tool calls followed by a final text response.
// Each tool call executes echo step N (safe, no side effects).
func multiTurnServer(t *testing.T, terminalCalls int) *httptest.Server {
	t.Helper()
	callCount := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount <= terminalCalls {
			fmt.Fprintf(w, `{"choices":[{"message":{"content":"Running step %d.","tool_calls":[{"id":"call_%d","function":{"name":"shell","arguments":"{\"command\":\"echo step %d\"}"}}]}}]}`,
				callCount, callCount, callCount)
		} else {
			w.Write([]byte(`{"choices":[{"message":{"content":"All steps completed successfully."}}]}`))
		}
	}))
}

// TestRunLearn_MultiStepProcedure is an end-to-end test of the
// --learn pipeline: mock LLM simulates 4 terminal calls → multi-step
// heuristic fires → user accepts → skill file saved on disk.
func TestRunLearn_MultiStepProcedure(t *testing.T) {
	server := multiTurnServer(t, 4)
	defer server.Close()

	homeDir := t.TempDir()
	origDS := os.Getenv("DEEPSEEK_API_KEY")
	origOAI := os.Getenv("OPENAI_API_KEY")
	origHome := os.Getenv("HOME")
	os.Setenv("DEEPSEEK_API_KEY", "sk-mock")
	os.Unsetenv("OPENAI_API_KEY")
	os.Setenv("HOME", homeDir)
	defer func() {
		os.Setenv("DEEPSEEK_API_KEY", origDS)
		os.Setenv("OPENAI_API_KEY", origOAI)
		os.Setenv("HOME", origHome)
	}()

	// Simulate stdin: "y" to accept the suggestion
	oldStdin := os.Stdin
	inR, inW, _ := os.Pipe()
	os.Stdin = inR
	defer func() { os.Stdin = oldStdin }()
	go func() {
		inW.Write([]byte("y\n"))
	}()

	// Capture stderr
	oldStderr := os.Stderr
	errR, errW, _ := os.Pipe()
	os.Stderr = errW
	defer func() { os.Stderr = oldStderr }()

	err := run([]string{"--learn", "--base-url", server.URL, "multi step task"})
	errW.Close()
	errOutput, _ := io.ReadAll(errR)

	if err != nil {
		t.Fatalf("run() error: %v", err)
	}

	stderrStr := string(errOutput)

	// Heuristic fired
	if !strings.Contains(stderrStr, "Learning: detected") {
		t.Error("expected 'Learning: detected' in stderr")
	}
	if !strings.Contains(stderrStr, "Save as skill?") {
		t.Error("expected 'Save as skill?' prompt")
	}
	if !strings.Contains(stderrStr, "Saved skill") {
		t.Error("expected 'Saved skill' confirmation")
	}
	if !strings.Contains(stderrStr, "multi-step") {
		t.Error("expected 'multi-step' heuristic in output")
	}

	// Skill file written to disk
	skillDir := filepath.Join(homeDir, ".kode", "skills", "procedure-echo")
	skillFile := filepath.Join(skillDir, "SKILL.md")
	if _, err := os.Stat(skillFile); os.IsNotExist(err) {
		t.Errorf("expected skill file at %s", skillFile)
	}
}

// TestRunLearn_RejectSuggestion verifies that when the user declines
// a skill suggestion, no file is written.
func TestRunLearn_RejectSuggestion(t *testing.T) {
	server := multiTurnServer(t, 4)
	defer server.Close()

	homeDir := t.TempDir()
	origDS := os.Getenv("DEEPSEEK_API_KEY")
	origOAI := os.Getenv("OPENAI_API_KEY")
	origHome := os.Getenv("HOME")
	os.Setenv("DEEPSEEK_API_KEY", "sk-mock")
	os.Unsetenv("OPENAI_API_KEY")
	os.Setenv("HOME", homeDir)
	defer func() {
		os.Setenv("DEEPSEEK_API_KEY", origDS)
		os.Setenv("OPENAI_API_KEY", origOAI)
		os.Setenv("HOME", origHome)
	}()

	// Simulate stdin: "n" to reject
	oldStdin := os.Stdin
	inR, inW, _ := os.Pipe()
	os.Stdin = inR
	defer func() { os.Stdin = oldStdin }()
	go func() {
		inW.Write([]byte("n\n"))
	}()

	oldStderr := os.Stderr
	errR, errW, _ := os.Pipe()
	os.Stderr = errW
	defer func() { os.Stderr = oldStderr }()

	err := run([]string{"--learn", "--base-url", server.URL, "multi step task"})
	errW.Close()
	errOutput, _ := io.ReadAll(errR)

	if err != nil {
		t.Fatalf("run() error: %v", err)
	}

	stderrStr := string(errOutput)

	if !strings.Contains(stderrStr, "Learning: detected") {
		t.Error("expected detection to fire")
	}
	if !strings.Contains(stderrStr, "Skipped") {
		t.Error("expected 'Skipped' when user rejects")
	}
	if strings.Contains(stderrStr, "Saved skill") {
		t.Error("should NOT contain 'Saved skill' when rejected")
	}

	// Verify no skill file written
	skillDir := filepath.Join(homeDir, ".kode", "skills", "procedure-echo")
	skillFile := filepath.Join(skillDir, "SKILL.md")
	if _, err := os.Stat(skillFile); !os.IsNotExist(err) {
		t.Errorf("skill file should NOT exist after rejection: %s", skillFile)
	}
}

// TestRunLearn_NoSuggestions verifies that when the agent produces
// only text (no tool calls), no learning suggestions are generated.
func TestRunLearn_NoSuggestions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"Here is a text-only response with no commands."}}]}`))
	}))
	defer server.Close()

	homeDir := t.TempDir()
	origDS := os.Getenv("DEEPSEEK_API_KEY")
	origOAI := os.Getenv("OPENAI_API_KEY")
	origHome := os.Getenv("HOME")
	os.Setenv("DEEPSEEK_API_KEY", "sk-mock")
	os.Unsetenv("OPENAI_API_KEY")
	os.Setenv("HOME", homeDir)
	defer func() {
		os.Setenv("DEEPSEEK_API_KEY", origDS)
		os.Setenv("OPENAI_API_KEY", origOAI)
		os.Setenv("HOME", origHome)
	}()

	oldStderr := os.Stderr
	errR, errW, _ := os.Pipe()
	os.Stderr = errW
	defer func() { os.Stderr = oldStderr }()

	err := run([]string{"--learn", "--base-url", server.URL, "text only task"})
	errW.Close()
	errOutput, _ := io.ReadAll(errR)

	if err != nil {
		t.Fatalf("run() error: %v", err)
	}

	stderrStr := string(errOutput)

	if strings.Contains(stderrStr, "Learning: detected") {
		t.Error("should NOT detect learning patterns for text-only response")
	}
	if strings.Contains(stderrStr, "Save as skill?") {
		t.Error("should NOT show save prompt when no patterns detected")
	}
}
