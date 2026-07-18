package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/BackendStack21/odek/internal/config"
	"golang.org/x/term"
)

// projectSandboxApprovalsFile is the persistent store for user-approved
// project-level sandbox configurations. It lives under ~/.odek and is created
// 0600.
const projectSandboxApprovalsFile = "project_sandbox_approvals.json"

// approveProjectSandbox requires explicit operator approval before any
// project-level ./odek.json sandbox knobs are applied. This closes the C-1
// vector where a malicious repo exfiltrates host secrets via ${VAR}
// interpolation in sandbox_env, pulls an attacker-controlled image, or widens
// the container's network access.
//
// Approval can be granted in three ways:
//   1. Set ODEK_APPROVE_PROJECT_SANDBOX=1 (useful for CI/non-interactive use).
//   2. Answer the interactive prompt when running on a TTY.
//   3. A prior approval for the same project/sandbox fingerprint is persisted
//      in ~/.odek/project_sandbox_approvals.json.
//
// If approval is required and cannot be obtained, approveProjectSandbox
// returns an error and the command should abort before creating the sandbox.
func approveProjectSandbox(resolved config.ResolvedConfig, stdin io.Reader, stdout io.Writer) error {
	isTTY := stdin == os.Stdin && term.IsTerminal(int(os.Stdin.Fd()))
	return approveProjectSandboxWithTTY(resolved, stdin, stdout, isTTY)
}

// approveProjectSandboxWithTTY is the testable core of approveProjectSandbox.
func approveProjectSandboxWithTTY(resolved config.ResolvedConfig, stdin io.Reader, stdout io.Writer, tty bool) error {
	o := resolved.ProjectSandboxOverride
	if !o.HasEnv && !o.HasImage && !o.HasNetwork && !o.HasVolumes {
		return nil
	}

	if os.Getenv("ODEK_APPROVE_PROJECT_SANDBOX") == "1" {
		return nil
	}

	projectDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("project sandbox approval: get working directory: %w", err)
	}
	projectDir, err = filepath.Abs(projectDir)
	if err != nil {
		return fmt.Errorf("project sandbox approval: abs working directory: %w", err)
	}

	approved, err := loadProjectSandboxApprovals()
	if err != nil {
		return fmt.Errorf("project sandbox approval: load approvals: %w", err)
	}

	key := projectSandboxApprovalKey(projectDir, o)
	if approved[key] {
		return nil
	}

	if !tty {
		return fmt.Errorf(
			"project-level sandbox config in %s requires explicit approval\n"+
				"set ODEK_APPROVE_PROJECT_SANDBOX=1 to approve, or run interactively",
			config.ProjectConfigPath(),
		)
	}

	reader := bufio.NewReader(stdin)

	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "WARNING: project config (%s) requests sandbox overrides:\n", config.ProjectConfigPath())
	if o.HasImage {
		fmt.Fprintf(stdout, "  image:   %s\n", o.Image)
	}
	if o.HasNetwork {
		fmt.Fprintf(stdout, "  network: %s\n", o.Network)
	}
	if o.HasEnv {
		fmt.Fprintf(stdout, "  env:     %s\n", strings.Join(o.EnvKeys, ", "))
		if o.EnvHasInterpolation {
			fmt.Fprintln(stdout, "  ⚠️  sandbox_env values contain ${...} interpolation against host environment variables")
		}
	}
	if o.HasVolumes {
		fmt.Fprintf(stdout, "  volumes: %s\n", strings.Join(o.Volumes, ", "))
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Allowing this means code in the sandbox can read workspace files and,")
	fmt.Fprintln(stdout, "depending on network mode, contact external hosts.")
	fmt.Fprintln(stdout)
	fmt.Fprint(stdout, "Approve? [y = once / t = trust this project / N] ")

	line, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("project sandbox approval: read prompt: %w", err)
	}
	line = strings.ToLower(strings.TrimSpace(line))

	switch line {
	case "y", "yes":
		return nil
	case "t", "trust":
		approved[key] = true
		if err := saveProjectSandboxApprovals(approved); err != nil {
			return fmt.Errorf("project sandbox approval: save approvals: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("project sandbox config was not approved")
	}
}

// projectSandboxApprovalKey returns a stable key for the persisted approval
// store. A change to the project directory, image, network, env keys, or
// volumes invalidates the prior approval.
func projectSandboxApprovalKey(projectDir string, o config.ProjectSandboxOverride) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s", projectDir, o.Image, o.Network)
	for _, k := range o.EnvKeys {
		fmt.Fprintf(h, "\x00env:%s", k)
	}
	for _, v := range o.Volumes {
		fmt.Fprintf(h, "\x00vol:%s", v)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// loadProjectSandboxApprovals reads the persisted approval map. A missing file
// is treated as an empty approval set.
func loadProjectSandboxApprovals() (map[string]bool, error) {
	path := filepath.Join(expandHome("~/.odek"), projectSandboxApprovalsFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]bool), nil
		}
		return nil, err
	}

	var approvals map[string]bool
	if err := json.Unmarshal(data, &approvals); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if approvals == nil {
		approvals = make(map[string]bool)
	}
	return approvals, nil
}

// saveProjectSandboxApprovals writes the approval map to disk with 0600
// permissions.
func saveProjectSandboxApprovals(approvals map[string]bool) error {
	dir := expandHome("~/.odek")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, projectSandboxApprovalsFile)
	data, err := json.MarshalIndent(approvals, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}
