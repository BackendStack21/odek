package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/sandbox"
)

// errSandboxConfig marks a sandbox failure caused by configuration (as
// opposed to Docker being unavailable). It must never degrade to unsandboxed
// host execution.
var errSandboxConfig = errors.New("sandbox configuration error")

// setupSandboxFn is the sandbox starter used by ensureSandbox; tests replace
// it to inject setup outcomes.
var setupSandboxFn = setupSandbox

// sandboxPostureLine renders the one-line startup summary of the effective
// sandbox posture from validated values.
func sandboxPostureLine(cfg sandboxConfig, image string) string {
	network, err := sandbox.ValidateNetwork(cfg.Network)
	if err != nil {
		network = "none"
	}
	pids, err := sandbox.ParsePidsLimit(cfg.PidsLimit)
	if err != nil {
		pids = sandbox.DefaultPidsLimit
	}
	// Attached networks replace "none", so the line names what the
	// container actually has.
	if len(cfg.Networks) > 0 || len(cfg.ResolvedNetworks) > 0 {
		if network == "bridge" {
			network = "bridge+attached"
		} else {
			network = "attached"
		}
	}
	line := fmt.Sprintf("odek: sandbox network=%s pids=%d image=%s", network, pids, danger.SanitizeInline(image))
	if len(cfg.Networks) > 0 {
		line += " networks=" + danger.SanitizeInline(strings.Join(cfg.Networks, ","))
	}
	return line
}

// sandboxPostureView reports the effective sandbox network, pids limit and
// image for config_view and the diagnostics surface.
func sandboxPostureView(resolved config.ResolvedConfig) map[string]any {
	network, err := sandbox.ValidateNetwork(resolved.SandboxNetwork)
	if err != nil {
		network = resolved.SandboxNetwork
	}
	pids, err := sandbox.ParsePidsLimit(resolved.SandboxPidsLimit)
	if err != nil {
		pids = sandbox.DefaultPidsLimit
	}
	return map[string]any{"network": network, "pids_limit": pids, "image": resolved.SandboxImage}
}

// sandboxConfigFromResolved builds the container settings for one run from
// the merged configuration. Every surface (run, continue, repl, serve,
// sub-agents, mcp) starts its sandbox from this single mapping.
func sandboxConfigFromResolved(resolved config.ResolvedConfig) sandboxConfig {
	return sandboxConfig{
		Image:          resolved.SandboxImage,
		Network:        resolved.SandboxNetwork,
		Readonly:       resolved.SandboxReadonly,
		Memory:         resolved.SandboxMemory,
		CPUs:           resolved.SandboxCPUs,
		PidsLimit:      resolved.SandboxPidsLimit,
		Networks:       resolved.SandboxNetworks,
		ComposeProject: resolved.SandboxComposeProject,
		User:           resolved.SandboxUser,
		Env:            resolved.SandboxEnv,
		Volumes:        resolved.SandboxVolumes,
	}
}

// dockerReachable reports whether the Docker daemon answers. It is a
// variable so tests can decide the outcome without a daemon.
var dockerReachable = func() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}").Run() == nil
}

// sandboxStartError classifies a failure to resolve the image or start the
// container. When the daemon answers, the failure came from the
// configuration (image, limits, user, networks), so it is marked as a
// configuration error and never falls back to host execution; only an
// unreachable daemon keeps the error as "Docker unavailable".
func sandboxStartError(err error) error {
	if errors.Is(err, errSandboxConfig) || !dockerReachable() {
		return err
	}
	return fmt.Errorf("%w: %w", errSandboxConfig, err)
}
