package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/BackendStack21/kode/internal/danger"
)

// shellTool is kode's built-in tool that lets the agent run shell commands.
//
// This is the only built-in tool — it's enough for reading files, running
// tests, building code, and interacting with git. Additional tools can be
// added by implementing the kode.Tool interface (see README.md#Custom-Tools).
//
// Execution modes:
//
//   - Host mode (default): commands run directly on the host via "sh -c".
//     The agent has the same permissions as the kode process. Use with
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
//     commands in non-sandboxed mode prompt the user for approval via /dev/tty.
type shellTool struct {
	// containerName, when set, routes commands through "docker exec"
	// into this container. Set by setupSandbox() when --sandbox is active.
	// When empty, commands run directly on the host.
	containerName string

	// dangerousConfig controls per-class actions and allow/denylists.
	dangerousConfig danger.DangerousConfig

	// trustedClasses caches user-approved risk classes for this process.
	// Set when user presses T (trust this session) at the prompt.
	trustedClasses map[danger.RiskClass]bool

	// ttyPath is the path to the terminal device for approval prompts.
	// Overridden in tests to mock user input.
	ttyPath string
}

func (t *shellTool) Name() string { return "shell" }

func (t *shellTool) Description() string {
	return `Run a shell command and return its output.
Use for: reading files, listing directories, running tests, building code, and git operations.
In sandbox mode (--sandbox), commands run inside the Docker container with restricted permissions.
In host mode (default), commands run with the same permissions as the kode process.

Risk classes: safe, local_write, system_write, destructive, network_egress, code_execution, install, blocked
High-risk operations may prompt for approval (configurable via dangerous section in kode.json).`
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

	cmd := t.buildCmd(input.Command)

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	output := strings.TrimSpace(outBuf.String())
	if errBuf.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += strings.TrimSpace(errBuf.String())
	}
	if err != nil && output == "" {
		return "", fmt.Errorf("shell: %w", err)
	}
	if output == "" {
		output = "(no output)"
	}
	return output, nil
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

// promptUser opens /dev/tty and asks the user to approve the command.
func (t *shellTool) promptUser(cmd, description string) error {
	cls := danger.Classify(cmd)

	// Check session trust cache
	if t.trustedClasses != nil && t.trustedClasses[cls] {
		return nil
	}

	// Open /dev/tty for interactive approval
	ttyPath := t.ttyPath
	if ttyPath == "" {
		ttyPath = "/dev/tty"
	}
	tty, err := os.OpenFile(ttyPath, os.O_RDWR, 0)
	if err != nil {
		// Non-interactive: use configured fallback
		action := t.dangerousConfig.NonInteractiveAction()
		if action == danger.Deny {
			return fmt.Errorf("operation denied (non-interactive mode): %s", cmd)
		}
		return nil
	}
	defer tty.Close()

	// Build the prompt
	fmt.Fprintf(os.Stderr, "\n⚠️  \033[1mRisk:\033[0m %s\n", cls)
	fmt.Fprintf(os.Stderr, "   \033[1mRun:\033[0m  %s\n", cmd)
	if description != "" {
		fmt.Fprintf(os.Stderr, "   \033[1mWhy:\033[0m  %s\n", description)
	}
	fmt.Fprint(os.Stderr, "\n   [A]pprove  [D]eny  [?] Context  [T]rust session: ")

	// Read a single line of input from the TTY
	reader := bufio.NewReader(tty)
	line, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("approval prompt error: %w", err)
	}
	line = strings.TrimSpace(strings.ToLower(line))

	switch line {
	case "a", "approve":
		return nil
	case "t", "trust":
		// Cache this risk class for the session
		if t.trustedClasses == nil {
			t.trustedClasses = make(map[danger.RiskClass]bool)
		}
		t.trustedClasses[cls] = true
		return nil
	case "?", "context":
		fmt.Fprintf(tty, "\n  Command: %s\n", cmd)
		fmt.Fprintf(tty, "  Risk class: %s\n", cls)
		if description != "" {
			fmt.Fprintf(tty, "  Description: %s\n", description)
		}
		fmt.Fprintf(tty, "  Trust this class: %v\n", t.trustedClasses[cls])
		// Re-prompt
		return t.promptUser(cmd, description)
	default:
		return fmt.Errorf("operation denied by user: %s", cmd)
	}
}

// buildCmd constructs the exec.Cmd for the given shell command.
//
// When sandbox mode is active (containerName is non-empty), the command
// is wrapped in "docker exec -w /workspace <container> sh -c <cmd>".
// The -w /workspace flag ensures the command runs in the working directory
// that was mounted into the container during setupSandbox().
//
// When running on the host (default), the command executes via "sh -c"
// in kode's current working directory.
func (t *shellTool) buildCmd(command string) *exec.Cmd {
	if t.containerName != "" {
		return exec.Command("docker", "exec", "-w", "/workspace", t.containerName, "sh", "-c", command)
	}
	return exec.Command("sh", "-c", command)
}
