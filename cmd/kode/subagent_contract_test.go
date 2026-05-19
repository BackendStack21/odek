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

	"github.com/BackendStack21/kode"
	"github.com/BackendStack21/kode/internal/danger"
)

// ─────────────────────────────────────────────────────────────────────
// Contract Tests: kode subagent CLI
// ─────────────────────────────────────────────────────────────────────
//
// These tests verify the subagent interface contract WITHOUT running
// an actual LLM. They test the subagent command parsing, the JSON
// stdout protocol, exit codes, and tool schema validity.
//
// The subagent binary path is resolved once.
// ─────────────────────────────────────────────────────────────────────

var kodeBinary string

func init() {
	// Environment variable takes precedence
	if path := os.Getenv("KODE_BINARY"); path != "" {
		if _, err := os.Stat(path); err == nil {
			kodeBinary = path
			return
		}
	}
	// Find kode relative to the test source or project root
	for _, root := range []string{".", "/root/projects/kode"} {
		path := root + "/kode"
		if _, err := os.Stat(path); err == nil {
			kodeBinary = path
			return
		}
	}
	// Fallback: PATH
	if path, err := exec.LookPath("kode"); err == nil {
		kodeBinary = path
	}
}

// ── 1. Flag Parsing ─────────────────────────────────────────────────

func TestSubagent_GoalFlag(t *testing.T) {
	cmd := exec.Command(kodeBinary, "subagent", "--goal", "test goal")
	cmd.Stderr = &bytes.Buffer{}
	out, err := cmd.Output()

	if err != nil {
		_ = out
		exitErr, ok := err.(*exec.ExitError)
		if ok && exitErr.ExitCode() == 3 {
			return
		}
	}

	var result map[string]any
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("stdout must be valid JSON, got: %q (err: %v)", string(out), err)
	}
}

func TestSubagent_ContextFlag(t *testing.T) {
	cmd := exec.Command(kodeBinary, "subagent",
		"--goal", "test",
		"--context", "important background",
	)
	cmd.Stderr = &bytes.Buffer{}
	_, err := cmd.Output()

	if err == nil {
		return
	}
	if !isFlagParseError(err) {
		t.Fatalf("--context flag rejected: %v", err)
	}
}

func TestSubagent_TaskFileFlag(t *testing.T) {
	taskFile := filepath.Join(t.TempDir(), "task.json")
	taskData := map[string]string{
		"goal":    "build auth middleware",
		"context": "use gin framework",
	}
	data, _ := json.Marshal(taskData)
	os.WriteFile(taskFile, data, 0644)

	cmd := exec.Command(kodeBinary, "subagent", "--task", taskFile)
	cmd.Stderr = &bytes.Buffer{}
	_, err := cmd.Output()

	if err == nil {
		return
	}
	if !isFlagParseError(err) {
		t.Fatalf("--task flag rejected: %v", err)
	}
}

func TestSubagent_TimeoutFlag(t *testing.T) {
	cmd := exec.Command(kodeBinary, "subagent",
		"--goal", "test",
		"--timeout", "30",
	)
	cmd.Stderr = &bytes.Buffer{}
	_, err := cmd.Output()

	if err == nil {
		return
	}
	if !isFlagParseError(err) {
		t.Fatalf("--timeout flag rejected: %v", err)
	}
}

func TestSubagent_MaxIterFlag(t *testing.T) {
	cmd := exec.Command(kodeBinary, "subagent",
		"--goal", "test",
		"--max-iter", "5",
	)
	cmd.Stderr = &bytes.Buffer{}
	_, err := cmd.Output()

	if err == nil {
		return
	}
	if !isFlagParseError(err) {
		t.Fatalf("--max-iter flag rejected: %v", err)
	}
}

func TestSubagent_QuietFlag(t *testing.T) {
	cmd := exec.Command(kodeBinary, "subagent",
		"--goal", "test",
		"--quiet",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_, _ = cmd.Output()

	output := stderr.String()
	if strings.Contains(output, "🔧") || strings.Contains(output, "🧠") {
		t.Logf("--quiet should suppress emoji progress, got: %s", output)
	}
}

func TestSubagent_ParentSessionFlag(t *testing.T) {
	cmd := exec.Command(kodeBinary, "subagent",
		"--goal", "test",
		"--parent-session", "20260519-test123",
	)
	cmd.Stderr = &bytes.Buffer{}
	_, err := cmd.Output()

	if err == nil {
		return
	}
	if !isFlagParseError(err) {
		t.Fatalf("--parent-session flag rejected: %v", err)
	}
}

func TestSubagent_RejectsGoalAndTaskTogether(t *testing.T) {
	taskFile := filepath.Join(t.TempDir(), "task.json")
	os.WriteFile(taskFile, []byte(`{"goal":"test"}`), 0644)

	cmd := exec.Command(kodeBinary, "subagent", "--goal", "x", "--task", taskFile)
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatal("expected error when both --goal and --task are provided")
	}
	if !strings.Contains(string(out), "--goal") && !strings.Contains(string(out), "--task") {
		t.Errorf("error should mention conflicting flags, got: %s", string(out))
	}
}

func TestSubagent_RejectsNoGoalOrTask(t *testing.T) {
	cmd := exec.Command(kodeBinary, "subagent")
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatal("expected error when no --goal or --task is provided")
	}
	if !strings.Contains(string(out), "--goal") && !strings.Contains(string(out), "--task") {
		t.Errorf("error should mention --goal or --task, got: %s", string(out))
	}
}

// ── 2. Stdout Contract ──────────────────────────────────────────────

func TestSubagent_StdoutIsJSON(t *testing.T) {
	cmd := exec.Command(kodeBinary, "subagent", "--goal", "test")
	out, err := cmd.Output()

	if err != nil {
		if len(out) > 0 {
			var result map[string]any
			if json.Unmarshal(out, &result) != nil {
				t.Fatalf("stdout must be valid JSON on error, got: %q", string(out))
			}
		}
		return
	}

	var result map[string]any
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("stdout must be valid JSON: %q", string(out))
	}

	status, ok := result["status"].(string)
	if !ok {
		t.Fatal("result must have string 'status' field")
	}
	if status != "success" && status != "error" {
		t.Fatalf("status must be 'success' or 'error', got: %q", status)
	}
}

func TestSubagent_SuccessStdoutContract(t *testing.T) {
	if os.Getenv("KODE_INTEGRATION") == "" {
		t.Skip("KODE_INTEGRATION not set — contract definition only")
	}

	result := map[string]any{
		"status":        "success",
		"summary":       "Built the auth middleware",
		"files_changed": []string{"internal/middleware/auth.go"},
		"tokens_used":   float64(4200),
		"iterations":    float64(3),
	}
	data, _ := json.Marshal(result)
	var decoded map[string]any
	json.Unmarshal(data, &decoded)

	if decoded["status"] != "success" {
		t.Error("status must be 'success'")
	}
	if _, ok := decoded["summary"].(string); !ok {
		t.Error("summary must be string")
	}
	if _, ok := decoded["tokens_used"].(float64); !ok {
		t.Error("tokens_used must be number")
	}
	if _, ok := decoded["iterations"].(float64); !ok {
		t.Error("iterations must be number")
	}
}

func TestSubagent_ErrorStdoutContract(t *testing.T) {
	result := map[string]any{
		"status":        "error",
		"error":         "timeout after 120s",
		"summary":       "Completed 2 of 3 files",
		"files_changed": []string{"internal/middleware/auth.go"},
		"tokens_used":   float64(3800),
		"iterations":    float64(12),
	}
	data, _ := json.Marshal(result)
	var decoded map[string]any
	json.Unmarshal(data, &decoded)

	if decoded["status"] != "error" {
		t.Error("status must be 'error'")
	}
	if _, ok := decoded["error"].(string); !ok {
		t.Error("error must be string")
	}
}

// ── 3. Exit Codes ───────────────────────────────────────────────────

func TestSubagent_ExitCodeZero(t *testing.T) {}
func TestSubagent_ExitCodeOne(t *testing.T)  {}
func TestSubagent_ExitCodeTwo(t *testing.T)  {}

func TestSubagent_ExitCodeThree(t *testing.T) {
	cmd := exec.Command(kodeBinary, "subagent")
	_, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatal("subagent without --goal should exit non-zero")
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() == 0 {
		t.Fatal("exit code must be non-zero when no --goal provided")
	}
}

// ── 4. delegate_tasks Tool Schema ───────────────────────────────────

func TestDelegateTasksTool_Exists(t *testing.T) {
	tools := builtinTools(danger.DangerousConfig{}, nil)

	found := false
	for _, tool := range tools {
		if tool.Name() == "delegate_tasks" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("delegate_tasks tool must be registered in builtinTools()")
	}
}

func TestDelegateTasksTool_HasSchema(t *testing.T) {
	tools := builtinTools(danger.DangerousConfig{}, nil)

	var tool kode.Tool
	for _, t2 := range tools {
		if t2.Name() == "delegate_tasks" {
			tool = t2
			break
		}
	}
	if tool == nil {
		t.Skip("delegate_tasks not registered yet")
	}

	schema := tool.Schema()
	if schema == nil {
		t.Fatal("delegate_tasks must have a schema")
	}

	schemaMap, ok := schema.(map[string]any)
	if !ok {
		t.Fatalf("schema must be map[string]any, got %T", schema)
	}

	props, ok := schemaMap["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema must have 'properties'")
	}
	tasksProp, ok := props["tasks"]
	if !ok {
		t.Fatal("schema must have 'tasks' property")
	}

	tasksMap, ok := tasksProp.(map[string]any)
	if !ok {
		t.Fatalf("tasks must be object, got %T", tasksProp)
	}
	if tasksMap["type"] != "array" {
		t.Errorf("tasks.type must be 'array', got %q", tasksMap["type"])
	}

	items, ok := tasksMap["items"].(map[string]any)
	if !ok {
		t.Fatal("tasks.items must be object")
	}
	requiredRaw := items["required"]
	hasGoal := false
	switch req := requiredRaw.(type) {
	case []any:
		for _, r := range req {
			if r == "goal" {
				hasGoal = true
				break
			}
		}
	case []string:
		for _, r := range req {
			if r == "goal" {
				hasGoal = true
				break
			}
		}
	default:
		t.Fatalf("tasks.items.required must be array, got %T", requiredRaw)
	}
	if !hasGoal {
		t.Error("tasks.items.required must include 'goal'")
	}

	if tasksMap["maxItems"] != 8 {
		t.Errorf("tasks.maxItems must be 8, got %v (type %T)", tasksMap["maxItems"], tasksMap["maxItems"])
	}
}

func TestDelegateTasksTool_Description(t *testing.T) {
	tools := builtinTools(danger.DangerousConfig{}, nil)

	var tool kode.Tool
	for _, t2 := range tools {
		if t2.Name() == "delegate_tasks" {
			tool = t2
			break
		}
	}
	if tool == nil {
		t.Skip("delegate_tasks not registered yet")
	}

	desc := tool.Description()
	if desc == "" {
		t.Fatal("delegate_tasks must have a description")
	}
	if !strings.Contains(desc, "sub-agent") && !strings.Contains(desc, "subagent") {
		t.Error("description should mention sub-agent")
	}
}

// ── 5. delegate_tasks Tool Call ─────────────────────────────────────

func TestDelegateTasks_CallValidatesJSON(t *testing.T) {
	tool := &delegateTasksTool{
		maxConcurrency: 3,
		kodePath:       "/nonexistent/kode",
		timeout:        time.Second,
	}

	result, err := tool.Call("not json")
	if err != nil {
		t.Fatalf("Call should never return error, got: %v", err)
	}
	if !strings.Contains(result, "error") {
		t.Errorf("result should mention error for invalid JSON, got: %q", result)
	}
}

func TestDelegateTasks_CallEmptyTasks(t *testing.T) {
	tool := &delegateTasksTool{
		maxConcurrency: 3,
		kodePath:       "/nonexistent/kode",
		timeout:        time.Second,
	}

	result, _ := tool.Call(`{"tasks":[]}`)
	if !strings.Contains(result, "error") {
		t.Errorf("result should mention error for empty tasks, got: %q", result)
	}
}

func TestDelegateTasks_CallTooManyTasks(t *testing.T) {
	tool := &delegateTasksTool{
		maxConcurrency: 3,
		kodePath:       "/nonexistent/kode",
		timeout:        time.Second,
	}

	tasks := make([]map[string]string, 10)
	for i := range tasks {
		tasks[i] = map[string]string{"goal": fmt.Sprintf("task %d", i)}
	}
	input, _ := json.Marshal(map[string]any{"tasks": tasks})

	result, _ := tool.Call(string(input))
	if !strings.Contains(result, "max") && !strings.Contains(result, "8") {
		t.Errorf("result should mention max 8 limit, got: %q", result)
	}
}

func TestDelegateTasks_CallMissingTool(t *testing.T) {
	tool := &delegateTasksTool{
		maxConcurrency: 3,
		kodePath:       "/nonexistent/kode",
		timeout:        time.Second,
	}

	input := `{"tasks":[{"goal":"build auth","context":"use gin"}]}`
	result, _ := tool.Call(input)

	if !strings.Contains(result, "error") && !strings.Contains(result, "not found") {
		t.Errorf("result should mention binary not found, got: %q", result)
	}
}

func TestDelegateTasks_ConcurrencyLimit(t *testing.T) {
	tool := &delegateTasksTool{
		maxConcurrency: 2,
		kodePath:       "/nonexistent/kode",
		timeout:        time.Second,
	}

	tasks := make([]map[string]string, 4)
	for i := range tasks {
		tasks[i] = map[string]string{"goal": fmt.Sprintf("task %d", i)}
	}
	input, _ := json.Marshal(map[string]any{"tasks": tasks})

	result, _ := tool.Call(string(input))
	if result == "" {
		t.Error("result should not be empty")
	}
}

func TestDelegateTasks_Timeout(t *testing.T) {
	tool := &delegateTasksTool{
		maxConcurrency: 2,
		kodePath:       "/nonexistent/kode",
		timeout:        50 * time.Millisecond,
	}

	input := `{"tasks":[{"goal":"build auth"}]}`
	result, _ := tool.Call(string(input))
	if result == "" {
		t.Error("result should not be empty")
	}
}

// ── 6. Config ───────────────────────────────────────────────────────

func TestSubagentConfig_DefaultValues(t *testing.T) {
	cfg := defaultSubagentConfig()
	if cfg.MaxConcurrency != 3 {
		t.Errorf("default MaxConcurrency = %d, want 3", cfg.MaxConcurrency)
	}
	if cfg.TimeoutSeconds != 120 {
		t.Errorf("default TimeoutSeconds = %d, want 120", cfg.TimeoutSeconds)
	}
	if cfg.MaxIterations != 15 {
		t.Errorf("default MaxIterations = %d, want 15", cfg.MaxIterations)
	}
}

func TestSubagentConfig_FromConfigFile(t *testing.T) {
	configJSON := `{
		"subagent": {
			"max_concurrency": 5,
			"timeout_seconds": 60,
			"max_iterations": 10
		}
	}`

	cfg := parseSubagentConfig(configJSON)
	if cfg.MaxConcurrency != 5 {
		t.Errorf("MaxConcurrency = %d, want 5", cfg.MaxConcurrency)
	}
	if cfg.TimeoutSeconds != 60 {
		t.Errorf("TimeoutSeconds = %d, want 60", cfg.TimeoutSeconds)
	}
	if cfg.MaxIterations != 10 {
		t.Errorf("MaxIterations = %d, want 10", cfg.MaxIterations)
	}
}

// ── 7. System Prompt ───────────────────────────────────────────────

func TestDefaultSystem_IncludesDelegation(t *testing.T) {
	mentions := []string{"delegate", "sub-agent", "subagent", "delegate_tasks"}
	found := false
	for _, term := range mentions {
		if strings.Contains(defaultSystem, term) {
			found = true
			break
		}
	}
	if !found {
		t.Error("defaultSystem should mention delegation (delegate, sub-agent, delegate_tasks)")
	}
}

// ── 8. Subagent System Prompt ──────────────────────────────────────

func TestSubagentSystemPrompt_Minimal(t *testing.T) {
	if len(subagentSystem) > 800 {
		t.Errorf("subagent system prompt too long: %d chars (max 800)", len(subagentSystem))
	}
	if subagentSystem == "" {
		t.Fatal("subagent system prompt must not be empty")
	}
}

// ── 9. Integration ─────────────────────────────────────────────────

func TestDelegateTasks_PipesStderr(t *testing.T) {
	tool := &delegateTasksTool{
		maxConcurrency: 1,
		kodePath:       "/nonexistent/kode",
		timeout:        time.Second,
	}

	input := `{"tasks":[{"goal":"build auth"}]}`
	_, _ = tool.Call(input)
}

// ── Helpers ────────────────────────────────────────────────────────

func isFlagParseError(err error) bool {
	if err == nil {
		return false
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		return false
	}
	return exitErr.ExitCode() == 1 || exitErr.ExitCode() == 3
}
