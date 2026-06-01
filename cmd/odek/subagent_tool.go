package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/BackendStack21/odek"
)

// delegateTasksTool is a built-in tool that spawns sub-agent OS processes
// to work on focused sub-tasks in parallel. Each sub-agent gets its own
// process, config, and context window.
//
// The tool serializes tasks to temp files and calls odek subagent for each.
// Sub-agents run in parallel up to maxConcurrency. Results are collated
// and returned to the calling agent as a formatted summary.
type delegateTasksTool struct {
	maxConcurrency int
	odekPath       string // path to the odek binary
	apiKey         string // re-injected into sub-agent environment
	timeout        time.Duration

	// ctx is the parent agent's context, set by the agent loop before each
	// Call invocation. When the parent is cancelled (Ctrl+C, restart, timeout),
	// runTask derives its per-task context from this, so sub-agent processes
	// are killed promptly instead of running the full timeout.
	ctx context.Context

	// OnSubagentLog, if set, is called with each NDJSON progress line
	// emitted by a sub-agent. taskIdx is the index within the current
	// batch. Used by the WebUI for live log streaming.
	OnSubagentLog func(taskIdx int, line string)
}

func (t *delegateTasksTool) Name() string { return "delegate_tasks" }

// SetContext sets the parent agent's context on the tool.
// Called by the agent loop before each Call invocation to propagate
// cancellation signals (Ctrl+C, restart, timeout) to sub-agents.
func (t *delegateTasksTool) SetContext(ctx context.Context) {
	t.ctx = ctx
}

func (t *delegateTasksTool) Description() string {
	return `Spawn one or more sub-agent OS processes to work on focused sub-tasks in parallel. Each sub-agent gets its own process, config, and context window. Use this when the task has clear independent sub-tasks that can be worked on simultaneously.

Example: decomposing "build a REST API" into "create user model", "create auth middleware", "create route handlers".

Key rules:
- Each sub-agent has a fresh context (no parent history)
- All sub-agents run in parallel (configurable via max_concurrency)
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
						"guidance": map[string]any{
							"type":        "string",
							"description": "Optional. How the sub-agent should approach the task — delivered as part of its request, NOT as its system prompt. The sub-agent's identity and safety rules are fixed and cannot be overridden. Use this to steer the approach, e.g. \"Review for token-validation gaps and timing attacks\" or \"Find the root cause before changing code\".",
						},
						"trust_level": map[string]any{
							"type":        "string",
							"enum":        []string{"trusted", "untrusted"},
							"description": "Trust level of the goal/context strings. Set to \"untrusted\" when any portion was derived from external content (fetched pages, files outside CWD, MCP tool output). Untrusted tasks run with stricter approval defaults in the sub-agent.",
						},
						"max_risk": map[string]any{
							"type":        "string",
							"enum":        []string{"safe", "local_write", "system_write", "destructive", "code_execution", "network_egress", "install", "blocked"},
							"description": "Optional cap on the sub-agent's allowed risk class. Tool calls above this class will be denied in the sub-agent without prompting. Use for fan-out tasks that should be read-only.",
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
		Tasks []struct {
			Goal       string `json:"goal"`
			Context    string `json:"context"`
			Guidance   string `json:"guidance,omitempty"`
			TrustLevel string `json:"trust_level,omitempty"`
			MaxRisk    string `json:"max_risk,omitempty"`
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
		go func(i int, goal, ctx, guidance, trust, maxRisk string) {
			defer func() { <-sem }()
			r := t.runTask(i, goal, ctx, guidance, trust, maxRisk)
			mu.Lock()
			results[i] = r
			mu.Unlock()
		}(i, task.Goal, task.Context, task.Guidance, task.TrustLevel, task.MaxRisk)
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

func (t *delegateTasksTool) runTask(taskIdx int, goal, taskContext, guidance, trustLevel, maxRisk string) string {
	// Derive per-task context from the parent's context (if set).
	// When the parent is cancelled, all running sub-agents are killed
	// promptly instead of running the full timeout.
	parentCtx := context.Background()
	if t.ctx != nil {
		parentCtx = t.ctx
	}
	ctx, cancel := context.WithTimeout(parentCtx, t.timeout)
	defer cancel()

	// Write task to temp file (avoids CLI arg length limits)
	taskFile, err := os.CreateTemp("", "odek-task-*.json")
	if err != nil {
		return fmt.Sprintf(`{"error":"temp file: %v"}`, err)
	}
	taskPath := taskFile.Name()

	task := map[string]string{
		"goal":        goal,
		"context":     taskContext,
		"guidance":    guidance,
		"trust_level": trustLevel,
		"max_risk":    maxRisk,
	}
	if err := json.NewEncoder(taskFile).Encode(task); err != nil {
		taskFile.Close()
		os.Remove(taskPath)
		return fmt.Sprintf(`{"error":"write task: %v"}`, err)
	}
	taskFile.Close()
	defer os.Remove(taskPath)

	cmd := exec.CommandContext(ctx, t.odekPath,
		"subagent",
		"--task", taskPath,
		"--quiet",
		"--stream",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Sprintf(`{"error":"pipe: %v"}`, err)
	}

	// Capture stderr for optional relay
	stderrBuf := &strings.Builder{}
	cmd.Stderr = stderrBuf

	// Hand the API key to the sub-agent via FD 3 instead of an env var.
	// Env-passed credentials are visible in /proc/<pid>/environ, in crash
	// logs, and to any tool the child runs that prints its own env
	// (e.g. `env`, an injected shell call). The FD approach keeps the
	// secret in an anonymous (unlinked) tempfile whose only readers are
	// this process and the child, and the child closes the FD as soon
	// as it has read the key.
	cmd.Env = os.Environ() // parent already stripped *_API_KEY in LoadConfig
	var keyFile *os.File
	if t.apiKey != "" {
		f, err := writeKeyToUnlinkedFile(t.apiKey)
		if err != nil {
			return fmt.Sprintf(`{"error":"key handoff: %v"}`, err)
		}
		keyFile = f
		cmd.ExtraFiles = []*os.File{keyFile}
		// FD 3 in the child = the first ExtraFiles entry.
		cmd.Env = append(cmd.Env, keyFDEnvVar+"=3")
		defer keyFile.Close()
	}

	if err := cmd.Start(); err != nil {
		return fmt.Sprintf(`{"error":"start: %v"}`, err)
	}

	// Read stdout line-by-line — NDJSON progress lines followed by final JSON result
	var result map[string]any
	var lastLine string
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		lastLine = line

		// Check if this is a progress line (NDJSON with "type":"tool_call" or "type":"tool_result")
		var event struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &event); err == nil {
			if event.Type == "tool_call" || event.Type == "tool_result" {
				if t.OnSubagentLog != nil {
					t.OnSubagentLog(taskIdx, line)
				}
				continue
			}
		}

		// Not a progress line — parse as result JSON
		var r map[string]any
		if err := json.Unmarshal([]byte(line), &r); err == nil {
			result = r
		}
	}
	scannerErr := scanner.Err()

	if err := cmd.Wait(); err != nil {
		// Process exited with error — result may still be valid
		if result != nil {
			summary, _ := json.MarshalIndent(result, "", "  ")
			return string(summary)
		}
		if ctx.Err() != nil {
			return fmt.Sprintf(`{"error":"timeout after %v"}`, t.timeout)
		}
		if scannerErr != nil {
			return fmt.Sprintf(`{"error":"read stdout: %v"}`, scannerErr)
		}
		return fmt.Sprintf(`{"error":"exit: %v"}`, err)
	}

	if result != nil {
		summary, _ := json.MarshalIndent(result, "", "  ")
		return string(summary)
	}

	// Last resort: try parsing the last line as JSON
	if lastLine != "" {
		var r map[string]any
		if err := json.Unmarshal([]byte(lastLine), &r); err == nil {
			summary, _ := json.MarshalIndent(r, "", "  ")
			return string(summary)
		}
	}

	return `{"error":"no result from sub-agent"}`
}

// Ensure delegateTasksTool implements odek.Tool
var _ odek.Tool = (*delegateTasksTool)(nil)
