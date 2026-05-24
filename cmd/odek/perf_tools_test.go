package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/kode/internal/danger"
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

func TestBatchPatch_FailSkipsRemaining(t *testing.T) {
	dir := t.TempDir()
	path1 := filepath.Join(dir, "a.txt")
	os.WriteFile(path1, []byte("hello"), 0644)

	tool := &batchPatchTool{}
	args := fmt.Sprintf(`{"patches":[
		{"path":"%s","old_string":"hello","new_string":"hi"},
		{"path":"/nonexistent/file.txt","old_string":"x","new_string":"y"},
		{"path":"%s","old_string":"should","new_string":"skip"}
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
		t.Errorf("second patch should have error")
	}
	if !strings.Contains(r.Results[2].Error, "skipped") {
		t.Errorf("third patch should be skipped, got: %s", r.Results[2].Error)
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
	if r.Results[0].Stdout != "hello" {
		t.Errorf("cmd 0 stdout = %q, want 'hello'", r.Results[0].Stdout)
	}
	if r.Results[1].Stdout != "world" {
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
		expr    string
		want    float64
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
	if r.Results[0].Count + r.Results[1].Count != 3 {
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
	if r.Value != "Alice" {
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

	if r.Value != "Bob" {
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
	if r.Output != "a\nb\nc" {
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

	if r.Output != "c\nb\na" {
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
	if r.Results[0].Lines[0] != "a" {
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

	if r.Results[0].Count != 2 || r.Results[0].Lines[0] != "d" {
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
