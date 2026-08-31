package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/BackendStack21/odek/internal/danger"
)

// defaultShellTimeout bounds a single shell command. It is deliberately
// generous — the goal is to stop a genuinely stuck command (a network read
// that never returns, an interactive prompt, an infinite loop) from wedging
// the agent forever, NOT to kill legitimate long builds or test suites. When
// the agent context is cancelled (Ctrl-C, turn timeout) the command is killed
// immediately regardless of this backstop.
const defaultShellTimeout = 30 * time.Minute

// clampShellTimeoutSeconds bounds an LLM-provided timeout_seconds to
// [1, 1800] seconds, mirroring parallel_shell's per-command cap so the
// agent can tighten the backstop but never exceed it.
func clampShellTimeoutSeconds(sec int) int {
	if sec < 1 {
		return 1
	}
	if max := int(defaultShellTimeout / time.Second); sec > max {
		return max
	}
	return sec
}

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
			// Back up to a UTF-8 rune boundary so a multibyte character cut
			// by the cap never ships U+FFFD replacement mojibake.
			for room > 0 && !utf8.RuneStart(p[room]) {
				room--
			}
			if room > 0 {
				w.buf.Write(p[:room])
			}
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
The gate fails closed: an unrecognised command classifies as "unknown" and is denied by default.

Output is fully buffered: nothing is returned until the command finishes. For known long-running
commands (builds, test suites), set timeout_seconds explicitly so a stuck command fails fast.`
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
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"description": "Optional: per-command timeout in seconds. Set it explicitly for known long-running commands (builds, test suites); values are clamped to 1800 max. Default: 1800.",
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
		Command        string `json:"command"`
		Description    string `json:"description,omitempty"`
		TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
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
	// and a timeout — an LLM-provided timeout_seconds when set, otherwise a
	// generous backstop — so a stuck command can never wedge the agent
	// forever. In sandbox mode this kills the host-side `docker exec`
	// client, which unblocks the agent — but Docker does not propagate the
	// signal to the in-container process, so buildCmd also returns a
	// follow-up that kills the in-container process group explicitly.
	base := t.toolCtx()
	timeout := t.timeout
	if timeout <= 0 {
		timeout = defaultShellTimeout
	}
	// An explicit timeout_seconds from the LLM overrides the tool default.
	// Zero or negative is treated as absent (same as parallel_shell).
	if input.TimeoutSeconds > 0 {
		timeout = time.Duration(clampShellTimeoutSeconds(input.TimeoutSeconds)) * time.Second
	}
	ctx, cancel := context.WithTimeout(base, timeout)
	defer cancel()

	cmd, killInContainer := t.buildCmd(ctx, input.Command)
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

	// On timeout/cancel the host-side `docker exec` client was killed by the
	// group kill above, but the in-container process survives it (Docker does
	// not propagate the signal). Kill its process group explicitly so a
	// timed-out command cannot keep burning CPU/memory or leave half-written
	// files in /workspace until the container is torn down.
	if ctx.Err() != nil && killInContainer != nil {
		killInContainer()
	}

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

	// H-6: a successful read-only viewer run (cat/head/tail/…) marks its
	// file operands as read for the session, so a later execution of the
	// same script passes the unread-exec gate. Only success counts — a
	// failed `cat env.sh` must never license executing env.sh.
	if err == nil {
		recordViewerReads(input.Command)
	}

	if stderrStr != "" {
		if output != "" {
			output += "\n"
		}
		output += stderrStr
	}
	if err != nil && output == "" {
		return "", fmt.Errorf("shell: %w", err)
	}
	if err != nil {
		// Failing command with captured output: return the output (the
		// model needs stdout/stderr, not just "exit status N") but name
		// the failure explicitly — without this, a failing test/build run
		// was indistinguishable from a passing one.
		output += "\n[command failed: " + err.Error() + "]"
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

	// H-6: executing a repo-supplied script whose contents have not been
	// read this session gates under unread_exec — even when code_execution
	// was allowed or its class trusted. The whole point is per-script
	// review: the payload in the study sat inside the correct, documented
	// fix and fired on the verification run. An explicit unread_exec action
	// participates in the decision: deny wins outright; allow alone does
	// not bypass a prompting base class — both must allow.
	if _, targets := danger.ClassifyScriptGate(cmd); len(targets) > 0 {
		unreadAction := t.dangerousConfig.ActionFor(danger.UnreadExec)
		switch {
		case unreadAction == danger.Deny:
			action = danger.Deny
		case action == danger.Allow && unreadAction == danger.Allow:
			action = danger.Allow
		default:
			action = danger.Prompt
			if description == "" {
				description = fmt.Sprintf("executes a script whose contents have not been read this session: %s", strings.Join(targets, ", "))
			}
			// Audit-then-exec (H-6 companion): the human decides with the
			// bytes. Content evidence from the local injection scanner —
			// read-only, ledger-neutral — rides in the approval description.
			if findings := scanUnreadScripts(targets); len(findings) > 0 {
				description += " — ⚠️ injection scan: " + strings.Join(findings, "; ")
			}
		}
	}

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
	cls, targets := danger.ClassifyScriptGate(cmd)
	if len(targets) > 0 && cls != danger.UnreadExec {
		// Stronger finding alongside unread targets: still surface which
		// scripts would run unread.
		if description == "" {
			description = fmt.Sprintf("also executes an unread script: %s", strings.Join(targets, ", "))
		}
	}

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
// is wrapped in "docker exec -w /workspace <container>" with a pid-marker
// wrapper (see wrapSandboxCommand), and the returned follow-up kills the
// in-container process group — call it when the command's context is
// cancelled or times out. The -w /workspace flag ensures the command runs
// in the working directory that was mounted into the container during
// setupSandbox(). The follow-up is nil in host mode.
//
// When running on the host (default), the command executes via "sh -c"
// in odek's current working directory.
func (t *shellTool) buildCmd(ctx context.Context, command string) (*exec.Cmd, func()) {
	if t.containerName != "" {
		argv, followUp := wrapSandboxCommand(t.containerName, command)
		return exec.CommandContext(ctx, "docker", argv...), followUp
	}
	return exec.CommandContext(ctx, "sh", "-c", command), nil
}

// sandboxCmdSeq numbers sandboxed command invocations so each gets a unique
// pid-marker file inside the container.
var sandboxCmdSeq atomic.Uint64

// readViewerCommands are commands whose only effect on a file operand is to
// show its contents. A successful run of one of these marks the operands as
// read for the session read ledger (H-6). Best-effort field parsing: the
// ledger is an approval affordance, not a security boundary — recording a
// false positive would only loosen a gate, never tighten one incorrectly.
var readViewerCommands = map[string]bool{
	"cat": true, "head": true, "tail": true, "less": true, "more": true,
	"bat": true, "zcat": true, "nl": true,
}

func recordViewerReads(cmd string) {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return
	}
	base := filepath.Base(strings.Trim(fields[0], `"'`))
	if !readViewerCommands[base] {
		return
	}
	// Review finding CRIT-001: any pipe or redirect means the model did not
	// see the operand's bytes — `cat payload.sh > run.sh` writes a copy the
	// model never viewed, and `cat big.sh | head -1` shows a prefix. Both
	// must license nothing: recording here would silently defeat the
	// unread-exec gate. Only plain viewer invocations record.
	for _, f := range fields[1:] {
		switch f {
		case "|", ">", ">>", "&>", "&>>", ">&", ">>&", "2>", "2>>", "||", "&&", ";":
			return
		}
		if strings.HasPrefix(f, ">") || strings.HasPrefix(f, "2>") {
			return // attached forms like >file, 2>file
		}
	}
	for _, f := range fields[1:] {
		if f == "" || strings.HasPrefix(f, "-") {
			continue
		}
		if st, err := os.Stat(f); err == nil && !st.IsDir() {
			danger.RecordRead(f)
		}
	}
}

// wrapSandboxCommand builds the "docker exec" argv that runs command inside
// the container with a pid-marker wrapper, plus a follow-up function that
// kills the in-container process group and removes the marker.
//
// Killing the host-side `docker exec` client on timeout/cancel does NOT
// terminate the in-container process — Docker does not propagate the signal
// — so without a follow-up, timed-out commands keep running (CPU/memory,
// half-written files in /workspace) until the container is destroyed.
//
// The wrapper records the container-side pid of the shell in a
// per-invocation pidfile. docker exec processes are their own process-group
// leaders (pgid == pid; verified on alpine and debian images), so
// signalling the negative pid tears down the command and every child it
// forked. Children that call setsid/setpgid themselves escape the group —
// this is best-effort, not a hard guarantee. The command string travels as
// a positional argument ($1), never interpolated into the wrapper, so
// quoting cannot break out of it.
// sandboxKillFollowupTimeout bounds the in-container kill follow-up. It
// runs synchronously after the command's own timeout/cancel already fired;
// without a deadline a hung Docker daemon would wedge the tool call forever
// after its timeout — exactly what the timeout exists to prevent.
const sandboxKillFollowupTimeout = 10 * time.Second

func wrapSandboxCommand(containerName, command string) (argv []string, followUp func()) {
	pidFile := fmt.Sprintf("/tmp/.odek-cmd-%d-%d.pid", os.Getpid(), sandboxCmdSeq.Add(1))
	wrapper := "echo $$ > " + pidFile + "; sh -c \"$1\"; rc=$?; rm -f " + pidFile + "; exit $rc"
	argv = []string{"exec", "-w", "/workspace", containerName, "sh", "-c", wrapper, "odek-cmd", command}
	followUp = func() {
		// Best-effort: the container may already be gone (session cleanup).
		ctx, cancel := context.WithTimeout(context.Background(), sandboxKillFollowupTimeout)
		defer cancel()
		_ = exec.CommandContext(ctx, "docker", "exec", containerName, "sh", "-c",
			"kill -KILL -$(cat "+pidFile+") 2>/dev/null; rm -f "+pidFile).Run()
	}
	return argv, followUp
}
