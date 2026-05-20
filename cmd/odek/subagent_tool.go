package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/BackendStack21/kode"
)

// delegateTasksTool is a built-in tool that spawns sub-agent OS processes
// to work on focused sub-tasks in parallel. Each sub-agent gets its own
// process, config, and context window.
//
// The tool serializes tasks to temp files and calls kode subagent for each.
// Sub-agents run in parallel up to maxConcurrency. Results are collated
// and returned to the calling agent as a formatted summary.
type delegateTasksTool struct {
	maxConcurrency int
	kodePath       string // path to the kode binary
	timeout        time.Duration
}

func (t *delegateTasksTool) Name() string { return "delegate_tasks" }

func (t *delegateTasksTool) Description() string {
	return `Spawn one or more sub-agent OS processes to work on focused sub-tasks in parallel. Each sub-agent gets its own process, config, and context window. Use this when the task has clear independent sub-tasks that can be worked on simultaneously.

Example: decomposing "build a REST API" into "create user model", "create auth middleware", "create route handlers".

Key rules:
- Each sub-agent has a fresh context (no parent history)
- All sub-agents run in parallel (up to 3 at a time)
- Each sub-agent has 120s to complete
- Sub-agents can use all tools (shell, read/write files, etc.)
- After all complete, synthesize the results into a cohesive answer

Output format per sub-agent:
- Summary of what was built
- Files changed
- Key decisions made
- Any issues encountered`
}

func (t *delegateTasksTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tasks": map[string]any{
				"type":        "array",
				"description": "Array of sub-tasks to execute. All run in parallel up to max concurrency.",
				"minItems":    1,
				"maxItems":    8,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"goal": map[string]any{
							"type":        "string",
							"description": "Required. The specific goal for this sub-agent. Be precise: what to build, where, and key constraints.",
						},
					"context": map[string]any{
						"type":        "string",
						"description": "Optional. Background context: file paths, architecture decisions, API contracts.",
					},
					"system": map[string]any{
						"type":        "string",
						"description": "Optional. System prompt for this sub-agent. Tailor the approach: \"You are a security engineer reviewing auth code\" for reviews, \"Find the root cause first\" for debugging.",
					},
					},
					"required": []string{"goal"},
				},
			},
			"description": map[string]any{
				"type":        "string",
				"description": "Optional. Explain why you're delegating these tasks. Shown in logs for debugging.",
			},
		},
		"required": []string{"tasks"},
	}
}

func (t *delegateTasksTool) Call(args string) (string, error) {
	var input struct {
		Tasks       []struct {
			Goal    string `json:"goal"`
			Context string `json:"context"`
			System  string `json:"system,omitempty"`
		} `json:"tasks"`
		Description string `json:"description,omitempty"`
	}
	if err := json.Unmarshal([]byte(args), &input); err != nil {
		return fmt.Sprintf(`{"error":"parse failed: %v"}`, err), nil
	}
	if len(input.Tasks) == 0 {
		return `{"error":"no tasks provided"}`, nil
	}
	if len(input.Tasks) > 8 {
		return `{"error":"max 8 tasks per call"}`, nil
	}

	// Run sub-agents in parallel with concurrency limit
	results := make([]string, len(input.Tasks))
	sem := make(chan struct{}, t.maxConcurrency)
	var mu sync.Mutex

	for i, task := range input.Tasks {
		sem <- struct{}{}
		go func(i int, goal, ctx, sys string) {
			defer func() { <-sem }()
			r := t.runTask(goal, ctx, sys)
			mu.Lock()
			results[i] = r
			mu.Unlock()
		}(i, task.Goal, task.Context, task.System)
	}

	// Drain semaphore = wait for all goroutines
	for i := 0; i < cap(sem); i++ {
		sem <- struct{}{}
	}

	// Build summary for the calling agent
	var buf strings.Builder
	buf.WriteString("📋 Sub-agent results:\n\n")
	for i, r := range results {
		buf.WriteString(fmt.Sprintf("─── Task %d: %s ───\n", i+1, truncate(input.Tasks[i].Goal, 60)))
		buf.WriteString(r)
		buf.WriteString("\n\n")
	}
	return buf.String(), nil
}

func (t *delegateTasksTool) runTask(goal, taskContext, system string) string {
	ctx, cancel := context.WithTimeout(context.Background(), t.timeout)
	defer cancel()

	// Write task to temp file (avoids CLI arg length limits)
	taskFile, err := os.CreateTemp("", "kode-task-*.json")
	if err != nil {
		return fmt.Sprintf(`{"error":"temp file: %v"}`, err)
	}
	taskPath := taskFile.Name()

	task := map[string]string{
		"goal":    goal,
		"context": taskContext,
		"system":  system,
	}
	if err := json.NewEncoder(taskFile).Encode(task); err != nil {
		taskFile.Close()
		os.Remove(taskPath)
		return fmt.Sprintf(`{"error":"write task: %v"}`, err)
	}
	taskFile.Close()
	defer os.Remove(taskPath)

	cmd := exec.CommandContext(ctx, t.kodePath,
		"subagent",
		"--task", taskPath,
		"--quiet",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Sprintf(`{"error":"pipe: %v"}`, err)
	}

	// Capture stderr for optional relay
	stderrBuf := &strings.Builder{}
	cmd.Stderr = stderrBuf

	if err := cmd.Start(); err != nil {
		return fmt.Sprintf(`{"error":"start: %v"}`, err)
	}

	var result map[string]any
	if err := json.NewDecoder(stdout).Decode(&result); err != nil {
		// Wait for process to finish, then check for timeout
		cmd.Wait()
		if ctx.Err() != nil {
			return fmt.Sprintf(`{"error":"timeout after %v"}`, t.timeout)
		}
		return fmt.Sprintf(`{"error":"parse result: %v"}`, err)
	}

	if err := cmd.Wait(); err != nil {
		// Process exited with error — result may still be valid JSON
		if result != nil {
			summary, _ := json.MarshalIndent(result, "", "  ")
			return string(summary)
		}
		if ctx.Err() != nil {
			return fmt.Sprintf(`{"error":"timeout after %v"}`, t.timeout)
		}
		return fmt.Sprintf(`{"error":"exit: %v"}`, err)
	}

	summary, _ := json.MarshalIndent(result, "", "  ")
	return string(summary)
}

// Ensure delegateTasksTool implements kode.Tool
var _ kode.Tool = (*delegateTasksTool)(nil)
