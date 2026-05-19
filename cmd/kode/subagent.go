package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/BackendStack21/kode"
	"github.com/BackendStack21/kode/internal/config"
	"github.com/BackendStack21/kode/internal/llm"
	"github.com/BackendStack21/kode/internal/render"
	"github.com/BackendStack21/kode/internal/skills"
)

// ── Sub-agent System Prompts ────────────────────────────────────────
//
// Sub-agents receive a system prompt matched to their task's category.
// The parent agent can also provide a custom system prompt via the
// `system` field in delegate_tasks. When neither is provided, use
// classifyGoal() to pick the best category.

const subagentSystem = `You are kode working on a single focused sub-task.
Complete the assigned goal and report what you did.
Do not expand scope. Do not ask questions.
Use the shell tool when you need information or to make changes.
Report: what you built, what files changed, any issues encountered.
Be concise. Output your answer, then stop.`

// Category-specific system prompts.
// Each is optimized for a different task type.

const buildSystem = `You are kode — an expert engineer building production code.
Architect and implement with confidence. Consider edge cases, error handling,
and maintainability from the start. Write clean, idiomatic code that another
engineer can read and extend. Report what you built and what files changed.`

const debugSystem = `You are kode — an expert debugger.
Find the root cause. Isolate the bug before you write any fix. Prove the fix
works by reasoning through the normal and edge cases. Report what was broken,
the root cause, and how you fixed it.`

const testSystem = `You are kode — an expert in testing and quality.
Write thorough tests. Cover the happy path, edge cases, and failure modes.
Use table-driven tests where appropriate. Tests should be readable and
maintainable. Report what you tested and the coverage pattern.`

const reviewSystem = `You are kode — a senior engineer reviewing code.
Read every line critically. Look for logic errors, security vulnerabilities,
performance issues, and style problems. Be constructive — propose specific
improvements. Report all findings with file:line references.`

const refactorSystem = `You are kode — an expert in code architecture.
Preserve behavior. Change structure only. Clean up technical debt while
ensuring nothing breaks. Report what you changed and why the new structure
is better.`

const configSystem = `You are kode — a DevOps engineer configuring systems.
Make every change reproducible and documented. Use minimal permissions.
Test the configuration after changing it. Report what you set up and how
to verify it works.`

const researchSystem = `You are kode — a technical researcher.
Explore thoroughly. Read source code, docs, and examples before concluding.
Cite your sources. Synthesize findings into a clear recommendation.
Report what you found and your recommended action.`

// classifyGoal returns a system prompt matched to the task's category
// by analyzing the goal text. Falls back to the default subagentSystem
// when no strong signal is detected.
func classifyGoal(goal string) string {
	lower := strings.ToLower(goal)
	switch {
	case containsAny(lower, "fix", "bug", "error", "crash", "broken", "incorrect", "wrong", "fail"):
		return debugSystem
	case containsAny(lower, "test", "spec", "coverage", "assert", "unit test", "integration test"):
		return testSystem
	case containsAny(lower, "review", "audit", "check", "inspect", "verify", "validate"):
		return reviewSystem
	case containsAny(lower, "refactor", "clean up", "simplify", "rename", "extract", "restructure", "reorganize"):
		return refactorSystem
	case containsAny(lower, "setup", "config", "install", "docker", "ci", "deploy", "provision"):
		return configSystem
	case containsAny(lower, "research", "explain", "compare", "understand", "find", "investigate", "analyze"):
		return researchSystem
	default:
		return buildSystem // greenfield / build tasks
	}
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// subagentResult is the JSON contract written to stdout.
type subagentResult struct {
	Status       string   `json:"status"`                  // "success" or "error"
	Error        string   `json:"error,omitempty"`         // error message
	Summary      string   `json:"summary"`                 // task summary
	FilesChanged []string `json:"files_changed,omitempty"` // changed files
	TokensUsed   int      `json:"tokens_used"`             // total tokens consumed
	Iterations   int      `json:"iterations"`              // think-act cycles used
}

// ── SubagentConfig ───────────────────────────────────────────────────

type subagentConfig struct {
	MaxConcurrency int    `json:"max_concurrency"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	MaxIterations  int    `json:"max_iterations"`
	SystemPrompt   string `json:"system_prompt,omitempty"`
}

func defaultSubagentConfig() subagentConfig {
	return subagentConfig{
		MaxConcurrency: 3,
		TimeoutSeconds: 120,
		MaxIterations:  15,
	}
}

// parseSubagentConfig extracts the subagent section from a config JSON string.
func parseSubagentConfig(data string) subagentConfig {
	cfg := defaultSubagentConfig()
	if data == "" {
		return cfg
	}
	var file struct {
		Subagent *subagentConfig `json:"subagent"`
	}
	if err := json.Unmarshal([]byte(data), &file); err != nil {
		return cfg
	}
	if file.Subagent != nil {
		if file.Subagent.MaxConcurrency > 0 {
			cfg.MaxConcurrency = file.Subagent.MaxConcurrency
		}
		if file.Subagent.TimeoutSeconds > 0 {
			cfg.TimeoutSeconds = file.Subagent.TimeoutSeconds
		}
		if file.Subagent.MaxIterations > 0 {
			cfg.MaxIterations = file.Subagent.MaxIterations
		}
		if file.Subagent.SystemPrompt != "" {
			cfg.SystemPrompt = file.Subagent.SystemPrompt
		}
	}
	return cfg
}

// ── Subagent Command ─────────────────────────────────────────────────

// subagentCmd handles `kode subagent [flags]`.
// It runs a focused agent with a minimal system prompt and outputs
// a JSON result to stdout. Stderr carries human-readable progress.
//
// Exit codes:
//   0 = success (status: "success")
//   1 = task error (status: "error" with message)
//   2 = timeout (killed by parent/context)
//   3 = internal setup error
func subagentCmd(args []string) error {
	// Parse flags
	var cfg struct {
		goal          string
		context       string
		taskFile      string
		timeout       int
		maxIter       int
		quiet         bool
		parentSession string
	}

	// Simple flag parser (matches existing pattern in parseRunFlags)
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--goal":
			i++
			if i < len(args) {
				cfg.goal = args[i]
			}
		case "--context":
			i++
			if i < len(args) {
				cfg.context = args[i]
			}
		case "--task":
			i++
			if i < len(args) {
				cfg.taskFile = args[i]
			}
		case "--timeout":
			i++
			if i < len(args) {
				fmt.Sscanf(args[i], "%d", &cfg.timeout)
			}
		case "--max-iter":
			i++
			if i < len(args) {
				fmt.Sscanf(args[i], "%d", &cfg.maxIter)
			}
		case "--quiet":
			cfg.quiet = true
		case "--parent-session":
			i++
			if i < len(args) {
				cfg.parentSession = args[i]
			}
		default:
			return fmt.Errorf("unknown flag %q", args[i])
		}
		i++
	}

	// Validate: --goal XOR --task
	hasGoal := cfg.goal != ""
	hasTaskFile := cfg.taskFile != ""
	if hasGoal && hasTaskFile {
		return fmt.Errorf("--goal and --task are mutually exclusive")
	}
	if !hasGoal && !hasTaskFile {
		return fmt.Errorf("either --goal or --task is required")
	}

	// Load task from file if --task is provided, including optional system prompt
	var taskSystem string // system prompt from task file (if any)
	if hasTaskFile {
		data, err := os.ReadFile(cfg.taskFile)
		if err != nil {
			return fmt.Errorf("read task file: %w", err)
		}
		var task struct {
			Goal    string `json:"goal"`
			Context string `json:"context"`
			System  string `json:"system,omitempty"`
		}
		if err := json.Unmarshal(data, &task); err != nil {
			return fmt.Errorf("parse task file: %w", err)
		}
		cfg.goal = task.Goal
		cfg.context = task.Context
		taskSystem = task.System
		// Clean up temp file
		os.Remove(cfg.taskFile)
	}

	// Apply defaults
	if cfg.timeout <= 0 {
		cfg.timeout = 120
	}
	if cfg.maxIter <= 0 {
		cfg.maxIter = 15
	}

	// Build the user prompt
	prompt := cfg.goal
	if cfg.context != "" {
		prompt = fmt.Sprintf("%s\n\nContext:\n%s", cfg.goal, cfg.context)
	}

	// Resolve config (inherits everything from normal chain)
	resolved := config.LoadConfig(config.CLIFlags{})

	// Resolve system prompt for this sub-agent.
	// Priority: 1) task file override  2) user config override  3) classifyGoal  4) default
	systemMsg := classifyGoal(cfg.goal)
	switch {
	case taskSystem != "":
		systemMsg = taskSystem
	case resolved.System != "":
		systemMsg = resolved.System
	}

	// Build tools
	var sm *skills.SkillManager
	if resolved.Skills.Learn {
		sm = skills.NewSkillManager(
			expandHome("~/.kode/skills"),
			"./.kode/skills",
		)
	}
	tools := builtinTools(resolved.Dangerous, sm)
	var sandboxCleanup func() error

	if resolved.Sandbox {
		sbCfg := sandboxConfig{
			Image:    resolved.SandboxImage,
			Network:  resolved.SandboxNetwork,
			Readonly: resolved.SandboxReadonly,
			Memory:   resolved.SandboxMemory,
			CPUs:     resolved.SandboxCPUs,
			User:     resolved.SandboxUser,
			Env:      resolved.SandboxEnv,
			Volumes:  resolved.SandboxVolumes,
		}
		cleanup, err := setupSandbox(tools, sbCfg)
		if err != nil {
			return fmt.Errorf("sandbox: %w", err)
		}
		sandboxCleanup = cleanup
	}

	// Context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.timeout)*time.Second)
	defer cancel()

	// Signal handling (for user-initiated cancellation)
	sigCtx, sigCancel := signal.NotifyContext(ctx, os.Interrupt)
	defer sigCancel()

	// Human-readable progress goes to stderr
	if !cfg.quiet {
		fmt.Fprintf(os.Stderr, "🔧 Sub-agent: %s\n", truncate(cfg.goal, 60))
	}

	// Create agent — silent renderer for stderr
	rend := render.New(os.Stderr, !cfg.quiet)

	agent, err := kode.New(kode.Config{
		Model:          resolved.Model,
		BaseURL:        resolved.BaseURL,
		APIKey:         resolved.APIKey,
		MaxIterations:  cfg.maxIter,
		SystemMessage:  systemMsg,
		NoProjectFile:  resolved.NoAgents,
		Thinking:       resolved.Thinking,
		Tools:          tools,
		SandboxCleanup: sandboxCleanup,
		Renderer:       rend,
		Skills:         &resolved.Skills,
		SkillManager:   sm,
	})
	if err != nil {
		return fmt.Errorf("create agent: %w", err)
	}
	defer agent.Close()

	// Run
	start := time.Now()
	_, allMessages, err := agent.RunWithMessages(sigCtx, []llm.Message{
		{Role: "system", Content: systemMsg},
		{Role: "user", Content: prompt},
	})
	latency := time.Since(start)

	// Count iterations (agent responses with tool calls)
	iterations := 0
	for _, msg := range allMessages {
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			iterations++
		}
	}

	// Count tokens (approximate from all messages)
	tokensUsed := 0
	for _, msg := range allMessages {
		tokensUsed += len(msg.Content) / 4 // rough estimate
	}

	// Build result
	result := subagentResult{
		Status:     "success",
		Summary:    extractSummary(allMessages),
		TokensUsed: tokensUsed,
		Iterations: iterations,
	}

	if err != nil {
		if sigCtx.Err() != nil {
			result.Status = "error"
			result.Error = fmt.Sprintf("timeout after %ds", cfg.timeout)
		} else {
			result.Status = "error"
			result.Error = err.Error()
		}
		result.Summary = extractSummary(allMessages)
	}

	// Extract files changed from tool calls
	result.FilesChanged = extractFilesChanged(allMessages)

	// Output JSON to stdout
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "")
	enc.Encode(result)

	if !cfg.quiet {
		fmt.Fprintf(os.Stderr, "✅ Sub-agent complete: %.1fs, %d tokens, %d iterations\n",
			latency.Seconds(), tokensUsed, iterations)
	}

	return nil
}

// ── Helpers ───────────────────────────────────────────────────────────

func extractSummary(messages []llm.Message) string {
	// Return the last assistant message content as summary
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" && messages[i].Content != "" {
			return truncate(messages[i].Content, 500)
		}
	}
	return ""
}

func extractFilesChanged(messages []llm.Message) []string {
	var files []string
	seen := make(map[string]bool)
	for _, msg := range messages {
		if msg.Role == "tool" && msg.Content != "" {
			// Look for file paths in tool output (write_file, patch commands)
			lines := strings.Split(msg.Content, "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				// Match patterns like "wrote file.go", "created path/to/file.go"
				for _, prefix := range []string{"wrote ", "created ", "modified ", "updated "} {
					if strings.HasPrefix(line, prefix) {
						f := strings.TrimSpace(line[len(prefix):])
						if !seen[f] && strings.Contains(f, ".") {
							seen[f] = true
							files = append(files, f)
						}
					}
				}
			}
		}
	}
	return files
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
