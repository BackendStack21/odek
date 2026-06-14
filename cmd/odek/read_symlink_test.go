package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
)

// symlinkSensitiveDir returns a directory under $HOME that danger.ClassifyPath
// treats as system_write. Tests use this as the traversal target so that a
// deny policy for system_write can detect whether the read tool resolved the
// intermediate symlink directory.
func symlinkSensitiveDir(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("get home dir: %v", err)
	}
	// ~/.ssh is one of the home-sensitive directories escalated to system_write.
	dir := filepath.Join(home, ".ssh")
	os.MkdirAll(dir, 0700)
	return dir
}

// TestReadFile_SymlinkDirectoryTraversal verifies that read_file resolves
// intermediate directory symlinks before classifying the path, so a symlink
// pointing outside the workspace is classified by its real target rather than
// by the lexical workspace path.
func TestReadFile_SymlinkDirectoryTraversal(t *testing.T) {
	skipIfSymlinksUnsupported(t)

	cwd := t.TempDir()
	origDir, _ := os.Getwd()
	os.Chdir(cwd)
	defer os.Chdir(origDir)

	outsideDir := symlinkSensitiveDir(t)
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	os.WriteFile(outsideFile, []byte("secret"), 0600)

	link := filepath.Join(cwd, "link")
	if err := os.Symlink(outsideDir, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	// Deny system_write so that the resolved ~/.ssh path is rejected.
	dc := danger.DangerousConfig{
		Classes: map[danger.RiskClass]danger.Action{
			danger.SystemWrite: danger.Deny,
		},
	}
	tool := &readFileTool{dangerousConfig: dc}
	result := callJSON(t, tool, fmt.Sprintf(`{"path":%q}`, filepath.Join(link, "secret.txt")))

	var r struct {
		Content string `json:"content"`
		Error   string `json:"error,omitempty"`
	}
	mustUnmarshal(t, result, &r)

	if r.Error == "" {
		t.Fatalf("read_file should reject symlink directory traversal to system_write path")
	}
	if !strings.Contains(r.Error, "denied") {
		t.Fatalf("expected denial error, got: %s", r.Error)
	}
}

// TestBatchRead_SymlinkDirectoryTraversal verifies the same for batch_read.
func TestBatchRead_SymlinkDirectoryTraversal(t *testing.T) {
	skipIfSymlinksUnsupported(t)

	cwd := t.TempDir()
	origDir, _ := os.Getwd()
	os.Chdir(cwd)
	defer os.Chdir(origDir)

	outsideDir := symlinkSensitiveDir(t)
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	os.WriteFile(outsideFile, []byte("secret"), 0600)

	link := filepath.Join(cwd, "link")
	if err := os.Symlink(outsideDir, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	dc := danger.DangerousConfig{
		Classes: map[danger.RiskClass]danger.Action{
			danger.SystemWrite: danger.Deny,
		},
	}
	tool := &batchReadTool{dangerousConfig: dc}
	result := callJSON(t, tool, fmt.Sprintf(`{"files":[{"path":%q}]}`, filepath.Join(link, "secret.txt")))

	var r struct {
		Results []struct {
			Content string `json:"content"`
			Error   string `json:"error,omitempty"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(r.Results))
	}
	entry := r.Results[0]
	if entry.Error == "" {
		t.Fatalf("batch_read should reject symlink directory traversal to system_write path")
	}
	if !strings.Contains(entry.Error, "denied") {
		t.Fatalf("expected denial error, got: %s", entry.Error)
	}
}

// TestHeadTail_SymlinkDirectoryTraversal verifies that head_tail resolves
// intermediate directory symlinks before classifying/opening.
func TestHeadTail_SymlinkDirectoryTraversal(t *testing.T) {
	skipIfSymlinksUnsupported(t)

	cwd := t.TempDir()
	origDir, _ := os.Getwd()
	os.Chdir(cwd)
	defer os.Chdir(origDir)

	outsideDir := symlinkSensitiveDir(t)
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	os.WriteFile(outsideFile, []byte("secret"), 0600)

	link := filepath.Join(cwd, "link")
	if err := os.Symlink(outsideDir, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	dc := danger.DangerousConfig{
		Classes: map[danger.RiskClass]danger.Action{
			danger.SystemWrite: danger.Deny,
		},
	}
	tool := &headTailTool{dangerousConfig: dc}
	result := callJSON(t, tool, fmt.Sprintf(`{"files":[{"path":%q}],"lines":1}`, filepath.Join(link, "secret.txt")))

	var r struct {
		Results []struct {
			Lines []string `json:"lines"`
			Error string   `json:"error,omitempty"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(r.Results))
	}
	entry := r.Results[0]
	if entry.Error == "" {
		t.Fatalf("head_tail should reject symlink directory traversal to system_write path")
	}
	if !strings.Contains(entry.Error, "denied") {
		t.Fatalf("expected denial error, got: %s", entry.Error)
	}
}
