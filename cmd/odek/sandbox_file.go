package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// hostToContainerPath translates a host path (relative or absolute) into the
// corresponding path inside a sandbox container. The sandbox mounts the working
// directory at /workspace, so any path under the host working directory becomes
// /workspace/<rel>. Paths outside the working directory are rejected.
func hostToContainerPath(hostPath string) (string, error) {
	if hostPath == "" {
		return "", fmt.Errorf("path is empty")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("cannot determine working directory: %w", err)
	}
	// Resolve symlinks in the working directory so comparisons work on hosts
	// where the cwd is reached through a symlink (e.g. macOS /var -> /private/var).
	evalCwd, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", fmt.Errorf("cannot resolve working directory symlinks: %w", err)
	}
	evalCwd, err = filepath.Abs(evalCwd)
	if err != nil {
		return "", fmt.Errorf("cannot resolve working directory: %w", err)
	}
	evalCwd = filepath.Clean(evalCwd)

	absHost := hostPath
	if !filepath.IsAbs(absHost) {
		absHost = filepath.Join(evalCwd, absHost)
	}
	absHost, err = filepath.Abs(absHost)
	if err != nil {
		return "", fmt.Errorf("cannot resolve path %q: %w", hostPath, err)
	}
	absHost = filepath.Clean(absHost)

	// Resolve symlinks in the host path. If the path does not exist yet,
	// resolve its directory and keep the final component.
	evalHost := absHost
	if ev, err := filepath.EvalSymlinks(absHost); err == nil {
		evalHost = filepath.Clean(ev)
	} else {
		dir := filepath.Dir(absHost)
		base := filepath.Base(absHost)
		if evDir, err := filepath.EvalSymlinks(dir); err == nil {
			evalHost = filepath.Join(evDir, base)
		}
	}

	if evalHost != evalCwd && !strings.HasPrefix(evalHost, evalCwd+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside working directory %q", hostPath, evalCwd)
	}

	rel, err := filepath.Rel(evalCwd, evalHost)
	if err != nil {
		return "", fmt.Errorf("cannot relativise %q: %w", hostPath, err)
	}
	if rel == "." {
		return "/workspace", nil
	}
	return "/workspace/" + filepath.ToSlash(rel), nil
}

// sandboxWriteFile writes data to a path inside a running sandbox container.
// It is used by the file tools when sandbox mode is active so that writes go
// through the container's filesystem view and respect its mount options (e.g.
// a read-only /workspace mount will cause this to fail).
//
// The implementation writes the content to a temporary file on the host and
// then copies it into the container with `docker cp`, creating parent
// directories with `docker exec mkdir -p` first.
func sandboxWriteFile(containerName, hostPath string, data []byte, mode os.FileMode) error {
	containerPath, err := hostToContainerPath(hostPath)
	if err != nil {
		return err
	}

	// Write content to a host temp file. docker cp needs a real file path.
	tmpFile, err := os.CreateTemp("", "odek-sandbox-write-*")
	if err != nil {
		return fmt.Errorf("cannot create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	removeTmp := true
	defer func() {
		if removeTmp {
			os.Remove(tmpPath)
		}
	}()

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return fmt.Errorf("cannot write temp file: %w", err)
	}
	if err := tmpFile.Chmod(mode); err != nil {
		tmpFile.Close()
		return fmt.Errorf("cannot chmod temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("cannot close temp file: %w", err)
	}

	// Ensure parent directories exist inside the container.
	parent := filepath.ToSlash(filepath.Dir(containerPath))
	if parent != "/workspace" && parent != "/" {
		mkdir := exec.Command("docker", "exec", containerName, "mkdir", "-p", parent)
		if out, err := mkdir.CombinedOutput(); err != nil {
			return fmt.Errorf("cannot create parent dir %s in container: %w\n%s", parent, err, string(out))
		}
	}

	// Copy the temp file into the container.
	cp := exec.Command("docker", "cp", tmpPath, containerName+":"+containerPath)
	if out, err := cp.CombinedOutput(); err != nil {
		return fmt.Errorf("cannot copy file to container: %w\n%s", err, string(out))
	}

	removeTmp = false
	os.Remove(tmpPath)
	return nil
}
