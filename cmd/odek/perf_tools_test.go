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

// ── BatchPatch Tests ──────────────────────────────────────────────────

func TestBatchPatch_Basic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello world\nfoo bar\n"), 0644)

	tool := &batchPatchTool{}
	args := fmt.Sprintf(`{"patches":[{"path":"%s","old_string":"foo","new_string":"baz"}]}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Path    string `json:"path"`
			Success bool   `json:"success"`
			Error   string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Results) != 1 {
		t.Fatalf("Results = %d, want 1", len(r.Results))
	}
	if !r.Results[0].Success {
		t.Fatalf("patch failed: %s", r.Results[0].Error)
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "baz") {
		t.Errorf("file should contain 'baz', got: %s", string(data))
	}
}

// TestBatchPatch_PathConfinement verifies batch_patch enforces the same
// restrictToCWD confinement as write_file/patch: escapes are rejected
// per-entry, and odek's trust anchors are excluded from the ~/.odek/
// carve-out.
func TestBatchPatch_PathConfinement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ok.txt")
	os.WriteFile(path, []byte("hello world\n"), 0644)

	origDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(origDir)

	home, _ := os.UserHomeDir()
	tool := &batchPatchTool{restrictToCWD: true}
	args := fmt.Sprintf(`{"patches":[
		{"path":"ok.txt","old_string":"world","new_string":"there"},
		{"path":"../escape.txt","old_string":"a","new_string":"b"},
		{"path":"%s/.odek/config.json","old_string":"a","new_string":"b"}
	]}`, home)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Results) != 3 {
		t.Fatalf("Results = %d, want 3", len(r.Results))
	}
	if !r.Results[0].Success {
		t.Errorf("in-CWD patch should succeed: %s", r.Results[0].Error)
	}
	if r.Results[1].Success || !strings.Contains(r.Results[1].Error, "escapes the working directory") {
		t.Errorf("escape should be rejected, got success=%v err=%q", r.Results[1].Success, r.Results[1].Error)
	}
	if r.Results[2].Success || r.Results[2].Error == "" {
		t.Errorf("~/.odek/config.json should be rejected, got success=%v err=%q", r.Results[2].Success, r.Results[2].Error)
	}
}

func TestBatchPatch_MultipleFiles(t *testing.T) {
	dir := t.TempDir()
	path1 := filepath.Join(dir, "a.txt")
	path2 := filepath.Join(dir, "b.txt")
	os.WriteFile(path1, []byte("hello"), 0644)
	os.WriteFile(path2, []byte("world"), 0644)

	tool := &batchPatchTool{}
	args := fmt.Sprintf(`{"patches":[
		{"path":"%s","old_string":"hello","new_string":"hi"},
		{"path":"%s","old_string":"world","new_string":"earth"}
	]}`, path1, path2)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Results) != 2 {
		t.Fatalf("Results = %d, want 2", len(r.Results))
	}
	for i, res := range r.Results {
		if !res.Success {
			t.Errorf("patch %d failed: %s", i, res.Error)
		}
	}
}

func TestBatchPatch_ContinueOnError(t *testing.T) {
	dir := t.TempDir()
	path1 := filepath.Join(dir, "a.txt")
	os.WriteFile(path1, []byte("hello"), 0644)

	tool := &batchPatchTool{}
	args := fmt.Sprintf(`{"patches":[
		{"path":"%s","old_string":"hello","new_string":"hi"},
		{"path":"/nonexistent/file.txt","old_string":"x","new_string":"y"},
		{"path":"%s","old_string":"hi","new_string":"bye"}
	]}`, path1, path1)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Results) != 3 {
		t.Fatalf("Results = %d, want 3", len(r.Results))
	}
	if !r.Results[0].Success {
		t.Errorf("first patch should succeed")
	}
	if r.Results[1].Error == "" {
		t.Errorf("second patch should have error (file not found)")
	}
	if !r.Results[2].Success {
		t.Errorf("third patch should succeed (independent of second patch failure), got: %s", r.Results[2].Error)
	}
	// Verify file content: first patch changed hello→hi, third changed hi→bye
	data, _ := os.ReadFile(path1)
	if string(data) != "bye" {
		t.Errorf("file content should be 'bye', got: %s", string(data))
	}
}

// ── ParallelShell Tests ───────────────────────────────────────────────

func TestParallelShell_Basic(t *testing.T) {
	tool := &parallelShellTool{}
	result := callJSON(t, tool, `{"commands":[
		{"command":"echo hello"},
		{"command":"echo world"}
	]}`)

	var r struct {
		Results []struct {
			Stdout   string `json:"stdout"`
			ExitCode int    `json:"exit_code"`
			Error    string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Results) != 2 {
		t.Fatalf("Results = %d, want 2", len(r.Results))
	}
	if unwrapUntrusted(r.Results[0].Stdout) != "hello" {
		t.Errorf("cmd 0 stdout = %q, want 'hello'", r.Results[0].Stdout)
	}
	if unwrapUntrusted(r.Results[1].Stdout) != "world" {
		t.Errorf("cmd 1 stdout = %q, want 'world'", r.Results[1].Stdout)
	}
}

func TestParallelShell_Error(t *testing.T) {
	tool := &parallelShellTool{}
	result := callJSON(t, tool, `{"commands":[{"command":"false"}]}`)

	var r struct {
		Results []struct {
			ExitCode int    `json:"exit_code"`
			Error    string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Results) != 1 {
		t.Fatalf("Results = %d, want 1", len(r.Results))
	}
	if r.Results[0].ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", r.Results[0].ExitCode)
	}
}

// ── HTTPBatch Tests ───────────────────────────────────────────────────

func TestHTTPBatch_InvalidURL(t *testing.T) {
	tool := newHTTPBatchTool(danger.DangerousConfig{})
	result := callJSON(t, tool, `{"requests":[{"url":"not-a-url"}]}`)

	var r struct {
		Results []struct {
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Results) != 1 {
		t.Fatalf("Results = %d, want 1", len(r.Results))
	}
	if r.Results[0].Error == "" {
		t.Errorf("expected error for invalid URL")
	}
}

// ── MathEval Tests ────────────────────────────────────────────────────

func TestMathEval_Basic(t *testing.T) {
	tool := &mathEvalTool{}
	result := callJSON(t, tool, `{"expression":"42 * 17"}`)

	var r struct {
		Result float64 `json:"result"`
		Error  string  `json:"error"`
	}
	mustUnmarshal(t, result, &r)

	if r.Error != "" {
		t.Fatalf("error: %s", r.Error)
	}
	if r.Result != 714 {
		t.Errorf("42*17 = %f, want 714", r.Result)
	}
}

func TestMathEval_Chained(t *testing.T) {
	tool := &mathEvalTool{}
	// 42 * 17 = 714, + 256 = 970, / 10 = 97
	result := callJSON(t, tool, `{"expression":"42 * 17 + 256"}`)

	var r struct {
		Result float64 `json:"result"`
	}
	mustUnmarshal(t, result, &r)

	if r.Result != 970 {
		t.Errorf("42 * 17 + 256 = %f, want 970", r.Result)
	}
}

func TestMathEval_Division(t *testing.T) {
	tool := &mathEvalTool{}
	result := callJSON(t, tool, `{"expression":"(42 * 17 + 256) / 10"}`)

	var r struct {
		Result float64 `json:"result"`
	}
	mustUnmarshal(t, result, &r)

	if r.Result != 97 {
		t.Errorf("(42*17+256)/10 = %f, want 97", r.Result)
	}
}

func TestMathEval_Steps(t *testing.T) {
	// The exact quick_math benchmark asks for intermediate steps
	tool := &mathEvalTool{}
	cases := []struct {
		expr string
		want float64
	}{
		{"42 * 17", 714},
		{"714 + 256", 970},
		{"970 / 10", 97},
	}
	for _, c := range cases {
		result := callJSON(t, tool, fmt.Sprintf(`{"expression":"%s"}`, c.expr))
		var r struct {
			Result float64 `json:"result"`
			Error  string  `json:"error"`
		}
		mustUnmarshal(t, result, &r)
		if r.Error != "" {
			t.Errorf("%s: error: %s", c.expr, r.Error)
		} else if r.Result != c.want {
			t.Errorf("%s = %f, want %f", c.expr, r.Result, c.want)
		}
	}
}

func TestMathEval_EmptyExpression(t *testing.T) {
	tool := &mathEvalTool{}
	result := callJSON(t, tool, `{"expression":""}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if !strings.Contains(r.Error, "expression is required") {
		t.Errorf("error should mention 'expression', got: %s", r.Error)
	}
}

// ── Diff Tests ────────────────────────────────────────────────────────

func TestDiff_FileVsFile(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.txt")
	pathB := filepath.Join(dir, "b.txt")
	os.WriteFile(pathA, []byte("hello\nworld\n"), 0644)
	os.WriteFile(pathB, []byte("hello\nearth\n"), 0644)

	tool := &diffTool{}
	args := fmt.Sprintf(`{"path_a":"%s","path_b":"%s"}`, pathA, pathB)
	result := callJSON(t, tool, args)

	var r struct {
		Hunks []struct {
			Type  string `json:"type"`
			Lines []any  `json:"lines"`
		} `json:"hunks"`
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)

	if r.Error != "" {
		t.Fatalf("error: %s", r.Error)
	}
	if len(r.Hunks) == 0 {
		t.Fatal("expected at least 1 hunk")
	}
}

func TestDiff_FileVsString(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("line1\nline2\n"), 0644)

	tool := &diffTool{}
	args := fmt.Sprintf(`{"path":"%s","content":"line1\nchanged\n"}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Hunks []struct {
			Type  string `json:"type"`
			Lines []any  `json:"lines"`
		} `json:"hunks"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Hunks) == 0 {
		t.Fatal("expected hunks for changed content")
	}
}

func TestDiff_IdenticalFiles(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.txt")
	pathB := filepath.Join(dir, "b.txt")
	os.WriteFile(pathA, []byte("same\ncontent\n"), 0644)
	os.WriteFile(pathB, []byte("same\ncontent\n"), 0644)

	tool := &diffTool{}
	args := fmt.Sprintf(`{"path_a":"%s","path_b":"%s"}`, pathA, pathB)
	result := callJSON(t, tool, args)

	var r struct {
		Hunks []struct {
			Type string `json:"type"`
		} `json:"hunks"`
	}
	mustUnmarshal(t, result, &r)

	// All hunks should be "equal"
	for _, h := range r.Hunks {
		if h.Type != "equal" {
			t.Errorf("expected all 'equal' hunks, got %q", h.Type)
		}
	}
}

// ── CountLines Tests ──────────────────────────────────────────────────

func TestCountLines_Basic(t *testing.T) {
	dir := t.TempDir()
	path1 := filepath.Join(dir, "a.txt")
	path2 := filepath.Join(dir, "b.txt")
	os.WriteFile(path1, []byte("line1\nline2\nline3\n"), 0644)
	os.WriteFile(path2, []byte("hello\n"), 0644)

	tool := &countLinesTool{}
	args := fmt.Sprintf(`{"files":[{"path":"%s"},{"path":"%s"}]}`, path1, path2)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Path  string `json:"path"`
			Lines int    `json:"lines"`
			Bytes int64  `json:"bytes"`
			Chars int    `json:"chars"`
			Error string `json:"error"`
		} `json:"results"`
		Total struct {
			Lines int   `json:"lines"`
			Bytes int64 `json:"bytes"`
		} `json:"total"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Results) != 2 {
		t.Fatalf("Results = %d, want 2", len(r.Results))
	}
	if r.Results[0].Lines != 3 {
		t.Errorf("file 0 lines = %d, want 3", r.Results[0].Lines)
	}
	if r.Results[1].Lines != 1 {
		t.Errorf("file 1 lines = %d, want 1", r.Results[1].Lines)
	}
	if r.Total.Lines != 4 {
		t.Errorf("total lines = %d, want 4", r.Total.Lines)
	}
}

func TestCountLines_NotFound(t *testing.T) {
	tool := &countLinesTool{}
	result := callJSON(t, tool, `{"files":[{"path":"/nonexistent"}]}`)

	var r struct {
		Results []struct {
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)

	if r.Results[0].Error == "" {
		t.Errorf("expected error for nonexistent file")
	}
}

// ── MultiGrep Tests ───────────────────────────────────────────────────

func TestMultiGrep_Basic(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("TODO: fix this\nFIXME: later\n"), 0644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("TODO: also this\n"), 0644)

	tool := &multiGrepTool{}
	args := fmt.Sprintf(`{"patterns":["TODO","FIXME"],"path":"%s"}`, dir)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Pattern string `json:"pattern"`
			Count   int    `json:"count"`
			Error   string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Results) != 2 {
		t.Fatalf("Results = %d, want 2", len(r.Results))
	}
	if r.Results[0].Count+r.Results[1].Count != 3 {
		t.Errorf("total matches should be 3, got TODO:%d FIXME:%d",
			r.Results[0].Count, r.Results[1].Count)
	}
}

func TestMultiGrep_EmptyPatterns(t *testing.T) {
	tool := &multiGrepTool{}
	result := callJSON(t, tool, `{"patterns":[]}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if !strings.Contains(r.Error, "at least one pattern") {
		t.Errorf("error should mention 'pattern', got: %s", r.Error)
	}
}

// ── JSONQuery Tests ───────────────────────────────────────────────────

func TestJSONQuery_Basic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	os.WriteFile(path, []byte(`{"name":"Alice","age":30,"items":[1,2,3]}`), 0644)

	tool := &jsonQueryTool{}
	args := fmt.Sprintf(`{"path":"%s","query":"name"}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Value     interface{} `json:"value"`
		ValueType string      `json:"value_type"`
		Error     string      `json:"error"`
	}
	mustUnmarshal(t, result, &r)

	if r.Error != "" {
		t.Fatalf("error: %s", r.Error)
	}
	if got, ok := r.Value.(string); !ok || unwrapUntrusted(got) != "Alice" {
		t.Errorf("value = %v, want 'Alice'", r.Value)
	}
}

func TestJSONQuery_ArrayIndex(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	os.WriteFile(path, []byte(`{"users":[{"name":"Alice"},{"name":"Bob"}]}`), 0644)

	tool := &jsonQueryTool{}
	args := fmt.Sprintf(`{"path":"%s","query":"users[1].name"}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Value interface{} `json:"value"`
	}
	mustUnmarshal(t, result, &r)

	if got, ok := r.Value.(string); !ok || unwrapUntrusted(got) != "Bob" {
		t.Errorf("value = %v, want 'Bob'", r.Value)
	}
}

func TestJSONQuery_EmptyQuery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	os.WriteFile(path, []byte(`{"key":"value"}`), 0644)

	tool := &jsonQueryTool{}
	args := fmt.Sprintf(`{"path":"%s","query":""}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Value     interface{} `json:"value"`
		ValueType string      `json:"value_type"`
	}
	mustUnmarshal(t, result, &r)

	if r.Value == nil {
		t.Errorf("expected full JSON, got nil")
	}
}

// ── Tree Tests ────────────────────────────────────────────────────────

func TestTree_Basic(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b\n"), 0644)
	subdir := filepath.Join(dir, "sub")
	os.Mkdir(subdir, 0755)
	os.WriteFile(filepath.Join(subdir, "c.go"), []byte("package c\n"), 0644)

	tool := &treeTool{}
	args := fmt.Sprintf(`{"path":"%s","max_depth":3}`, dir)
	result := callJSON(t, tool, args)

	var r struct {
		Tree struct {
			IsDir     bool   `json:"is_dir"`
			FileCount int    `json:"file_count"`
			TotalSize int64  `json:"total_size"`
			Children  []any  `json:"children"`
			ErrMsg    string `json:"error"`
		} `json:"tree"`
	}
	mustUnmarshal(t, result, &r)

	if r.Tree.ErrMsg != "" {
		t.Fatalf("error: %s", r.Tree.ErrMsg)
	}
	if r.Tree.FileCount == 0 {
		t.Errorf("expected files in tree, got 0")
	}
	if r.Tree.TotalSize <= 0 {
		t.Errorf("expected total_size > 0")
	}
}

// ── Checksum Tests ────────────────────────────────────────────────────

func TestChecksum_Basic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello\n"), 0644)

	tool := &checksumTool{}
	args := fmt.Sprintf(`{"files":[{"path":"%s","algorithm":"sha256"}]}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Path      string `json:"path"`
			Algorithm string `json:"algorithm"`
			Hash      string `json:"hash"`
			Error     string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Results) != 1 {
		t.Fatalf("Results = %d, want 1", len(r.Results))
	}
	if r.Results[0].Error != "" {
		t.Fatalf("error: %s", r.Results[0].Error)
	}
	if r.Results[0].Algorithm != "sha256" {
		t.Errorf("algorithm = %q, want sha256", r.Results[0].Algorithm)
	}
	if len(r.Results[0].Hash) != 64 {
		t.Errorf("SHA256 hash length = %d, want 64", len(r.Results[0].Hash))
	}
}

func TestChecksum_MultipleAlgorithms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("test data\n"), 0644)

	tool := &checksumTool{}
	args := fmt.Sprintf(`{"files":[
		{"path":"%s","algorithm":"sha256"},
		{"path":"%s","algorithm":"sha1"},
		{"path":"%s","algorithm":"md5"}
	]}`, path, path, path)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Algorithm string `json:"algorithm"`
			Hash      string `json:"hash"`
			Error     string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Results) != 3 {
		t.Fatalf("Results = %d, want 3", len(r.Results))
	}
	if len(r.Results[0].Hash) != 64 {
		t.Errorf("SHA256 length = %d, want 64", len(r.Results[0].Hash))
	}
	if len(r.Results[1].Hash) != 40 {
		t.Errorf("SHA1 length = %d, want 40", len(r.Results[1].Hash))
	}
	if len(r.Results[2].Hash) != 32 {
		t.Errorf("MD5 length = %d, want 32", len(r.Results[2].Hash))
	}
}

func TestChecksum_DefaultAlgorithm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("data\n"), 0644)

	tool := &checksumTool{}
	args := fmt.Sprintf(`{"files":[{"path":"%s"}]}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Algorithm string `json:"algorithm"`
			Hash      string `json:"hash"`
			Error     string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)

	if r.Results[0].Algorithm != "sha256" {
		t.Errorf("default algorithm = %q, want sha256", r.Results[0].Algorithm)
	}
}

// ── Sort Tests ───────────────────────────────────────────────────────

func TestSort_Basic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("c\nb\na\n"), 0644)

	tool := &sortTool{}
	args := fmt.Sprintf(`{"path":"%s"}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Output string `json:"output"`
		Total  int    `json:"total"`
		Error  string `json:"error,omitempty"`
	}
	mustUnmarshal(t, result, &r)

	if r.Error != "" {
		t.Fatalf("error: %s", r.Error)
	}
	if r.Total != 3 {
		t.Errorf("total = %d, want 3", r.Total)
	}
	if unwrapUntrusted(r.Output) != "a\nb\nc" {
		t.Errorf("output = %q, want a\\nb\\nc", r.Output)
	}
}

func TestSort_Desc(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("a\nb\nc\n"), 0644)

	tool := &sortTool{}
	args := fmt.Sprintf(`{"path":"%s","order":"desc"}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Output string `json:"output"`
	}
	mustUnmarshal(t, result, &r)

	if unwrapUntrusted(r.Output) != "c\nb\na" {
		t.Errorf("output = %q, want c\\nb\\na", r.Output)
	}
}

func TestSort_Unique(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("a\nb\na\nc\nb\n"), 0644)

	tool := &sortTool{}
	args := fmt.Sprintf(`{"path":"%s","unique":true}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Output string `json:"output"`
		Total  int    `json:"total"`
	}
	mustUnmarshal(t, result, &r)

	if r.Total != 3 {
		t.Errorf("total = %d, want 3", r.Total)
	}
}

// ── HeadTail Tests ───────────────────────────────────────────────────

func TestHeadTail_Head(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("a\nb\nc\nd\ne\n"), 0644)

	tool := &headTailTool{}
	args := fmt.Sprintf(`{"files":[{"path":"%s"}],"lines":2,"mode":"head"}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Lines []string `json:"lines"`
			Count int      `json:"count"`
			Total int      `json:"total"`
			Error string   `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Results) != 1 {
		t.Fatalf("Results = %d, want 1", len(r.Results))
	}
	if r.Results[0].Count != 2 {
		t.Errorf("count = %d, want 2", r.Results[0].Count)
	}
	if r.Results[0].Total != 5 {
		t.Errorf("total = %d, want 5", r.Results[0].Total)
	}
	if unwrapUntrusted(r.Results[0].Lines[0]) != "a" {
		t.Errorf("first line = %q, want 'a'", r.Results[0].Lines[0])
	}
}

func TestHeadTail_Tail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("a\nb\nc\nd\ne\n"), 0644)

	tool := &headTailTool{}
	args := fmt.Sprintf(`{"files":[{"path":"%s"}],"lines":2,"mode":"tail"}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Lines []string `json:"lines"`
			Count int      `json:"count"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)

	if r.Results[0].Count != 2 || unwrapUntrusted(r.Results[0].Lines[0]) != "d" {
		t.Errorf("tail(2) = %v, want [d e]", r.Results[0].Lines)
	}
}

func TestHeadTail_NotFound(t *testing.T) {
	tool := &headTailTool{}
	result := callJSON(t, tool, `{"files":[{"path":"/nonexistent"}]}`)
	var r struct {
		Results []struct {
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if r.Results[0].Error == "" {
		t.Errorf("expected error")
	}
}

// ── Base64 Tests ─────────────────────────────────────────────────────

func TestBase64_EncodeString(t *testing.T) {
	tool := &base64Tool{}
	result := callJSON(t, tool, `{"content":"hello"}`)

	var r struct {
		Encoded string `json:"encoded"`
		Size    int    `json:"size"`
	}
	mustUnmarshal(t, result, &r)

	if r.Encoded != "aGVsbG8=" {
		t.Errorf("encoded = %q, want aGVsbG8=", r.Encoded)
	}
	if r.Size != 5 {
		t.Errorf("size = %d, want 5", r.Size)
	}
}

func TestBase64_Decode(t *testing.T) {
	tool := &base64Tool{}
	result := callJSON(t, tool, `{"string":"aGVsbG8=","decode":true}`)

	var r struct {
		Decoded string `json:"decoded"`
	}
	mustUnmarshal(t, result, &r)

	if r.Decoded != "hello" {
		t.Errorf("decoded = %q, want 'hello'", r.Decoded)
	}
}

func TestBase64_File(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	os.WriteFile(path, []byte("test data\n"), 0644)

	tool := &base64Tool{}
	args := fmt.Sprintf(`{"path":"%s"}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Encoded string `json:"encoded"`
		Size    int    `json:"size"`
	}
	mustUnmarshal(t, result, &r)

	if r.Encoded == "" {
		t.Errorf("expected non-empty encoded string")
	}
	if r.Size <= 0 {
		t.Errorf("expected size > 0")
	}
}

// ── Tr Tests ─────────────────────────────────────────────────────────

func TestTr_Upper(t *testing.T) {
	tool := &trTool{}
	result := callJSON(t, tool, `{"content":"hello world","transformations":[{"type":"upper"}]}`)

	var r struct {
		Result string `json:"result"`
	}
	mustUnmarshal(t, result, &r)

	if r.Result != "HELLO WORLD" {
		t.Errorf("result = %q, want 'HELLO WORLD'", r.Result)
	}
}

func TestTr_Lower(t *testing.T) {
	tool := &trTool{}
	result := callJSON(t, tool, `{"content":"HELLO","transformations":[{"type":"lower"}]}`)

	var r struct {
		Result string `json:"result"`
	}
	mustUnmarshal(t, result, &r)

	if r.Result != "hello" {
		t.Errorf("result = %q, want 'hello'", r.Result)
	}
}

func TestTr_StringReplace(t *testing.T) {
	tool := &trTool{}
	result := callJSON(t, tool, `{"content":"foo bar foo","transformations":[{"type":"string","from":"foo","to":"baz"}]}`)

	var r struct {
		Result string `json:"result"`
	}
	mustUnmarshal(t, result, &r)

	if r.Result != "baz bar baz" {
		t.Errorf("result = %q, want 'baz bar baz'", r.Result)
	}
}

func TestTr_Delete(t *testing.T) {
	tool := &trTool{}
	result := callJSON(t, tool, `{"content":"hello 123 world","transformations":[{"type":"delete","from":"123"}]}`)

	var r struct {
		Result string `json:"result"`
	}
	mustUnmarshal(t, result, &r)

	if r.Result != "hello  world" {
		t.Errorf("result = %q, want 'hello  world'", r.Result)
	}
}

func TestTr_MultipleTransforms(t *testing.T) {
	tool := &trTool{}
	result := callJSON(t, tool, `{"content":"Hello World","transformations":[{"type":"lower"},{"type":"string","from":" ","to":"_"}]}`)

	var r struct {
		Result string `json:"result"`
	}
	mustUnmarshal(t, result, &r)

	if r.Result != "hello_world" {
		t.Errorf("result = %q, want 'hello_world'", r.Result)
	}
}

// ── WordCount Tests ──────────────────────────────────────────────────

func TestWordCount_Basic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello world\nfoo bar baz\n"), 0644)

	tool := &wordCountTool{}
	args := fmt.Sprintf(`{"files":[{"path":"%s"}]}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Lines int    `json:"lines"`
			Words int    `json:"words"`
			Chars int    `json:"chars"`
			Bytes int64  `json:"bytes"`
			Error string `json:"error"`
		} `json:"results"`
		Total struct {
			Lines int `json:"lines"`
			Words int `json:"words"`
		} `json:"total"`
	}
	mustUnmarshal(t, result, &r)

	if len(r.Results) != 1 {
		t.Fatalf("Results = %d, want 1", len(r.Results))
	}
	if r.Results[0].Error != "" {
		t.Fatalf("error: %s", r.Results[0].Error)
	}
	if r.Results[0].Lines != 2 {
		t.Errorf("lines = %d, want 2", r.Results[0].Lines)
	}
	if r.Results[0].Words != 5 {
		t.Errorf("words = %d, want 5", r.Results[0].Words)
	}
	if r.Total.Words != 5 {
		t.Errorf("total words = %d, want 5", r.Total.Words)
	}
}

func TestWordCount_MultipleFiles(t *testing.T) {
	dir := t.TempDir()
	path1 := filepath.Join(dir, "a.txt")
	path2 := filepath.Join(dir, "b.txt")
	os.WriteFile(path1, []byte("one two\n"), 0644)
	os.WriteFile(path2, []byte("three four five\n"), 0644)

	tool := &wordCountTool{}
	args := fmt.Sprintf(`{"files":[{"path":"%s"},{"path":"%s"}]}`, path1, path2)
	result := callJSON(t, tool, args)

	var r struct {
		Total struct {
			Lines int `json:"lines"`
			Words int `json:"words"`
		} `json:"total"`
	}
	mustUnmarshal(t, result, &r)

	if r.Total.Lines != 2 {
		t.Errorf("total lines = %d, want 2", r.Total.Lines)
	}
	if r.Total.Words != 5 {
		t.Errorf("total words = %d, want 5", r.Total.Words)
	}
}

func TestWordCount_NotFound(t *testing.T) {
	tool := &wordCountTool{}
	result := callJSON(t, tool, `{"files":[{"path":"/nonexistent"}]}`)
	var r struct {
		Results []struct {
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if r.Results[0].Error == "" {
		t.Errorf("expected error for nonexistent file")
	}
}

// ── Security & Edge Case Tests ──────────────────────────────────────
//
// These tests verify that every tool properly gates through the danger
// system, handles empty/binary/symlink files, rejects path traversal,
// respects max limits, and never panics on any input.

// ── Symlink Attack Detection ──────────────────────────────────────────

func TestBatchPatch_SymlinkRejected(t *testing.T) {

	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	os.WriteFile(target, []byte("secret\n"), 0644)
	link := filepath.Join(dir, "link.txt")
	os.Symlink(target, link)

	tool := &batchPatchTool{}
	// Try to patch through a symlink — should fail with O_NOFOLLOW
	args := fmt.Sprintf(`{"patches":[{"path":"%s","old_string":"secret","new_string":"leaked"}]}`, link)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Results) > 0 && r.Results[0].Success {
		t.Error("batch_patch should reject symlinks")
	}
}

func TestHeadTail_SymlinkRejected(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	os.WriteFile(target, []byte("data\n"), 0644)
	link := filepath.Join(dir, "link.txt")
	os.Symlink(target, link)

	tool := &headTailTool{}
	args := fmt.Sprintf(`{"files":[{"path":"%s"}],"lines":1}`, link)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Results) > 0 && r.Results[0].Error == "" {
		t.Error("head_tail should reject symlinks")
	}
}

// ── Empty File Handling ──────────────────────────────────────────────

func TestSort_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	os.WriteFile(path, []byte{}, 0644)

	tool := &sortTool{}
	args := fmt.Sprintf(`{"path":"%s"}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Output string `json:"output"`
		Total  int    `json:"total"`
	}
	mustUnmarshal(t, result, &r)
	if r.Total != 0 {
		t.Errorf("total = %d, want 0", r.Total)
	}
}

func TestHeadTail_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	os.WriteFile(path, []byte{}, 0644)

	tool := &headTailTool{}
	args := fmt.Sprintf(`{"files":[{"path":"%s"}],"lines":5}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Count int      `json:"count"`
			Lines []string `json:"lines"`
			Error string   `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if r.Results[0].Error != "" {
		t.Fatalf("error: %s", r.Results[0].Error)
	}
	if r.Results[0].Count != 0 {
		t.Errorf("count = %d, want 0", r.Results[0].Count)
	}
}

func TestWordCount_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	os.WriteFile(path, []byte{}, 0644)

	tool := &wordCountTool{}
	args := fmt.Sprintf(`{"files":[{"path":"%s"}]}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Lines int    `json:"lines"`
			Words int    `json:"words"`
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if r.Results[0].Lines != 0 || r.Results[0].Words != 0 {
		t.Errorf("empty file: lines=%d words=%d, want 0", r.Results[0].Lines, r.Results[0].Words)
	}
}

// ── Binary File Protection ───────────────────────────────────────────

func TestCountLines_BinaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "binary.bin")
	os.WriteFile(path, []byte{0x00, 0x01, 0x02, 0x03}, 0644)

	tool := &countLinesTool{}
	args := fmt.Sprintf(`{"files":[{"path":"%s"}]}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Lines int    `json:"lines"`
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if r.Results[0].Error != "" {
		t.Fatalf("error: %s", r.Results[0].Error)
	}
}

// ── Max Limits Enforcement ───────────────────────────────────────────

func TestBatchPatch_MaxLimit(t *testing.T) {
	tool := &batchPatchTool{}
	patches := make([]string, 11)
	for i := range patches {
		patches[i] = fmt.Sprintf(`{"path":"test%d.txt","old_string":"a","new_string":"b"}`, i)
	}
	args := `{"patches":[` + strings.Join(patches, ",") + `]}`
	result := callJSON(t, tool, args)

	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if !strings.Contains(r.Error, "max 10") {
		t.Errorf("should reject >10 patches, got: %s", r.Error)
	}
}

func TestMultiGrep_MaxPatterns(t *testing.T) {
	tool := &multiGrepTool{}
	patterns := make([]string, 11)
	for i := range patterns {
		patterns[i] = fmt.Sprintf(`"pattern%d"`, i)
	}
	args := `{"patterns":[` + strings.Join(patterns, ",") + `]}`
	result := callJSON(t, tool, args)

	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if !strings.Contains(r.Error, "max 10") {
		t.Errorf("should reject >10 patterns, got: %s", r.Error)
	}
}

func TestHTTPBatch_MaxURLs(t *testing.T) {
	tool := newHTTPBatchTool(danger.DangerousConfig{})
	urls := make([]string, 11)
	for i := range urls {
		urls[i] = fmt.Sprintf(`{"url":"https://example.com/page%d"}`, i)
	}
	args := `{"requests":[` + strings.Join(urls, ",") + `]}`
	result := callJSON(t, tool, args)

	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if !strings.Contains(r.Error, "max 10") {
		t.Errorf("should reject >10 URLs, got: %s", r.Error)
	}
}

// ── Empty Args Rejection ─────────────────────────────────────────────

func TestSort_NoPath(t *testing.T) {
	tool := &sortTool{}
	result := callJSON(t, tool, `{}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if !strings.Contains(r.Error, "provide path") {
		t.Errorf("should require path, got: %s", r.Error)
	}
}

func TestBase64_NoArgs(t *testing.T) {
	tool := &base64Tool{}
	result := callJSON(t, tool, `{}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if !strings.Contains(r.Error, "provide path") {
		t.Errorf("should require args, got: %s", r.Error)
	}
}

func TestTr_NoTransformations(t *testing.T) {
	tool := &trTool{}
	result := callJSON(t, tool, `{"content":"hello","transformations":[]}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if !strings.Contains(r.Error, "at least one") {
		t.Errorf("should require transformations, got: %s", r.Error)
	}
}

// ── Invalid JSON Rejection ───────────────────────────────────────────

func TestTools_InvalidJSON(t *testing.T) {
	tools := []struct {
		name string
		tool interface{ Call(string) (string, error) }
	}{
		{"batch_patch", &batchPatchTool{}},
		{"parallel_shell", &parallelShellTool{}},
		{"http_batch", newHTTPBatchTool(danger.DangerousConfig{})},
		{"math_eval", &mathEvalTool{}},
		{"diff", &diffTool{}},
		{"count_lines", &countLinesTool{}},
		{"multi_grep", &multiGrepTool{}},
		{"json_query", &jsonQueryTool{}},
		{"tree", &treeTool{}},
		{"checksum", &checksumTool{}},
		{"sort", &sortTool{}},
		{"head_tail", &headTailTool{}},
		{"base64", &base64Tool{}},
		{"tr", &trTool{}},
		{"word_count", &wordCountTool{}},
	}

	for _, tc := range tools {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tc.tool.Call(`{bad json}`)
			if err != nil {
				return
			}
			var r struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal([]byte(result), &r); err != nil {
				t.Fatalf("unmarshal failed: %v\nraw: %s", err, result)
			}
			if !strings.Contains(r.Error, "invalid") {
				t.Errorf("expected 'invalid' in error, got: %s", r.Error)
			}
		})
	}
}

// ── Missing Required Fields ──────────────────────────────────────────

func TestTools_MissingRequired(t *testing.T) {
	t.Run("batch_patch/empty", func(t *testing.T) {
		result, _ := (&batchPatchTool{}).Call(`{"patches":[]}`)
		var r struct{ Error string }
		json.Unmarshal([]byte(result), &r)
		if !strings.Contains(r.Error, "at least one") {
			t.Errorf("expected error, got: %s", r.Error)
		}
	})

	t.Run("parallel_shell/empty", func(t *testing.T) {
		result, _ := (&parallelShellTool{}).Call(`{"commands":[]}`)
		var r struct{ Error string }
		json.Unmarshal([]byte(result), &r)
		if !strings.Contains(r.Error, "at least one") {
			t.Errorf("expected error, got: %s", r.Error)
		}
	})

	t.Run("math_eval/empty", func(t *testing.T) {
		result, _ := (&mathEvalTool{}).Call(`{"expression":""}`)
		var r struct{ Error string }
		json.Unmarshal([]byte(result), &r)
		if !strings.Contains(r.Error, "required") {
			t.Errorf("expected error, got: %s", r.Error)
		}
	})

	t.Run("diff/no_paths", func(t *testing.T) {
		result, _ := (&diffTool{}).Call(`{}`)
		var r struct{ Error string }
		json.Unmarshal([]byte(result), &r)
		if !strings.Contains(r.Error, "provide") {
			t.Errorf("expected error, got: %s", r.Error)
		}
	})

	t.Run("json_query/no_path", func(t *testing.T) {
		result, _ := (&jsonQueryTool{}).Call(`{}`)
		var r struct{ Error string }
		json.Unmarshal([]byte(result), &r)
		if !strings.Contains(r.Error, "path") {
			t.Errorf("expected error, got: %s", r.Error)
		}
	})
}

// ── Tr Edge Cases ────────────────────────────────────────────────────

func TestTr_ChainTransforms(t *testing.T) {
	tool := &trTool{}
	result := callJSON(t, tool, `{"content":"abc123def456","transformations":[
		{"type":"delete","from":"123456"},
		{"type":"upper"},
		{"type":"string","from":"DEF","to":"XYZ"}
	]}`)
	var r struct{ Result string }
	mustUnmarshal(t, result, &r)
	if r.Result != "ABCXYZ" {
		t.Errorf("chained result = %q, want 'ABCXYZ'", r.Result)
	}
}

func TestTr_FileInput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello world\n"), 0644)

	tool := &trTool{}
	args := fmt.Sprintf(`{"path":"%s","transformations":[{"type":"upper"}]}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Result   string `json:"result"`
		FromFile bool   `json:"from_file"`
	}
	mustUnmarshal(t, result, &r)
	if !r.FromFile {
		t.Error("should indicate from_file=true")
	}
	if unwrapUntrusted(r.Result) != "HELLO WORLD" {
		t.Errorf("result = %q, want 'HELLO WORLD' (unwrapped)", r.Result)
	}
}

// ── Sort Edge Cases ──────────────────────────────────────────────────

func TestSort_IgnoreCase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("Beta\nalpha\nGamma\n"), 0644)

	tool := &sortTool{}
	args := fmt.Sprintf(`{"path":"%s","ignore_case":true}`, path)
	result := callJSON(t, tool, args)

	var r struct{ Output string }
	mustUnmarshal(t, result, &r)
	if unwrapUntrusted(r.Output) != "alpha\nBeta\nGamma" {
		t.Errorf("case-insensitive sort = %q", r.Output)
	}
}

func TestSort_MultipleFiles(t *testing.T) {
	dir := t.TempDir()
	path1 := filepath.Join(dir, "a.txt")
	path2 := filepath.Join(dir, "b.txt")
	os.WriteFile(path1, []byte("b\nc\n"), 0644)
	os.WriteFile(path2, []byte("a\n"), 0644)

	tool := &sortTool{}
	args := fmt.Sprintf(`{"files":[{"path":"%s"},{"path":"%s"}]}`, path1, path2)
	result := callJSON(t, tool, args)

	var r struct{ Output string }
	mustUnmarshal(t, result, &r)
	if unwrapUntrusted(r.Output) != "a\nb\nc" {
		t.Errorf("merged sort = %q, want 'a\\nb\\nc'", r.Output)
	}
}

// ── Diff Edge Cases ──────────────────────────────────────────────────

func TestDiff_FileVsStringEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("content\n"), 0644)

	tool := &diffTool{}
	args := fmt.Sprintf(`{"path":"%s","content":""}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Hunks []struct {
			Type  string `json:"type"`
			Lines []any  `json:"lines"`
		} `json:"hunks"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Hunks) == 0 {
		t.Error("expected at least one hunk (removed)")
	}
}

// ── WordCount Edge Cases ─────────────────────────────────────────────

func TestWordCount_TotalAggregation(t *testing.T) {
	dir := t.TempDir()
	path1 := filepath.Join(dir, "a.txt")
	path2 := filepath.Join(dir, "b.txt")
	os.WriteFile(path1, []byte("one two\nthree\n"), 0644)
	os.WriteFile(path2, []byte("four five six\n"), 0644)

	tool := &wordCountTool{}
	args := fmt.Sprintf(`{"files":[{"path":"%s"},{"path":"%s"}]}`, path1, path2)
	result := callJSON(t, tool, args)

	var r struct {
		Total struct {
			Lines int `json:"lines"`
			Words int `json:"words"`
		} `json:"total"`
	}
	mustUnmarshal(t, result, &r)
	if r.Total.Lines != 3 {
		t.Errorf("total lines = %d, want 3", r.Total.Lines)
	}
	if r.Total.Words != 6 {
		t.Errorf("total words = %d, want 6", r.Total.Words)
	}
}

// ── CountLines Edge Cases ────────────────────────────────────────────

func TestCountLines_TotalLineCount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("a\nb\nc\n"), 0644)

	tool := &countLinesTool{}
	args := fmt.Sprintf(`{"files":[{"path":"%s"}]}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Total struct {
			Lines int `json:"lines"`
		} `json:"total"`
	}
	mustUnmarshal(t, result, &r)
	if r.Total.Lines != 3 {
		t.Errorf("total = %d, want 3", r.Total.Lines)
	}
}

// ── JSONQuery Edge Cases ─────────────────────────────────────────────

func TestJSONQuery_MissingKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	os.WriteFile(path, []byte(`{"name":"Alice"}`), 0644)

	tool := &jsonQueryTool{}
	args := fmt.Sprintf(`{"path":"%s","query":"age"}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if !strings.Contains(r.Error, "not found") {
		t.Errorf("expected 'not found', got: %s", r.Error)
	}
}

// ── Checksum Edge Cases ──────────────────────────────────────────────

func TestChecksum_InvalidAlgorithm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("data\n"), 0644)

	tool := &checksumTool{}
	args := fmt.Sprintf(`{"files":[{"path":"%s","algorithm":"sha3"}]}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Algorithm string `json:"algorithm"`
			Hash      string `json:"hash"`
			Error     string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if !strings.Contains(r.Results[0].Error, "unsupported") {
		t.Errorf("expected 'unsupported' error, got: %s", r.Results[0].Error)
	}
}

// ── Tree Edge Cases ──────────────────────────────────────────────────

func TestTree_DepthLimit(t *testing.T) {
	dir := t.TempDir()
	deep := filepath.Join(dir, "a", "b", "c")
	os.MkdirAll(deep, 0755)
	os.WriteFile(filepath.Join(deep, "f.txt"), []byte("data\n"), 0644)

	tool := &treeTool{}
	args := fmt.Sprintf(`{"path":"%s","max_depth":1}`, dir)
	result := callJSON(t, tool, args)

	var r struct {
		Tree struct {
			FileCount int `json:"file_count"`
		} `json:"tree"`
	}
	mustUnmarshal(t, result, &r)
	if r.Tree.FileCount != 0 {
		t.Errorf("depth=1 shouldn't see nested file, got count=%d", r.Tree.FileCount)
	}
}

// ── Math Edge Cases ──────────────────────────────────────────────────

func TestMathEval_DivisionByZero(t *testing.T) {
	tool := &mathEvalTool{}
	result := callJSON(t, tool, `{"expression":"1/0"}`)

	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if !strings.Contains(r.Error, "division by zero") {
		t.Errorf("expected division by zero error, got: %s", r.Error)
	}
}

func TestMathEval_InvalidExpression(t *testing.T) {
	tool := &mathEvalTool{}
	result := callJSON(t, tool, `{"expression":"hello + world"}`)

	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Errorf("expected some error message")
	}
}

// ── Base64 Edge Cases ────────────────────────────────────────────────

func TestBase64_DecodeInvalid(t *testing.T) {
	tool := &base64Tool{}
	result := callJSON(t, tool, `{"string":"not-valid-base64!!!","decode":true}`)

	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if !strings.Contains(r.Error, "decode") {
		t.Errorf("expected decode error, got: %s", r.Error)
	}
}

// ── HTTP Batch Edge Cases ────────────────────────────────────────────

func TestHTTPBatch_DangerConfigDenyAll(t *testing.T) {
	action := "deny"
	dc := danger.DangerousConfig{
		DefaultAction: &action,
	}
	tool := newHTTPBatchTool(dc)
	result := callJSON(t, tool, `{"requests":[{"url":"https://example.com"}]}`)

	var r struct {
		Results []struct {
			URL   string `json:"url"`
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Results) > 0 && r.Results[0].Error == "" {
		t.Error("expected error for denied URL")
	}
}

// ── Tool Metadata Tests ──────────────────────────────────────────────────

func TestBatchPatch_Metadata(t *testing.T) {
	tool := &batchPatchTool{}
	if n := tool.Name(); n != "batch_patch" {
		t.Errorf("Name = %q, want 'batch_patch'", n)
	}
	if d := tool.Description(); d == "" {
		t.Error("Description should not be empty")
	}
	if s := tool.Schema(); s == nil {
		t.Error("Schema should not be nil")
	}
}

func TestParallelShell_Metadata(t *testing.T) {
	tool := &parallelShellTool{}
	if n := tool.Name(); n != "parallel_shell" {
		t.Errorf("Name = %q", n)
	}
	if d := tool.Description(); d == "" {
		t.Error("Description should not be empty")
	}
	if s := tool.Schema(); s == nil {
		t.Error("Schema should not be nil")
	}
}

func TestHTTPBatch_Metadata(t *testing.T) {
	tool := newHTTPBatchTool(danger.DangerousConfig{})
	if n := tool.Name(); n != "http_batch" {
		t.Errorf("Name = %q", n)
	}
	if tool.Description() == "" {
		t.Error("Description should not be empty")
	}
	if tool.Schema() == nil {
		t.Error("Schema should not be nil")
	}
}

func TestMathEval_Metadata(t *testing.T) {
	tool := &mathEvalTool{}
	if n := tool.Name(); n != "math_eval" {
		t.Errorf("Name = %q, want 'math_eval'", n)
	}
	if tool.Description() == "" {
		t.Error("Description should not be empty")
	}
	if tool.Schema() == nil {
		t.Error("Schema should not be nil")
	}
}

func TestPerfTools_Metadata(t *testing.T) {
	type metaTool interface {
		Name() string
		Description() string
		Schema() any
	}
	tools := []struct {
		name string
		tool metaTool
	}{
		{"diff", &diffTool{}},
		{"count_lines", &countLinesTool{}},
		{"multi_grep", &multiGrepTool{}},
		{"json_query", &jsonQueryTool{}},
		{"tree", &treeTool{}},
		{"checksum", &checksumTool{}},
		{"sort", &sortTool{}},
		{"head_tail", &headTailTool{}},
		{"base64", &base64Tool{}},
		{"tr", &trTool{}},
		{"word_count", &wordCountTool{}},
	}
	for _, tc := range tools {
		t.Run(tc.name, func(t *testing.T) {
			if tc.tool.Name() != tc.name {
				t.Errorf("Name = %q, want %q", tc.tool.Name(), tc.name)
			}
			if tc.tool.Description() == "" {
				t.Error("Description should not be empty")
			}
			if tc.tool.Schema() == nil {
				t.Error("Schema should not be nil")
			}
		})
	}
}

// ── Sort Numeric Edge Cases ──────────────────────────────────────────────

func TestSort_Numeric(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nums.txt")
	os.WriteFile(path, []byte("10\n2\n30\n1\n"), 0644)

	tool := &sortTool{}
	args := fmt.Sprintf(`{"path":"%s","numeric":true}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Output string `json:"output"`
		Total  int    `json:"total"`
	}
	mustUnmarshal(t, result, &r)
	if r.Total != 4 {
		t.Errorf("total = %d, want 4", r.Total)
	}
	if unwrapUntrusted(r.Output) != "1\n2\n10\n30" {
		t.Errorf("numeric sort = %q, want '1\\n2\\n10\\n30'", r.Output)
	}
}

func TestSort_NumericWithEmptyLine(t *testing.T) {
	// Empty lines should not cause panic (regression test)
	dir := t.TempDir()
	path := filepath.Join(dir, "mixed.txt")
	os.WriteFile(path, []byte("10\n\n30\n1\n"), 0644)

	tool := &sortTool{}
	args := fmt.Sprintf(`{"path":"%s","numeric":true}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Output string `json:"output"`
		Total  int    `json:"total"`
		Error  string `json:"error,omitempty"`
	}
	mustUnmarshal(t, result, &r)
	if r.Total != 4 {
		t.Errorf("total = %d, want 4", r.Total)
	}
}

// ── HeadTail Edge Cases ─────────────────────────────────────────────────

func TestHeadTail_HeadTotalAccuracy(t *testing.T) {
	// Verify that total line count is accurate when file is longer than N
	dir := t.TempDir()
	path := filepath.Join(dir, "many.txt")
	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644)

	tool := &headTailTool{}
	args := fmt.Sprintf(`{"files":[{"path":"%s"}],"lines":3,"mode":"head"}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Lines []string `json:"lines"`
			Count int      `json:"count"`
			Total int      `json:"total"`
			Error string   `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if r.Results[0].Error != "" {
		t.Fatalf("error: %s", r.Results[0].Error)
	}
	if r.Results[0].Count != 3 {
		t.Errorf("count = %d, want 3", r.Results[0].Count)
	}
	if r.Results[0].Total != 100 {
		t.Errorf("total = %d, want 100", r.Results[0].Total)
	}
}

// ── MultiGrep Glob Filter ───────────────────────────────────────────────

func TestMultiGrep_GlobFilter(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("TODO: in go\n"), 0644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("TODO: in txt\n"), 0644)

	tool := &multiGrepTool{}
	args := fmt.Sprintf(`{"patterns":["TODO"],"path":"%s","file_glob":"*.txt"}`, dir)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Pattern string `json:"pattern"`
			Count   int    `json:"count"`
			Error   string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Results) != 1 || r.Results[0].Count != 1 {
		t.Errorf("expected 1 match in *.txt files, got TODO count=%d", r.Results[0].Count)
	}
}

// ── Parallel Shell Timeout ──────────────────────────────────────────────

func TestParallelShell_Timeout(t *testing.T) {
	if raceEnabled {
		t.Skip("skipping under race detector due to inherent Process.Kill() vs Run() timing race")
	}
	// Verify timeout mechanism works by checking the returned error
	tool := &parallelShellTool{}
	result := callJSON(t, tool, `{"commands":[{"command":"sleep 10","timeout":1}]}`)

	var r struct {
		Results []struct {
			Error      string `json:"error"`
			DurationMs int64  `json:"duration_ms"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Results) != 1 {
		t.Fatalf("Results = %d, want 1", len(r.Results))
	}
	if r.Results[0].Error == "" {
		t.Fatal("expected error for timed-out command")
	}
	if !strings.Contains(r.Results[0].Error, "timeout") {
		t.Errorf("expected timeout error, got: %s", r.Results[0].Error)
	}
	// Duration should be ~1s (the timeout), not 10s (the sleep)
	// Allow generous margin for CI variance
	if r.Results[0].DurationMs > 5000 {
		t.Errorf("duration_ms = %d, expected ~1000 (timeout=1s, sleep=10)", r.Results[0].DurationMs)
	}
}

// ── WordCount Binary File ───────────────────────────────────────────────

func TestWordCount_BinaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.bin")
	os.WriteFile(path, []byte{0x00, 0xFF, 0x00, 0xFF}, 0644)

	tool := &wordCountTool{}
	args := fmt.Sprintf(`{"files":[{"path":"%s"}]}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Lines int    `json:"lines"`
			Words int    `json:"words"`
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if r.Results[0].Error != "" {
		t.Fatalf("error: %s", r.Results[0].Error)
	}
}

// makeOversizedFile creates a sparse file larger than maxFileReadBytes for
// testing size-cap rejections without actually writing multi-gigabyte data.
func makeOversizedFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.bin")
	if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, maxFileReadBytes+1); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCountLines_RejectsHugeFile(t *testing.T) {
	path := makeOversizedFile(t)
	tool := &countLinesTool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"files":[{"path":"%s"}]}`, path))

	var r struct {
		Results []struct {
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if r.Results[0].Error == "" || !strings.Contains(r.Results[0].Error, "too large") {
		t.Errorf("expected 'too large' error, got %q", r.Results[0].Error)
	}
}

func TestChecksum_RejectsHugeFile(t *testing.T) {
	path := makeOversizedFile(t)
	tool := &checksumTool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"files":[{"path":"%s"}]}`, path))

	var r struct {
		Results []struct {
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if r.Results[0].Error == "" || !strings.Contains(r.Results[0].Error, "too large") {
		t.Errorf("expected 'too large' error, got %q", r.Results[0].Error)
	}
}

func TestHeadTail_RejectsHugeFile(t *testing.T) {
	path := makeOversizedFile(t)
	tool := &headTailTool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"files":[{"path":"%s"}]}`, path))

	var r struct {
		Results []struct {
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if r.Results[0].Error == "" || !strings.Contains(r.Results[0].Error, "too large") {
		t.Errorf("expected 'too large' error, got %q", r.Results[0].Error)
	}
}

func TestWordCount_RejectsHugeFile(t *testing.T) {
	path := makeOversizedFile(t)
	tool := &wordCountTool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"files":[{"path":"%s"}]}`, path))

	var r struct {
		Results []struct {
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if r.Results[0].Error == "" || !strings.Contains(r.Results[0].Error, "too large") {
		t.Errorf("expected 'too large' error, got %q", r.Results[0].Error)
	}
}

func TestBase64_RejectsHugeInlineContent(t *testing.T) {
	huge := strings.Repeat("a", maxInlineContentBytes+1)
	tool := &base64Tool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"content":"%s"}`, huge))

	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" || !strings.Contains(r.Error, "too large") {
		t.Errorf("expected 'too large' error, got %q", r.Error)
	}
}

func TestTr_RejectsHugeInlineContent(t *testing.T) {
	huge := strings.Repeat("a", maxInlineContentBytes+1)
	tool := &trTool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"content":"%s","transformations":[{"type":"upper"}]}`, huge))

	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" || !strings.Contains(r.Error, "too large") {
		t.Errorf("expected 'too large' error, got %q", r.Error)
	}
}
