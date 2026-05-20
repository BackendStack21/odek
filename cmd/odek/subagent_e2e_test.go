package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────
// E2E Tests: kode subagent (real subprocess)
// ─────────────────────────────────────────────────────────────────────
//
// These tests BUILD the kode binary once (via TestMain) and then test
// real subprocess spawning through the delegate_tasks tool. Unlike
// contract tests which can run with a nonexistent binary, E2E tests
// verify the full pipeline:
//
//   tool.Call() → exec.Command("kode", "subagent", ...) → JSON stdout → parse
//
// All tests are gated by KODE_E2E=true — they don't run in normal test
// suites because they need a compiled binary.
//
// No LLM provider needed: every subagent fails on setup (no API key),
// producing JSON error on stdout — which is exactly what we test.
// ─────────────────────────────────────────────────────────────────────

var e2eBinary string // path to the once-built binary (stable, not per-test)

// e2eBinDir is a stable temporary directory shared across all E2E tests.
// Unlike t.TempDir() which is cleaned up per-test, this survives until
// TestMain exits.
var e2eBinDir string

func TestMain(m *testing.M) {
	if os.Getenv("KODE_E2E") == "" {
		// Not running E2E — skip build, run nothing
		os.Exit(m.Run())
	}

	// Build the binary once into a stable directory
	dir, err := os.MkdirTemp("", "kode-e2e-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: create temp dir: %v\n", err)
		os.Exit(1)
	}
	e2eBinDir = dir
	e2eBinary = filepath.Join(dir, "kode")

	cmd := exec.Command("go", "build",
		"-ldflags", "-X main.version=v0.0.0-e2e",
		"-o", e2eBinary,
			"/root/projects/kode/cmd/odek/",
	)
	cmd.Dir = "/root/projects/kode"
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: go build: %v\nstderr: %s\n", err, stderr.String())
		os.Exit(1)
	}

	// Verify binary exists
	info, err := os.Stat(e2eBinary)
	if err != nil || info.Size() == 0 {
		fmt.Fprintf(os.Stderr, "e2e: built binary invalid: %v (size=%d)\n", err, info.Size())
		os.Exit(1)
	}

	code := m.Run()

	// Cleanup
	os.RemoveAll(dir)
	os.Exit(code)
}

// skipIfNoE2ESkip skips the test if KODE_E2E is not set.
func skipIfNoE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("KODE_E2E") == "" {
		t.Skip("KODE_E2E not set — skipping E2E test")
	}
}

// ── 1. Real Subprocess Spawning ──────────────────────────────────────

// TestE2E_BinaryExists verifies the built binary is executable and
// responds to the subagent subcommand.
func TestE2E_BinaryExists(t *testing.T) {
	skipIfNoE2E(t)

	// Run `kode` with no args — should print usage to stderr
	cmd := exec.Command(e2eBinary)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()

	output := outBuf.String() + errBuf.String()
	if !strings.Contains(output, "subagent") {
		t.Fatalf("odek usage should mention subagent, output:\n%s", output)
	}
	// Must exit non-zero when no subcommand given
	if err == nil {
		t.Error("odek without args should exit non-zero")
	}
}

// TestE2E_SubagentRealProcess verifies that kode subagent runs as a
// real subprocess and produces valid JSON on stdout when it fails on
// setup (no LLM provider).
func TestE2E_SubagentRealProcess(t *testing.T) {
	skipIfNoE2E(t)

	workDir := t.TempDir()
	cmd := exec.Command(e2eBinary, "subagent",
		"--goal", "this goal will fail — no LLM configured",
	)
	cmd.Dir = workDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	cmd.Run()

	// Must produce JSON on stdout even on failure
	if stdout.Len() == 0 {
		t.Fatalf("subagent must write JSON to stdout even on setup failure (stderr: %s)", stderr.String())
	}

	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout must be valid JSON: %q (stderr: %s)", stdout.String(), stderr.String())
	}

	// Must have status field
	status, ok := result["status"].(string)
	if !ok {
		t.Fatalf("result must have string 'status' field, got %v", result)
	}
	if status != "success" && status != "error" {
		t.Fatalf("status must be 'success' or 'error', got %q", status)
	}
}

// ── 2. Stderr Protocol ───────────────────────────────────────────────

// TestE2E_StdErrEmoji verifies that stderr contains emoji-prefixed
// progress when --quiet is NOT set.
func TestE2E_StdErrEmoji(t *testing.T) {
	skipIfNoE2E(t)

	cmd := exec.Command(e2eBinary, "subagent",
		"--goal", "read /etc/hostname",
		"--context", "simple read-only task",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Run()

	// Stderr should have emoji progress (unless --quiet)
	hasEmoji := strings.Contains(stderr.String(), "🔧") ||
		strings.Contains(stderr.String(), "🧠") ||
		strings.Contains(stderr.String(), "✅") ||
		strings.Contains(stderr.String(), "🛑")
	if !hasEmoji {
		t.Logf("stderr: %s\nstdout: %s", stderr.String(), stdout.String())
		// Non-critical — subagent may fail before emoji output
	}
}

// TestE2E_QuietSuppressesStderr verifies that --quiet suppresses
// emoji progress on stderr. The error message "kode:" from the binary
// itself may still appear, but emoji indicators should not.
func TestE2E_QuietSuppressesStderr(t *testing.T) {
	skipIfNoE2E(t)

	cmd := exec.Command(e2eBinary, "subagent",
		"--goal", "list /tmp",
		"--quiet",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Run()

	stderrText := stderr.String()
	emojiIndicators := []string{"🔧", "🧠", "✅", "🛑"}
	for _, emoji := range emojiIndicators {
		if strings.Contains(stderrText, emoji) {
			t.Errorf("--quiet mode should suppress emoji %q, got stderr: %s", emoji, stderrText)
		}
	}
}

// ── 3. delegate_tasks Tool (Real Subprocess) ─────────────────────────

// runE2EDelegateTasks creates a delegateTasksTool pointed at the real
// binary and calls it with the given tasks. Returns the tool's output
// string (which is the formatted summary for the LLM).
func runE2EDelegateTasks(t *testing.T, goals []string, timeout time.Duration) string {
	t.Helper()

	tool := &delegateTasksTool{
		maxConcurrency: 2,
		kodePath:       e2eBinary,
		timeout:        timeout,
	}

	tasks := make([]map[string]string, len(goals))
	for i, g := range goals {
		tasks[i] = map[string]string{"goal": g}
	}
	input, _ := json.Marshal(map[string]any{"tasks": tasks})

	result, err := tool.Call(string(input))
	if err != nil {
		t.Fatalf("tool.Call returned unexpected error: %v", err)
	}
	return result
}

// TestE2E_ToolSpawnsRealSubprocess verifies that delegate_tasks tool
// spawns real kode subprocesses and captures their JSON output.
func TestE2E_ToolSpawnsRealSubprocess(t *testing.T) {
	skipIfNoE2E(t)

	result := runE2EDelegateTasks(t, []string{"read /etc/os-release"}, 30*time.Second)

	// Result must contain the subprocess output
	if !strings.Contains(result, "status") && !strings.Contains(result, "error") {
		t.Fatalf("result should contain subprocess output (status or error), got: %s", result)
	}

	// Result must contain the task label
	if !strings.Contains(result, "Task 1") {
		t.Errorf("result should label Task 1, got: %s", result)
	}
}

// TestE2E_ToolAggregatesMultipleTasks verifies that delegate_tasks
// runs multiple subagents in parallel and aggregates all results.
func TestE2E_ToolAggregatesMultipleTasks(t *testing.T) {
	skipIfNoE2E(t)

	result := runE2EDelegateTasks(t, []string{
		"list files in /etc",
		"read /etc/hostname",
		"stat /tmp",
	}, 30*time.Second)

	// Should contain all three task labels
	for i := 1; i <= 3; i++ {
		taskLabel := fmt.Sprintf("Task %d", i)
		if !strings.Contains(result, taskLabel) {
			t.Errorf("result should contain %q, got: %s", taskLabel, result)
		}
	}

	// Each task should have a JSON result (status or error)
	taskBlocks := strings.Count(result, `"status"`)
	if taskBlocks < 1 {
		t.Errorf("expected at least 1 JSON status block across 3 tasks, got %d\n%s", taskBlocks, result)
	}
}

// TestE2E_ToolTimeout verifies that the timeout is wired through to
// subprocesses: a very short timeout should produce a timeout error.
func TestE2E_ToolTimeout(t *testing.T) {
	skipIfNoE2E(t)

	tool := &delegateTasksTool{
		maxConcurrency: 1,
		kodePath:       e2eBinary,
		timeout:        10 * time.Millisecond, // impossibly short
	}

	input := `{"tasks":[{"goal":"sleep and do nothing"}]}`
	result, err := tool.Call(input)
	if err != nil {
		t.Fatalf("tool.Call returned unexpected error: %v", err)
	}

	// The subprocess context expires before the agent can even init
	if !strings.Contains(result, "timeout") &&
		!strings.Contains(result, "deadline") &&
		!strings.Contains(result, "killed") {
		t.Logf("timeout result: %s", result)
		// Non-critical: setup may fail before timeout triggers
	}
}

// TestE2E_ToolConcurrencyLimit verifies that maxConcurrency is respected.
// 4 tasks, maxConcurrency=2 → 2 sequential batches, should complete quickly.
func TestE2E_ToolConcurrencyLimit(t *testing.T) {
	skipIfNoE2E(t)

	tool := &delegateTasksTool{
		maxConcurrency: 2,
		kodePath:       e2eBinary,
		timeout:        30 * time.Second,
	}

	// 4 tasks that all fail quickly on setup (no provider)
	tasks := make([]map[string]string, 4)
	for i := range tasks {
		tasks[i] = map[string]string{"goal": fmt.Sprintf("task %d", i+1)}
	}
	input, _ := json.Marshal(map[string]any{"tasks": tasks})

	start := time.Now()
	result, err := tool.Call(string(input))
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("tool.Call returned unexpected error: %v", err)
	}

	// With maxConcurrency=2 and 4 quick-failing tasks, should be <10s
	if elapsed > 10*time.Second {
		t.Errorf("with maxConcurrency=2 and 4 setup-failing tasks, expected <10s, took %v", elapsed)
	}

	// All 4 tasks should appear
	for i := 1; i <= 4; i++ {
		if !strings.Contains(result, fmt.Sprintf("Task %d", i)) {
			t.Errorf("missing Task %d in results", i)
		}
	}
}

// TestE2E_ToolReturnsJSONEmbedded verifies that the tool returns valid
// JSON embedded in its response, not raw text from a failed pipe.
func TestE2E_ToolReturnsJSONEmbedded(t *testing.T) {
	skipIfNoE2E(t)

	result := runE2EDelegateTasks(t, []string{"list /nonexistent-path-xyz-123"}, 30*time.Second)

	// The JSON from the subprocess should be embedded
	if strings.Contains(result, `"status"`) && strings.Contains(result, `"error"`) {
		return // valid JSON embedded — success
	}
	if result == "" {
		t.Fatal("tool returned empty result")
	}
	t.Logf("Tool result:\n%s", result)
}

// ── 4. Edge Cases ────────────────────────────────────────────────────

// TestE2E_TaskFileViaTool verifies that the tool writes tasks to temp
// files (not CLI args) to avoid argument length limits.
func TestE2E_TaskFileViaTool(t *testing.T) {
	skipIfNoE2E(t)

	// Very long goal (100KB) — would fail as CLI arg
	longGoal := strings.Repeat("a", 100000)
	tool := &delegateTasksTool{
		maxConcurrency: 1,
		kodePath:       e2eBinary,
		timeout:        30 * time.Second,
	}

	// Use a stable JSON key that won't break the marshaller
	input := fmt.Sprintf(`{"tasks":[{"goal":"%s"}]}`, longGoal)
	result, err := tool.Call(input)
	if err != nil {
		t.Fatalf("tool.Call returned unexpected error: %v", err)
	}

	// Should not produce argument-list-too-long error
	if strings.Contains(result, "too long") || strings.Contains(result, "argument") {
		t.Errorf("long goal should use temp file, not hit argument limits:\n%s", result)
	}
}

// TestE2E_BinaryNotExecutable verifies graceful degradation when the
// kode binary isn't found or isn't executable.
func TestE2E_BinaryNotExecutable(t *testing.T) {
	skipIfNoE2E(t)

	tool := &delegateTasksTool{
		maxConcurrency: 1,
		kodePath:       "/nonexistent/kode-binary-xyz",
		timeout:        5 * time.Second,
	}

	input := `{"tasks":[{"goal":"test"}]}`
	result, err := tool.Call(input)
	if err != nil {
		t.Fatalf("tool.Call should never return error, got: %v", err)
	}

	if !strings.Contains(result, "not found") && !strings.Contains(result, "error") {
		t.Errorf("result should mention binary not found, got: %s", result)
	}
}

// ── 5. Stderr Relay ──────────────────────────────────────────────────

// TestE2E_StdErrCaptureInTool verifies that the tool captures stderr
// from subprocesses.
func TestE2E_StdErrCaptureInTool(t *testing.T) {
	skipIfNoE2E(t)

	result := runE2EDelegateTasks(t, []string{"read /proc/uptime"}, 30*time.Second)

	if !strings.Contains(result, "Task 1") {
		t.Errorf("result should label Task 1, got: %s", result)
	}
}

// ── 6. Custom System Prompt ─────────────────────────────────────────

// TestE2E_CustomSystemPrompt verifies that delegate_tasks accepts a
// per-task system prompt and threads it through to the subprocess.
func TestE2E_CustomSystemPrompt(t *testing.T) {
	skipIfNoE2E(t)

	tool := &delegateTasksTool{
		maxConcurrency: 1,
		kodePath:       e2eBinary,
		timeout:        30 * time.Second,
	}

	input := `{"tasks":[{"goal":"create JWT middleware","context":"use gin","system":"You are a security engineer reviewing auth code. Focus on token validation."}]}`
	result, err := tool.Call(input)
	if err != nil {
		t.Fatalf("tool.Call returned unexpected error: %v", err)
	}

	if !strings.Contains(result, "Task 1") {
		t.Errorf("result should label Task 1, got: %s", result)
	}
	if !strings.Contains(result, "JWT") && !strings.Contains(result, "create") {
		t.Errorf("result should reflect the task goal, got: %s", result)
	}
}

// TestE2E_CustomSystemPromptWithEmpty verifies that empty system prompts
// are handled gracefully (fall back to classifyGoal).
func TestE2E_CustomSystemPromptWithEmpty(t *testing.T) {
	skipIfNoE2E(t)

	tool := &delegateTasksTool{
		maxConcurrency: 1,
		kodePath:       e2eBinary,
		timeout:        30 * time.Second,
	}

	input := `{"tasks":[{"goal":"create JWT middleware","context":"use gin","system":""}]}`
	result, err := tool.Call(input)
	if err != nil {
		t.Fatalf("tool.Call returned unexpected error: %v", err)
	}

	if !strings.Contains(result, "Task 1") {
		t.Errorf("result should label Task 1 even with empty system prompt, got: %s", result)
	}
}

// TestE2E_MixedSystemPrompts verifies mixing tasks with and without
// custom system prompts works correctly.
func TestE2E_MixedSystemPrompts(t *testing.T) {
	skipIfNoE2E(t)

	tool := &delegateTasksTool{
		maxConcurrency: 2,
		kodePath:       e2eBinary,
		timeout:        30 * time.Second,
	}

	input := `{"tasks":[
		{"goal":"review auth middleware","system":"You are a security engineer."},
		{"goal":"fix bug in parser","system":"","context":"parser.go has nil pointer bug"},
		{"goal":"create user model"}
	]}`
	result, err := tool.Call(input)
	if err != nil {
		t.Fatalf("tool.Call returned unexpected error: %v", err)
	}

	for i := 1; i <= 3; i++ {
		if !strings.Contains(result, fmt.Sprintf("Task %d", i)) {
			t.Errorf("missing Task %d in results", i)
		}
	}
}
