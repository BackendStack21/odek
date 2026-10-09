package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
)

// An explicit search root that is itself a hidden directory returns nothing.
func TestRED_SearchFilesHiddenRootSilentlyEmpty(t *testing.T) {
	dir := t.TempDir()
	hid := filepath.Join(dir, ".work")
	os.MkdirAll(hid, 0o755)
	os.WriteFile(filepath.Join(hid, "x.txt"), []byte("needle\n"), 0o644)
	args, _ := json.Marshal(map[string]any{"pattern": "needle", "path": hid})
	out, _ := (&searchFilesTool{dangerousConfig: danger.DangerousConfig{}}).Call(string(args))
	if !strings.Contains(out, "x.txt") {
		t.Fatalf("search rooted at an explicit hidden dir found nothing: %s", out)
	}
}

// Hidden directories below the root stay skipped, and an explicit root named
// like a build-artifact directory is searched.
func TestSearchFilesHiddenRootKeepsNestedHiddenSkipped(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, ".work")
	os.MkdirAll(filepath.Join(root, ".inner"), 0o755)
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("needle\n"), 0o644)
	os.WriteFile(filepath.Join(root, ".inner", "b.txt"), []byte("needle\n"), 0o644)
	args, _ := json.Marshal(map[string]any{"pattern": "needle", "path": root})
	out, _ := (&searchFilesTool{dangerousConfig: danger.DangerousConfig{}}).Call(string(args))
	if !strings.Contains(out, "a.txt") || strings.Contains(out, "b.txt") {
		t.Fatalf("want a.txt only, got %s", out)
	}

	nm := filepath.Join(dir, "node_modules")
	os.MkdirAll(nm, 0o755)
	os.WriteFile(filepath.Join(nm, "c.txt"), []byte("needle\n"), 0o644)
	args, _ = json.Marshal(map[string]any{"pattern": "needle", "path": nm})
	out, _ = (&searchFilesTool{dangerousConfig: danger.DangerousConfig{}}).Call(string(args))
	if !strings.Contains(out, "c.txt") {
		t.Fatalf("explicit node_modules root should be searched: %s", out)
	}
}
