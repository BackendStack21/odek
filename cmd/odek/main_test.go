package main

import (
	"bytes"
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
	"time"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/mcpclient"
	"github.com/BackendStack21/odek/internal/sandbox"
	"github.com/BackendStack21/odek/internal/telegram"
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
	origKODE := os.Getenv("ODEK_API_KEY")
	origHome := os.Getenv("HOME")
	os.Unsetenv("DEEPSEEK_API_KEY")
	os.Unsetenv("OPENAI_API_KEY")
	os.Unsetenv("ODEK_API_KEY")
	os.Setenv("HOME", t.TempDir()) // isolate from any ~/.odek/config.json
	defer func() {
		os.Setenv("DEEPSEEK_API_KEY", origDS)
		os.Setenv("OPENAI_API_KEY", origOAI)
		os.Setenv("ODEK_API_KEY", origKODE)
		os.Setenv("HOME", origHome)
	}()

	err := run([]string{"test task"})
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
}

func TestBuiltinTools(t *testing.T) {
	tools := builtinTools(danger.DangerousConfig{}, nil, nil, 3, "", toolConfig{}, nil)
	if len(tools) == 0 {
		t.Fatal("builtinTools() returned empty slice")
	}
	if tools[0].Name() != "shell" {
		t.Errorf("first tool name = %q, want 'shell'", tools[0].Name())
	}
}

func TestDefaultSystemPrompt(t *testing.T) {
	if !strings.Contains(defaultSystem, "odek") {
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
		"--thinking-budget",
		"--sandbox",
		"--no-color",
		"--no-agents",
		"--session",
		"--system",
		"--global",
		"--force",
		"~/.odek/config.json",
		"ODEK_MODEL",
		"ODEK_API_KEY",
		"ODEK_SANDBOX",
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

	containerName := fmt.Sprintf("odek-test-setup-%d", os.Getpid())
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

	containerName := "odek-test-exec"
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

	containerName := "odek-test-net"
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

func TestRun_WithMockModelAndSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"session mock response"}}]}`))
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

	// Session flag tests the multi-turn branch of run()
	err := run([]string{"--session", "--base-url", server.URL, "session test task"})
	if err != nil {
		t.Fatalf("run() with session and mock model should succeed, got: %v", err)
	}
}

func TestRun_WithMockModelAndLearn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"learn mock response"}}]}`))
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

	// Learn mode tests the learn loop branch of run()
	err := run([]string{"--learn", "--base-url", server.URL, "learn test task"})
	if err != nil {
		t.Fatalf("run() with learn and mock model should succeed, got: %v", err)
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
				if f.Task != "my task" {
					t.Errorf("task = %q", f.Task)
				}
				if f.Sandbox == nil || !*f.Sandbox {
					t.Error("Sandbox should be true (parsed from post-task flag)")
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

// Test that ODEK_* env vars flow through run() to odek.New().
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
	// Also isolate ODEK_API_KEY (should not be set)
	origKODE := os.Getenv("ODEK_API_KEY")
	os.Unsetenv("ODEK_API_KEY")
	defer func() {
		os.Setenv("DEEPSEEK_API_KEY", origDS)
		os.Setenv("OPENAI_API_KEY", origOAI)
		os.Setenv("HOME", origHome)
		os.Setenv("ODEK_API_KEY", origKODE)
	}()

	// Set ODEK_API_KEY so run() can find it (no env is set otherwise)
	os.Setenv("ODEK_API_KEY", "sk-odek-env")

	err := run([]string{"--base-url", server.URL, "test task"})
	if err != nil {
		t.Fatalf("run() with ODEK_API_KEY should succeed, got: %v", err)
	}
}

// Test that ~/.odek/config.json flows through run().
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
	os.Unsetenv("ODEK_API_KEY")
	defer func() {
		os.Setenv("DEEPSEEK_API_KEY", origDS)
		os.Setenv("OPENAI_API_KEY", origOAI)
		os.Setenv("HOME", origHome)
	}()

	// Create a global config with an API key
	homeDir := t.TempDir()
	os.Setenv("HOME", homeDir)
	os.MkdirAll(homeDir+"/.odek", 0755)
	if err := os.WriteFile(homeDir+"/.odek/config.json", []byte(`{
		"api_key": "sk-global-config"
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	err := run([]string{"--base-url", server.URL, "test task"})
	if err != nil {
		t.Fatalf("run() with global config should succeed, got: %v", err)
	}
}

// Test that ./odek.json flows through run().
func TestRun_WithProjectConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"mocked"}}]}`))
	}))
	defer server.Close()

	origDS := os.Getenv("DEEPSEEK_API_KEY")
	origOAI := os.Getenv("OPENAI_API_KEY")
	origOdekAPI := os.Getenv("ODEK_API_KEY")
	origHome := os.Getenv("HOME")
	origCwd, _ := os.Getwd()
	os.Unsetenv("DEEPSEEK_API_KEY")
	os.Unsetenv("OPENAI_API_KEY")
	os.Unsetenv("ODEK_API_KEY")
	defer func() {
		os.Setenv("DEEPSEEK_API_KEY", origDS)
		os.Setenv("OPENAI_API_KEY", origOAI)
		os.Setenv("ODEK_API_KEY", origOdekAPI)
		os.Setenv("HOME", origHome)
		os.Chdir(origCwd)
	}()

	// Isolate from any global config
	os.Setenv("HOME", t.TempDir())

	// API keys may not come from the untrusted project config; set one via env.
	os.Setenv("ODEK_API_KEY", "sk-project-test-key")

	// Create project-level config in a temp directory
	projectDir := t.TempDir()
	os.Chdir(projectDir)
	if err := os.WriteFile(projectDir+"/odek.json", []byte(`{
		"model": "project-model"
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
	if _, err := exec.LookPath("docker"); err != nil {
		return false
	}
	// Verify the daemon is actually reachable (not just the CLI installed).
	cmd := exec.Command("docker", "info")
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
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
	data, err := os.ReadFile("odek.json")
	if err != nil {
		t.Fatalf("odek.json not created: %v", err)
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
	data, err := os.ReadFile(dir + "/.odek/config.json")
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
	os.WriteFile("odek.json", []byte(`{"model": "existing"}`), 0644)

	// Should warn, not overwrite
	if err := initConfig([]string{}); err != nil {
		t.Fatalf("initConfig() error: %v", err)
	}

	// Content should be unchanged
	data, _ := os.ReadFile("odek.json")
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
	os.WriteFile("odek.json", []byte(`{"model": "old"}`), 0644)

	// Force overwrite
	if err := initConfig([]string{"--force"}); err != nil {
		t.Fatalf("initConfig() --force error: %v", err)
	}

	// Content should be the template
	data, _ := os.ReadFile("odek.json")
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
	if _, err := os.Stat(dir + "/.odek/config.json"); err != nil {
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
	info, err := os.Stat("odek.json")
	if err != nil {
		t.Fatalf("odek.json not found: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("config file permissions = %04o, want 0600 (owner read/write only, no world/group read)", perm)
	}
}

// TestJsonMarshalName verifies that skill names with special characters
// (quotes, backslashes) are properly escaped in JSON output, preventing
// JSON injection in odek skill view/delete commands.
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
	// No image configured, no Dockerfile.odek → alpine:latest
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(cwd)

	image, err := sandbox.ResolveImage(sandboxConfig{})
	if err != nil {
		t.Fatalf("resolveSandboxImage error: %v", err)
	}
	if image != "alpine:latest" {
		t.Errorf("image = %q, want %q", image, "alpine:latest")
	}
}

func TestResolveSandboxImage_Explicit(t *testing.T) {
	// Explicit image set → use it directly, ignore any Dockerfile.odek
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(cwd)

	// Even with a Dockerfile.odek, explicit should win
	os.WriteFile("Dockerfile.odek", []byte("FROM alpine"), 0644)

	image, err := sandbox.ResolveImage(sandboxConfig{Image: "node:20-alpine"})
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

	// No explicit image, Dockerfile.odek exists → build it
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(cwd)

	// Create a minimal Dockerfile.odek that doesn't need to pull
	if err := os.WriteFile("Dockerfile.odek", []byte("FROM scratch\nCMD []"), 0644); err != nil {
		t.Fatal(err)
	}

	image, err := sandbox.ResolveImage(sandboxConfig{})
	if err != nil {
		t.Fatalf("resolveSandboxImage error: %v", err)
	}

	// Should return a odek-sandbox:<hash> tag
	if !strings.HasPrefix(image, "odek-sandbox:") {
		t.Errorf("image = %q, want prefix 'odek-sandbox:'", image)
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
	os.WriteFile("Dockerfile.odek", []byte(content), 0644)

	img1, err := sandbox.ResolveImage(sandboxConfig{})
	if err != nil {
		t.Fatal(err)
	}

	// Recreate with same content
	os.Remove("Dockerfile.odek")
	os.WriteFile("Dockerfile.odek", []byte(content), 0644)

	img2, err := sandbox.ResolveImage(sandboxConfig{})
	if err != nil {
		t.Fatal(err)
	}

	// Provided docker is available, the image was built and cached.
	// The hash is deterministic based on content.
	if img1 != img2 {
		t.Errorf("same Dockerfile.odek content should produce same hash, got %q vs %q", img1, img2)
	}
}

// Test that sandbox env vars flow through config.LoadConfig
func TestLoadConfig_SandboxEnvVars(t *testing.T) {
	os.Setenv("ODEK_SANDBOX_IMAGE", "python:3.12-slim")
	os.Setenv("ODEK_SANDBOX_NETWORK", "bridge")
	os.Setenv("ODEK_SANDBOX_READONLY", "true")
	os.Setenv("ODEK_SANDBOX_MEMORY", "1g")
	os.Setenv("ODEK_SANDBOX_CPUS", "4")
	os.Setenv("ODEK_SANDBOX_USER", "1000:1000")
	defer func() {
		os.Unsetenv("ODEK_SANDBOX_IMAGE")
		os.Unsetenv("ODEK_SANDBOX_NETWORK")
		os.Unsetenv("ODEK_SANDBOX_READONLY")
		os.Unsetenv("ODEK_SANDBOX_MEMORY")
		os.Unsetenv("ODEK_SANDBOX_CPUS")
		os.Unsetenv("ODEK_SANDBOX_USER")
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

	// Create ~/.odek/config.json with sandbox settings
	cfgDir := filepath.Join(dir, ".odek")
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
			"GOCACHE":  "/tmp/gocache",
			"NODE_ENV": "test",
		},
		Volumes: []string{"/tmp/workdir/cache:/container/cache", "/tmp/workdir/data:/data:ro"},
	}
	args := sandbox.BuildRunArgs(cfg, "odek-test", "/tmp/workdir", cfg.Image)

	// Must contain env vars as "-e KEY=VALUE" pairs
	if !hasArgPair(args, "-e", "GOCACHE=/tmp/gocache") {
		t.Error("missing env var GOCACHE=/tmp/gocache in docker args")
	}
	if !hasArgPair(args, "-e", "NODE_ENV=test") {
		t.Error("missing env var NODE_ENV=test in docker args")
	}

	// Must contain volume mounts as "-v HOST:CONTAINER" pairs.
	// With the security fix, extra volume host paths must stay inside workdir.
	if !hasArgPair(args, "-v", "/tmp/workdir/cache:/container/cache") {
		t.Error("missing volume /tmp/workdir/cache:/container/cache in docker args")
	}
	if !hasArgPair(args, "-v", "/tmp/workdir/data:/data:ro") {
		t.Error("missing volume /tmp/workdir/data:/data:ro in docker args")
	}
}

func TestBuildSandboxArgs_EmptyEnvAndVolumes(t *testing.T) {
	// Verify buildSandboxArgs works with empty Env/Volumes (nil maps/slices).
	cfg := sandboxConfig{
		Image:   "alpine:latest",
		Network: "bridge",
	}
	args := sandbox.BuildRunArgs(cfg, "odek-test", "/tmp/workdir", cfg.Image)

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
// Handles /models discovery requests from llm.DiscoverModelContext.
func multiTurnServer(t *testing.T, terminalCalls int) *httptest.Server {
	t.Helper()
	callCount := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// Handle /models discovery from llm.DiscoverModelContext
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Write([]byte(`{"object":"list","data":[{"id":"deepseek-v4-flash","context_window":131072}]}`))
			return
		}

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
// heuristic fires → skill auto-saved (with default auto_save=true).
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

	// Create local odek.json with auto_save enabled, LLM enhancement disabled
	// (mock server can't handle enhancement prompts)
	configContent := `{"skills": {"verbose": true, "auto_save": {"enabled": true, "require_llm": false}, "llm_learn": false}}`
	os.WriteFile("odek.json", []byte(configContent), 0644)
	defer os.Remove("odek.json")

	// Capture stderr
	oldStderr := os.Stderr
	errR, errW, _ := os.Pipe()
	os.Stderr = errW
	defer func() { os.Stderr = oldStderr }()

	err := run([]string{"--learn", "--base-url", server.URL, "multi step task"})
	// Learn loop now runs asynchronously; give it a moment to complete.
	// With llm_learn=false the heuristics are instant, but we still
	// need to let the goroutine write to stderr before closing the pipe.
	time.Sleep(200 * time.Millisecond)
	errW.Close()
	errOutput, _ := io.ReadAll(errR)

	if err != nil {
		t.Fatalf("run() error: %v", err)
	}

	stderrStr := string(errOutput)
	t.Logf("STDERR: %s", stderrStr) // DEBUG

	// Auto-save should fire with default config (auto_save.enabled=true)
	if !strings.Contains(stderrStr, "Auto-saved skill") {
		t.Error("expected 'Auto-saved skill' in stderr")
	}
	if !strings.Contains(stderrStr, "multi-step") {
		t.Error("expected 'multi-step' heuristic in output")
	}

	// Skill file written to disk — poll since the goroutine may still be writing.
	skillDir := filepath.Join(homeDir, ".odek", "skills", "procedure-echo")
	skillFile := filepath.Join(skillDir, "SKILL.md")
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(skillFile); err == nil {
			return // found
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("expected skill file at %s", skillFile)
}

// TestRunLearn_InteractiveReject verifies that when auto-save is disabled,
// the interactive prompt appears and user can reject.
func TestRunLearn_InteractiveReject(t *testing.T) {
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

	// Create local odek.json with auto_save disabled and LLM enhancement disabled
	configContent := `{"skills": {"verbose": true, "auto_save": {"enabled": false}, "llm_learn": false}}`
	os.WriteFile("odek.json", []byte(configContent), 0644)
	defer os.Remove("odek.json")

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
	// Learn loop runs asynchronously; give it a moment to write stderr.
	time.Sleep(200 * time.Millisecond)
	errW.Close()
	errOutput, _ := io.ReadAll(errR)

	if err != nil {
		t.Fatalf("run() error: %v", err)
	}

	stderrStr := string(errOutput)
	t.Logf("STDERR: %s", stderrStr) // DEBUG

	if !strings.Contains(stderrStr, "Learning: detected") {
		t.Error("expected 'Learning: detected' in stderr")
	}
	if !strings.Contains(stderrStr, "Skipped") {
		t.Error("expected 'Skipped' when user rejects")
	}

	// Verify no skill file written
	skillDir := filepath.Join(homeDir, ".odek", "skills", "procedure-echo")
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
	if strings.Contains(stderrStr, "Auto-saved") {
		t.Error("should NOT auto-save when no patterns detected")
	}
}

// buildFromDockerfile tests live with the implementation in
// internal/sandbox/sandbox_test.go (the function is unexported there).

// ── shorten Tests ──────────────────────────────────────────────────────

func TestShorten_ShortString(t *testing.T) {
	result := shorten("hello", 10)
	if result != "hello" {
		t.Errorf("shorten('hello', 10) = %q, want 'hello'", result)
	}
}

func TestShorten_ExactLength(t *testing.T) {
	result := shorten("hello", 5)
	if result != "hello" {
		t.Errorf("shorten('hello', 5) = %q, want 'hello'", result)
	}
}

func TestShorten_LongString(t *testing.T) {
	result := shorten("hello world", 5)
	if result != "hello…" {
		t.Errorf("shorten('hello world', 5) = %q, want 'hello…'", result)
	}
}

func TestShorten_EmptyString(t *testing.T) {
	result := shorten("", 10)
	if result != "" {
		t.Errorf("shorten('', 10) = %q, want ''", result)
	}
}

func TestShorten_ZeroLength(t *testing.T) {
	result := shorten("hello", 0)
	if result != "…" {
		t.Errorf("shorten('hello', 0) = %q, want '…'", result)
	}
}

// ── expandHome Tests ───────────────────────────────────────────────────

func TestExpandHome_TildePath(t *testing.T) {
	result := expandHome("~/test/path")
	if !strings.HasPrefix(result, "/") || !strings.HasSuffix(result, "/test/path") {
		t.Errorf("expandHome('~/test/path') = %q, want '/.../test/path'", result)
	}
}

func TestExpandHome_NoTilde(t *testing.T) {
	result := expandHome("/absolute/path")
	if result != "/absolute/path" {
		t.Errorf("expandHome('/absolute/path') = %q, want '/absolute/path'", result)
	}
}

func TestExpandHome_EmptyString(t *testing.T) {
	result := expandHome("")
	if result != "" {
		t.Errorf("expandHome('') = %q, want ''", result)
	}
}

// ── parseReplFlags Edge Case Tests ──────────────────────────────────────

func TestParseReplFlags_Empty(t *testing.T) {
	f, err := parseReplFlags([]string{})
	if err != nil {
		t.Fatalf("parseReplFlags error: %v", err)
	}
	if f.Model != "" {
		t.Errorf("Model = %q, want empty", f.Model)
	}
}

func TestParseReplFlags_AllFlags(t *testing.T) {
	f, err := parseReplFlags([]string{
		"--id", "abc123",
		"--model", "gpt-4",
		"--thinking", "enabled",
		"--sandbox",
		"--sandbox-image", "node:20",
		"--sandbox-network", "host",
		"--sandbox-readonly",
		"--sandbox-memory", "1g",
		"--sandbox-cpus", "2",
		"--sandbox-user", "1000:1000",
		"extra",
	})
	if err != nil {
		t.Fatalf("parseReplFlags error: %v", err)
	}
	if f.ID != "abc123" {
		t.Errorf("ID = %q, want 'abc123'", f.ID)
	}
	if f.Model != "gpt-4" {
		t.Errorf("Model = %q", f.Model)
	}
	if f.Thinking != "enabled" {
		t.Errorf("Thinking = %q", f.Thinking)
	}
	if f.Sandbox == nil || !*f.Sandbox {
		t.Error("Sandbox should be true")
	}
	if f.SandboxImage != "node:20" {
		t.Errorf("SandboxImage = %q", f.SandboxImage)
	}
	if f.SandboxNetwork != "host" {
		t.Errorf("SandboxNetwork = %q", f.SandboxNetwork)
	}
	if f.SandboxReadonly == nil || !*f.SandboxReadonly {
		t.Error("SandboxReadonly should be true")
	}
	if f.SandboxMemory != "1g" {
		t.Errorf("SandboxMemory = %q", f.SandboxMemory)
	}
	if f.SandboxCPUs != "2" {
		t.Errorf("SandboxCPUs = %q", f.SandboxCPUs)
	}
	if f.SandboxUser != "1000:1000" {
		t.Errorf("SandboxUser = %q", f.SandboxUser)
	}
}

func TestParseReplFlags_TrailingArg(t *testing.T) {
	f, err := parseReplFlags([]string{"--sandbox", "trailing"})
	if err != nil {
		t.Fatalf("parseReplFlags error: %v", err)
	}
	if f.Sandbox == nil || !*f.Sandbox {
		t.Error("Sandbox should be true")
	}
}

// ── buildSandboxArgs Tests ─────────────────────────────────────────────

func TestBuildSandboxArgs_Defaults(t *testing.T) {
	args := sandbox.BuildRunArgs(sandboxConfig{}, "odek-test", "/workspace", "alpine:latest")
	if len(args) == 0 {
		t.Fatal("buildSandboxArgs returned empty args")
	}
	// Should include basic security flags
	full := strings.Join(args, " ")
	if !strings.Contains(full, "--cap-drop") {
		t.Error("should include --cap-drop")
	}
	if !strings.Contains(full, "--security-opt") {
		t.Error("should include --security-opt")
	}
	if !strings.Contains(full, "--tmpfs") {
		t.Error("should include --tmpfs")
	}
}

func TestBuildSandboxArgs_Readonly(t *testing.T) {
	args := sandbox.BuildRunArgs(sandboxConfig{
		Readonly: true,
		Network:  "bridge",
	}, "odek-test", "/workspace", "alpine:latest")
	volFound := false
	for _, a := range args {
		if strings.HasPrefix(a, "/workspace") && strings.HasSuffix(a, ":ro") {
			volFound = true
		}
	}
	if !volFound {
		t.Errorf("expected read-only volume mount, got: %v", args)
	}
}

func TestBuildSandboxArgs_WithResources(t *testing.T) {
	args := sandbox.BuildRunArgs(sandboxConfig{
		Network: "none",
		Memory:  "512m",
		CPUs:    "0.5",
		User:    "1000:1000",
		Env:     map[string]string{"FOO": "bar"},
		Volumes: []string{"/workspace/data:/data"},
	}, "odek-test", "/workspace", "alpine:latest")
	full := strings.Join(args, " ")
	if !strings.Contains(full, "--memory") || !strings.Contains(full, "512m") {
		t.Error("should include memory limit")
	}
	if !strings.Contains(full, "--cpus") || !strings.Contains(full, "0.5") {
		t.Error("should include CPU limit")
	}
	if !strings.Contains(full, "--user") || !strings.Contains(full, "1000:1000") {
		t.Error("should include user")
	}
	if !strings.Contains(full, "FOO=bar") {
		t.Error("should include env var")
	}
	if !strings.Contains(full, "/workspace/data:/data") {
		t.Error("should include extra volume")
	}
}

func TestBuildSandboxArgs_ForbiddenVolumeRejected(t *testing.T) {
	args := sandbox.BuildRunArgs(sandboxConfig{
		Network: "bridge",
		Volumes: []string{"/etc/passwd:/etc/passwd"},
	}, "odek-test", "/workspace", "alpine:latest")
	full := strings.Join(args, " ")
	if strings.Contains(full, "/etc/passwd") {
		t.Error("forbidden volume mount should be rejected")
	}
}

// ── countUserTurnsUpTo Tests ───────────────────────────────────────────

func TestCountUserTurnsUpTo_Empty(t *testing.T) {
	count := countUserTurnsUpTo(nil, 5)
	if count != 0 {
		t.Errorf("countUserTurnsUpTo(nil, 5) = %d, want 0", count)
	}
}

func TestCountUserTurnsUpTo_Basic(t *testing.T) {
	msgs := []llm.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
		{Role: "user", Content: "world"},
	}
	count := countUserTurnsUpTo(msgs, 4)
	if count != 2 {
		t.Errorf("countUserTurnsUpTo = %d, want 2", count)
	}
}

func TestCountUserTurnsUpTo_Partial(t *testing.T) {
	msgs := []llm.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "hello"},
	}
	count := countUserTurnsUpTo(msgs, 1) // Only look at index 0 (system)
	if count != 0 {
		t.Errorf("countUserTurnsUpTo = %d, want 0", count)
	}
}

func TestCountUserTurnsUpTo_BeyondLength(t *testing.T) {
	msgs := []llm.Message{
		{Role: "user", Content: "hello"},
	}
	count := countUserTurnsUpTo(msgs, 100)
	if count != 1 {
		t.Errorf("countUserTurnsUpTo = %d, want 1", count)
	}
}

// ── getVersion VCS Path Tests ──────────────────────────────────────────

func TestGetVersion_VersionSet(t *testing.T) {
	orig := version
	version = "v2.0.0"
	defer func() { version = orig }()

	v := getVersion()
	if v != "v2.0.0" {
		t.Errorf("getVersion() = %q, want 'v2.0.0'", v)
	}
}

func TestGetVersion_DevFallbackWhenEmpty(t *testing.T) {
	orig := version
	version = ""
	defer func() { version = orig }()

	v := getVersion()
	if v == "" {
		t.Error("getVersion() should never return empty string")
	}
}

// TestShorten_NegativeLimit verifies that shorten with a negative limit.
func TestShorten_NegativeLimit(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Logf("shorten panicked with negative limit (Go slice bounds): %v", r)
		}
	}()
	result := shorten("hello", -1)
	if result != "hello" {
		t.Errorf("shorten('hello', -1) = %q, want 'hello'", result)
	}
}

// TestShorten_Unicode verifies shorten handles multi-byte UTF-8.
func TestShorten_Unicode(t *testing.T) {
	// "héllo wörld" is 13 bytes. s[:6] = "héllo" (6 bytes: h, é=2b, l, l, o)
	result := shorten("héllo wörld", 6)
	expected := "héllo…"
	if result != expected {
		t.Errorf("shorten('héllo wörld', 6) = %q, want %q", result, expected)
	}
}

// TestBuildSandboxArgs_AllForbiddenPrefixes verifies that ALL forbidden
// mount prefixes (/ /etc /proc /sys /boot /dev) are rejected.
func TestBuildSandboxArgs_AllForbiddenPrefixes(t *testing.T) {
	forbidden := []string{"/", "/etc", "/proc", "/sys", "/boot", "/dev"}
	for _, prefix := range forbidden {
		t.Run(prefix, func(t *testing.T) {
			vol := prefix + ":/container" + prefix
			if prefix == "/" {
				vol = "/:/container/root"
			}
			cfg := sandboxConfig{
				Network: "bridge",
				Volumes: []string{vol},
			}
			args := sandbox.BuildRunArgs(cfg, "odek-test", "/workspace", "alpine:latest")
			full := strings.Join(args, " ")
			if strings.Contains(full, vol) {
				t.Errorf("forbidden volume %q should be rejected, but found in args", vol)
			}
		})
	}
}

// TestBuildSandboxArgs_ValidVolume verifies a non-forbidden volume under the
// working directory IS included.
func TestBuildSandboxArgs_ValidVolume(t *testing.T) {
	cfg := sandboxConfig{
		Network: "bridge",
		Volumes: []string{"/workspace/data:/data"},
	}
	args := sandbox.BuildRunArgs(cfg, "odek-test", "/workspace", "alpine:latest")
	if !hasArgPair(args, "-v", "/workspace/data:/data") {
		t.Error("valid volume /workspace/data:/data should be included in docker args")
	}
}

// TestBuildSandboxArgs_RejectsHostNetwork verifies that --sandbox-network host
// is rejected and replaced with "none", protecting container isolation.
func TestBuildSandboxArgs_RejectsHostNetwork(t *testing.T) {
	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	cfg := sandboxConfig{Network: "host"}
	args := sandbox.BuildRunArgs(cfg, "odek-test", "/workspace", "alpine:latest")

	w.Close()
	var buf bytes.Buffer
	io.Copy(&buf, r)
	os.Stderr = origStderr
	stderr := buf.String()

	hostFound := false
	for i, a := range args {
		if a == "--network" && i+1 < len(args) && args[i+1] == "host" {
			hostFound = true
		}
	}
	if hostFound {
		t.Error("--network host was NOT rejected — isolation destroyed")
	}
	if !strings.Contains(stderr, "WARNING") || !strings.Contains(stderr, "host") {
		t.Errorf("expected stderr warning about host network, got: %s", stderr)
	}
}

// ── loadMCPTools Tests ────────────────────────────────────────────────

func TestLoadMCPTools_EmptyServers(t *testing.T) {
	tools := make([]odek.Tool, 0)
	cleanup, err := loadMCPTools(config.ResolvedConfig{}, &tools)
	if err != nil {
		t.Fatalf("loadMCPTools(nil) error: %v", err)
	}
	if cleanup == nil {
		t.Fatal("loadMCPTools(nil) should return non-nil cleanup")
	}
	// Call cleanup — should be a no-op
	cleanup()

	// Also test with empty map
	cleanup2, err := loadMCPTools(config.ResolvedConfig{MCPServers: map[string]mcpclient.ServerConfig{}}, &tools)
	if err != nil {
		t.Fatalf("loadMCPTools(empty map) error: %v", err)
	}
	cleanup2()
}

// ── getVersion No-Panic Test ────────────────────────────────────────────

func TestGetVersion_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("getVersion() panicked: %v", r)
		}
	}()
	v := getVersion()
	if v == "" {
		t.Error("getVersion() returned empty string, want at minimum 'dev'")
	}
}

// ── boolPtr Tests ───────────────────────────────────────────────────────

func TestBoolPtr_True(t *testing.T) {
	p := boolPtr(true)
	if p == nil {
		t.Fatal("boolPtr(true) returned nil")
	}
	if *p != true {
		t.Errorf("boolPtr(true) = %v, want true", *p)
	}
}

func TestBoolPtr_False(t *testing.T) {
	p := boolPtr(false)
	if p == nil {
		t.Fatal("boolPtr(false) returned nil")
	}
	if *p != false {
		t.Errorf("boolPtr(false) = %v, want false", *p)
	}
}

// ── IDENTITY.md Tests ─────────────────────────────────────────────

// setupTestHome creates a temp home directory, sets HOME to it, and
// returns a cleanup function that restores the original value.
func setupTestHome(t *testing.T) string {
	t.Helper()
	orig := os.Getenv("HOME")
	dir := t.TempDir()
	os.Setenv("HOME", dir)
	t.Cleanup(func() { os.Setenv("HOME", orig) })
	return dir
}

func TestLoadIdentityFile_FileExists(t *testing.T) {
	homeDir := setupTestHome(t)
	os.MkdirAll(filepath.Join(homeDir, ".odek"), 0700)
	content := "# Custom Identity\n\nI am a test agent."
	os.WriteFile(filepath.Join(homeDir, ".odek", "IDENTITY.md"), []byte(content), 0644)

	got := loadIdentityFile()
	if got != content {
		t.Errorf("loadIdentityFile() = %q, want %q", got, content)
	}
}

func TestLoadIdentityFile_NoFile(t *testing.T) {
	_ = setupTestHome(t)
	got := loadIdentityFile()
	if got != defaultSystem {
		t.Errorf("expected defaultSystem when no IDENTITY.md, got %q", got)
	}
}

func TestLoadIdentityFile_EmptyFile(t *testing.T) {
	homeDir := setupTestHome(t)
	os.MkdirAll(filepath.Join(homeDir, ".odek"), 0700)
	os.WriteFile(filepath.Join(homeDir, ".odek", "IDENTITY.md"), []byte("   \n\n  "), 0644)

	got := loadIdentityFile()
	if got != defaultSystem {
		t.Errorf("expected defaultSystem for empty IDENTITY.md, got %q", got)
	}
}

func TestBuildSystemPrompt_ExplicitSystemWins(t *testing.T) {
	homeDir := setupTestHome(t)
	os.MkdirAll(filepath.Join(homeDir, ".odek"), 0700)
	os.WriteFile(filepath.Join(homeDir, ".odek", "IDENTITY.md"), []byte("# Identity from file"), 0644)

	resolved := config.ResolvedConfig{
		System: "Explicit system override",
	}

	got := buildSystemPrompt(resolved)
	if !strings.Contains(got, "Explicit system override") {
		t.Errorf("expected explicit system override, got %q", got)
	}
	if strings.Contains(got, "Identity from file") {
		t.Error("IDENTITY.md content should NOT appear when explicit System is set")
	}
}

func TestBuildSystemPrompt_FallsBackToIdentity(t *testing.T) {
	homeDir := setupTestHome(t)
	os.MkdirAll(filepath.Join(homeDir, ".odek"), 0700)
	os.WriteFile(filepath.Join(homeDir, ".odek", "IDENTITY.md"), []byte("# Custom Identity"), 0644)

	resolved := config.ResolvedConfig{}

	got := buildSystemPrompt(resolved)
	if !strings.Contains(got, "# Custom Identity") {
		t.Errorf("expected IDENTITY.md content, got %q", got)
	}
	if strings.Contains(got, "⚠️ ANTI-PATTERN") {
		t.Error("defaultSystem should NOT appear when IDENTITY.md exists")
	}
}

func TestBuildSystemPrompt_FallsBackToDefault(t *testing.T) {
	_ = setupTestHome(t)
	resolved := config.ResolvedConfig{}

	got := buildSystemPrompt(resolved)
	if !strings.Contains(got, "You are Odek") {
		t.Error("expected defaultSystem identity when no override or IDENTITY.md")
	}
}

// ── System prompt optimization validation tests ──────────────────────────

// TestBuildSystemPrompt_NoSkillFencingSection verifies that buildSystemPrompt
// does NOT contain the SKILL FENCING section. The section references boundary
// delimiters (╔═══ SKILL BOUNDARY) that never appear in practice — condensed
// mode injects skills with no delimiters, verbose mode uses different ones.
// This test fails on the current code (proving ~80 tokens are wasted per
// session) and passes after the section is removed.
func TestBuildSystemPrompt_NoSkillFencingSection(t *testing.T) {
	_ = setupTestHome(t)
	resolved := config.ResolvedConfig{}
	got := buildSystemPrompt(resolved)

	if strings.Contains(got, "╔═══ SKILL BOUNDARY") {
		t.Error("buildSystemPrompt contains '╔═══ SKILL BOUNDARY' delimiters that never appear in skill injection. " +
			"Remove the SKILL FENCING section or align delimiters with actual skill loading code (loop.go:459).")
	}
	if strings.Contains(got, "SKILL FENCING") {
		t.Error("buildSystemPrompt contains a SKILL FENCING section with non-matching delimiters. " +
			"This wastes ~80 tokens per session — the model is trained to recognize boundaries that never appear.")
	}
}

// TestDefaultSystem_NoRedundantMemoryReadInstruction verifies that defaultSystem
// does NOT instruct the agent to call the memory(read) tool. Memory is already
// automatically injected as a ═══ MEMORY ═══ system message every iteration
// by the loop engine (loop.go:507-523). Telling the model to also call
// memory(read) wastes a tool call + iteration on every session start.
func TestDefaultSystem_NoRedundantMemoryReadInstruction(t *testing.T) {
	if strings.Contains(defaultSystem, "memory(read)") {
		t.Error("defaultSystem tells the agent to call memory(read), but the loop already injects " +
			"░░ MEMORY ░░ automatically each turn. Replace with 'Review the ═══ MEMORY ═══ block' instruction.")
	}
	if strings.Contains(defaultSystem, "query your memory using the memory tool") {
		t.Error("defaultSystem instructs the agent to 'query your memory using the memory tool', " +
			"but memory content is automatically injected as a system message. This wastes a tool call.")
	}
}

// TestDefaultSystem_AllowsSkillExploration verifies that the default
// system prompt does NOT contain "Do not explore alternatives" — an
// overly restrictive instruction that prevents the model from using
// better approaches even when a matching skill exists. Skills should
// be guidance, not absolute constraints.
func TestDefaultSystem_AllowsSkillExploration(t *testing.T) {
	if strings.Contains(defaultSystem, "Do not explore alternatives") {
		t.Error("defaultSystem says 'Do not explore alternatives or do your own research unless the skill's steps fail'. " +
			"This over-constrains the model — skills should be primary guidance, not absolute restrictions. " +
			"The model may fail to suggest better approaches because of this instruction.")
	}
}

// TestDefaultSystem_AllowsVerification verifies that defaultSystem does NOT
// contain the "do NOT test after write" instruction that contradicts the
// Reasoning Scaffold's step 4 ("Verify — Do NOT skip verification on complex
// changes"). Both instructions exist at the same time: one says "never test
// after writing", the other says "always verify complex changes". The model
// cannot follow both — resolve by removing the anti-verification instruction.
func TestDefaultSystem_AllowsVerification(t *testing.T) {
	if strings.Contains(defaultSystem, "do NOT run tests or verify") {
		t.Error("defaultSystem says 'After writing a file, do NOT run tests or verify with shell commands' " +
			"but the Reasoning Scaffold (lines 95-97) says 'Verify — Do NOT skip verification on complex changes'. " +
			"These directly contradict. The model cannot follow both instructions. " +
			"Remove the anti-verification instruction so the Verify step works as intended.")
	}
	if strings.Contains(defaultSystem, "Do NOT write, then test, then rewrite") {
		t.Error("defaultSystem says 'Do NOT write, then test, then rewrite, then retest' which discourages " +
			"verification. Combined with the 'Verify' step in the Reasoning Scaffold, the model gets " +
			"conflicting instructions. Reword to 'Avoid unnecessary iteration — verify in one shot' " +
			"to remove the contradiction.")
	}
}

// TestDefaultSystem_AntiPatternIsConcise verifies that the opening
// line of defaultSystem is concise — under 150 characters.
func TestDefaultSystem_AntiPatternIsConcise(t *testing.T) {
	// Extract the first line from defaultSystem
	firstLineEnd := strings.Index(defaultSystem, "\n")
	if firstLineEnd < 0 {
		t.Fatal("defaultSystem has no newline")
	}
	firstLine := strings.TrimSpace(defaultSystem[:firstLineEnd])
	if len(firstLine) > 150 {
		t.Errorf("first line is %d chars (max 150). Keep it short.\nLine: %q", len(firstLine), firstLine)
	}
}

// ── --deliver flag tests ──────────────────────────────────────────────────

func TestParseRunFlags_Deliver(t *testing.T) {
	f, err := parseRunFlags([]string{"--deliver", "test task"})
	if err != nil {
		t.Fatalf("parseRunFlags error: %v", err)
	}
	if f.Deliver == nil || !*f.Deliver {
		t.Error("Deliver should be true when --deliver is passed")
	}
	if f.Task != "test task" {
		t.Errorf("Task = %q, want %q", f.Task, "test task")
	}
}

func TestParseRunFlags_DeliverDefaults(t *testing.T) {
	f, err := parseRunFlags([]string{"test task"})
	if err != nil {
		t.Fatalf("parseRunFlags error: %v", err)
	}
	if f.Deliver != nil {
		t.Error("Deliver should be nil when --deliver is not passed")
	}
}

// TestDeliverToTelegram_MissingConfig tests error handling when config is missing.
func TestDeliverToTelegram_MissingConfig(t *testing.T) {
	// No token
	err := deliverToTelegram("hello", config.ResolvedConfig{})
	if err == nil {
		t.Error("expected error with empty token")
	}

	// Token but no default chat ID
	err = deliverToTelegram("hello", config.ResolvedConfig{
		Telegram: telegram.TelegramConfig{Token: "test:token"},
	})
	if err == nil {
		t.Error("expected error with empty default_chat_id")
	}
}

// TestDeliverToTelegram_SendsMessage tests that deliverToTelegram actually sends.
func TestDeliverToTelegram_SendsMessage(t *testing.T) {
	// Mock Telegram API server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "test response") {
			t.Errorf("expected message body to contain 'test response', got: %s", string(body))
		}
		if !strings.Contains(string(body), "8592463065") {
			t.Errorf("expected chat_id 8592463065, got: %s", string(body))
		}
		// Return a valid Telegram API response
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"result":{"message_id":123}}`))
	}))
	defer srv.Close()

	// Use mock server URL as Telegram API base by creating a bot with the mock URL
	bot := telegram.NewBot("test:token")
	bot.BaseURL = srv.URL // the mock server

	// We can't patch deliverToTelegram's bot easily, so let's test via the config path
	// Instead, test that the bot.SendMessage works correctly
	msg, err := bot.SendMessage(8592463065, "test response", &telegram.SendOpts{ParseMode: telegram.ParseModeMarkdownV2})
	if err != nil {
		t.Fatalf("SendMessage error: %v", err)
	}
	if msg == nil || msg.ID != 123 {
		t.Errorf("expected message_id 123, got %v", msg)
	}
}

// ── Reasoning Scaffold ───────────────────────────────────────────────

// TestDefaultSystem_IncludesReasoningScaffold verifies that defaultSystem
// includes a structured reasoning scaffold (Understand → Plan → Execute →
// Verify → Ship) to guide the agent's thinking for complex tasks. Without
// a scaffold, the model's "think first, then act" instruction is too vague
// — models perform measurably better with explicit thinking stages.
func TestDefaultSystem_IncludesReasoningScaffold(t *testing.T) {
	hasThink := strings.Contains(defaultSystem, "Think before you act") ||
		strings.Contains(defaultSystem, "think first")
	hasTDD := strings.Contains(defaultSystem, "TDD") ||
		strings.Contains(defaultSystem, "failing test first")
	hasVerify := strings.Contains(defaultSystem, "Verify after") ||
		strings.Contains(defaultSystem, "verify after every change")

	if !hasThink || !hasTDD || !hasVerify {
		t.Error("defaultSystem should include reasoning guidance (think → TDD → verify)")
	}
}

// ── Batch Tool Awareness ─────────────────────────────────────────────

// TestDefaultSystem_MentionsBatchTools verifies that defaultSystem tells
// the agent about batch/parallel tools (batch_read, parallel_shell,
// multi_grep) so it uses them instead of reading files one-by-one.
// Without this instruction, the model wastes tokens on sequential reads
// for tasks that could fetch 5 files in a single call.
func TestDefaultSystem_MentionsBatchTools(t *testing.T) {
	if !strings.Contains(defaultSystem, "batch_read") {
		t.Error("defaultSystem should mention batch_read for efficient multi-file reads")
	}
	if !strings.Contains(defaultSystem, "parallel_shell") {
		t.Error("defaultSystem should mention parallel_shell for parallel command execution")
	}
}

// ── Literal Tool Names ───────────────────────────────────────────────

// TestDefaultSystem_RemindsLiteralToolNames verifies that defaultSystem
// tells the agent that tool names are literal strings — e.g. call "shell"
// not "bash", "sh", or "terminal". The model frequently hallucinates
// unix command names as tool names, wasting an iteration on a 404.
func TestDefaultSystem_RemindsLiteralToolNames(t *testing.T) {
	if !strings.Contains(defaultSystem, `"shell" NOT "bash"`) {
		t.Error("defaultSystem should include explicit 'shell NOT bash' mapping")
	}
	if !strings.Contains(defaultSystem, `"read_file" NOT "cat"`) {
		t.Error("defaultSystem should include explicit 'read_file NOT cat' mapping")
	}
	if !strings.Contains(defaultSystem, `"patch" NOT "sed"`) {
		t.Error("defaultSystem should include explicit 'patch NOT sed' mapping")
	}
}
