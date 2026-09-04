package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/sandbox"
	"golang.org/x/term"
)

// projectSandboxApprovalsFile is the persistent store for user-approved
// project-level sandbox configurations. It lives under ~/.odek and is created
// 0600.
const projectSandboxApprovalsFile = "project_sandbox_approvals.json"

// approveProjectSandbox requires explicit operator approval before any
// project-controlled sandbox inputs are applied. This covers two surfaces:
//
//  1. Project-level ./odek.json sandbox knobs (sandbox_env, sandbox_image,
//     sandbox_network, sandbox_volumes) — the C-1 vector where a malicious
//     repo exfiltrates host secrets via ${VAR} interpolation in sandbox_env,
//     pulls an attacker-controlled image, or widens the container's network
//     access.
//  2. An implicit Dockerfile.odek build — docker build executes the
//     repo-controlled Dockerfile's RUN instructions outside the sandbox
//     threat model (default capabilities, full read access to the entire
//     working directory as build context), so merely running odek inside a
//     malicious repository would otherwise grant host-adjacent code
//     execution. The approval is keyed on the Dockerfile content hash, so
//     changing the file invalidates a prior approval.
//
// Approval can be granted in three ways:
//  1. Set ODEK_APPROVE_PROJECT_SANDBOX=1 (useful for CI/non-interactive use).
//  2. Answer the interactive prompt when running on a TTY.
//  3. A prior approval for the same project/sandbox fingerprint is persisted
//     in ~/.odek/project_sandbox_approvals.json.
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
	hasOverride := o.HasEnv || o.HasImage || o.HasNetwork || o.HasVolumes ||
		o.HasUser || o.HasMemory || o.HasCPUs
	dfHash, dfRequired := dockerfileBuildRequirement(resolved)
	if !hasOverride && !dfRequired {
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

	overrideKey := projectSandboxApprovalKey(projectDir, o)
	dfKey := dockerfileApprovalKey(projectDir, dfHash)
	overrideOK := !hasOverride || approved[overrideKey]
	dfOK := !dfRequired || approved[dfKey] || sessionDockerfileApproved(dfKey)
	if overrideOK && dfOK {
		return nil
	}

	if !tty {
		what := fmt.Sprintf("project-level sandbox config in %s", config.ProjectConfigPath())
		switch {
		case !hasOverride:
			what = fmt.Sprintf("%s in the working directory (implicit docker build)", sandbox.DockerfileName)
		case dfRequired:
			what += fmt.Sprintf(" and %s (implicit docker build)", sandbox.DockerfileName)
		}
		return fmt.Errorf(
			"%s requires explicit approval\n"+
				"set ODEK_APPROVE_PROJECT_SANDBOX=1 to approve, or run interactively",
			what,
		)
	}

	reader := bufio.NewReader(stdin)

	fmt.Fprintln(stdout)
	if hasOverride {
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
		if o.HasUser {
			fmt.Fprintf(stdout, "  user:    %s\n", o.User)
		}
		if o.HasMemory {
			fmt.Fprintf(stdout, "  memory:  %s\n", o.Memory)
		}
		if o.HasCPUs {
			fmt.Fprintf(stdout, "  cpus:    %s\n", o.CPUs)
		}
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, "Allowing this means code in the sandbox can read workspace files and,")
		fmt.Fprintln(stdout, "depending on network mode, contact external hosts.")
	}
	if dfRequired {
		fmt.Fprintf(stdout, "WARNING: %s found in the working directory — odek will build a sandbox image from it.\n", sandbox.DockerfileName)
		fmt.Fprintln(stdout, "  docker build executes the Dockerfile's RUN instructions as repo-controlled code")
		fmt.Fprintln(stdout, "  on this host, with the ENTIRE working directory readable as build context.")
		fmt.Fprintln(stdout, "  (Build network is disabled by default; set ODEK_SANDBOX_BUILD_NETWORK=1 to")
		fmt.Fprintln(stdout, "  allow networked builds such as `RUN apk add …`.)")
	}
	fmt.Fprintln(stdout)
	fmt.Fprint(stdout, "Approve? [y = once / t = trust this project / N] ")

	line, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("project sandbox approval: read prompt: %w", err)
	}
	line = strings.ToLower(strings.TrimSpace(line))

	switch line {
	case "y", "yes":
		if dfRequired {
			recordSessionDockerfileApproval(dfKey)
		}
		return nil
	case "t", "trust":
		if hasOverride {
			approved[overrideKey] = true
		}
		if dfRequired {
			approved[dfKey] = true
			recordSessionDockerfileApproval(dfKey)
		}
		if err := saveProjectSandboxApprovals(approved); err != nil {
			return fmt.Errorf("project sandbox approval: save approvals: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("project sandbox config was not approved")
	}
}

// dockerfileBuildRequirement reports whether the resolved config will trigger
// an implicit Dockerfile.odek build: sandbox is active, no explicit image is
// configured (an explicit image makes ResolveImage ignore the Dockerfile),
// and Dockerfile.odek exists in the working directory. The returned hash is
// the SHA-256 of the file content, used to key approvals so that changing
// the Dockerfile invalidates a prior approval.
func dockerfileBuildRequirement(resolved config.ResolvedConfig) (hash string, required bool) {
	if !resolved.Sandbox || resolved.SandboxImage != "" {
		return "", false
	}
	data, err := os.ReadFile(sandbox.DockerfileName)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), true
}

// requireDockerfileBuildApproval enforces the Dockerfile.odek build gate at
// the point of container creation. It is intentionally non-interactive:
// approval must already have been granted — interactively at startup
// (recorded as a session approval), persisted via "trust this project", or
// given via ODEK_APPROVE_PROJECT_SANDBOX=1. Enforcing here (in addition to
// the startup prompt) closes the gap where a Dockerfile appears or changes
// AFTER startup — e.g. a serve-mode sandbox created per WebSocket
// connection, or a resumed session that flips sandbox on — would otherwise
// build unapproved repo-controlled code.
func requireDockerfileBuildApproval() error {
	data, err := os.ReadFile(sandbox.DockerfileName)
	if err != nil {
		return nil // no Dockerfile → ResolveImage falls back to alpine:latest
	}
	if os.Getenv("ODEK_APPROVE_PROJECT_SANDBOX") == "1" {
		return nil
	}

	projectDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("dockerfile build approval: get working directory: %w", err)
	}
	projectDir, err = filepath.Abs(projectDir)
	if err != nil {
		return fmt.Errorf("dockerfile build approval: abs working directory: %w", err)
	}

	sum := sha256.Sum256(data)
	key := dockerfileApprovalKey(projectDir, hex.EncodeToString(sum[:]))
	if sessionDockerfileApproved(key) {
		return nil
	}
	if approved, err := loadProjectSandboxApprovals(); err == nil && approved[key] {
		return nil
	}
	return fmt.Errorf(
		"%s requires explicit approval before odek builds it — docker build executes\n"+
			"repo-controlled RUN instructions with the entire working directory as build context.\n"+
			"Approve interactively at startup, or set ODEK_APPROVE_PROJECT_SANDBOX=1",
		sandbox.DockerfileName,
	)
}

// sessionDockerfileApprovals records Dockerfile content approvals granted
// interactively in this process ("y" = once), so the build-time enforcement
// in setupSandbox can honour an approval that was given seconds earlier
// without persisting it. Keyed the same as the persisted store.
var (
	sessionDockerfileApprovalsMu sync.Mutex
	sessionDockerfileApprovals   = map[string]bool{}
)

func recordSessionDockerfileApproval(key string) {
	sessionDockerfileApprovalsMu.Lock()
	defer sessionDockerfileApprovalsMu.Unlock()
	sessionDockerfileApprovals[key] = true
}

func sessionDockerfileApproved(key string) bool {
	sessionDockerfileApprovalsMu.Lock()
	defer sessionDockerfileApprovalsMu.Unlock()
	return sessionDockerfileApprovals[key]
}

// hashField appends one length-prefixed field to an approval-key hash.
// Bare NUL-concatenation allowed boundary-shifting collisions: a "\x00"
// inside one field could reshape the hash stream into a different,
// attacker-chosen field split, letting a stale persisted approval
// auto-apply to a different config (2026-09 security review, wave B).
// The byte length prefix makes every field self-delimiting.
func hashField(h hash.Hash, tag, value string) {
	fmt.Fprintf(h, "\x00%s:%d:", tag, len(value))
	_, _ = io.WriteString(h, value)
}

// projectSandboxApprovalKey returns a stable key for the persisted approval
// store. A change to the project directory, image, network, env keys OR
// VALUES, volumes, user, memory, or cpus invalidates the prior approval.
func projectSandboxApprovalKey(projectDir string, o config.ProjectSandboxOverride) string {
	h := sha256.New()
	hashField(h, "dir", projectDir)
	hashField(h, "image", o.Image)
	hashField(h, "network", o.Network)
	for _, k := range o.EnvKeys {
		hashField(h, "env", k)
		hashField(h, "envval", o.Env[k])
	}
	for _, v := range o.Volumes {
		hashField(h, "vol", v)
	}
	hashField(h, "user", o.User)
	hashField(h, "memory", o.Memory)
	hashField(h, "cpus", o.CPUs)
	return hex.EncodeToString(h.Sum(nil))
}

// dockerfileApprovalKey returns the persisted-approval key for an implicit
// Dockerfile.odek build. It is keyed on the project directory and the
// Dockerfile content hash, so editing the Dockerfile invalidates the prior
// approval and forces re-review.
func dockerfileApprovalKey(projectDir, contentHash string) string {
	h := sha256.New()
	hashField(h, "kind", "dockerfile")
	hashField(h, "dir", projectDir)
	hashField(h, "content", contentHash)
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
