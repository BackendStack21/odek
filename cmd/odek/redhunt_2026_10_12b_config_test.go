package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/config"
)

// A staging directory that is itself a symlink (the child, even inside a
// sandbox, can plant one in the bind-mounted workspace) must never make the
// trusted host-side runner move host files into the artifact channel.
func TestRED_RelocateStagingArtifacts_SymlinkedStagingDirMovesHostFiles(t *testing.T) {
	root := t.TempDir()
	hostSecrets := filepath.Join(root, "host-ssh")
	if err := os.MkdirAll(hostSecrets, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(hostSecrets, "id_rsa")
	if err := os.WriteFile(secret, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "ws")
	if err := os.MkdirAll(filepath.Join(workspace, stagingDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	staging := stagingDirFor(workspace, "task-1")
	if err := os.Symlink(hostSecrets, staging); err != nil {
		t.Fatal(err)
	}
	canonical := filepath.Join(root, "canonical", "task-1")

	moved, _ := relocateStagingArtifacts(staging, canonical)

	if _, err := os.Stat(secret); err != nil {
		t.Errorf("host file outside the workspace was moved away by relocation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(canonical, "id_rsa")); err == nil {
		t.Errorf("host file reached through a symlinked staging dir was delivered as an artifact (moved=%d)", moved)
	}
}

// Same, with the staging ROOT (.odek-artifacts) replaced by a symlink.
func TestRED_RelocateStagingArtifacts_SymlinkedStagingRootMovesHostFiles(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "host-dir")
	if err := os.MkdirAll(filepath.Join(outside, "task-2"), 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "task-2", "credentials.json")
	if err := os.WriteFile(secret, []byte(`{"token":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "ws")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, stagingDirName)); err != nil {
		t.Fatal(err)
	}
	canonical := filepath.Join(root, "canonical", "task-2")

	moved, _ := relocateStagingArtifacts(stagingDirFor(workspace, "task-2"), canonical)

	if _, err := os.Stat(secret); err != nil {
		t.Errorf("file outside the workspace was moved away by relocation: %v", err)
	}
	if moved != 0 {
		t.Errorf("relocation followed a symlinked staging root out of the workspace (moved=%d)", moved)
	}
}

// A cloned repository can commit `.odek-artifacts` as a symlink (e.g. to
// ".."). Every artifact-bearing sub-agent run calls sweepStagingOrphans at
// start, which ReadDirs through the symlink and RemoveAlls every directory
// older than 24h — deleting the user's sibling projects (or whatever the
// link targets) wholesale.
func TestRED_SweepStagingOrphans_SymlinkedRootDeletesOutsideDirs(t *testing.T) {
	parent := t.TempDir()
	victim := filepath.Join(parent, "other-project")
	if err := os.MkdirAll(filepath.Join(victim, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "src", "main.go"), []byte("package main"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(victim, old, old); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(parent, "malicious-repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	// Committed in the repo: .odek-artifacts -> ..
	if err := os.Symlink("..", filepath.Join(repo, stagingDirName)); err != nil {
		t.Fatal(err)
	}

	removed := sweepStagingOrphans(repo, "task-current", stagingSweepMaxAge)

	if _, err := os.Stat(filepath.Join(victim, "src", "main.go")); err != nil {
		t.Errorf("staging sweep followed a repo-planted symlink and deleted a directory outside the workspace (removed=%d): %v", removed, err)
	}
}

// ensureStagingRoot writes "*\n!.gitignore\n" into <cwd>/.odek-artifacts/
// .gitignore, following a repo-planted symlinked root: another repository's
// .gitignore is overwritten so that everything in it becomes ignored.
func TestRED_EnsureStagingRoot_SymlinkedRootOverwritesOutsideGitignore(t *testing.T) {
	parent := t.TempDir()
	other := filepath.Join(parent, "other-project")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	const orig = "node_modules/\n"
	if err := os.WriteFile(filepath.Join(other, ".gitignore"), []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(parent, "malicious-repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "other-project"), filepath.Join(repo, stagingDirName)); err != nil {
		t.Fatal(err)
	}

	ensureStagingRoot(repo)

	got, _ := os.ReadFile(filepath.Join(other, ".gitignore"))
	if string(got) != orig {
		t.Errorf("ensureStagingRoot wrote through a symlinked staging root outside the workspace: other-project/.gitignore = %q", got)
	}
}

// confighuntWriteEnvDumpChild writes a fake odek binary that records its
// environment and task file and returns a successful result.
func confighuntWriteEnvDumpChild(t *testing.T, dir string) (fake, dump string) {
	t.Helper()
	dump = filepath.Join(dir, "env.txt")
	fake = filepath.Join(dir, "fake-odek")
	task := filepath.Join(dir, "task.json")
	script := fmt.Sprintf("#!/bin/sh\nenv > %q\nwhile [ \"$#\" -gt 0 ]; do if [ \"$1\" = \"--task\" ]; then shift; cp \"$1\" %q; fi; shift; done\nprintf '{\"status\":\"success\",\"summary\":\"ok\",\"iterations\":1,\"tokens_used\":1}\\n'\n", dump, task)
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return fake, dump
}

// confighuntApplyEnvDump sets every ODEK_* variable the fake child saw.
func confighuntApplyEnvDump(t *testing.T, dump string) {
	t.Helper()
	f, err := os.Open(dump)
	if err != nil {
		t.Fatalf("fake child did not run: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if ok && strings.HasPrefix(k, "ODEK_") {
			t.Setenv(k, v)
		}
	}
}

// confighuntTaskSpec decodes the task file the fake child received.
func confighuntTaskSpec(t *testing.T, dump string) taskFileSpec {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(filepath.Dir(dump), "task.json"))
	if err != nil {
		t.Fatalf("fake child did not receive a task file: %v", err)
	}
	spec, err := decodeTaskFileSpec(data)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

// A parent started with `--sandbox --sandbox-readonly` (or another CLI
// sandbox knob) spawns sandboxed children, but the CLI-only knobs never
// reached them: the child's own config resolution never saw the read-only
// constraint, so its container mounted the workspace read-write.
func TestRED_SubagentChild_InheritsParentSandboxReadonly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	ro := true
	sb := true
	parent := config.LoadConfig(config.CLIFlags{Sandbox: &sb, SandboxReadonly: &ro})
	if !parent.SandboxReadonly {
		t.Fatal("precondition: parent resolved sandbox_readonly=false")
	}

	fake, dump := confighuntWriteEnvDumpChild(t, t.TempDir())
	tools := builtinTools(parent.Dangerous, nil, nil, parent.MaxConcurrency, "", toolConfigFromResolved(parent), nil)
	applySandboxToolBindings(tools, "odek-test-container")
	recordChildSandbox(tools, sandboxConfig{Readonly: true, Image: "img:1", Network: "none", Memory: "256m", CPUs: "1", User: "1000:1000"})
	var dt *delegateTasksTool
	for _, tl := range tools {
		if d, ok := tl.(*delegateTasksTool); ok {
			dt = d
		}
	}
	if dt == nil {
		t.Fatal("delegate_tasks tool not registered")
	}
	dt.odekPath = fake
	dt.timeout = 30 * time.Second
	dt.SetContext(context.Background())
	if _, err := dt.Call(`{"tasks":[{"goal":"edit nothing"}]}`); err != nil {
		t.Fatal(err)
	}

	// Resolve the child's config exactly as `odek subagent` does.
	confighuntApplyEnvDump(t, dump)
	child := config.LoadConfig(config.CLIFlags{})
	if !child.Sandbox {
		t.Fatal("precondition: child is not sandboxed")
	}
	applyTaskSandboxPosture(&child, confighuntTaskSpec(t, dump).Sandbox)
	if !child.SandboxReadonly {
		t.Errorf("sandboxed child resolves sandbox_readonly=false although the parent ran --sandbox-readonly: the child container mounts the workspace read-write")
	}
	if child.SandboxImage != "img:1" || child.SandboxNetwork != "none" || child.SandboxMemory != "256m" || child.SandboxCPUs != "1" || child.SandboxUser != "1000:1000" {
		t.Errorf("child sandbox knobs not inherited: %+v", child)
	}
}

// `odek run --no-tool shell --no-tool write_file "review this code"`
// (docs/CONFIG.md) removes shell and write_file from the parent, but
// delegate_tasks children resolve their own config from env/files only: the
// CLI tool filter never reached them, so the model (or an injection it
// ingested) regained the disabled tools by delegating.
func TestRED_SubagentChild_InheritsParentCLINoToolFilter(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	parent := config.LoadConfig(config.CLIFlags{ToolsDisabled: []string{"shell", "write_file"}})

	fake, dump := confighuntWriteEnvDumpChild(t, t.TempDir())
	tools := builtinTools(parent.Dangerous, nil, nil, parent.MaxConcurrency, "", toolConfigFromResolved(parent), nil)
	tools = filterBuiltinTools(tools, parent.Tools, nil)
	var dt *delegateTasksTool
	for _, tl := range tools {
		if d, ok := tl.(*delegateTasksTool); ok {
			dt = d
		}
		if tl.Name() == "shell" || tl.Name() == "write_file" {
			t.Fatalf("precondition: parent still has %s", tl.Name())
		}
	}
	if dt == nil {
		t.Fatal("delegate_tasks tool not registered")
	}
	dt.odekPath = fake
	dt.timeout = 30 * time.Second
	dt.SetContext(context.Background())
	if _, err := dt.Call(`{"tasks":[{"goal":"review this code"}]}`); err != nil {
		t.Fatal(err)
	}

	confighuntApplyEnvDump(t, dump)
	child := config.LoadConfig(config.CLIFlags{})
	childTools := filterBuiltinTools(builtinTools(child.Dangerous, nil, nil, child.MaxConcurrency, "", toolConfigFromResolved(child), nil), child.Tools, nil)
	pf, ok := confighuntTaskSpec(t, dump).Tools.filter()
	if !ok {
		t.Fatal("task file carries no parent tool filter")
	}
	childTools = filterBuiltinTools(childTools, pf, nil)
	for _, tl := range childTools {
		if tl.Name() == "shell" || tl.Name() == "write_file" {
			t.Errorf("sub-agent of a `--no-tool shell --no-tool write_file` parent still gets the %s tool", tl.Name())
		}
	}
	if len(childTools) == 0 {
		t.Error("child lost every tool")
	}
}

// A sandboxed parent whose Dockerfile.odek build was approved once ("y" at
// the startup prompt) spawns children with ODEK_SANDBOX=true. The child is a
// separate non-interactive process, the session approval lives only in the
// parent's memory, and so every child's sandbox setup is refused.
func TestRED_SubagentChild_DockerfileApprovedOnceStillBuildable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODEK_APPROVE_PROJECT_SANDBOX", "")
	work := t.TempDir()
	t.Chdir(work)
	if err := os.WriteFile(filepath.Join(work, "Dockerfile.odek"), []byte("FROM alpine:3.19\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sb := true
	parent := config.LoadConfig(config.CLIFlags{Sandbox: &sb})
	// Parent: the operator answers "y" (approve once).
	if err := approveProjectSandboxWithTTY(parent, strings.NewReader("y\n"), &strings.Builder{}, true); err != nil {
		t.Fatalf("parent approval: %v", err)
	}
	if err := requireDockerfileBuildApproval(); err != nil {
		t.Fatalf("precondition: parent cannot build after approving: %v", err)
	}

	fake, dump := confighuntWriteEnvDumpChild(t, t.TempDir())
	tools := builtinTools(parent.Dangerous, nil, nil, parent.MaxConcurrency, "", toolConfigFromResolved(parent), nil)
	applySandboxToolBindings(tools, "odek-test-container")
	var dt *delegateTasksTool
	for _, tl := range tools {
		if d, ok := tl.(*delegateTasksTool); ok {
			dt = d
		}
	}
	dt.odekPath = fake
	dt.timeout = 30 * time.Second
	dt.SetContext(context.Background())
	if _, err := dt.Call(`{"tasks":[{"goal":"a"}]}`); err != nil {
		t.Fatal(err)
	}

	// Child process: fresh in-memory approvals, its environment, no TTY.
	sessionDockerfileApprovalsMu.Lock()
	saved := sessionDockerfileApprovals
	sessionDockerfileApprovals = map[string]bool{}
	sessionDockerfileApprovalsMu.Unlock()
	t.Cleanup(func() {
		sessionDockerfileApprovalsMu.Lock()
		sessionDockerfileApprovals = saved
		sessionDockerfileApprovalsMu.Unlock()
	})
	confighuntApplyEnvDump(t, dump)
	child := config.LoadConfig(config.CLIFlags{})
	applyTaskSandboxPosture(&child, confighuntTaskSpec(t, dump).Sandbox)
	if err := approveProjectSandboxWithTTY(child, strings.NewReader(""), &strings.Builder{}, false); err != nil {
		t.Errorf("sandboxed child of an approved parent fails its startup gate: %v", err)
	}
	if err := requireDockerfileBuildApproval(); err != nil {
		t.Errorf("sandboxed child of an approved parent cannot build its sandbox: %v", err)
	}
}

// A staging task dir that is a symlink to another directory INSIDE the
// workspace is refused too, and a symlinked sibling is never swept.
func TestRED_Staging_InWorkspaceSymlinksRefused(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, stagingDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(ws, "src")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "main.go"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(ws, stagingDirName, "task-9")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-72 * time.Hour)
	_ = os.Chtimes(real, old, old)
	canonical := filepath.Join(t.TempDir(), "c")
	if n, err := relocateStagingArtifacts(link, canonical); n != 0 || err == nil {
		t.Errorf("symlinked task dir must be refused: moved=%d err=%v", n, err)
	}
	if sweepStagingOrphans(ws, "other", stagingSweepMaxAge) != 0 {
		t.Error("symlinked staging entry must not be swept")
	}
	if _, err := os.Stat(filepath.Join(real, "main.go")); err != nil {
		t.Errorf("workspace file touched: %v", err)
	}
}

// Approval keys handed to a child are bound to the Dockerfile content hash:
// a different Dockerfile in the child's workspace is still refused.
func TestRED_SubagentChild_GrantedApprovalBoundToDockerfileContent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODEK_APPROVE_PROJECT_SANDBOX", "")
	work := t.TempDir()
	t.Chdir(work)
	if err := os.WriteFile(filepath.Join(work, "Dockerfile.odek"), []byte("FROM alpine:3.19\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionDockerfileApprovalsMu.Lock()
	saved := sessionDockerfileApprovals
	sessionDockerfileApprovals = map[string]bool{}
	sessionDockerfileApprovalsMu.Unlock()
	t.Cleanup(func() {
		sessionDockerfileApprovalsMu.Lock()
		sessionDockerfileApprovals = saved
		sessionDockerfileApprovalsMu.Unlock()
	})
	cwd, _ := os.Getwd()
	cwd, _ = filepath.Abs(cwd)
	sum := sha256.Sum256([]byte("FROM alpine:3.19\n"))
	key := dockerfileApprovalKey(cwd, hex.EncodeToString(sum[:]))
	var child config.ResolvedConfig
	applyTaskSandboxPosture(&child, &taskSandboxPosture{ApprovalKeys: []string{key, "not-a-key"}})
	if err := requireDockerfileBuildApproval(); err != nil {
		t.Fatalf("granted key for the same content must be honoured: %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, "Dockerfile.odek"), []byte("FROM evil\nRUN id\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := requireDockerfileBuildApproval(); err == nil {
		t.Error("a changed Dockerfile must not be covered by the parent's granted key")
	}
}

// confighuntResetSessionApprovals gives a test an empty in-memory approval
// set and restores the previous one afterwards.
func confighuntResetSessionApprovals(t *testing.T) {
	t.Helper()
	sessionDockerfileApprovalsMu.Lock()
	saved := sessionDockerfileApprovals
	sessionDockerfileApprovals = map[string]bool{}
	sessionDockerfileApprovalsMu.Unlock()
	t.Cleanup(func() {
		sessionDockerfileApprovalsMu.Lock()
		sessionDockerfileApprovals = saved
		sessionDockerfileApprovalsMu.Unlock()
	})
}

// The approval prompt names the interpolating keys with their templates
// (never the expanded host values).
func TestRED_ApprovalPromptListsInterpolatingEnv(t *testing.T) {
	confighuntResetSessionApprovals(t)
	resolved := config.ResolvedConfig{
		ProjectSandboxOverride: config.ProjectSandboxOverride{
			HasEnv: true, EnvKeys: []string{"PLAIN", "TOKEN"},
			Env:                 map[string]string{"PLAIN": "1", "TOKEN": "$OPENAI_API_KEY"},
			EnvHasInterpolation: true,
		},
	}
	var out strings.Builder
	_ = approveProjectSandboxWithTTY(resolved, strings.NewReader("\n"), &out, true)
	if !strings.Contains(out.String(), "TOKEN = $OPENAI_API_KEY") {
		t.Errorf("prompt does not show the interpolating key: %q", out.String())
	}
	if strings.Contains(out.String(), "PLAIN =") {
		t.Errorf("non-interpolating key listed: %q", out.String())
	}
}
