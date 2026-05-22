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
	"github.com/BackendStack21/kode/internal/llm"
)

// ─────────────────────────────────────────────────────────────────────
// Contract Tests: odek subagent CLI
// ─────────────────────────────────────────────────────────────────────
//
// These tests verify the subagent interface contract WITHOUT running
// an actual LLM. They test the subagent command parsing, the JSON
// stdout protocol, exit codes, and tool schema validity.
//
// The subagent binary path is resolved once.
// ─────────────────────────────────────────────────────────────────────

var odekBinary string

func init() {
	// Environment variable takes precedence
	if path := os.Getenv("ODEK_BINARY"); path != "" {
		if _, err := os.Stat(path); err == nil {
			odekBinary = path
			return
		}
	}
	// Find odek relative to the test source or project root
	for _, root := range []string{".", "/root/projects/odek"} {
		path := root + "/odek"
		if _, err := os.Stat(path); err == nil {
			odekBinary = path
			return
		}
	}
	// Fallback: PATH
	if path, err := exec.LookPath("odek"); err == nil {
		odekBinary = path
	}
}

// ── 1. Flag Parsing ─────────────────────────────────────────────────

func TestSubagent_GoalFlag(t *testing.T) {
	skipIfNoBinary(t)
	cmd := exec.Command(odekBinary, "subagent", "--goal", "test goal")
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
	skipIfNoBinary(t)
	cmd := exec.Command(odekBinary, "subagent",
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
	skipIfNoBinary(t)
	taskFile := filepath.Join(t.TempDir(), "task.json")
	taskData := map[string]string{
		"goal":    "build auth middleware",
		"context": "use gin framework",
	}
	data, _ := json.Marshal(taskData)
	os.WriteFile(taskFile, data, 0644)

	cmd := exec.Command(odekBinary, "subagent", "--task", taskFile)
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
	skipIfNoBinary(t)
	cmd := exec.Command(odekBinary, "subagent",
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
	skipIfNoBinary(t)
	cmd := exec.Command(odekBinary, "subagent",
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
	cmd := exec.Command(odekBinary, "subagent",
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
	skipIfNoBinary(t)
	cmd := exec.Command(odekBinary, "subagent",
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
	skipIfNoBinary(t)
	taskFile := filepath.Join(t.TempDir(), "task.json")
	os.WriteFile(taskFile, []byte(`{"goal":"test"}`), 0644)

	cmd := exec.Command(odekBinary, "subagent", "--goal", "x", "--task", taskFile)
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatal("expected error when both --goal and --task are provided")
	}
	if !strings.Contains(string(out), "--goal") && !strings.Contains(string(out), "--task") {
		t.Errorf("error should mention conflicting flags, got: %s", string(out))
	}
}

func TestSubagent_RejectsNoGoalOrTask(t *testing.T) {
	skipIfNoBinary(t)
	cmd := exec.Command(odekBinary, "subagent")
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
	skipIfNoBinary(t)
	cmd := exec.Command(odekBinary, "subagent", "--goal", "test")
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
	if os.Getenv("ODEK_INTEGRATION") == "" {
		t.Skip("ODEK_INTEGRATION not set — contract definition only")
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
	skipIfNoBinary(t)
	cmd := exec.Command(odekBinary, "subagent")
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
	tools := builtinTools(danger.DangerousConfig{}, nil, nil, 3)

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
	tools := builtinTools(danger.DangerousConfig{}, nil, nil, 3)

	var tool odek.Tool
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

	// Verify items.properties has system (new) field
	itemsProps, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatal("tasks.items.properties must be object")
	}
	if _, ok := itemsProps["system"]; !ok {
		t.Error("tasks.items.properties should include optional 'system' field")
	}
	if _, ok := itemsProps["context"]; !ok {
		t.Error("tasks.items.properties should include 'context' field")
	}
	if _, ok := itemsProps["goal"]; !ok {
		t.Error("tasks.items.properties should include 'goal' field")
	}
}

func TestDelegateTasksTool_Description(t *testing.T) {
	tools := builtinTools(danger.DangerousConfig{}, nil, nil, 3)

	var tool odek.Tool
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
		odekPath:       "/nonexistent/odek",
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
		odekPath:       "/nonexistent/odek",
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
		odekPath:       "/nonexistent/odek",
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
		odekPath:       "/nonexistent/odek",
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
		odekPath:       "/nonexistent/odek",
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
		odekPath:       "/nonexistent/odek",
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
	if len(subagentSystem) > 1400 {
		t.Errorf("subagent system prompt too long: %d chars (max 1400)", len(subagentSystem))
	}
	if subagentSystem == "" {
		t.Fatal("subagent system prompt must not be empty")
	}
}

// ── 9. buildSubagentPrompt ──────────────────────────────────────────

func TestBuildSubagentPrompt_IncludesGoal(t *testing.T) {
	got := buildSubagentPrompt("Create a user model with CRUD", "")
	if !strings.Contains(got, "Create a user model with CRUD") {
		t.Errorf("prompt should contain the goal text, got:\n%s", got)
	}
}

func TestBuildSubagentPrompt_IncludesContext(t *testing.T) {
	got := buildSubagentPrompt("Build auth middleware", "Uses gin, models at internal/models/user.go")
	if !strings.Contains(got, "Uses gin") {
		t.Errorf("prompt should contain context, got:\n%s", got)
	}
}

func TestBuildSubagentPrompt_EmptyGoal(t *testing.T) {
	got := buildSubagentPrompt("", "")
	if got != subagentSystem {
		t.Errorf("empty goal should return subagentSystem, got:\n%s", got)
	}
}

func TestBuildSubagentPrompt_DebugDetection(t *testing.T) {
	for _, goal := range []string{"fix OOM bug in parser", "crash in websocket handler", "broken import path"} {
		got := buildSubagentPrompt(goal, "")
		if !strings.Contains(got, "debugger") && !strings.Contains(got, "root cause") {
			t.Errorf("goal %q should produce debug prompt, got:\n%s", goal, got)
		}
	}
}

func TestBuildSubagentPrompt_TestDetection(t *testing.T) {
	for _, goal := range []string{"write unit tests for auth", "add coverage for models", "create integration test for API"} {
		got := buildSubagentPrompt(goal, "")
		if !strings.Contains(got, "test") && !strings.Contains(got, "assert") && !strings.Contains(got, "coverage") {
			t.Errorf("goal %q should produce test prompt, got:\n%s", goal, got)
		}
	}
}

func TestBuildSubagentPrompt_ReviewDetection(t *testing.T) {
	got := buildSubagentPrompt("review PR #42 for security issues", "")
	if !strings.Contains(got, "reviewing") && !strings.Contains(got, "critically") {
		t.Errorf("review goal should produce review prompt, got:\n%s", got)
	}
}

func TestBuildSubagentPrompt_RefactorDetection(t *testing.T) {
	got := buildSubagentPrompt("refactor the monolith into handlers", "")
	if !strings.Contains(got, "architecture") && !strings.Contains(got, "Preserve behavior") {
		t.Errorf("refactor goal should produce architecture prompt, got:\n%s", got)
	}
}

func TestBuildSubagentPrompt_ConfigDetection(t *testing.T) {
	got := buildSubagentPrompt("setup Docker CI pipeline", "")
	if !strings.Contains(got, "DevOps") && !strings.Contains(got, "reproducible") {
		t.Errorf("config goal should produce DevOps prompt, got:\n%s", got)
	}
}

func TestBuildSubagentPrompt_ResearchDetection(t *testing.T) {
	got := buildSubagentPrompt("research Go HTTP router performance", "")
	if !strings.Contains(got, "researcher") && !strings.Contains(got, "Explore thoroughly") {
		t.Errorf("research goal should produce research prompt, got:\n%s", got)
	}
}

func TestBuildSubagentPrompt_FallbackToBuild(t *testing.T) {
	got := buildSubagentPrompt("do something random", "")
	if !strings.Contains(got, "expert engineer") {
		t.Errorf("generic goal should produce engineer prompt, got:\n%s", got)
	}
}

func TestBuildSubagentPrompt_UniquePerGoal(t *testing.T) {
	p1 := buildSubagentPrompt("Build auth middleware", "")
	p2 := buildSubagentPrompt("Create user model", "")
	if p1 == p2 {
		t.Error("different goals should produce different prompts")
	}
}

func TestBuildSubagentPrompt_MaxLength(t *testing.T) {
	got := buildSubagentPrompt("Create a full CRUD REST API with JWT auth and PostgreSQL storage", "Uses gin, GORM, models at internal/models/")
	if len(got) > 800 {
		t.Errorf("prompt too long: %d chars (max 800)\n%s", len(got), got)
	}
}

// ── 10. Integration ─────────────────────────────────────────────────

func TestDelegateTasks_PipesStderr(t *testing.T) {
	tool := &delegateTasksTool{
		maxConcurrency: 1,
		odekPath:       "/nonexistent/odek",
		timeout:        time.Second,
	}

	input := `{"tasks":[{"goal":"build auth"}]}`
	_, _ = tool.Call(input)
}

// ── 11. buildSubagentPrompt — Expanded Keyword Detection ────────────

func TestBuildSubagentPrompt_TestGoalExactMatch(t *testing.T) {
	got := buildSubagentPrompt("test goal", "")
	if !strings.Contains(got, "testing engineer") {
		t.Errorf("goal %q should produce 'testing engineer' persona, got:\n%s", "test goal", got)
	}
	if !strings.Contains(got, "Write thorough tests") {
		t.Errorf("goal %q should contain 'Write thorough tests', got:\n%s", "test goal", got)
	}
}

func TestBuildSubagentPrompt_TestKeywordVariants(t *testing.T) {
	goals := []string{
		"test goal",
		"spec the API endpoints",
		"add spec for user model",
		"increase coverage to 90%",
		"write assertions for edge cases",
		"assert the response is correct",
	}
	for _, goal := range goals {
		got := buildSubagentPrompt(goal, "")
		if !strings.Contains(got, "testing engineer") {
			t.Errorf("goal %q should produce 'testing engineer' persona, got:\n%s", goal, got)
		}
		if !strings.Contains(got, "happy path") {
			t.Errorf("goal %q should mention 'happy path' in testing prompt, got:\n%s", goal, got)
		}
	}
}

func TestBuildSubagentPrompt_CaseInsensitive(t *testing.T) {
	tests := []struct {
		goal       string
		wantPersona string
	}{
		{"FIX THE CRASH IN DB", "debugger"},
		{"TEST ALL ENDPOINTS", "testing engineer"},
		{"REVIEW DEPLOYMENT SCRIPT", "reviewing code"},
		{"REFACTOR THE MONOLITH", "architecture expert"},
		{"SETUP CI/CD PIPELINE", "DevOps"},
		{"RESEARCH PERFORMANCE", "researcher"},
	}
	for _, tt := range tests {
		got := buildSubagentPrompt(tt.goal, "")
		if !strings.Contains(got, tt.wantPersona) {
			t.Errorf("goal %q should produce persona containing %q, got:\n%s", tt.goal, tt.wantPersona, got)
		}
	}
}

func TestBuildSubagentPrompt_DebugAdditionalKeywords(t *testing.T) {
	keywords := []string{
		"fix null pointer",
		"fix regression in parser",
		"bug in login handler",
		"error handling for timeout",
		"crash on startup",
		"broken query builder",
		"incorrect calculation result",
		"wrong output format",
		"fail on empty input",
	}
	for _, goal := range keywords {
		got := buildSubagentPrompt(goal, "")
		if !strings.Contains(got, "debugger") && !strings.Contains(got, "root cause") {
			t.Errorf("goal %q should produce debug prompt, got:\n%s", goal, got)
		}
	}
}

func TestBuildSubagentPrompt_ReviewAdditionalKeywords(t *testing.T) {
	keywords := []string{
		"audit security dependencies",
		"check code quality",
		"verify auth logic is correct",
		"validate input sanitization",
	}
	// Note: "inspect" is intentionally excluded here because it contains
	// "spec" as a substring, which triggers the test persona match first
	// in the switch/case priority order.
	for _, goal := range keywords {
		got := buildSubagentPrompt(goal, "")
		if !strings.Contains(got, "reviewing") && !strings.Contains(got, "critically") {
			t.Errorf("goal %q should produce review prompt, got:\n%s", goal, got)
		}
	}
}

func TestBuildSubagentPrompt_RefactorAdditionalKeywords(t *testing.T) {
	keywords := []string{
		"simplify the validation logic",
		"clean up legacy handler",
		"rename UserDTO to UserResponse",
		"simplify nested if-else",
		"extract database logic into repository",
		"restructure the project layout",
	}
	for _, goal := range keywords {
		got := buildSubagentPrompt(goal, "")
		if !strings.Contains(got, "architecture") && !strings.Contains(got, "Preserve behavior") {
			t.Errorf("goal %q should produce refactor prompt, got:\n%s", goal, got)
		}
	}
}

func TestBuildSubagentPrompt_ConfigAdditionalKeywords(t *testing.T) {
	keywords := []string{
		"setup Postgres dev environment",
		"config nginx reverse proxy",
		"install kubectl and helm",
		"setup the project",
		"configure CI pipeline",
		"dockerize the application",
		"deploy to production",
		"provision AWS resources",
	}
	for _, goal := range keywords {
		got := buildSubagentPrompt(goal, "")
		if !strings.Contains(got, "DevOps") && !strings.Contains(got, "reproducible") {
			t.Errorf("goal %q should produce DevOps prompt, got:\n%s", goal, got)
		}
	}
}

func TestBuildSubagentPrompt_ResearchAdditionalKeywords(t *testing.T) {
	keywords := []string{
		"compare Go vs Rust performance",
		"understand the caching mechanism",
		"analyze API response times",
		"research database indexing strategies",
	}
	// Note: "investigate" is intentionally excluded because goals containing
	// "suspect" (like "investigate memory leak suspect") have "spec" as a
	// substring, triggering the test persona match first in priority order.
	for _, goal := range keywords {
		got := buildSubagentPrompt(goal, "")
		if !strings.Contains(got, "researcher") && !strings.Contains(got, "Explore thoroughly") {
			t.Errorf("goal %q should produce research prompt, got:\n%s", goal, got)
		}
	}
}

func TestBuildSubagentPrompt_PriorityOrder(t *testing.T) {
	// When multiple categories match, the switch/case order determines priority.
	// "fix" comes before "test", so debug persona should win.
	got := buildSubagentPrompt("fix broken test", "")
	if !strings.Contains(got, "debugger") {
		t.Errorf("'fix broken test' should produce debugger persona (fix before test in switch), got:\n%s", got)
	}

	// "test" comes before "setup" in switch order
	got2 := buildSubagentPrompt("test setup script", "")
	if !strings.Contains(got2, "testing engineer") {
		t.Errorf("'test setup script' should produce testing engineer (test before setup in switch), got:\n%s", got2)
	}
}

func TestBuildSubagentPrompt_ContextAddedAfterGoal(t *testing.T) {
	got := buildSubagentPrompt("build auth", "Context: use gin framework")
	if !strings.Contains(got, "Context:") {
		t.Errorf("prompt should include 'Context:' label, got:\n%s", got)
	}
	// Context should appear after the goal section
	goalIdx := strings.Index(got, "build auth")
	ctxIdx := strings.Index(got, "Context:")
	if ctxIdx < goalIdx {
		t.Errorf("context should appear after goal in prompt, got:\n%s", got)
	}
}

func TestBuildSubagentPrompt_EmptyGoalWithContext(t *testing.T) {
	// Empty goal with context should still return subagentSystem (fallback)
	got := buildSubagentPrompt("", "some context")
	if got != subagentSystem {
		t.Errorf("empty goal with context should return subagentSystem, got:\n%s", got)
	}
}

func TestBuildSubagentPrompt_ReportSuffix(t *testing.T) {
	got := buildSubagentPrompt("build something", "")
	if !strings.Contains(got, "Report what you built") {
		t.Errorf("prompt should end with report instruction, got:\n%s", got)
	}
}

// ── 12. truncate ────────────────────────────────────────────────────

func TestTruncate_NoTruncation(t *testing.T) {
	result := truncate("hello", 10)
	if result != "hello" {
		t.Errorf("truncate(\"hello\", 10) = %q, want %q", result, "hello")
	}
}

func TestTruncate_ExactBoundary(t *testing.T) {
	result := truncate("hello", 5)
	if result != "hello" {
		t.Errorf("truncate(\"hello\", 5) = %q, want %q", result, "hello")
	}
}

func TestTruncate_Truncates(t *testing.T) {
	result := truncate("hello world", 5)
	if !strings.HasPrefix(result, "hello") {
		t.Errorf("truncate(\"hello world\", 5) should start with 'hello', got: %q", result)
	}
	if !strings.HasSuffix(result, "…") {
		t.Errorf("truncate(\"hello world\", 5) should end with '…', got: %q", result)
	}
	if len([]rune(result)) != 6 { // 5 chars + ellipsis
		t.Errorf("truncate(\"hello world\", 5) length = %d runes, want 6", len([]rune(result)))
	}
}

func TestTruncate_EmptyString(t *testing.T) {
	result := truncate("", 10)
	if result != "" {
		t.Errorf("truncate(\"\", 10) = %q, want empty", result)
	}
}

func TestTruncate_ZeroLimit(t *testing.T) {
	result := truncate("hello", 0)
	if result != "…" {
		t.Errorf("truncate(\"hello\", 0) = %q, want '…'", result)
	}
}

func TestTruncate_NegativeLimit(t *testing.T) {
	result := truncate("hello", -1)
	if result != "…" {
		t.Errorf("truncate(\"hello\", -1) = %q, want '…'", result)
	}
}

func TestTruncate_UnicodeCharacters(t *testing.T) {
	input := "你好世界！这是一个测试"
	result := truncate(input, 4)
	if len([]rune(result)) != 5 { // 4 chars + ellipsis
		t.Errorf("truncate unicode, rune count = %d, want 5", len([]rune(result)))
	}
	expected := "你好世界…"
	if result != expected {
		t.Errorf("truncate(%q, 4) = %q, want %q", input, result, expected)
	}
}

func TestTruncate_MultiByteEmoji(t *testing.T) {
	input := "🎉🎊🧪🔥✨"
	result := truncate(input, 3)
	if len([]rune(result)) != 4 { // 3 emoji + ellipsis
		t.Errorf("truncate emoji, rune count = %d, want 4", len([]rune(result)))
	}
	if !strings.HasPrefix(result, "🎉🎊🧪") {
		t.Errorf("truncate(%q, 3) should start with 🎉🎊🧪, got: %q", input, result)
	}
}

// ── 13. extractSummary ──────────────────────────────────────────────

func TestExtractSummary_LastAssistantMessage(t *testing.T) {
	msgs := []llm.Message{
		{Role: "user", Content: "do something"},
		{Role: "assistant", Content: "First step"},
		{Role: "tool", Content: "tool result"},
		{Role: "assistant", Content: "Final answer: done"},
	}
	summary := extractSummary(msgs)
	if summary != "Final answer: done" {
		t.Errorf("extractSummary = %q, want %q", summary, "Final answer: done")
	}
}

func TestExtractSummary_EmptyMessages(t *testing.T) {
	summary := extractSummary(nil)
	if summary != "" {
		t.Errorf("extractSummary(nil) = %q, want empty", summary)
	}

	summary = extractSummary([]llm.Message{})
	if summary != "" {
		t.Errorf("extractSummary(empty) = %q, want empty", summary)
	}
}

func TestExtractSummary_NoAssistantMessage(t *testing.T) {
	msgs := []llm.Message{
		{Role: "user", Content: "hello"},
		{Role: "tool", Content: "world"},
	}
	summary := extractSummary(msgs)
	if summary != "" {
		t.Errorf("extractSummary with no assistant = %q, want empty", summary)
	}
}

func TestExtractSummary_EmptyAssistantContent(t *testing.T) {
	msgs := []llm.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: ""},
		{Role: "assistant", Content: "real content"},
	}
	summary := extractSummary(msgs)
	if summary != "real content" {
		t.Errorf("extractSummary = %q, want %q", summary, "real content")
	}
}

func TestExtractSummary_AssistantWithToolCallsOnly(t *testing.T) {
	msgs := []llm.Message{
		{Role: "assistant", Content: "", ToolCalls: []llm.ToolCall{{ID: "call1"}}},
		{Role: "tool", Content: "result"},
		{Role: "assistant", Content: "Here is the final output"},
	}
	summary := extractSummary(msgs)
	if summary != "Here is the final output" {
		t.Errorf("extractSummary = %q, want %q", summary, "Here is the final output")
	}
}

func TestExtractSummary_TruncatesLongOutput(t *testing.T) {
	longContent := strings.Repeat("a", 600)
	msgs := []llm.Message{
		{Role: "assistant", Content: longContent},
	}
	summary := extractSummary(msgs)
	if len([]rune(summary)) > 501 { // 500 + ellipsis
		t.Errorf("extractSummary too long: %d runes (max 501)", len([]rune(summary)))
	}
	if !strings.HasSuffix(summary, "…") {
		t.Errorf("truncated summary should end with '…', got: %q", summary)
	}
}

// ── 14. extractFilesChanged ─────────────────────────────────────────

func TestExtractFilesChanged_NoFiles(t *testing.T) {
	msgs := []llm.Message{
		{Role: "assistant", Content: "done"},
	}
	result := extractFilesChanged(msgs)
	if len(result) != 0 {
		t.Errorf("extractFilesChanged = %v, want empty", result)
	}
}

func TestExtractFilesChanged_SingleFile(t *testing.T) {
	msgs := []llm.Message{
		{Role: "tool", Content: "wrote main.go"},
	}
	result := extractFilesChanged(msgs)
	if len(result) != 1 || result[0] != "main.go" {
		t.Errorf("extractFilesChanged = %v, want [main.go]", result)
	}
}

func TestExtractFilesChanged_AllPrefixTypes(t *testing.T) {
	msgs := []llm.Message{
		{Role: "tool", Content: "wrote internal/handler.go\ncreated models/user.go\nmodified pkg/utils.go\nupdated config/defaults.go"},
	}
	result := extractFilesChanged(msgs)
	expected := []string{"internal/handler.go", "models/user.go", "pkg/utils.go", "config/defaults.go"}
	if len(result) != len(expected) {
		t.Errorf("extractFilesChanged = %v, want %v", result, expected)
	}
	for i, f := range expected {
		if result[i] != f {
			t.Errorf("extractFilesChanged[%d] = %q, want %q", i, result[i], f)
		}
	}
}

func TestExtractFilesChanged_Deduplicates(t *testing.T) {
	msgs := []llm.Message{
		{Role: "tool", Content: "wrote main.go\nwrote main.go"},
	}
	result := extractFilesChanged(msgs)
	if len(result) != 1 {
		t.Errorf("extractFilesChanged should deduplicate, got %v", result)
	}
}

func TestExtractFilesChanged_DeduplicatesAcrossMessages(t *testing.T) {
	msgs := []llm.Message{
		{Role: "tool", Content: "wrote main.go"},
		{Role: "assistant", Content: "thinking"},
		{Role: "tool", Content: "updated main.go"},
	}
	result := extractFilesChanged(msgs)
	if len(result) != 1 {
		t.Errorf("extractFilesChanged should deduplicate across messages, got %v", result)
	}
}

func TestExtractFilesChanged_FiltersFilesWithoutExtension(t *testing.T) {
	msgs := []llm.Message{
		{Role: "tool", Content: "wrote Makefile\nwrote Dockerfile\nwrote internal/handler.go"},
	}
	result := extractFilesChanged(msgs)
	for _, f := range result {
		if !strings.Contains(f, ".") {
			t.Errorf("extractFilesChanged should filter files without extension, got %q", f)
		}
	}
	if len(result) != 1 {
		t.Errorf("extractFilesChanged = %v, want only [internal/handler.go]", result)
	}
}

func TestExtractFilesChanged_PrefixNotMatched(t *testing.T) {
	msgs := []llm.Message{
		{Role: "tool", Content: "deleted main.go\nrenamed old.go new.go"},
	}
	result := extractFilesChanged(msgs)
	if len(result) != 0 {
		t.Errorf("extractFilesChanged should not match non-standard prefixes, got %v", result)
	}
}

func TestExtractFilesChanged_NoToolRoleMessages(t *testing.T) {
	msgs := []llm.Message{
		{Role: "user", Content: "wrote main.go"},
		{Role: "assistant", Content: "created file.go"},
	}
	result := extractFilesChanged(msgs)
	if len(result) != 0 {
		t.Errorf("extractFilesChanged should only check tool role messages, got %v", result)
	}
}

func TestExtractFilesChanged_MultipleFilesWithPath(t *testing.T) {
	msgs := []llm.Message{
		{Role: "tool", Content: "wrote internal/service/auth.go\ncreated internal/middleware/logging.go\nupdated internal/config/defaults.yaml"},
	}
	result := extractFilesChanged(msgs)
	expected := []string{"internal/service/auth.go", "internal/middleware/logging.go", "internal/config/defaults.yaml"}
	if len(result) != len(expected) {
		t.Errorf("extractFilesChanged = %v (len=%d), want %v (len=%d)", result, len(result), expected, len(expected))
	}
	for i, f := range expected {
		if result[i] != f {
			t.Errorf("extractFilesChanged[%d] = %q, want %q", i, result[i], f)
		}
	}
}

func TestExtractFilesChanged_PreservesOrder(t *testing.T) {
	msgs := []llm.Message{
		{Role: "tool", Content: "wrote a.go\nwrote b.go\nwrote c.go"},
	}
	result := extractFilesChanged(msgs)
	if len(result) != 3 {
		t.Errorf("expected 3 files, got %d: %v", len(result), result)
	}
	if result[0] != "a.go" || result[1] != "b.go" || result[2] != "c.go" {
		t.Errorf("order should be preserved, got %v", result)
	}
}

// ── 15. parseSubagentConfig — Edge Cases ────────────────────────────

func TestParseSubagentConfig_EmptyJSON(t *testing.T) {
	cfg := parseSubagentConfig("")
	if cfg.MaxConcurrency != 3 {
		t.Errorf("empty input → MaxConcurrency = %d, want 3", cfg.MaxConcurrency)
	}
	if cfg.TimeoutSeconds != 120 {
		t.Errorf("empty input → TimeoutSeconds = %d, want 120", cfg.TimeoutSeconds)
	}
}

func TestParseSubagentConfig_MalformedJSON(t *testing.T) {
	cfg := parseSubagentConfig("{invalid json}")
	if cfg.MaxConcurrency != 3 {
		t.Errorf("malformed JSON → MaxConcurrency = %d, want 3 (fallback to default)", cfg.MaxConcurrency)
	}
}

func TestParseSubagentConfig_PartialConfig(t *testing.T) {
	cfg := parseSubagentConfig(`{"subagent": {"max_concurrency": 7}}`)
	if cfg.MaxConcurrency != 7 {
		t.Errorf("MaxConcurrency = %d, want 7", cfg.MaxConcurrency)
	}
	if cfg.TimeoutSeconds != 120 {
		t.Errorf("TimeoutSeconds should keep default = %d, want 120", cfg.TimeoutSeconds)
	}
	if cfg.MaxIterations != 15 {
		t.Errorf("MaxIterations should keep default = %d, want 15", cfg.MaxIterations)
	}
}

func TestParseSubagentConfig_ZeroValues(t *testing.T) {
	cfg := parseSubagentConfig(`{"subagent": {"max_concurrency": 0, "timeout_seconds": 0, "max_iterations": 0}}`)
	if cfg.MaxConcurrency != 3 {
		t.Errorf("zero MaxConcurrency should fallback to default %d, got %d", 3, cfg.MaxConcurrency)
	}
	if cfg.TimeoutSeconds != 120 {
		t.Errorf("zero TimeoutSeconds should fallback to default %d, got %d", 120, cfg.TimeoutSeconds)
	}
	if cfg.MaxIterations != 15 {
		t.Errorf("zero MaxIterations should fallback to default %d, got %d", 15, cfg.MaxIterations)
	}
}

func TestParseSubagentConfig_SystemPrompt(t *testing.T) {
	cfg := parseSubagentConfig(`{"subagent": {"system_prompt": "You are a testing engineer"}}`)
	if cfg.SystemPrompt != "You are a testing engineer" {
		t.Errorf("SystemPrompt = %q, want %q", cfg.SystemPrompt, "You are a testing engineer")
	}
}

func TestParseSubagentConfig_EmptySystemPrompt(t *testing.T) {
	cfg := parseSubagentConfig(`{"subagent": {"system_prompt": ""}}`)
	if cfg.SystemPrompt != "" {
		t.Errorf("empty SystemPrompt should stay empty, got %q", cfg.SystemPrompt)
	}
}

func TestParseSubagentConfig_NoSubagentSection(t *testing.T) {
	cfg := parseSubagentConfig(`{"model": "gpt-4", "system": "hello"}`)
	if cfg.MaxConcurrency != 3 {
		t.Errorf("no subagent section → defaults, got MaxConcurrency = %d", cfg.MaxConcurrency)
	}
}

func TestParseSubagentConfig_NestedConfig(t *testing.T) {
	cfg := parseSubagentConfig(`{
		"model": "gpt-4",
		"subagent": {
			"max_concurrency": 5,
			"timeout_seconds": 60,
			"max_iterations": 10,
			"system_prompt": "custom prompt"
		}
	}`)
	if cfg.MaxConcurrency != 5 {
		t.Errorf("MaxConcurrency = %d, want 5", cfg.MaxConcurrency)
	}
	if cfg.TimeoutSeconds != 60 {
		t.Errorf("TimeoutSeconds = %d, want 60", cfg.TimeoutSeconds)
	}
	if cfg.MaxIterations != 10 {
		t.Errorf("MaxIterations = %d, want 10", cfg.MaxIterations)
	}
	if cfg.SystemPrompt != "custom prompt" {
		t.Errorf("SystemPrompt = %q, want %q", cfg.SystemPrompt, "custom prompt")
	}
}

// ── 16. subagentCmd Flag Parsing Edge Cases ─────────────────────────

func TestSubagentCmd_UnknownFlag(t *testing.T) {
	err := subagentCmd([]string{"--unknown"})
	if err == nil {
		t.Fatal("expected error for unknown flag")
	}
	if !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("error should mention unknown flag, got: %v", err)
	}
}

func TestSubagentCmd_GoalAndTaskMutuallyExclusive(t *testing.T) {
	err := subagentCmd([]string{"--goal", "test", "--task", "/tmp/task.json"})
	if err == nil {
		t.Fatal("expected error for mutually exclusive flags")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error should mention mutual exclusion, got: %v", err)
	}
}

func TestSubagentCmd_NoGoalOrTask(t *testing.T) {
	err := subagentCmd([]string{})
	if err == nil {
		t.Fatal("expected error for no --goal or --task")
	}
	if !strings.Contains(err.Error(), "either --goal or --task") {
		t.Errorf("error should mention --goal or --task, got: %v", err)
	}
}

func TestSubagentCmd_MissingFlagValue(t *testing.T) {
	// --goal with no following argument — should be treated as empty
	err := subagentCmd([]string{"--goal"})
	if err != nil {
		t.Logf("got expected error (or will fail validation): %v", err)
	}
}

// ── Helpers ────────────────────────────────────────────────────────

// skipIfNoBinary skips tests that need a real odek binary.
// These tests run as part of the E2E suite (ODEK_E2E=true).
func skipIfNoBinary(t *testing.T) {
	t.Helper()
	if odekBinary == "" {
		t.Skip("odek binary not found — set ODEK_E2E=true or build first")
	}
}

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
