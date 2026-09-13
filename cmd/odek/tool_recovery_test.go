package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── Shell Stderr Tests ───────────────────────────────────────────────

func TestShellTool_StderrWithEmptyStdout(t *testing.T) {
	st := &shellTool{}
	// Command that writes only to stderr and fails
	result, err := st.Call(`{"command": "echo error msg >&2 && exit 1"}`)
	if err == nil {
		t.Fatal("expected typed operation failure alongside result")
	}
	if !strings.Contains(result, "error msg") {
		t.Errorf("result = %q, should contain stderr 'error msg'", result)
	}
}

func TestShellTool_StderrWithStdoutAndError(t *testing.T) {
	st := &shellTool{}
	result, err := st.Call(`{"command": "echo stdout_line && echo stderr_line >&2 && exit 1"}`)
	if err == nil {
		t.Fatal("failing shell command must return a typed failure and combined output")
	}
	if !strings.Contains(result, "stdout_line") {
		t.Errorf("result should contain stdout content, got: %s", result)
	}
	if !strings.Contains(result, "stderr_line") {
		t.Errorf("result should contain stderr content, got: %s", result)
	}
}

func TestShellTool_StderrOnlySuccess(t *testing.T) {
	st := &shellTool{}
	result, err := st.Call(`{"command": "echo warning >&2"}`)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	if !strings.Contains(result, "warning") {
		t.Errorf("result = %q, should contain stderr 'warning'", result)
	}
}

// ── Directory Error Message Tests ────────────────────────────────────

func TestReadFile_DirectorySuggestsAlternatives(t *testing.T) {
	dir := t.TempDir()
	tool := &readFileTool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"path":"%s"}`, dir))
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error for directory path")
	}
	if !strings.Contains(r.Error, "tree") && !strings.Contains(r.Error, "search_files") && !strings.Contains(r.Error, "glob") {
		t.Errorf("error should suggest alternatives (tree/search_files/glob), got: %s", r.Error)
	}
}

func TestBatchRead_DirectorySuggestsAlternatives(t *testing.T) {
	dir := t.TempDir()
	tool := &batchReadTool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"files":[{"path":"%s"}]}`, dir))
	var r struct {
		Results []struct {
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Results) == 0 {
		t.Fatal("expected a result")
	}
	if r.Results[0].Error == "" {
		t.Fatal("expected error for directory path")
	}
	if !strings.Contains(r.Results[0].Error, "tree") && !strings.Contains(r.Results[0].Error, "search_files") && !strings.Contains(r.Results[0].Error, "glob") {
		t.Errorf("error should suggest alternatives (tree/search_files/glob), got: %s", r.Results[0].Error)
	}
}

// ── WriteFile Edge Cases (non-conflicting) ───────────────────────────

func TestWriteFile_EmptyPath_Recovery(t *testing.T) {
	tool := &writeFileTool{}
	result := callJSON(t, tool, `{"path":"","content":"test"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error for empty path")
	}
}

// ── Patch Edge Cases (non-conflicting) ───────────────────────────────

func TestPatch_OldStringNotFound_Recovery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello world"), 0644)
	tool := &patchTool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"path":"%s","old_string":"zzz","new_string":"yyy"}`, path))
	var r struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Success {
		t.Fatal("expected patch to fail when old_string not found")
	}
	if !strings.Contains(r.Error, "not found") {
		t.Errorf("error should say 'not found', got: %s", r.Error)
	}
}

// ── Glob Edge Cases (non-conflicting) ────────────────────────────────

func TestGlob_EmptyPattern_Recovery(t *testing.T) {
	tool := &globTool{}
	result := callJSON(t, tool, `{"pattern":""}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error for empty pattern")
	}
}

// ── Tree Edge Cases (non-conflicting) ────────────────────────────────

func TestTree_DirectoryPath_Recovery(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello"), 0644)
	tool := &treeTool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"path":"%s"}`, dir))
	var r struct {
		Tree struct {
			Path     string        `json:"path"`
			IsDir    bool          `json:"is_dir"`
			Children []interface{} `json:"children"`
		} `json:"tree"`
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error != "" {
		t.Fatalf("error: %s", r.Error)
	}
	if !r.Tree.IsDir {
		t.Errorf("expected root to be a directory")
	}
}

// ── ReadFile Binary Detection (non-conflicting) ────────────────────

func TestReadFile_BinaryContent_Recovery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.bin")
	os.WriteFile(path, []byte{0x00, 0xFF, 0x00, 0xFF}, 0644)
	tool := &readFileTool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"path":"%s"}`, path))
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error for binary file")
	}
	if !strings.Contains(r.Error, "binary") {
		t.Errorf("error should mention 'binary', got: %s", r.Error)
	}
}
