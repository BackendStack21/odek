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

	"github.com/BackendStack21/odek/internal/pathutil"
)

// DockerfileName is the project-local Dockerfile name. Presence in the
// working directory triggers a cached image build via buildFromDockerfile.
const DockerfileName = "Dockerfile.odek"

// ForbiddenMountPrefixes lists host paths that volume mounts must never
// touch. A user could accidentally (or, in --task scenarios, be coaxed
// into) requesting one of these and undo the whole point of sandboxing.
// Mounts to these paths are dropped with a stderr warning.
var ForbiddenMountPrefixes = []string{
	"/", "/etc", "/proc", "/sys", "/boot", "/dev",
	"/var", "/run", "/root", "/home",
	"/var/run/docker.sock",
}

// Config is the resolved sandbox configuration for one agent run. All
// fields come from the merged config (files → env → CLI) — this struct
// has no notion of where values were sourced.
type Config struct {
	Image    string            // Docker image (e.g. "node:20-alpine"); empty triggers Dockerfile/default resolution
	Network  string            // "bridge" | "none" | "host"; "host" and any other value are forced to "none"
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
	switch network {
	case "", "bridge":
		// Docker default bridge network; "bridge" is the only non-isolated
		// mode we allow besides "none".
		network = "bridge"
	case "none":
		// Fully isolated mode.
	case "host":
		fmt.Fprintf(os.Stderr, "odek: WARNING: --sandbox-network host destroys container isolation. Forcing 'none'.\n")
		network = "none"
	default:
		// Reject modes such as "container:<name>" that share another
		// container's network namespace and bypass isolation.
		fmt.Fprintf(os.Stderr, "odek: WARNING: --sandbox-network %q is not allowed. Only 'none' and 'bridge' are permitted. Forcing 'none'.\n", network)
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
		sanitized, ok := sanitizeVolumeMount(vol, workdir)
		if !ok {
			continue
		}
		args = append(args, "-v", sanitized)
	}

	args = append(args, image, "sleep", "infinity")
	return args
}

// sanitizeVolumeMount validates and canonicalises a user-supplied docker -v
// string. It returns the canonical mount string and true if the mount is
// allowed, or an empty string and false if it should be dropped.
//
// Security rules:
//   - The host path must be absolute or a local path under workdir.
//   - Any ".." component is rejected.
//   - The resolved host path must be inside workdir.
//   - The resolved host path must not match ForbiddenMountPrefixes.
//   - Symlinks are rejected (they could point outside workdir).
//
// The returned string uses the resolved absolute host path so Docker does not
// interpret a relative path relative to the daemon's working directory.
func sanitizeVolumeMount(vol, workdir string) (string, bool) {
	// Docker volume format: host[:container[:options]]. We only need the host.
	parts := strings.SplitN(vol, ":", 2)
	host := strings.TrimSpace(parts[0])
	if host == "" {
		fmt.Fprintf(os.Stderr, "odek: WARNING: rejecting malformed volume mount %q (empty host path)\n", vol)
		return "", false
	}

	// Reject paths with traversal attempts before resolving them.
	if hasDotDotComponent(host) {
		fmt.Fprintf(os.Stderr, "odek: WARNING: rejecting volume mount %q (contains ..)\n", vol)
		return "", false
	}

	// Resolve to an absolute path. Relative paths are interpreted relative to
	// the current working directory, which should be the project root.
	absHost := host
	if !filepath.IsAbs(absHost) {
		absHost = filepath.Join(workdir, absHost)
	}
	absHost = filepath.Clean(absHost)

	absWorkdir, err := filepath.Abs(workdir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odek: WARNING: rejecting volume mount %q (cannot resolve workdir: %v)\n", vol, err)
		return "", false
	}
	absWorkdir = filepath.Clean(absWorkdir)

	// The host path must stay inside the working directory.
	if !isPathUnder(absHost, absWorkdir) {
		fmt.Fprintf(os.Stderr, "odek: WARNING: rejecting volume mount %q (host path %s is outside working directory %s)\n", vol, absHost, absWorkdir)
		return "", false
	}

	// Reject symlinks — they could escape the working directory even if the
	// link itself is inside it. Lstat only inspects the final component.
	if info, err := os.Lstat(absHost); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			fmt.Fprintf(os.Stderr, "odek: WARNING: rejecting volume mount %q (symlinks are not allowed)\n", vol)
			return "", false
		}
	}

	// Resolve symlinks in the parent chain and re-check containment so an
	// intermediate symlinked directory cannot escape the working directory
	// (e.g. workdir/link -> /etc, requested as workdir/link/passwd: the final
	// component "passwd" is not itself a symlink, so the Lstat check above
	// passes, but the resolved path is outside workdir). Both sides are
	// resolved so platforms where the workdir contains symlinks (macOS
	// /var -> /private/var) compare canonical paths. When the parent does not
	// exist yet, ResolveDirSymlinks returns the original absolute path and the
	// lexical confinement check above remains the guarantee.
	resolvedHost := pathutil.ResolveDirSymlinks(absHost)
	if !pathutil.WithinRoot(absWorkdir, resolvedHost) {
		fmt.Fprintf(os.Stderr, "odek: WARNING: rejecting volume mount %q (resolved host path %s escapes working directory %s)\n", vol, resolvedHost, absWorkdir)
		return "", false
	}

	// Reject forbidden host paths. Skip any forbidden prefix that the working
	// directory itself sits at or under: the confinement check above already
	// bounds the mount to the working directory, and the broad system roots
	// (/home, /root, /var, /run) are the normal parents of a project directory.
	// Without this exemption every legitimate in-workdir mount on a typical
	// Linux host (cwd under /home/<user>) would be rejected, while paths that
	// genuinely escape into a forbidden area are still caught — either by the
	// confinement check above or by a forbidden prefix the workdir is not under.
	//
	// The forbidden-prefix check uses the symlink-resolved host path so it is
	// composed on the same canonical path as the confinement check above.
	sep := string(filepath.Separator)
	for _, forbidden := range ForbiddenMountPrefixes {
		if absWorkdir == forbidden || strings.HasPrefix(absWorkdir, forbidden+sep) {
			continue
		}
		if resolvedHost == forbidden || strings.HasPrefix(resolvedHost, forbidden+sep) {
			fmt.Fprintf(os.Stderr, "odek: WARNING: rejecting forbidden volume mount %q (host path %s)\n", vol, resolvedHost)
			return "", false
		}
	}

	// Rebuild the mount with the canonical absolute host path.
	rest := ""
	if len(parts) == 2 {
		rest = ":" + parts[1]
	}
	return absHost + rest, true
}

// hasDotDotComponent reports whether p contains a ".." path component after
// cleaning. It allows names that merely contain ".." as a substring (e.g.
// "foo..bar") but rejects any traversal attempt.
func hasDotDotComponent(p string) bool {
	clean := filepath.Clean(p)
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if part == ".." {
			return true
		}
	}
	return false
}

// isPathUnder reports whether path is inside root. Both paths must be clean
// absolute paths. A path equal to root is considered under root.
func isPathUnder(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	// filepath.Rel returns paths starting with ".." when path is outside root.
	return !strings.HasPrefix(rel, "..") && !strings.Contains(filepath.ToSlash(rel), "/../")
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
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return 0, fmt.Errorf("resolve cwd: %w", err)
	}
	absCwd = filepath.Clean(absCwd)

	injected := 0
	for _, f := range files {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}

		absPath := f
		if !filepath.IsAbs(absPath) {
			absPath = filepath.Join(absCwd, absPath)
		}
		absPath = filepath.Clean(absPath)

		// Use Lstat (not Stat) so symlinks are not followed. A symlink inside
		// cwd can point at arbitrary host files; following it would copy the
		// target into the container and bypass workspace confinement.
		info, err := os.Lstat(absPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "odek: warning: ctx file %q not found, skipping sandbox injection\n", f)
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			fmt.Fprintf(os.Stderr, "odek: warning: ctx file %q is a symlink, skipping sandbox injection\n", f)
			continue
		}
		if info.IsDir() {
			fmt.Fprintf(os.Stderr, "odek: warning: ctx path %q is a directory, skipping sandbox injection\n", f)
			continue
		}

		dest := filepath.Base(absPath)
		if isPathUnder(absPath, absCwd) {
			if rel, err := filepath.Rel(absCwd, absPath); err == nil {
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
