package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
)

// ── ReadFile Tool ──────────────────────────────────────────────────────

func TestReadFile_Basic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("line1\nline2\nline3\n"), 0644)

	tool := &readFileTool{}
	result := callJSON(t, tool, `{"path":"`+path+`"}`)
	var r struct {
		Content    string `json:"content"`
		TotalLines int    `json:"total_lines"`
	}
	mustUnmarshal(t, result, &r)

	if r.TotalLines != 3 {
		t.Errorf("TotalLines = %d, want 3", r.TotalLines)
	}
	if !strings.Contains(r.Content, "line2") {
		t.Errorf("Content missing 'line2': %q", r.Content)
	}
}

func TestReadFile_OffsetLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("a\nb\nc\nd\ne\n"), 0644)

	tool := &readFileTool{}
	result := callJSON(t, tool, `{"path":"`+path+`","offset":2,"limit":2}`)
	var r struct {
		Content    string `json:"content"`
		TotalLines int    `json:"total_lines"`
	}
	mustUnmarshal(t, result, &r)

	if r.TotalLines != 5 {
		t.Errorf("TotalLines = %d, want 5", r.TotalLines)
	}
	if !strings.Contains(r.Content, "b") || strings.Contains(r.Content, "a") {
		t.Errorf("offset 2 should skip 'a', got: %q", r.Content)
	}
	if strings.Contains(r.Content, "d") {
		t.Errorf("limit 2 should stop after 'c', got: %q", r.Content)
	}
}

func TestReadFile_NotFound(t *testing.T) {
	tool := &readFileTool{}
	result := callJSON(t, tool, `{"path":"/nonexistent/path.txt"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestReadFile_NegativeOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello\nworld\n"), 0644)

	tool := &readFileTool{}
	result := callJSON(t, tool, `{"path":"`+path+`","offset":-5,"limit":10}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error for negative offset")
	}
}

func TestReadFile_LimitCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.txt")
	var lines []string
	for i := 0; i < 5000; i++ {
		lines = append(lines, "line")
	}
	os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644)

	tool := &readFileTool{}
	result := callJSON(t, tool, `{"path":"`+path+`","limit":9999}`)
	var r struct {
		Content    string `json:"content"`
		TotalLines int    `json:"total_lines"`
	}
	mustUnmarshal(t, result, &r)
	if r.TotalLines != 5000 {
		t.Errorf("TotalLines = %d, want 5000", r.TotalLines)
	}
	// Should cap at maxLines (2000)
	count := strings.Count(r.Content, "\n") + 1
	if count > 2000+1 { // +1 for cap boundary
		t.Errorf("Returned %d lines, should be capped at 2000", count)
	}
}

func TestReadFile_BinaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "binary.bin")
	// Write a file with null bytes
	os.WriteFile(path, []byte("hello\x00world\n"), 0644)

	tool := &readFileTool{}
	result := callJSON(t, tool, `{"path":"`+path+`"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error for binary file")
	}
	if !strings.Contains(r.Error, "binary file") {
		t.Errorf("error should mention 'binary file', got: %s", r.Error)
	}
}

func TestReadFile_Directory(t *testing.T) {
	dir := t.TempDir()

	tool := &readFileTool{}
	result := callJSON(t, tool, `{"path":"`+dir+`"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error for directory")
	}
	if !strings.Contains(r.Error, "directory") {
		t.Errorf("error should mention 'directory', got: %s", r.Error)
	}
}

func TestReadFile_InvalidJSON(t *testing.T) {
	tool := &readFileTool{}
	result, err := tool.Call(`{invalid}`)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestReadFile_EmptyPath(t *testing.T) {
	tool := &readFileTool{}
	result := callJSON(t, tool, `{}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error for empty path")
	}
	if !strings.Contains(r.Error, "path is required") {
		t.Errorf("error should mention 'path is required', got: %s", r.Error)
	}
}

func TestReadFile_LineNumberFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello\nworld\n"), 0644)

	tool := &readFileTool{}
	result := callJSON(t, tool, `{"path":"`+path+`"}`)
	var r struct {
		Content string `json:"content"`
	}
	mustUnmarshal(t, result, &r)

	// Output should include line numbers (e.g., "1|", "2|")
	if !strings.Contains(r.Content, "1|") && !strings.Contains(r.Content, "1  ") {
		t.Errorf("Output should include line numbers, got: %q", r.Content)
	}
}

func TestReadFile_Schema(t *testing.T) {
	tool := &readFileTool{}
	schema := tool.Schema().(map[string]any)
	props := schema["properties"].(map[string]any)

	req, ok := schema["required"].([]string)
	if !ok {
		reqI := schema["required"].([]any)
		for _, r := range reqI {
			if s, ok := r.(string); ok {
				req = append(req, s)
			}
		}
	}
	for _, required := range []string{"path"} {
		if !containsStr(req, required) {
			t.Errorf("Schema missing required field: %s", required)
		}
	}
	if _, ok := props["path"]; !ok {
		t.Error("Schema missing 'path' property")
	}
	if _, ok := props["offset"]; !ok {
		t.Error("Schema missing 'offset' property (optional)")
	}
	if _, ok := props["limit"]; !ok {
		t.Error("Schema missing 'limit' property (optional)")
	}
}

// ── WriteFile Tool ─────────────────────────────────────────────────────

func TestWriteFile_Create(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")

	tool := &writeFileTool{}
	result := callJSON(t, tool, `{"path":"`+path+`","content":"hello world"}`)
	var r struct {
		Success bool   `json:"success"`
		Path    string `json:"path"`
	}
	mustUnmarshal(t, result, &r)

	if !r.Success {
		t.Fatal("WriteFile should succeed")
	}
	if r.Path != path {
		t.Errorf("Path = %q, want %q", r.Path, path)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "hello world" {
		t.Errorf("Content = %q, want %q", string(data), "hello world")
	}
}

func TestWriteFile_Overwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.txt")
	os.WriteFile(path, []byte("old content"), 0644)

	tool := &writeFileTool{}
	result := callJSON(t, tool, `{"path":"`+path+`","content":"new content"}`)
	var r struct {
		Success bool `json:"success"`
	}
	mustUnmarshal(t, result, &r)
	if !r.Success {
		t.Fatal("WriteFile should succeed")
	}

	data, _ := os.ReadFile(path)
	if string(data) != "new content" {
		t.Errorf("Content = %q, want %q", string(data), "new content")
	}
}

func TestWriteFile_MissingPath(t *testing.T) {
	tool := &writeFileTool{}
	result := callJSON(t, tool, `{"content":"hello"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error when path is missing")
	}
}

func TestWriteFile_EmptyContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")

	tool := &writeFileTool{}
	result := callJSON(t, tool, `{"path":"`+path+`","content":""}`)
	var r struct {
		Success bool `json:"success"`
	}
	mustUnmarshal(t, result, &r)
	if !r.Success {
		t.Fatal("WriteFile with empty content should succeed")
	}

	data, _ := os.ReadFile(path)
	if len(data) != 0 {
		t.Errorf("expected empty file, got %d bytes", len(data))
	}
}

func TestWriteFile_CreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "c", "deep.txt")

	tool := &writeFileTool{}
	result := callJSON(t, tool, `{"path":"`+path+`","content":"nested"}`)
	var r struct {
		Success bool `json:"success"`
	}
	mustUnmarshal(t, result, &r)
	if !r.Success {
		t.Fatal("WriteFile should create parent directories")
	}

	data, _ := os.ReadFile(path)
	if string(data) != "nested" {
		t.Errorf("Content = %q, want %q", string(data), "nested")
	}
}

func TestWriteFile_Schema(t *testing.T) {
	tool := &writeFileTool{}
	schema := tool.Schema().(map[string]any)
	props := schema["properties"].(map[string]any)
	req := toStringSlice(schema["required"])

	for _, required := range []string{"path", "content"} {
		if !containsStr(req, required) {
			t.Errorf("Schema missing required field: %s", required)
		}
	}
	if _, ok := props["path"]; !ok {
		t.Error("Schema missing 'path' property")
	}
	if _, ok := props["content"]; !ok {
		t.Error("Schema missing 'content' property")
	}
}

// ── SearchFiles Tool ───────────────────────────────────────────────────

func TestSearchFiles_Grep(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello world\nfoo bar\n"), 0644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("foo bar\nbaz qux\n"), 0644)

	tool := &searchFilesTool{}
	result := callJSON(t, tool, `{"pattern":"foo","target":"content","path":"`+dir+`"}`)
	var r struct {
		Matches []struct {
			Path     string `json:"path"`
			Line     int    `json:"line"`
			Content  string `json:"content"`
		} `json:"matches"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(r.Matches))
	}
	// a.txt: "foo bar" on line 2, b.txt: "foo bar" on line 1
	files := map[string]int{}
	for _, m := range r.Matches {
		files[filepath.Base(m.Path)] = m.Line
	}
	if files["a.txt"] != 2 {
		t.Errorf("a.txt expected line 2, got %d", files["a.txt"])
	}
	if files["b.txt"] != 1 {
		t.Errorf("b.txt expected line 1, got %d", files["b.txt"])
	}
}

func TestSearchFiles_FindByName(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644)
	os.WriteFile(filepath.Join(dir, "main_test.go"), []byte("package main\n"), 0644)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# readme\n"), 0644)

	tool := &searchFilesTool{}
	result := callJSON(t, tool, `{"pattern":"main*","target":"files","path":"`+dir+`"}`)
	var r struct {
		Matches []struct {
			Path string `json:"path"`
		} `json:"matches"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Matches) != 2 {
		t.Fatalf("expected 2 file matches, got %d", len(r.Matches))
	}

	for _, m := range r.Matches {
		name := filepath.Base(m.Path)
		if name != "main.go" && name != "main_test.go" {
			t.Errorf("unexpected match: %s", name)
		}
	}
}

func TestSearchFiles_NoMatches(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0644)

	tool := &searchFilesTool{}
	result := callJSON(t, tool, `{"pattern":"nonexistent","target":"content","path":"`+dir+`"}`)
	var r struct {
		Matches []any `json:"matches"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Matches) != 0 {
		t.Errorf("expected 0 matches, got %d", len(r.Matches))
	}
}

func TestSearchFiles_FileGlob(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("func hello\n"), 0644)
	os.WriteFile(filepath.Join(dir, "a.py"), []byte("def hello\n"), 0644)

	tool := &searchFilesTool{}
	result := callJSON(t, tool, `{"pattern":"hello","target":"content","path":"`+dir+`","file_glob":"*.go"}`)
	var r struct {
		Matches []struct {
			Path string `json:"path"`
		} `json:"matches"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Matches) != 1 || !strings.HasSuffix(r.Matches[0].Path, ".go") {
		t.Errorf("expected 1 match in .go file, got %d matches", len(r.Matches))
	}
}

func TestSearchFiles_InvalidTarget(t *testing.T) {
	tool := &searchFilesTool{}
	result := callJSON(t, tool, `{"pattern":"x","target":"invalid"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error for invalid target")
	}
}

func TestSearchFiles_Schema(t *testing.T) {
	tool := &searchFilesTool{}
	schema := tool.Schema().(map[string]any)
	props := schema["properties"].(map[string]any)
	req := toStringSlice(schema["required"])

	for _, required := range []string{"pattern"} {
		if !containsStr(req, required) {
			t.Errorf("Schema missing required field: %s", required)
		}
	}
	if _, ok := props["pattern"]; !ok {
		t.Error("Schema missing 'pattern' property")
	}
	if _, ok := props["target"]; !ok {
		t.Error("Schema missing 'target' property")
	}
	if _, ok := props["path"]; !ok {
		t.Error("Schema missing 'path' property")
	}
	if _, ok := props["file_glob"]; !ok {
		t.Error("Schema missing 'file_glob' property")
	}
}

// ── Patch Tool ─────────────────────────────────────────────────────────

func TestPatch_BasicReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	os.WriteFile(path, []byte("hello old world\n"), 0644)

	tool := &patchTool{}
	result := callJSON(t, tool, `{"path":"`+path+`","old_string":"old","new_string":"new"}`)
	var r struct {
		Success bool   `json:"success"`
		Diff    string `json:"diff"`
	}
	mustUnmarshal(t, result, &r)

	if !r.Success {
		t.Fatal("patch should succeed")
	}
	if r.Diff == "" {
		t.Error("expected diff output")
	}

	data, _ := os.ReadFile(path)
	if string(data) != "hello new world\n" {
		t.Errorf("Content = %q, want %q", string(data), "hello new world\n")
	}
}

func TestPatch_ReplaceAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	os.WriteFile(path, []byte("x y x y x\n"), 0644)

	tool := &patchTool{}
	result := callJSON(t, tool, `{"path":"`+path+`","old_string":"x","new_string":"z","replace_all":true}`)
	var r struct {
		Success bool `json:"success"`
	}
	mustUnmarshal(t, result, &r)
	if !r.Success {
		t.Fatal("patch should succeed")
	}

	data, _ := os.ReadFile(path)
	if string(data) != "z y z y z\n" {
		t.Errorf("Content = %q, want %q", string(data), "z y z y z\n")
	}
}

func TestPatch_StringNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	os.WriteFile(path, []byte("hello world\n"), 0644)

	tool := &patchTool{}
	result := callJSON(t, tool, `{"path":"`+path+`","old_string":"nonexistent","new_string":"x"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error when old_string not found")
	}
}

func TestPatch_DeleteString(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	os.WriteFile(path, []byte("hello world\n"), 0644)

	tool := &patchTool{}
	result := callJSON(t, tool, `{"path":"`+path+`","old_string":"world","new_string":""}`)
	var r struct {
		Success bool `json:"success"`
	}
	mustUnmarshal(t, result, &r)
	if !r.Success {
		t.Fatal("patch should succeed")
	}

	data, _ := os.ReadFile(path)
	if string(data) != "hello \n" {
		t.Errorf("Content = %q, want %q", string(data), "hello \n")
	}
}

func TestPatch_FileNotFound(t *testing.T) {
	tool := &patchTool{}
	result := callJSON(t, tool, `{"path":"/nonexistent/file.txt","old_string":"x","new_string":"y"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestPatch_EmptyPath(t *testing.T) {
	tool := &patchTool{}
	result := callJSON(t, tool, `{"old_string":"x","new_string":"y"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error for empty path")
	}
	if !strings.Contains(r.Error, "path is required") {
		t.Errorf("error should mention 'path is required', got: %s", r.Error)
	}
}

func TestPatch_EmptyOldString(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	os.WriteFile(path, []byte("content"), 0644)

	tool := &patchTool{}
	result := callJSON(t, tool, `{"path":"`+path+`","new_string":"y"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error for empty old_string")
	}
}

func TestPatch_InvalidJSON(t *testing.T) {
	tool := &patchTool{}
	result, err := tool.Call(`{invalid}`)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error for invalid JSON")
	}
}

// ── truncateDiff Tests ─────────────────────────────────────────────────

func TestTruncateDiff_ShortString(t *testing.T) {
	result := truncateDiff("hello", 100)
	if result != "hello" {
		t.Errorf("truncateDiff('hello', 100) = %q, want 'hello'", result)
	}
}

func TestTruncateDiff_LongString(t *testing.T) {
	longStr := string(make([]byte, 200))
	result := truncateDiff(longStr, 10)
	if len(result) > 20 { // 10 chars + "..." = 13
		t.Errorf("truncateDiff long string = %q (len=%d), want truncated to ~13", result, len(result))
	}
}

func TestTruncateDiff_MultiLine(t *testing.T) {
	result := truncateDiff("first line\nsecond line\nthird line", 100)
	if result != "first line" {
		t.Errorf("truncateDiff(multiline, 100) = %q, want 'first line'", result)
	}
}

func TestTruncateDiff_ExactBoundary(t *testing.T) {
	result := truncateDiff("hello world", 11)
	if result != "hello world" {
		t.Errorf("truncateDiff('hello world', 11) = %q, want 'hello world'", result)
	}
}

func TestPatch_MissingOldString(t *testing.T) {
	tool := &patchTool{}
	result := callJSON(t, tool, `{"path":"/tmp/x","new_string":"y"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error when old_string is missing")
	}
}

func TestPatch_ReplaceAllWithoutFlag(t *testing.T) {
	// Without replace_all, only the first occurrence should be replaced
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	os.WriteFile(path, []byte("a a a\n"), 0644)

	tool := &patchTool{}
	result := callJSON(t, tool, `{"path":"`+path+`","old_string":"a","new_string":"b"}`)
	mustSuccess(t, result)

	data, _ := os.ReadFile(path)
	if string(data) != "b a a\n" {
		t.Errorf("Content = %q, want %q (only first 'a' replaced)", string(data), "b a a\n")
	}
}

func TestPatch_Schema(t *testing.T) {
	tool := &patchTool{}
	schema := tool.Schema().(map[string]any)
	props := schema["properties"].(map[string]any)
	req := toStringSlice(schema["required"])

	for _, required := range []string{"path", "old_string"} {
		if !containsStr(req, required) {
			t.Errorf("Schema missing required field: %s", required)
		}
	}
	if _, ok := props["path"]; !ok {
		t.Error("Schema missing 'path' property")
	}
	if _, ok := props["old_string"]; !ok {
		t.Error("Schema missing 'old_string' property")
	}
	if _, ok := props["new_string"]; !ok {
		t.Error("Schema missing 'new_string' property")
	}
	if _, ok := props["replace_all"]; !ok {
		t.Error("Schema missing 'replace_all' property")
	}
}

// ── confineToCWD Tests ─────────────────────────────────────────────────

func TestConfineToCWD_ValidRelativePath(t *testing.T) {
	// A relative path inside CWD should resolve correctly
	path := "some/file.txt"
	resolved, err := confineToCWD(path)
	if err != nil {
		t.Fatalf("confineToCWD(%q) error: %v", path, err)
	}
	cwd, _ := os.Getwd()
	want := filepath.Join(cwd, path)
	if resolved != want {
		t.Errorf("confineToCWD(%q) = %q, want %q", path, resolved, want)
	}
}

func TestConfineToCWD_ValidCWD(t *testing.T) {
	// The CWD itself should pass
	cwd, _ := os.Getwd()
	resolved, err := confineToCWD(cwd)
	if err != nil {
		t.Fatalf("confineToCWD(%q) error: %v", cwd, err)
	}
	if resolved != cwd {
		t.Errorf("confineToCWD(%q) = %q, want %q", cwd, resolved, cwd)
	}
}

func TestConfineToCWD_ParentDirEscape(t *testing.T) {
	// ".." traversal should be rejected
	path := "../etc/passwd"
	_, err := confineToCWD(path)
	if err == nil {
		t.Fatal("expected error for parent directory traversal")
	}
	if !strings.Contains(err.Error(), "escapes the working directory") {
		t.Errorf("error should mention escaping: %v", err)
	}
}

func TestConfineToCWD_AbsolutePathRejected(t *testing.T) {
	// Absolute path outside CWD should be rejected
	path := "/tmp/some-file.txt"
	_, err := confineToCWD(path)
	if err == nil {
		t.Fatal("expected error for absolute path outside CWD")
	}
	if !strings.Contains(err.Error(), "escapes the working directory") {
		t.Errorf("error should mention escaping: %v", err)
	}
}

func TestConfineToCWD_DoubleDotEscape(t *testing.T) {
	// Multiple levels of parent traversal
	path := "a/b/../../../../etc/passwd"
	_, err := confineToCWD(path)
	if err == nil {
		t.Fatal("expected error for multi-level parent traversal")
	}
}

// TestConfineToCWD_AllowsOdekDir verifies that paths under ~/.odek/
// are allowed by confineToCWD even when they are outside the project
// CWD. The agent frequently needs to write skills, memory, and config
// to ~/.odek/ — blocking these forces wasteful shell workarounds.
func TestConfineToCWD_AllowsOdekDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	odekPath := home + "/.odek/skills/test-skill/SKILL.md"

	_, err = confineToCWD(odekPath)
	if err != nil {
		t.Errorf("~/.odek/ paths should be allowed by confineToCWD, got: %v", err)
	}
}

// ── isBinary Tests ─────────────────────────────────────────────────────

func TestIsBinary_NullByte(t *testing.T) {
	// Content with null byte should be detected as binary
	data := []byte("hello\x00world")
	if !isBinary(data) {
		t.Error("isBinary should return true for content with null byte")
	}
}

func TestIsBinary_TextContent(t *testing.T) {
	// Normal text should not be detected as binary
	data := []byte("Hello World!\nThis is a text file.\nWith multiple lines.\tAnd tabs.\r\n")
	if isBinary(data) {
		t.Error("isBinary should return false for normal text content")
	}
}

func TestIsBinary_HighNonPrintable(t *testing.T) {
	// Content with >30% non-printable bytes should be detected as binary
	data := make([]byte, 100)
	for i := 0; i < 40; i++ {
		data[i] = 0x01 // non-printable
	}
	for i := 40; i < 100; i++ {
		data[i] = 'A'
	}
	if !isBinary(data) {
		t.Error("isBinary should return true for >30% non-printable content")
	}
}

func TestIsBinary_LowNonPrintable(t *testing.T) {
	// Content with <30% non-printable should not be binary
	data := make([]byte, 100)
	for i := 0; i < 20; i++ {
		data[i] = 0x01 // non-printable
	}
	for i := 20; i < 100; i++ {
		data[i] = 'A'
	}
	if isBinary(data) {
		t.Error("isBinary should return false for <30% non-printable content")
	}
}

func TestIsBinary_EmptyContent(t *testing.T) {
	// Empty content should not be detected as binary
	if isBinary([]byte{}) {
		t.Error("isBinary should return false for empty content")
	}
}

func TestIsBinary_ShortContent(t *testing.T) {
	// Very short content with no null byte should not be binary
	if isBinary([]byte("Hi")) {
		t.Error("isBinary should return false for short text content")
	}
}

func TestIsBinary_ShortBinaryContent(t *testing.T) {
	// Very short content WITH a null byte should be binary
	if !isBinary([]byte("H\x00i")) {
		t.Error("isBinary should return true for short content with null byte")
	}
}

func TestIsBinary_OnlyWhitespace(t *testing.T) {
	// Common whitespace chars should not be detected as binary
	data := []byte("\n\r\t   \n")
	if isBinary(data) {
		t.Error("isBinary should return false for whitespace-only content")
	}
}

// ── SearchFiles Additional Tests ────────────────────────────────────────

func TestSearchFiles_PatternRequired(t *testing.T) {
	tool := &searchFilesTool{}
	result := callJSON(t, tool, `{"target":"content"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error when pattern is missing")
	}
	if !strings.Contains(r.Error, "pattern is required") {
		t.Errorf("error should mention 'pattern is required', got: %s", r.Error)
	}
}

func TestSearchFiles_GlobWithPathSeparator(t *testing.T) {
	dir := t.TempDir()
	// Create a subdirectory structure
	subDir := filepath.Join(dir, "subdir")
	os.MkdirAll(subDir, 0755)
	os.WriteFile(filepath.Join(subDir, "result.txt"), []byte("data\n"), 0644)
	os.WriteFile(filepath.Join(dir, "other.txt"), []byte("data\n"), 0644)

	tool := &searchFilesTool{}
	result := callJSON(t, tool, `{"pattern":"subdir/*.txt","target":"files","path":"`+dir+`"}`)
	var r struct {
		Matches []struct {
			Path string `json:"path"`
		} `json:"matches"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Matches) != 1 {
		t.Fatalf("expected 1 match for 'subdir/*.txt', got %d", len(r.Matches))
	}
	if !strings.HasSuffix(r.Matches[0].Path, "subdir/result.txt") && !strings.HasSuffix(r.Matches[0].Path, "subdir\\result.txt") {
		t.Errorf("unexpected match path: %s", r.Matches[0].Path)
	}
}

func TestSearchFiles_FileGlobNoMatch(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("world\n"), 0644)

	tool := &searchFilesTool{}
	// Search for "hello" but with file_glob matching *.py (none exist)
	result := callJSON(t, tool, `{"pattern":"hello","target":"content","path":"`+dir+`","file_glob":"*.py"}`)
	var r struct {
		Matches []any `json:"matches"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Matches) != 0 {
		t.Errorf("expected 0 matches, got %d", len(r.Matches))
	}
}

func TestSearchFiles_HiddenDirSkipped(t *testing.T) {
	dir := t.TempDir()
	hiddenDir := filepath.Join(dir, ".hidden")
	os.Mkdir(hiddenDir, 0755)
	os.WriteFile(filepath.Join(hiddenDir, "secret.txt"), []byte("hidden data\n"), 0644)
	os.WriteFile(filepath.Join(dir, "visible.txt"), []byte("visible data\n"), 0644)

	tool := &searchFilesTool{}
	result := callJSON(t, tool, `{"pattern":"data","target":"content","path":"`+dir+`"}`)
	var r struct {
		Matches []struct {
			Path string `json:"path"`
		} `json:"matches"`
	}
	mustUnmarshal(t, result, &r)
	// Should find visible.txt but NOT hidden files
	for _, m := range r.Matches {
		if strings.Contains(m.Path, ".hidden") {
			t.Errorf("should not include hidden dir contents: %s", m.Path)
		}
	}
	if len(r.Matches) != 1 {
		t.Errorf("expected 1 match (visible.txt), got %d", len(r.Matches))
	}
}

func TestSearchFiles_InvalidRegex(t *testing.T) {
	tool := &searchFilesTool{}
	result := callJSON(t, tool, `{"pattern":"[invalid","target":"content","path":"."}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error for invalid regex")
	}
	if !strings.Contains(r.Error, "invalid regex") {
		t.Errorf("error should mention 'invalid regex', got: %s", r.Error)
	}
}

func TestSearchFiles_EmptyLimitDefaults(t *testing.T) {
	// Verify limit <= 0 defaults to maxMatches
	dir := t.TempDir()
	// Create more than maxMatches files
	for i := 0; i < 3; i++ {
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%d.go", i)), []byte("package main\n"), 0644)
	}

	tool := &searchFilesTool{}
	result := callJSON(t, tool, `{"pattern":"*.go","target":"files","path":"`+dir+`","limit":0}`)
	var r struct {
		Matches []any `json:"matches"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Matches) != 3 {
		t.Errorf("expected 3 matches, got %d", len(r.Matches))
	}
}

func TestSearchFiles_InvalidTargetMode(t *testing.T) {
	tool := &searchFilesTool{}
	result := callJSON(t, tool, `{"pattern":"test","target":"unknown"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error for invalid target")
	}
}

// ── WriteFile Additional Tests ─────────────────────────────────────────

func TestWriteFile_InvalidJSON(t *testing.T) {
	tool := &writeFileTool{}
	// Invalid JSON should return an error
	result, err := tool.Call(`{invalid json}`)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestWriteFile_PathConfinementReject(t *testing.T) {
	// Create tool with restrictToCWD and a path attempting traversal
	tool := &writeFileTool{
		restrictToCWD: true,
	}
	// ../ escape should be rejected
	result := callJSON(t, tool, `{"path":"../escape-test.txt","content":"should be rejected"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error for path confinement violation")
	}
	if !strings.Contains(r.Error, "escapes the working directory") {
		t.Errorf("error should mention escaping, got: %s", r.Error)
	}
}

func TestWriteFile_PathConfinementAllow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allowed.txt")

	// Change to temp dir for this test
	origDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(origDir)

	tool := &writeFileTool{
		restrictToCWD: true,
	}
	result := callJSON(t, tool, `{"path":"allowed.txt","content":"should succeed"}`)
	var r struct {
		Success bool   `json:"success"`
		Path    string `json:"path"`
		Error   string `json:"error,omitempty"`
	}
	mustUnmarshal(t, result, &r)
	if !r.Success {
		t.Fatalf("expected success, got error: %s", r.Error)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "should succeed" {
		t.Errorf("content = %q, want %q", string(data), "should succeed")
	}
}

func TestWriteFile_SecurityDenied(t *testing.T) {
	// Test that a deny configuration blocks the write
	action := "deny"
	dc := danger.DangerousConfig{
		DefaultAction: &action,
	}
	tool := &writeFileTool{
		dangerousConfig: dc,
	}
	result := callJSON(t, tool, `{"path":"/tmp/test-deny.txt","content":"should be denied"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error when dangerous config denies operation")
	}
}

// ── Helpers ────────────────────────────────────────────────────────────

func callJSON(t *testing.T, tool interface{ Call(args string) (string, error) }, args string) string {
	t.Helper()
	result, err := tool.Call(args)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	return result
}

func mustUnmarshal(t *testing.T, data string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(data), v); err != nil {
		t.Fatalf("json.Unmarshal: %v\nraw: %s", err, data)
	}
}

func mustSuccess(t *testing.T, result string) {
	t.Helper()
	var r struct {
		Success bool `json:"success"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal([]byte(result), &r); err != nil {
		t.Fatalf("json.Unmarshal: %v\nraw: %s", err, result)
	}
	if !r.Success && r.Error != "" {
		t.Fatalf("expected success, got error: %s", r.Error)
	}
}

func toStringSlice(v any) []string {
	switch s := v.(type) {
	case []string:
		return s
	case []any:
		result := make([]string, 0, len(s))
		for _, item := range s {
			if str, ok := item.(string); ok {
				result = append(result, str)
			}
		}
		return result
	default:
		return nil
	}
}

func containsStr(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// TestWriteFile_TOCTOU_SymlinkRejected verifies that write_file does NOT
// follow symlinks (security: TOCTOU race prevention).
// This test writes to a temp path, then replaces it with a symlink
// to a protected file, and verifies the tool refuses or replaces the symlink.
func TestWriteFile_TOCTOU_SymlinkRejected(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "target.txt")
	protectedPath := filepath.Join(dir, "protected.txt")

	// Create the protected file (what attacker wants us to overwrite)
	if err := os.WriteFile(protectedPath, []byte("sensitive data"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create target then replace with symlink to protected file
	if err := os.WriteFile(targetPath, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	os.Remove(targetPath)
	if err := os.Symlink(protectedPath, targetPath); err != nil {
		t.Fatal(err)
	}

	// Try to write to targetPath — if the tool follows the symlink,
	// protected.txt gets overwritten. Our fix should prevent this.
	tool := &writeFileTool{}
	result := callJSON(t, tool, `{"path":"`+targetPath+`","content":"overwritten!"}`)

	// After the write, protected.txt should still be intact
	data, _ := os.ReadFile(protectedPath)
	if string(data) != "sensitive data" {
		t.Errorf("protected file was overwritten! Got: %q", string(data))
	}

	// The call should have succeeded (it writes to a temp file + renames,
	// which replaces the symlink entry not the target)
	_ = result
}

// TestReadFile_CountAndContentSinglePass verifies that readLinesWithCount
// returns both content and total line count in one pass.
func TestReadFile_CountAndContentSinglePass(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "count.txt")
	content := "line1\nline2\nline3\nline4\nline5\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	gotContent, totalLines, err := readLinesWithCount(f, 1, 3)
	if err != nil {
		t.Fatalf("readLinesWithCount error: %v", err)
	}
	if totalLines != 5 {
		t.Errorf("totalLines = %d, want 5", totalLines)
	}
	if !strings.Contains(gotContent, "1|line1") || !strings.Contains(gotContent, "3|line3") {
		t.Errorf("gotContent missing expected lines: %s", gotContent)
	}
}

func TestReadLinesWithCount_NoLimit(t *testing.T) {
	// Test the limit=0 path — the code skips content output but counts lines
	dir := t.TempDir()
	path := filepath.Join(dir, "nolimit.txt")
	content := "a\nb\nc\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	gotContent, totalLines, err := readLinesWithCount(f, 1, 0)
	if err != nil {
		t.Fatalf("readLinesWithCount error: %v", err)
	}
	// When limit=0, end = offset-1, so content isn't written but count is correct
	if totalLines != 3 {
		t.Errorf("totalLines = %d, want 3", totalLines)
	}
	_ = gotContent
}

func TestReadLinesWithCount_OffsetLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "offset.txt")
	content := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	gotContent, totalLines, err := readLinesWithCount(f, 5, 3)
	if err != nil {
		t.Fatalf("readLinesWithCount error: %v", err)
	}
	if totalLines != 10 {
		t.Errorf("totalLines = %d, want 10", totalLines)
	}
	// Should contain lines 5-7
	if !strings.Contains(gotContent, "5|5") || !strings.Contains(gotContent, "7|7") {
		t.Errorf("gotContent missing expected lines: %s", gotContent)
	}
	// Should NOT contain lines outside range
	if strings.Contains(gotContent, "4|4") || strings.Contains(gotContent, "8|8") {
		t.Errorf("gotContent has lines outside offset/limit: %s", gotContent)
	}
}

func TestReadLinesWithCount_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	gotContent, totalLines, err := readLinesWithCount(f, 1, 10)
	if err != nil {
		t.Fatalf("readLinesWithCount error: %v", err)
	}
	if totalLines != 0 {
		t.Errorf("totalLines = %d, want 0", totalLines)
	}
	if gotContent != "" {
		t.Errorf("gotContent = %q, want empty", gotContent)
	}
}

// defaultTestDangerousConfig returns a permissive DangerousConfig for tests.
func defaultTestDangerousConfig() DangerConfig {
	return DangerConfig{
		Action: "allow",
		NonInteractive: "allow",
		Classes: map[RiskClass]string{
			RiskClassDestructive:    "allow",
			RiskClassNetworkEgress:  "allow",
			RiskClassCodeExecution:  "allow",
			RiskClassInstall:        "allow",
			RiskClassSystemWrite:    "allow",
		},
	}
}

// DangerConfig matches the danger.DangerousConfig structure for test use.
type DangerConfig struct {
	Action         string                 `json:"action"`
	NonInteractive string                 `json:"non_interactive"`
	Classes        map[RiskClass]string   `json:"classes"`
	Allowlist      []string               `json:"allowlist,omitempty"`
	Denylist       []string               `json:"denylist,omitempty"`
}

type RiskClass string

const (
	RiskClassDestructive   RiskClass = "destructive"
	RiskClassNetworkEgress RiskClass = "network_egress"
	RiskClassCodeExecution RiskClass = "code_execution"
	RiskClassInstall       RiskClass = "install"
	RiskClassSystemWrite   RiskClass = "system_write"
)

// ── ReadFile O_NOFOLLOW Tests ─────────────────────────────────────────

func TestReadFile_SymlinkRefused(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(targetPath, []byte("real content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(dir, "link.txt")
	if err := os.Symlink("target.txt", linkPath); err != nil {
		t.Fatal(err)
	}

	tool := &readFileTool{}
	result := callJSON(t, tool, `{"path":"`+linkPath+`"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error when reading a symlink (O_NOFOLLOW)")
	}
	if !strings.Contains(r.Error, "cannot open") {
		t.Errorf("error should mention 'cannot open', got: %s", r.Error)
	}

	// Verify the real file was NOT read through the symlink
	data, _ := os.ReadFile(targetPath)
	if string(data) != "real content\n" {
		t.Errorf("target file content changed: %q", string(data))
	}
}

// ── SearchFiles Absolute Path Tests ───────────────────────────────────

func TestSearchFiles_AbsolutePath(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "data.txt"), []byte("needle in haystack\n"), 0644)

	tool := &searchFilesTool{}
	result := callJSON(t, tool, `{"pattern":"needle","target":"content","path":"`+dir+`"}`)
	var r struct {
		Matches []struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		} `json:"matches"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(r.Matches))
	}
	if !strings.Contains(r.Matches[0].Content, "needle") {
		t.Errorf("match content should contain 'needle', got: %s", r.Matches[0].Content)
	}
}

func TestSearchFiles_FilesTargetEmpty(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644)

	tool := &searchFilesTool{}
	result := callJSON(t, tool, `{"pattern":"*.py","target":"files","path":"`+dir+`"}`)
	var r struct {
		Matches []any `json:"matches"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Matches) != 0 {
		t.Errorf("expected 0 matches for non-existent glob, got %d", len(r.Matches))
	}
}

func TestSearchFiles_FilesTargetWithPathSeparator(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "sub")
	os.Mkdir(subDir, 0755)
	os.WriteFile(filepath.Join(dir, "root.txt"), []byte("data\n"), 0644)
	os.WriteFile(filepath.Join(subDir, "nested.txt"), []byte("data\n"), 0644)

	tool := &searchFilesTool{}
	result := callJSON(t, tool, `{"pattern":"sub/*.txt","target":"files","path":"`+dir+`"}`)
	var r struct {
		Matches []struct {
			Path string `json:"path"`
		} `json:"matches"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Matches) != 1 {
		t.Fatalf("expected 1 match for 'sub/*.txt', got %d", len(r.Matches))
	}
	if !strings.Contains(r.Matches[0].Path, "nested.txt") {
		t.Errorf("expected nested.txt match, got: %s", r.Matches[0].Path)
	}
}

func TestSearchFiles_FilesTargetHiddenDirSkipped(t *testing.T) {
	dir := t.TempDir()
	hiddenDir := filepath.Join(dir, ".hidden")
	os.Mkdir(hiddenDir, 0755)
	os.WriteFile(filepath.Join(hiddenDir, "secret.go"), []byte("package main\n"), 0644)
	os.WriteFile(filepath.Join(dir, "visible.go"), []byte("package main\n"), 0644)

	tool := &searchFilesTool{}
	result := callJSON(t, tool, `{"pattern":"*.go","target":"files","path":"`+dir+`"}`)
	var r struct {
		Matches []struct {
			Path string `json:"path"`
		} `json:"matches"`
	}
	mustUnmarshal(t, result, &r)
	for _, m := range r.Matches {
		if strings.Contains(m.Path, ".hidden") {
			t.Errorf("should not include hidden dir contents: %s", m.Path)
		}
	}
	if len(r.Matches) != 1 {
		t.Errorf("expected 1 match (visible.go), got %d", len(r.Matches))
	}
}

// ── WriteFile TrustedClasses Tests ────────────────────────────────────

func TestWriteFile_TrustedClassesDeny(t *testing.T) {
	// Config with class-level action=prompt + non-interactive deny
	// When a class is NOT in trustedClasses, non-interactive mode rejects it
	prompt := "prompt"
	deny := "deny"
	dc := danger.DangerousConfig{
		DefaultAction:  &prompt,
		NonInteractive: &deny,
	}
	// Empty trusted classes — nothing is trusted
	trusted := make(map[danger.RiskClass]bool)
	tool := &writeFileTool{
		dangerousConfig: dc,
		trustedClasses:  trusted,
	}
	result := callJSON(t, tool, `{"path":"`+filepath.Join(t.TempDir(), "test.txt")+`","content":"should be denied"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error when trusted classes deny operation")
	}
}

func TestWriteFile_TrustedClassesAllow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trusted.txt")

	// Config with class-level action=prompt + non-interactive deny
	// When a class IS in trustedClasses, it bypasses the prompt
	prompt := "prompt"
	deny := "deny"
	dc := danger.DangerousConfig{
		DefaultAction:  &prompt,
		NonInteractive: &deny,
	}
	// trustedClasses marks LocalWrite as trusted
	trusted := map[danger.RiskClass]bool{
		danger.LocalWrite: true,
	}

	tool := &writeFileTool{
		dangerousConfig: dc,
		trustedClasses:  trusted,
	}

	result := callJSON(t, tool, `{"path":"`+path+`","content":"should be allowed via trusted class"}`)
	var r struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if !r.Success {
		t.Fatalf("expected success with trusted class, got error: %s", r.Error)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "should be allowed via trusted class" {
		t.Errorf("content = %q, want %q", string(data), "should be allowed via trusted class")
	}
}

// ── Patch Tool Edge Cases ─────────────────────────────────────────────

func TestPatch_InvalidJSONArgs(t *testing.T) {
	tool := &patchTool{}
	result, err := tool.Call(`{invalid}`)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(r.Error, "invalid arguments") {
		t.Errorf("error should mention 'invalid arguments', got: %s", r.Error)
	}
}

// ── jsonResult Tests ──────────────────────────────────────────────────

func TestJsonResult_Error(t *testing.T) {
	// json.Marshal fails on functions — but jsonResult wraps the error
	// in a JSON response, so it returns nil error.
	result, err := jsonResult(func() {})
	if err != nil {
		t.Fatalf("jsonResult should return nil error, got: %v", err)
	}
	// The error should be in the JSON string
	if !strings.Contains(result, `"error"`) {
		t.Errorf("result should contain 'error' field, got: %s", result)
	}
	if !strings.Contains(result, "marshal error") {
		t.Errorf("result should mention 'marshal error', got: %s", result)
	}
}

func TestJsonResult_Success(t *testing.T) {
	result, err := jsonResult(map[string]string{"key": "value"})
	if err != nil {
		t.Fatalf("jsonResult error: %v", err)
	}
	if !strings.Contains(result, `"key":"value"`) {
		t.Errorf("result = %q, want key:value", result)
	}
}

// ── searchContent Limit Tests ─────────────────────────────────────────

func TestSearchContent_LimitReached(t *testing.T) {
	dir := t.TempDir()
	// Create files that match the search pattern
	for i := 0; i < 3; i++ {
		content := fmt.Sprintf("line with match_%d\n", i)
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%d.txt", i)), []byte(content), 0644)
	}

	tool := &searchFilesTool{}
	// Set limit to 1 — should stop after first match
	result := callJSON(t, tool, `{"pattern":"match","target":"content","path":"`+dir+`","limit":1}`)
	var r struct {
		Matches []any `json:"matches"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Matches) == 0 {
		t.Fatal("expected at least 1 match")
	}
	if len(r.Matches) > 2 {
		t.Errorf("limit=1 should return at most 2 matches, got %d", len(r.Matches))
	}
}

func TestSearchFiles_SkipsBuildDirs(t *testing.T) {
	dir := t.TempDir()
	// Create a file in the root search dir
	os.WriteFile(filepath.Join(dir, "work.go"), []byte("package main\nfunc init() {}\n"), 0644)
	// Create node_modules with a file that should NOT be found
	nmDir := filepath.Join(dir, "node_modules")
	os.MkdirAll(nmDir, 0755)
	os.WriteFile(filepath.Join(nmDir, "dep.js"), []byte("module.exports.init = function() {}\n"), 0644)
	// Create __pycache__ with a file that should NOT be found
	pcDir := filepath.Join(dir, "__pycache__")
	os.MkdirAll(pcDir, 0755)
	os.WriteFile(filepath.Join(pcDir, "cache.py"), []byte("def init():\n    pass\n"), 0644)
	// Create vendor with a file that should NOT be found
	vDir := filepath.Join(dir, "vendor")
	os.MkdirAll(vDir, 0755)
	os.WriteFile(filepath.Join(vDir, "lib.go"), []byte("package vendor\nfunc Init() {}\n"), 0644)

	tool := &searchFilesTool{}

	// Search for "init" — should only find work.go
	result := callJSON(t, tool, `{"pattern":"init","target":"content","path":"`+dir+`"}`)
	var r struct {
		Matches []struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		} `json:"matches"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Matches) == 0 {
		t.Fatal("expected at least 1 match (work.go)")
	}
	for _, m := range r.Matches {
		rel, _ := filepath.Rel(dir, m.Path)
		if strings.Contains(rel, "node_modules") ||
			strings.Contains(rel, "__pycache__") ||
			strings.Contains(rel, "vendor") {
			t.Errorf("search matched file inside skipped dir: %s", rel)
		}
	}
	// Verify work.go IS found
	foundWork := false
	for _, m := range r.Matches {
		if strings.HasSuffix(m.Path, "work.go") {
			foundWork = true
			break
		}
	}
	if !foundWork {
		t.Error("work.go should have been found (outside build dirs)")
	}
}

// ── BatchRead Tool Tests ──────────────────────────────────────────────

func TestBatchRead_Basic(t *testing.T) {
	dir := t.TempDir()
	path1 := filepath.Join(dir, "a.txt")
	path2 := filepath.Join(dir, "b.txt")
	os.WriteFile(path1, []byte("line1\nline2\nline3\n"), 0644)
	os.WriteFile(path2, []byte("hello\nworld\n"), 0644)

	tool := &batchReadTool{}
	args := fmt.Sprintf(`{"files":[{"path":"%s"},{"path":"%s"}]}`, path1, path2)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Path       string `json:"path"`
			Content    string `json:"content"`
			TotalLines int    `json:"total_lines"`
			Error      string `json:"error,omitempty"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Results) != 2 {
		t.Fatalf("Results len = %d, want 2", len(r.Results))
	}
	if r.Results[0].Error != "" {
		t.Errorf("file 0 error: %s", r.Results[0].Error)
	}
	if r.Results[1].TotalLines != 2 {
		t.Errorf("file 1 TotalLines = %d, want 2", r.Results[1].TotalLines)
	}
	if !strings.Contains(r.Results[1].Content, "hello") {
		t.Errorf("file 1 missing 'hello': %q", r.Results[1].Content)
	}
}

func TestBatchRead_NotFound(t *testing.T) {
	tool := &batchReadTool{}
	result := callJSON(t, tool, `{"files":[{"path":"/nonexistent/file.txt"}]}`)

	var r struct {
		Results []struct {
			Path  string `json:"path"`
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Results) != 1 {
		t.Fatalf("Results len = %d, want 1", len(r.Results))
	}
	if r.Results[0].Error == "" {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestBatchRead_MaxFiles(t *testing.T) {
	tool := &batchReadTool{}
	// Create a request with more than 10 files
	files := make([]string, 11)
	for i := range files {
		files[i] = fmt.Sprintf(`{"path":"test%d.txt"}`, i)
	}
	args := `{"files":[` + strings.Join(files, ",") + `]}`
	result := callJSON(t, tool, args)

	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if !strings.Contains(r.Error, "max 10") {
		t.Errorf("error should mention max 10, got: %s", r.Error)
	}
}

func TestBatchRead_PartialFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("a\nb\nc\nd\ne\nf\n"), 0644)

	tool := &batchReadTool{}
	args := fmt.Sprintf(`{"files":[{"path":"%s","offset":2,"limit":2}]}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Content    string `json:"content"`
			TotalLines int    `json:"total_lines"`
			Error      string `json:"error,omitempty"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Results) != 1 {
		t.Fatalf("Results len = %d, want 1", len(r.Results))
	}
	if r.Results[0].Error != "" {
		t.Fatalf("error: %s", r.Results[0].Error)
	}
	if r.Results[0].TotalLines != 6 {
		t.Errorf("TotalLines = %d, want 6", r.Results[0].TotalLines)
	}
	if !strings.Contains(r.Results[0].Content, "2|b") {
		t.Errorf("Content should start at line 2: %q", r.Results[0].Content)
	}
	if strings.Contains(r.Results[0].Content, "4|d") {
		t.Errorf("Content should not contain line 4 (limit=2): %q", r.Results[0].Content)
	}
}

func TestBatchRead_Directory(t *testing.T) {
	dir := t.TempDir()
	tool := &batchReadTool{}
	args := fmt.Sprintf(`{"files":[{"path":"%s"}]}`, dir)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Results) != 1 {
		t.Fatalf("Results len = %d, want 1", len(r.Results))
	}
	if !strings.Contains(r.Results[0].Error, "directory") {
		t.Errorf("error should mention 'directory', got: %s", r.Results[0].Error)
	}
}

func TestBatchRead_EmptyFiles(t *testing.T) {
	tool := &batchReadTool{}
	result := callJSON(t, tool, `{"files":[]}`)

	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if !strings.Contains(r.Error, "at least one file") {
		t.Errorf("error should mention 'at least one file', got: %s", r.Error)
	}
}

func TestBatchRead_InvalidJSON(t *testing.T) {
	tool := &batchReadTool{}
	result := callJSON(t, tool, `{invalid}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if !strings.Contains(r.Error, "invalid arguments") {
		t.Errorf("error should mention 'invalid arguments', got: %s", r.Error)
	}
}

// ── Glob Tool Tests ───────────────────────────────────────────────────

func TestGlob_Basic(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b\n"), 0644)
	os.WriteFile(filepath.Join(dir, "c.py"), []byte("print('hello')\n"), 0644)

	tool := &globTool{}
	args := fmt.Sprintf(`{"pattern":"*.go","path":"%s"}`, dir)
	result := callJSON(t, tool, args)

	var r struct {
		Matches []struct {
			Path  string `json:"path"`
			Size  int64  `json:"size"`
			IsDir bool   `json:"is_dir"`
		} `json:"matches"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Matches) != 2 {
		t.Fatalf("Matches = %d, want 2", len(r.Matches))
	}
	for _, m := range r.Matches {
		if m.IsDir {
			t.Errorf("%s should not be a directory", m.Path)
		}
		if m.Size < 1 {
			t.Errorf("%s size = %d, want > 0", m.Path, m.Size)
		}
	}
}

func TestGlob_NoMatches(t *testing.T) {
	dir := t.TempDir()
	tool := &globTool{}
	args := fmt.Sprintf(`{"pattern":"*.xyz","path":"%s"}`, dir)
	result := callJSON(t, tool, args)

	var r struct {
		Matches []struct {
			Path string `json:"path"`
		} `json:"matches"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Matches) != 0 {
		t.Errorf("Matches = %d, want 0", len(r.Matches))
	}
}

func TestGlob_EmptyPattern(t *testing.T) {
	tool := &globTool{}
	result := callJSON(t, tool, `{"pattern":""}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if !strings.Contains(r.Error, "pattern is required") {
		t.Errorf("error should mention 'pattern', got: %s", r.Error)
	}
}

// ── FileInfo Tool Tests ───────────────────────────────────────────────

func TestFileInfo_File(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	content := []byte("hello world\n")
	os.WriteFile(path, content, 0644)

	tool := &fileInfoTool{}
	args := fmt.Sprintf(`{"path":"%s"}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Path      string `json:"path"`
		Size      int64  `json:"size"`
		ModTime   string `json:"mod_time"`
		Mode      string `json:"mode"`
		IsDir     bool   `json:"is_dir"`
		IsSymlink bool   `json:"is_symlink"`
		IsRegular bool   `json:"is_regular"`
		Error     string `json:"error,omitempty"`
	}
	mustUnmarshal(t, result, &r)

	if r.Error != "" {
		t.Fatalf("error: %s", r.Error)
	}
	if r.Size != int64(len(content)) {
		t.Errorf("Size = %d, want %d", r.Size, len(content))
	}
	if !r.IsRegular {
		t.Errorf("IsRegular should be true")
	}
	if r.IsDir {
		t.Errorf("IsDir should be false")
	}
	if r.ModTime == "" {
		t.Errorf("ModTime should not be empty")
	}
	if r.Mode == "" {
		t.Errorf("Mode should not be empty")
	}
}

func TestFileInfo_Directory(t *testing.T) {
	dir := t.TempDir()
	tool := &fileInfoTool{}
	args := fmt.Sprintf(`{"path":"%s"}`, dir)
	result := callJSON(t, tool, args)

	var r struct {
		IsDir     bool   `json:"is_dir"`
		IsRegular bool   `json:"is_regular"`
		IsSymlink bool   `json:"is_symlink"`
		Error     string `json:"error,omitempty"`
	}
	mustUnmarshal(t, result, &r)

	if r.Error != "" {
		t.Fatalf("error: %s", r.Error)
	}
	if !r.IsDir {
		t.Errorf("IsDir should be true for directory")
	}
	if r.IsRegular {
		t.Errorf("IsRegular should be false for directory")
	}
}

func TestFileInfo_NotFound(t *testing.T) {
	tool := &fileInfoTool{}
	result := callJSON(t, tool, `{"path":"/nonexistent_path_xyz"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if !strings.Contains(r.Error, "file not found") {
		t.Errorf("error should mention 'file not found', got: %s", r.Error)
	}
}

func TestFileInfo_EmptyPath(t *testing.T) {
	tool := &fileInfoTool{}
	result := callJSON(t, tool, `{"path":""}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if !strings.Contains(r.Error, "path is required") {
		t.Errorf("error should mention 'path is required', got: %s", r.Error)
	}
}


