package main

import (
	"errors"
	"fmt"

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
	return fmt.Sprintf("odek: sandbox network=%s pids=%d image=%s", network, pids, danger.SanitizeInline(image))
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
		Image:     resolved.SandboxImage,
		Network:   resolved.SandboxNetwork,
		Readonly:  resolved.SandboxReadonly,
		Memory:    resolved.SandboxMemory,
		CPUs:      resolved.SandboxCPUs,
		PidsLimit: resolved.SandboxPidsLimit,
		User:      resolved.SandboxUser,
		Env:       resolved.SandboxEnv,
		Volumes:   resolved.SandboxVolumes,
	}
}
