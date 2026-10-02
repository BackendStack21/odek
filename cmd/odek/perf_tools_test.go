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

// ── HTTPRequest Tests ───────────────────────────────────────────────────

func TestHTTPRequest_InvalidURL(t *testing.T) {
	tool := newHTTPRequestTool(danger.DangerousConfig{})
	result := callJSON(t, tool, `{"url":"not-a-url"}`)

	var r httpRequestResult
	mustUnmarshal(t, result, &r)

	if r.Error == "" {
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
	args := fmt.Sprintf(`{"path":"%s","algorithm":"sha256"}`, path)
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
	path := filepath.Join(t.TempDir(), "test.txt")
	if err := os.WriteFile(path, []byte("test data\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for algorithm, size := range map[string]int{"sha256": 64, "sha1": 40, "md5": 32} {
		result := callJSON(t, &checksumTool{}, fmt.Sprintf(`{"path":%q,"algorithm":%q}`, path, algorithm))
		var r checksumResult
		mustUnmarshal(t, result, &r)
		if len(r.Results) != 1 || r.Results[0].Error != "" || len(r.Results[0].Hash) != size {
			t.Fatalf("%s: %s", algorithm, result)
		}
	}
}

func TestChecksum_DefaultAlgorithm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("data\n"), 0644)

	tool := &checksumTool{}
	args := fmt.Sprintf(`{"path":"%s"}`, path)
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

// ── HeadTail Tests ───────────────────────────────────────────────────

func TestHeadTail_Head(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("a\nb\nc\nd\ne\n"), 0644)

	tool := &headTailTool{}
	args := fmt.Sprintf(`{"path":"%s","lines":2,"mode":"head"}`, path)
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
	args := fmt.Sprintf(`{"path":"%s","lines":2,"mode":"tail"}`, path)
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
	result := callJSON(t, tool, `{"path":"/nonexistent"}`)
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

	if !strings.Contains(r.Decoded, "hello") || !strings.HasPrefix(r.Decoded, "<untrusted") {
		t.Errorf("decoded = %q, want wrapped 'hello' (decoded output is untrusted data)", r.Decoded)
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

// ── Security & Edge Case Tests ──────────────────────────────────────
//
// These tests verify that every tool properly gates through the danger
// system, handles empty/binary/symlink files, rejects path traversal,
// respects max limits, and never panics on any input.

// ── Symlink Attack Detection ──────────────────────────────────────────

func TestHeadTail_SymlinkRejected(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	os.WriteFile(target, []byte("data\n"), 0644)
	link := filepath.Join(dir, "link.txt")
	os.Symlink(target, link)

	tool := &headTailTool{}
	args := fmt.Sprintf(`{"path":"%s","lines":1}`, link)
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

func TestHeadTail_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	os.WriteFile(path, []byte{}, 0644)

	tool := &headTailTool{}
	args := fmt.Sprintf(`{"path":"%s","lines":5}`, path)
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

// ── Max Limits Enforcement ───────────────────────────────────────────

func TestHTTPRequest_RejectsBatchInput(t *testing.T) {
	tool := newHTTPRequestTool(danger.DangerousConfig{})
	result, err := tool.Call(`{"requests":[{"url":"https://example.com"}]}`)
	if err == nil || !strings.Contains(result, "url is required") {
		t.Fatalf("batch input accepted: %s, %v", result, err)
	}
}

// ── Empty Args Rejection ─────────────────────────────────────────────

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

// ── Invalid JSON Rejection ───────────────────────────────────────────

func TestTools_InvalidJSON(t *testing.T) {
	tools := []struct {
		name string
		tool interface{ Call(string) (string, error) }
	}{
		{"http_request", newHTTPRequestTool(danger.DangerousConfig{})},
		{"math_eval", &mathEvalTool{}},
		{"diff", &diffTool{}},
		{"json_query", &jsonQueryTool{}},
		{"tree", &treeTool{}},
		{"checksum", &checksumTool{}},
		{"head_tail", &headTailTool{}},
		{"base64", &base64Tool{}},
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
	args := fmt.Sprintf(`{"path":"%s","algorithm":"sha3"}`, path)
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

func TestHTTPRequest_DangerConfigDenyAll(t *testing.T) {
	action := "deny"
	dc := danger.DangerousConfig{
		DefaultAction: &action,
	}
	tool := newHTTPRequestTool(dc)
	result := callJSON(t, tool, `{"url":"https://example.com"}`)

	var r httpRequestResult
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Error("expected error for denied URL")
	}
}

// ── Tool Metadata Tests ──────────────────────────────────────────────────

func TestHTTPRequest_Metadata(t *testing.T) {
	tool := newHTTPRequestTool(danger.DangerousConfig{})
	if n := tool.Name(); n != "http_request" {
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
		{"json_query", &jsonQueryTool{}},
		{"tree", &treeTool{}},
		{"checksum", &checksumTool{}},
		{"head_tail", &headTailTool{}},
		{"base64", &base64Tool{}},
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
	args := fmt.Sprintf(`{"path":"%s","lines":3,"mode":"head"}`, path)
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

// ── Parallel Shell Timeout ──────────────────────────────────────────────

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

func TestChecksum_RejectsHugeFile(t *testing.T) {
	path := makeOversizedFile(t)
	tool := &checksumTool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"path":"%s"}`, path))

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
	result := callJSON(t, tool, fmt.Sprintf(`{"path":"%s"}`, path))

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

func makeExactSizeFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "exact.bin")
	if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, maxFileReadBytes); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestChecksum_AcceptsExactSizeFile(t *testing.T) {
	path := makeExactSizeFile(t)
	tool := &checksumTool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"path":"%s"}`, path))

	var r struct {
		Results []struct {
			Hash  string `json:"hash"`
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if r.Results[0].Error != "" {
		t.Errorf("expected no error at exact size limit, got %q", r.Results[0].Error)
	}
	if r.Results[0].Hash == "" {
		t.Errorf("expected a hash at exact size limit")
	}
}

func TestHeadTail_AcceptsExactSizeFile(t *testing.T) {
	path := makeExactSizeFile(t)
	tool := &headTailTool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"path":"%s"}`, path))

	var r struct {
		Results []struct {
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	// The exact-size file must pass the size gate (not rejected as "too
	// large"). A 10 MiB single-line file does exceed the 1 MiB scanner cap,
	// which now surfaces as an explicit partial-read error instead of
	// silently reporting zero lines.
	if strings.Contains(r.Results[0].Error, "too large") {
		t.Errorf("size gate must accept a file at the exact limit, got %q", r.Results[0].Error)
	}
}

func TestHeadTail_TailRejectsHugeFile(t *testing.T) {
	path := makeOversizedFile(t)
	tool := &headTailTool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"path":"%s","mode":"tail"}`, path))

	var r struct {
		Results []struct {
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if r.Results[0].Error == "" || !strings.Contains(r.Results[0].Error, "too large") {
		t.Errorf("expected 'too large' error for tail mode, got %q", r.Results[0].Error)
	}
}

func TestBase64_AcceptsExactSizeInlineContent(t *testing.T) {
	// All-'a' string of exactly maxInlineContentBytes bytes; base64 encoding
	// will succeed and the cap should allow it.
	content := strings.Repeat("a", maxInlineContentBytes)
	tool := &base64Tool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"content":"%s"}`, content))

	var r struct {
		Encoded string `json:"encoded"`
		Error   string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error != "" {
		t.Errorf("expected no error at exact inline size limit, got %q", r.Error)
	}
	if r.Encoded == "" {
		t.Errorf("expected encoded output at exact inline size limit")
	}
}

func TestBase64_RejectsHugeDecodeString(t *testing.T) {
	huge := strings.Repeat("a", maxInlineContentBytes+1)
	tool := &base64Tool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"string":"%s","decode":true}`, huge))

	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" || !strings.Contains(r.Error, "too large") {
		t.Errorf("expected 'too large' error for decode string, got %q", r.Error)
	}
}

// ── Parallel Shell Hardening (#44) ─────────────────────────────────────
