// Package sandbox builds and operates the Docker container that isolates
// the agent's shell and file-tool execution from the host.
//
// The package is intentionally narrow: it owns the container lifecycle
// inputs (image resolution, "docker run" argument construction, file
// injection) but does not know about agent tools. The cmd/odek wrapper
// composes these helpers with the tool registry — keeping the
// shellTool / parallelShellTool wiring out of this package preserves the
// "no agent tool internals" boundary.
//
// See docs/SANDBOXING.md for the user-facing security model.
package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DockerfileName is the project-local Dockerfile name. Presence in the
// working directory triggers a cached image build via buildFromDockerfile.
const DockerfileName = "Dockerfile.odek"

// ForbiddenMountPrefixes lists host paths that volume mounts must never
// touch. A user could accidentally (or, in --task scenarios, be coaxed
// into) requesting one of these and undo the whole point of sandboxing.
// Mounts to these paths are dropped with a stderr warning.
var ForbiddenMountPrefixes = []string{"/", "/etc", "/proc", "/sys", "/boot", "/dev"}

// Config is the resolved sandbox configuration for one agent run. All
// fields come from the merged config (files → env → CLI) — this struct
// has no notion of where values were sourced.
type Config struct {
	Image    string            // Docker image (e.g. "node:20-alpine"); empty triggers Dockerfile/default resolution
	Network  string            // "bridge" | "none" | "host" — "host" is rejected and forced to "none"
	Readonly bool              // Mount the working directory read-only
	Memory   string            // Memory limit (e.g. "512m", "2g")
	CPUs     string            // CPU limit (e.g. "0.5", "2")
	User     string            // Container user (e.g. "1000:1000")
	Env      map[string]string // Extra environment variables
	Volumes  []string          // Extra volume mounts (filtered against ForbiddenMountPrefixes)
}

// ResolveImage determines the Docker image for a sandbox container.
// Resolution order:
//  1. cfg.Image is explicit → return it unchanged.
//  2. Dockerfile.odek exists in CWD → build (or reuse cached) image.
//  3. Otherwise → "alpine:latest".
func ResolveImage(cfg Config) (string, error) {
	if cfg.Image != "" {
		return cfg.Image, nil
	}
	if _, err := os.Stat(DockerfileName); err == nil {
		return buildFromDockerfile()
	}
	return "alpine:latest", nil
}

// buildFromDockerfile builds (or reuses a cached) image from Dockerfile.odek.
// The tag is "odek-sandbox:<sha256[:12]>" so the cache key is content-addressed:
// changing the Dockerfile produces a new tag, identical content reuses the
// existing image instantly. Build output is piped to stderr so the user
// sees progress on first build.
func buildFromDockerfile() (string, error) {
	data, err := os.ReadFile(DockerfileName)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", DockerfileName, err)
	}
	hash := sha256.Sum256(data)
	tag := "odek-sandbox:" + hex.EncodeToString(hash[:12])

	if _, err := exec.Command("docker", "image", "inspect", tag).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "odek: building sandbox image from %s...\n", DockerfileName)
		build := exec.Command("docker", "build", "-t", tag, "-f", DockerfileName, ".")
		build.Stderr = os.Stderr
		build.Stdout = os.Stderr
		if err := build.Run(); err != nil {
			return "", fmt.Errorf("docker build failed: %w", err)
		}
		fmt.Fprintf(os.Stderr, "odek: built image %s\n", tag)
	}
	return tag, nil
}

// BuildRunArgs returns the "docker run" argument slice for the given
// config — it does not execute Docker. Caller is expected to pass the
// already-resolved image (ResolveImage) so the same argument list can be
// regenerated for diagnostics without re-running the resolution.
//
// Security defaults applied unconditionally:
//
//	--rm, --detach, --cap-drop ALL, --security-opt no-new-privileges,
//	--tmpfs /tmp:noexec. Volume mounts are filtered against
//	ForbiddenMountPrefixes; "host" network is forced to "none".
func BuildRunArgs(cfg Config, containerName, workdir, image string) []string {
	args := []string{
		"run",
		"--rm",
		"--detach",
		"--name", containerName,
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
	}

	network := cfg.Network
	if network == "host" {
		fmt.Fprintf(os.Stderr, "odek: WARNING: --sandbox-network host destroys container isolation. Forcing 'none'.\n")
		network = "none"
	}
	args = append(args, "--network", network)

	volume := workdir + ":/workspace"
	if cfg.Readonly {
		volume += ":ro"
	}
	args = append(args, "-v", volume)
	args = append(args, "--tmpfs", "/tmp:noexec")

	if cfg.Memory != "" {
		args = append(args, "--memory", cfg.Memory)
	}
	if cfg.CPUs != "" {
		args = append(args, "--cpus", cfg.CPUs)
	}
	if cfg.User != "" {
		args = append(args, "--user", cfg.User)
	}

	for k, v := range cfg.Env {
		args = append(args, "-e", k+"="+v)
	}

	for _, vol := range cfg.Volumes {
		reject := false
		parts := strings.SplitN(vol, ":", 2)
		if len(parts) > 0 {
			hostPath := filepath.Clean(parts[0])
			for _, forbidden := range ForbiddenMountPrefixes {
				if hostPath == forbidden || strings.HasPrefix(hostPath, forbidden+"/") {
					fmt.Fprintf(os.Stderr, "odek: WARNING: rejecting forbidden volume mount %q (host path %s)\n", vol, hostPath)
					reject = true
					break
				}
			}
		}
		if !reject {
			args = append(args, "-v", vol)
		}
	}

	args = append(args, image, "sleep", "infinity")
	return args
}

// InjectFiles copies each file under cwd into a running container via
// `docker cp`. Files inside cwd preserve their relative path; absolute
// paths outside cwd are placed by basename at /workspace/. Nested paths
// trigger an in-container `mkdir -p` for the parent so docker cp succeeds.
//
// Returns the number of files actually injected. Missing files are
// skipped with a stderr warning (not errors), since --ctx is best-effort.
// Returns an error only when Docker itself fails.
func InjectFiles(containerName string, files []string, cwd string) (int, error) {
	injected := 0
	for _, f := range files {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}

		absPath := f
		if !filepath.IsAbs(absPath) {
			absPath = filepath.Join(cwd, absPath)
		}
		absPath = filepath.Clean(absPath)

		info, err := os.Stat(absPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "odek: warning: ctx file %q not found, skipping sandbox injection\n", f)
			continue
		}
		if info.IsDir() {
			fmt.Fprintf(os.Stderr, "odek: warning: ctx path %q is a directory, skipping sandbox injection\n", f)
			continue
		}

		dest := filepath.Base(absPath)
		if strings.HasPrefix(absPath, cwd+string(filepath.Separator)) || absPath == cwd {
			if rel, err := filepath.Rel(cwd, absPath); err == nil {
				dest = rel
			}
		}

		// Containers are always Linux; flip to forward slashes for the
		// in-container path so Windows hosts producing "subdir\nested.txt"
		// still write to /workspace/subdir/nested.txt.
		destSlash := filepath.ToSlash(dest)
		if parent := filepath.ToSlash(filepath.Dir(destSlash)); parent != "." && parent != "/" {
			mkdir := exec.Command("docker", "exec", containerName,
				"mkdir", "-p", "/workspace/"+parent)
			if output, err := mkdir.CombinedOutput(); err != nil {
				return injected, fmt.Errorf("mkdir -p /workspace/%s: %w\n%s", parent, err, string(output))
			}
		}

		containerDest := fmt.Sprintf("%s:/workspace/%s", containerName, destSlash)
		cpCmd := exec.Command("docker", "cp", absPath, containerDest)
		if output, err := cpCmd.CombinedOutput(); err != nil {
			return injected, fmt.Errorf("docker cp %q: %w\n%s", f, err, string(output))
		}
		injected++
	}
	return injected, nil
}
