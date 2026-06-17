package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
)

// skipIfSymlinksUnsupported skips the test on platforms where creating
// symlinks is unreliable (Windows without dev mode / admin).
func skipIfSymlinksUnsupported(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlink tests skipped on Windows")
	}
}

// ── 1. Symlink directory traversal in write_file / patch / batch_patch ───

func TestWriteFile_SymlinkDirectoryTraversal(t *testing.T) {
	skipIfSymlinksUnsupported(t)

	cwd := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "escaped.txt")

	link := filepath.Join(cwd, "link")
	if err := os.Symlink(outsideDir, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	tool := &writeFileTool{restrictToCWD: true}
	result := callJSON(t, tool, fmt.Sprintf(`{"path":%q,"content":"escaped"}`, filepath.Join(link, "escaped.txt")))
	var r struct {
		Success bool   `json:"success"`
		Error   string `json:"error,omitempty"`
	}
	mustUnmarshal(t, result, &r)

	if r.Success {
		t.Fatalf("write_file should reject symlink directory traversal, but succeeded")
	}
	if _, err := os.Stat(outsideFile); !os.IsNotExist(err) {
		t.Fatalf("write_file escaped CWD via directory symlink; file exists at %s", outsideFile)
	}
}

func TestPatch_SymlinkDirectoryTraversal(t *testing.T) {
	skipIfSymlinksUnsupported(t)

	cwd := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "target.txt")
	os.WriteFile(outsideFile, []byte("old content"), 0644)

	link := filepath.Join(cwd, "link")
	if err := os.Symlink(outsideDir, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	tool := &patchTool{restrictToCWD: true}
	result := callJSON(t, tool, fmt.Sprintf(`{"path":%q,"old_string":"old content","new_string":"new content"}`, filepath.Join(link, "target.txt")))
	var r struct {
		Success bool   `json:"success"`
		Error   string `json:"error,omitempty"`
	}
	mustUnmarshal(t, result, &r)

	if r.Success {
		t.Fatalf("patch should reject symlink directory traversal, but succeeded")
	}
	data, _ := os.ReadFile(outsideFile)
	if string(data) != "old content" {
		t.Fatalf("patch escaped CWD and modified outside file: %q", string(data))
	}
}

func TestBatchPatch_SymlinkDirectoryTraversal(t *testing.T) {
	skipIfSymlinksUnsupported(t)

	cwd := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "target.txt")
	os.WriteFile(outsideFile, []byte("old content"), 0644)

	link := filepath.Join(cwd, "link")
	if err := os.Symlink(outsideDir, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	tool := &batchPatchTool{restrictToCWD: true}
	args := fmt.Sprintf(`{"patches":[{"path":%q,"old_string":"old content","new_string":"new content"}]}`, filepath.Join(link, "target.txt"))
	result := callJSON(t, tool, args)
	var r struct {
		Results []struct {
			Success bool   `json:"success"`
			Error   string `json:"error,omitempty"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(r.Results))
	}
	if r.Results[0].Success {
		t.Fatalf("batch_patch should reject symlink directory traversal, but succeeded")
	}
	data, _ := os.ReadFile(outsideFile)
	if string(data) != "old content" {
		t.Fatalf("batch_patch escaped CWD and modified outside file: %q", string(data))
	}
}

// ── 2. glob must not follow symlinks in simple mode ─────────────────────

func TestGlob_SymlinkFileTraversal(t *testing.T) {
	skipIfSymlinksUnsupported(t)

	cwd := t.TempDir()
	outsideFile := filepath.Join(t.TempDir(), "secret.txt")
	os.WriteFile(outsideFile, []byte("secret"), 0644)

	link := filepath.Join(cwd, "link.txt")
	if err := os.Symlink(outsideFile, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	tool := &globTool{}
	result := callJSON(t, tool, `{"pattern":"*.txt","path":"`+cwd+`"}`)
	var r struct {
		Matches []globMatch `json:"matches"`
	}
	mustUnmarshal(t, result, &r)

	for _, m := range r.Matches {
		if m.Path == link || strings.HasPrefix(m.Path, filepath.Dir(outsideFile)) {
			t.Fatalf("glob followed file symlink to outside path: %s", m.Path)
		}
	}
}

func TestGlob_SymlinkDirectoryTraversal(t *testing.T) {
	skipIfSymlinksUnsupported(t)

	cwd := t.TempDir()
	outsideDir := t.TempDir()
	os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte("secret"), 0644)

	link := filepath.Join(cwd, "link")
	if err := os.Symlink(outsideDir, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	tool := &globTool{}
	result := callJSON(t, tool, `{"pattern":"*","path":"`+cwd+`"}`)
	var r struct {
		Matches []globMatch `json:"matches"`
	}
	mustUnmarshal(t, result, &r)

	for _, m := range r.Matches {
		if m.Path == link || strings.HasPrefix(m.Path, outsideDir) {
			t.Fatalf("glob listed directory symlink that points outside: %s", m.Path)
		}
	}
}

// ── 2b. glob must not traverse .. patterns ───────────────────────────────

func TestGlob_DotDotPatternRejected(t *testing.T) {
	cwd := t.TempDir()
	parent := filepath.Dir(cwd)
	outsideFile := filepath.Join(parent, "outside.txt")
	os.WriteFile(outsideFile, []byte("secret"), 0644)

	tool := &globTool{dangerousConfig: danger.DangerousConfig{}}
	result := callJSON(t, tool, fmt.Sprintf(`{"pattern":"../outside.txt","path":%q}`, cwd))
	var r struct {
		Error   string      `json:"error,omitempty"`
		Matches []globMatch `json:"matches"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" && len(r.Matches) > 0 {
		t.Fatalf("glob with .. pattern escaped workspace: matches=%+v", r.Matches)
	}
	for _, m := range r.Matches {
		if strings.Contains(m.Path, "outside.txt") {
			t.Fatalf("glob matched file outside workspace: %s", m.Path)
		}
	}
}

// ── 2c. search_files(target=files) must not traverse .. patterns ─────────

func TestSearchFiles_DotDotPatternRejected(t *testing.T) {
	cwd := t.TempDir()
	parent := filepath.Dir(cwd)
	outsideFile := filepath.Join(parent, "outside.txt")
	os.WriteFile(outsideFile, []byte("secret"), 0644)

	tool := &searchFilesTool{dangerousConfig: danger.DangerousConfig{}}
	result := callJSON(t, tool, fmt.Sprintf(`{"pattern":"../outside.txt","path":%q,"target":"files"}`, cwd))
	var r struct {
		Error   string `json:"error,omitempty"`
		Matches []struct {
			Path string `json:"path"`
		} `json:"matches"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" && len(r.Matches) > 0 {
		t.Fatalf("search_files with .. pattern escaped workspace: matches=%+v", r.Matches)
	}
	for _, m := range r.Matches {
		if strings.Contains(unwrapUntrusted(m.Path), "outside.txt") {
			t.Fatalf("search_files matched file outside workspace: %s", m.Path)
		}
	}
}

// ── 2d. glob must not accept absolute patterns ───────────────────────────

func TestGlob_AbsolutePatternRejected(t *testing.T) {
	cwd := t.TempDir()
	tool := &globTool{dangerousConfig: danger.DangerousConfig{}}
	result := callJSON(t, tool, fmt.Sprintf(`{"pattern":"/etc/passwd","path":%q}`, cwd))
	var r struct {
		Error   string      `json:"error,omitempty"`
		Matches []globMatch `json:"matches"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" && len(r.Matches) > 0 {
		t.Fatalf("absolute glob pattern escaped workspace: matches=%+v", r.Matches)
	}
}

// ── 3. batch_read must wrap content with untrusted_content ───────────────

func TestBatchRead_WrapsContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello world"), 0644)

	tool := &batchReadTool{}
	result := callJSON(t, tool, `{"files":[{"path":"`+path+`"}]}`)
	var r struct {
		Results []batchReadFileResult `json:"results"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(r.Results))
	}
	if !strings.HasPrefix(r.Results[0].Content, "<untrusted_content_") {
		t.Fatalf("batch_read content should be wrapped in untrusted_content, got: %q", r.Results[0].Content)
	}
}

// ── 4. read_file / batch_read must cap total returned bytes ──────────────

func TestReadFile_CapsTotalSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")

	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, strings.Repeat("x", 500*1024))
	}
	os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644)

	tool := &readFileTool{}
	result := callJSON(t, tool, `{"path":"`+path+`","limit":10}`)
	var r struct {
		Content string `json:"content"`
		Error   string `json:"error,omitempty"`
	}
	mustUnmarshal(t, result, &r)

	body := unwrapUntrusted(r.Content)
	if len(body) > 1024*1024 {
		t.Fatalf("read_file returned %d bytes, expected cap at 1 MiB", len(body))
	}
}

func TestBatchRead_CapsTotalSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")

	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, strings.Repeat("x", 500*1024))
	}
	os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644)

	tool := &batchReadTool{}
	result := callJSON(t, tool, `{"files":[{"path":"`+path+`","limit":10}]}`)
	var r struct {
		Results []batchReadFileResult `json:"results"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(r.Results))
	}
	body := r.Results[0].Content
	if len(body) > 1024*1024 {
		t.Fatalf("batch_read returned %d bytes, expected cap at 1 MiB", len(body))
	}
}

// ── 5. write_file must reject ~/.odek trust anchors ─────────────────────

func TestWriteFile_RejectsOdekTrustAnchors(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}

	tool := &writeFileTool{restrictToCWD: true}
	for _, path := range []string{
		home + "/.odek/schedules.json",
		home + "/.odek/schedule-state.json",
		home + "/.odek/sessions/abc.json",
		home + "/.odek/mcp_approvals.json",
		home + "/.odek/mcp_tool_approvals.json",
		home + "/.odek/restart.json",
		home + "/.odek/telegram.lock",
		home + "/.odek/audit/turn-1.json",
		home + "/.odek/plans/evil.md",
	} {
		result := callJSON(t, tool, fmt.Sprintf(`{"path":%q,"content":"evil"}`, path))
		var r struct {
			Success bool   `json:"success"`
			Error   string `json:"error,omitempty"`
		}
		mustUnmarshal(t, result, &r)
		if r.Success {
			t.Errorf("write_file should reject protected odek path %q", path)
		}
	}
}

// ── 6. write_file must preserve original file mode on overwrite ──────────

func TestWriteFile_PreservesFileMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.txt")
	// Start with a specific mode (e.g., group-readable). write_file's temp+rename
	// currently drops it to the temp-file default (0600), leaking/changing mode.
	if err := os.WriteFile(path, []byte("old"), 0640); err != nil {
		t.Fatalf("write initial file: %v", err)
	}

	tool := &writeFileTool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"path":%q,"content":"new"}`, path))
	var r struct {
		Success bool   `json:"success"`
		Error   string `json:"error,omitempty"`
	}
	mustUnmarshal(t, result, &r)
	if !r.Success {
		t.Fatalf("write_file failed: %s", r.Error)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if info.Mode().Perm() != 0640 {
		t.Fatalf("write_file changed mode from 0640 to %04o", info.Mode().Perm())
	}
}
