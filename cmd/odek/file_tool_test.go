package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	path := filepath.Join(dir, "test.txt")
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
