package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// dockerAvailable returns true if the docker CLI is installed and the
// daemon is reachable. Tests that exercise real docker invocations skip
// otherwise.
func dockerAvailable() bool {
	if _, err := exec.LookPath("docker"); err != nil {
		return false
	}
	cmd := exec.Command("docker", "info")
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// chdir temporarily changes to dir and restores the original on cleanup.
// Avoids the leak that os.Chdir + defer suffers when tests run in parallel.
func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

// ── ResolveImage ───────────────────────────────────────────────────────

func TestResolveImage_ExplicitImage(t *testing.T) {
	img, err := ResolveImage(Config{Image: "node:20-alpine"})
	if err != nil {
		t.Fatalf("ResolveImage: %v", err)
	}
	if img != "node:20-alpine" {
		t.Errorf("img = %q, want 'node:20-alpine'", img)
	}
}

func TestResolveImage_DefaultsToAlpine(t *testing.T) {
	chdir(t, t.TempDir()) // no Dockerfile.odek here
	img, err := ResolveImage(Config{})
	if err != nil {
		t.Fatalf("ResolveImage: %v", err)
	}
	if img != "alpine:latest" {
		t.Errorf("img = %q, want 'alpine:latest'", img)
	}
}

// ── buildFromDockerfile ────────────────────────────────────────────────

func TestBuildFromDockerfile_FileNotFound(t *testing.T) {
	chdir(t, t.TempDir())
	_, err := buildFromDockerfile()
	if err == nil {
		t.Fatal("expected error when Dockerfile.odek does not exist")
	}
	if !strings.Contains(err.Error(), "Dockerfile.odek") {
		t.Errorf("error should mention Dockerfile.odek: %v", err)
	}
}

func TestBuildFromDockerfile_InvalidDockerfile(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker not available")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile.odek"), []byte("INVALID DOCKERFILE CONTENT $$$"), 0644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	_, err := buildFromDockerfile()
	if err == nil {
		t.Fatal("expected error for invalid Dockerfile")
	}
	if !strings.Contains(err.Error(), "docker build failed") {
		t.Errorf("error should mention docker build failure: %v", err)
	}
}

// ── BuildRunArgs ───────────────────────────────────────────────────────

func TestBuildRunArgs_DefaultsApplied(t *testing.T) {
	args := BuildRunArgs(Config{}, "odek-test", "/workspace", "alpine:latest")
	for _, want := range []string{
		"run", "--rm", "--detach", "--name", "odek-test",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--tmpfs", "/tmp:noexec",
		"alpine:latest", "sleep", "infinity",
	} {
		if !contains(args, want) {
			t.Errorf("BuildRunArgs missing %q\nargs: %v", want, args)
		}
	}
}

func TestBuildRunArgs_HostNetworkForcedToNone(t *testing.T) {
	args := BuildRunArgs(Config{Network: "host"}, "odek-test", "/workspace", "alpine:latest")
	for i, a := range args {
		if a == "--network" && i+1 < len(args) {
			if args[i+1] != "none" {
				t.Errorf("network arg = %q, want 'none' (host must be rejected)", args[i+1])
			}
			return
		}
	}
	t.Error("--network flag not found in args")
}

func TestBuildRunArgs_ReadonlyAppendsRoSuffix(t *testing.T) {
	args := BuildRunArgs(Config{Readonly: true}, "odek-test", "/workspace", "alpine:latest")
	for i, a := range args {
		if a == "-v" && i+1 < len(args) && strings.HasPrefix(args[i+1], "/workspace:") {
			if !strings.HasSuffix(args[i+1], ":ro") {
				t.Errorf("workspace volume = %q, want :ro suffix", args[i+1])
			}
			return
		}
	}
	t.Error("workspace volume mount not found")
}

func TestBuildRunArgs_ForbiddenVolumeMountRejected(t *testing.T) {
	args := BuildRunArgs(Config{
		Volumes: []string{"/etc:/container/etc", "/workspace/extra:/container/extra"},
	}, "odek-test", "/workspace", "alpine:latest")
	for _, a := range args {
		if strings.HasPrefix(a, "/etc:") {
			t.Errorf("forbidden /etc mount should have been rejected, found %q", a)
		}
	}
	if !contains(args, "/workspace/extra:/container/extra") {
		t.Errorf("safe mount %q should have been preserved\nargs: %v", "/workspace/extra:/container/extra", args)
	}
}

// TestBuildRunArgs_InWorkdirMountUnderSystemRootAllowed guards against the
// regression where the broad system-root forbidden prefixes (/home, /root,
// /var, /run) rejected every legitimate in-workdir mount on a typical Linux
// host, where the working directory itself lives under /home/<user>.
func TestBuildRunArgs_InWorkdirMountUnderSystemRootAllowed(t *testing.T) {
	for _, workdir := range []string{"/home/alice/project", "/root/project", "/var/lib/app", "/run/app"} {
		mount := workdir + "/data:/container/data"
		args := BuildRunArgs(Config{Volumes: []string{mount}}, "odek-test", workdir, "alpine:latest")
		if !contains(args, mount) {
			t.Errorf("in-workdir mount under system root should be allowed for workdir %q\nargs: %v", workdir, args)
		}
	}
}

// TestBuildRunArgs_SystemRootMountOutsideWorkdirStillRejected confirms the
// system-root protection still fires when the path is NOT inside the workdir.
func TestBuildRunArgs_SystemRootMountOutsideWorkdirStillRejected(t *testing.T) {
	// workdir is /, so /etc/secret is lexically "under" the workdir and passes
	// confinement, but must still be rejected by the /etc forbidden prefix.
	args := BuildRunArgs(Config{Volumes: []string{"/etc/secret:/container/secret"}}, "odek-test", "/", "alpine:latest")
	for i, a := range args {
		if a == "-v" && i+1 < len(args) && strings.HasPrefix(args[i+1], "/etc/secret:") {
			t.Errorf("mount into /etc should be rejected even when workdir is /, found %q", args[i+1])
		}
	}
}

func TestBuildRunArgs_VolumeOutsideWorkdirRejected(t *testing.T) {
	args := BuildRunArgs(Config{
		Volumes: []string{"/tmp:/container/tmp"},
	}, "odek-test", "/workspace", "alpine:latest")
	for i, a := range args {
		if a == "-v" && i+1 < len(args) && strings.HasPrefix(args[i+1], "/tmp:") {
			t.Errorf("volume outside workdir should have been rejected, found %q", args[i+1])
		}
	}
}

func TestBuildRunArgs_RelativeTraversalRejected(t *testing.T) {
	args := BuildRunArgs(Config{
		Volumes: []string{"../../etc:/container/etc"},
	}, "odek-test", "/workspace", "alpine:latest")
	for i, a := range args {
		if a == "-v" && i+1 < len(args) && strings.Contains(args[i+1], "etc") {
			t.Errorf("relative traversal volume should have been rejected, found %q", args[i+1])
		}
	}
}

func TestBuildRunArgs_DockerSocketRejected(t *testing.T) {
	args := BuildRunArgs(Config{
		Volumes: []string{"/var/run/docker.sock:/var/run/docker.sock"},
	}, "odek-test", "/workspace", "alpine:latest")
	for i, a := range args {
		if a == "-v" && i+1 < len(args) && strings.Contains(args[i+1], "docker.sock") {
			t.Errorf("docker socket mount should have been rejected, found %q", args[i+1])
		}
	}
}

// TestBuildRunArgs_IntermediateSymlinkEscapeRejected verifies that a mount
// whose final component is a regular file (passing the Lstat check) but whose
// parent is a symlink pointing outside the working directory is rejected.
func TestBuildRunArgs_IntermediateSymlinkEscapeRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink tests skipped on windows")
	}
	workdir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	// workdir/link -> outside (a directory symlink that escapes the workdir).
	if err := os.Symlink(outside, filepath.Join(workdir, "link")); err != nil {
		t.Fatal(err)
	}

	mount := filepath.Join(workdir, "link", "secret") + ":/container/secret"
	args := BuildRunArgs(Config{Volumes: []string{mount}}, "odek-test", workdir, "alpine:latest")
	for i, a := range args {
		if a == "-v" && i+1 < len(args) && strings.Contains(args[i+1], "secret") {
			t.Errorf("mount traversing an intermediate symlink out of workdir should be rejected, found %q", args[i+1])
		}
	}
}

func TestBuildRunArgs_RelativeVolumeResolvedUnderWorkdir(t *testing.T) {
	args := BuildRunArgs(Config{
		Volumes: []string{"extra:/container/extra"},
	}, "odek-test", "/workspace", "alpine:latest")
	if !contains(args, "/workspace/extra:/container/extra") {
		t.Errorf("relative volume should be resolved to absolute under workdir\nargs: %v", args)
	}
}

func TestBuildRunArgs_EnvAndResourceLimits(t *testing.T) {
	args := BuildRunArgs(Config{
		Memory: "512m",
		CPUs:   "1.5",
		User:   "1000:1000",
		Env:    map[string]string{"FOO": "bar"},
	}, "odek-test", "/workspace", "alpine:latest")

	for _, want := range []string{"--memory", "512m", "--cpus", "1.5", "--user", "1000:1000", "-e", "FOO=bar"} {
		if !contains(args, want) {
			t.Errorf("missing %q\nargs: %v", want, args)
		}
	}
}

// ── InjectFiles ────────────────────────────────────────────────────────

func TestInjectFiles_NilAndEmptyAreNoOps(t *testing.T) {
	for _, files := range [][]string{nil, {}, {"", " "}} {
		n, err := InjectFiles("any", files, "/tmp")
		if err != nil {
			t.Errorf("InjectFiles(%v) err = %v", files, err)
		}
		if n != 0 {
			t.Errorf("InjectFiles(%v) injected %d, want 0", files, n)
		}
	}
}

func TestInjectFiles_SkipsMissingFile(t *testing.T) {
	dir := t.TempDir()
	n, err := InjectFiles("nonexistent-container", []string{"missing.txt"}, dir)
	if err != nil {
		t.Errorf("missing-file path should warn-and-skip, got err: %v", err)
	}
	if n != 0 {
		t.Errorf("injected = %d, want 0", n)
	}
}

func TestInjectFiles_SkipsDirectoryArg(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "subdir")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	n, err := InjectFiles("nonexistent-container", []string{"subdir"}, dir)
	if err != nil {
		t.Errorf("directory-arg path should warn-and-skip, got err: %v", err)
	}
	if n != 0 {
		t.Errorf("injected = %d, want 0", n)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
