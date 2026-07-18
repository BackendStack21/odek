package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
)

// defaultShellTimeout bounds a single shell command. It is deliberately
// generous — the goal is to stop a genuinely stuck command (a network read
// that never returns, an interactive prompt, an infinite loop) from wedging
// the agent forever, NOT to kill legitimate long builds or test suites. When
// the agent context is cancelled (Ctrl-C, turn timeout) the command is killed
// immediately regardless of this backstop.
const defaultShellTimeout = 30 * time.Minute

// maxShellOutputBytes caps the stdout + stderr captured from a single shell
// command to prevent memory DoS from commands that dump huge files.
const maxShellOutputBytes = 1 << 20 // 1 MiB

// limitWriter wraps a bytes.Buffer and drops further writes once the total
// size would exceed limit, recording that output was truncated.
type limitWriter struct {
	buf       *bytes.Buffer
	limit     int
	truncated bool
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if w.truncated {
		return len(p), nil
	}
	if w.buf.Len()+len(p) > w.limit {
		w.truncated = true
		room := w.limit - w.buf.Len()
		if room > 0 {
			w.buf.Write(p[:room])
		}
		w.buf.WriteString("\n... [output truncated]")
		return len(p), nil
	}
	return w.buf.Write(p)
}

// shellTool is odek's built-in tool that lets the agent run shell commands.
//
// This is the only built-in tool — it's enough for reading files, running
// tests, building code, and interacting with git. Additional tools can be
// added by implementing the odek.Tool interface (see README.md#Custom-Tools).
//
// Execution modes:
//
//   - Host mode (default): commands run directly on the host via "sh -c".
//     The agent has the same permissions as the odek process. Use with
//     caution — the agent can read, write, and execute anything your user
//     can. Prefer --sandbox for untrusted or exploratory tasks.
//
//   - Sandbox mode (--sandbox): every command executes inside a Docker
//     container via "docker exec -w /workspace <container> sh -c".
//     The container runs with restricted capabilities, no network (by
//     default), and the working directory mounted at /workspace. The
//     container is destroyed when the agent finishes.
//
// Safety:
//
//   - Shell injection is not a concern — the agent's LLM generates the
//     command string as JSON; the shell tool executes it as-is.
//   - Error output is merged into stdout (stderr follows stdout in output).
//   - Empty output returns "(no output)" so the LLM always gets a response.
//   - Commands are classified by risk (see internal/danger). High-risk
//     commands in non-sandboxed mode prompt the user for approval.
//     The approval mechanism uses the configured Approver — TTY in CLI mode,
//     WebSocket in serve mode — ensuring the same experience everywhere.
type shellTool struct {
	// containerName, when set, routes commands through "docker exec"
	// into this container. Set by setupSandbox() when --sandbox is active.
	// When empty, commands run directly on the host.
	containerName string

	// dangerousConfig controls per-class actions and allow/denylists.
	dangerousConfig danger.DangerousConfig

	// approver handles interactive approval prompts. When nil, falls back
	// to TTYApprover (CLI-compatible default).
	approver danger.Approver

	// trustedClasses caches user-approved risk classes for this process.
	// Set when user presses T (trust this session) at the prompt.
	trustedClasses map[danger.RiskClass]bool
	trustedMu      sync.Mutex

	// ttyPath is the path to the terminal device for approval prompts.
	// Overridden in tests to mock user input. Only used when approver is nil.
	ttyPath string

	// ctxTool provides SetContext/toolCtx so cancelling the agent context
	// (Ctrl-C, turn timeout) kills the running command.
	ctxTool

	// timeout bounds a single command. Zero falls back to defaultShellTimeout.
	timeout time.Duration
}

func (t *shellTool) Name() string { return "shell" }

func (t *shellTool) Description() string {
	return `Run a shell command and return its output.
Use for: reading files, listing directories, running tests, building code, and git operations.
In sandbox mode (--sandbox), commands run inside the Docker container with restricted permissions.
In host mode (default), commands run with the same permissions as the odek process.

Risk classes: safe, local_write, system_write, destructive, network_egress, code_execution, install, unknown, blocked
High-risk operations may prompt for approval (configurable via dangerous section in odek.json).
The gate fails closed: an unrecognised command classifies as "unknown" and is denied by default.`
}

func (t *shellTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The shell command to execute. Supports pipes, redirects, and multi-line scripts.",
			},
			"description": map[string]any{
				"type":        "string",
				"description": "Optional: explain what this command does and why. Shown in the approval prompt for high-risk operations.",
			},
		},
		"required": []string{"command"},
	}
}

// Call executes a shell command and returns its output.
// The command is executed via sh -c (host mode) or docker exec (sandbox mode).
// Both stdout and stderr are captured and merged into the return string.
func (t *shellTool) Call(args string) (string, error) {
	var input struct {
		Command     string `json:"command"`
		Description string `json:"description,omitempty"`
	}
	if err := json.Unmarshal([]byte(args), &input); err != nil {
		return "", fmt.Errorf("shell: parse args: %w", err)
	}
	if input.Command == "" {
		return "", fmt.Errorf("shell: empty command")
	}

	// Check approval before executing
	if err := t.checkApproval(input.Command, input.Description); err != nil {
		return "", err
	}

	// Bound execution: cancel with the agent context (Ctrl-C / turn timeout)
	// and a generous backstop timeout so a stuck command can never wedge the
	// agent forever. Note: in sandbox mode this kills the host-side
	// `docker exec` client, which unblocks the agent, but Docker does not
	// propagate the signal to the in-container process — that lingers until the
	// container is torn down at session end.
	base := t.toolCtx()
	timeout := t.timeout
	if timeout <= 0 {
		timeout = defaultShellTimeout
	}
	ctx, cancel := context.WithTimeout(base, timeout)
	defer cancel()

	cmd := t.buildCmd(ctx, input.Command)
	// Run the command in its own process group and, on cancel/timeout, kill the
	// WHOLE group — not just the `sh` leader. `sh -c "<cmd>"` may fork children
	// (e.g. `sleep`); killing only the leader leaves them alive holding the
	// output pipes, so Run() would block until WaitDelay. Signalling the group
	// (negative pid) tears the whole tree down at once.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			// Best-effort group kill; ignore ESRCH if it already exited.
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	// WaitDelay is a backstop in case a process somehow outlives the group kill.
	cmd.WaitDelay = 3 * time.Second

	var outBuf, errBuf bytes.Buffer
	outW := &limitWriter{buf: &outBuf, limit: maxShellOutputBytes}
	errW := &limitWriter{buf: &errBuf, limit: maxShellOutputBytes}
	cmd.Stdout = outW
	cmd.Stderr = errW

	err := cmd.Run()

	// Surface cancellation/timeout as a clear, actionable error rather than an
	// opaque "signal: killed".
	if ctxErr := ctx.Err(); ctxErr != nil {
		if ctxErr == context.DeadlineExceeded {
			return "", fmt.Errorf("shell: command timed out after %s (still running? it was killed): %s", timeout, input.Command)
		}
		return "", fmt.Errorf("shell: command cancelled: %s", input.Command)
	}

	output := strings.TrimSpace(outBuf.String())
	stderrStr := strings.TrimSpace(errBuf.String())
	if stderrStr != "" {
		if output != "" {
			output += "\n"
		}
		output += stderrStr
	}
	if err != nil && output == "" {
		return "", fmt.Errorf("shell: %w", err)
	}
	if err != nil && stderrStr != "" {
		// Include stderr even when stdout is empty — "exit status 1" alone
		// gives the LLM no clue why the command failed.
		return wrapUntrusted(t.toolCtx(), "$ "+input.Command, output), nil
	}
	if output == "" {
		output = "(no output)"
	}
	return wrapUntrusted(t.toolCtx(), "$ "+input.Command, output), nil
}

// checkApproval classifies the command and prompts the user if needed.
func (t *shellTool) checkApproval(cmd, description string) error {
	// Check allowlist/denylist + risk class via dangerous config
	action := t.dangerousConfig.ActionForCommand(cmd)

	switch action {
	case danger.Allow:
		return nil
	case danger.Deny:
		return fmt.Errorf("operation denied by configuration: %s", cmd)
	case danger.Prompt:
		return t.promptUser(cmd, description)
	default:
		return nil
	}
}

// promptUser classifies the command and asks the user to approve it.
// Delegates to the configured Approver, or falls back to TTYApprover.
func (t *shellTool) promptUser(cmd, description string) error {
	cls := danger.Classify(cmd)

	// Get or create the approver. Reuse a single TTYApprover per tool instance
	// so the friction counter and trust cache survive across multiple prompts.
	t.trustedMu.Lock()
	approver := t.approver
	if approver == nil {
		ttyApprover := danger.NewTTYApprover(&t.dangerousConfig)
		if t.trustedClasses != nil {
			ttyApprover.SetTrustedClasses(t.trustedClasses)
		}
		if t.ttyPath != "" {
			ttyApprover.TTYPath = t.ttyPath
		}
		t.approver = ttyApprover
		approver = ttyApprover
	}
	t.trustedMu.Unlock()

	err := approver.PromptCommand(cls, cmd, description)
	if err == nil {
		// Sync trusted classes back if using TTYApprover
		if tty, ok := approver.(*danger.TTYApprover); ok {
			t.trustedMu.Lock()
			t.trustedClasses = tty.TrustedClasses
			t.trustedMu.Unlock()
		}
	}
	return err
}

// buildCmd constructs the exec.Cmd for the given shell command.
//
// When sandbox mode is active (containerName is non-empty), the command
// is wrapped in "docker exec -w /workspace <container> sh -c <cmd>".
// The -w /workspace flag ensures the command runs in the working directory
// that was mounted into the container during setupSandbox().
//
// When running on the host (default), the command executes via "sh -c"
// in odek's current working directory.
func (t *shellTool) buildCmd(ctx context.Context, command string) *exec.Cmd {
	if t.containerName != "" {
		return exec.CommandContext(ctx, "docker", "exec", "-w", "/workspace", t.containerName, "sh", "-c", command)
	}
	return exec.CommandContext(ctx, "sh", "-c", command)
}
