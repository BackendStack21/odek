package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
)

// ─── MathEval Edge Cases ──────────────────────────────────────────────

func TestMathEval_Modulo(t *testing.T) {
	tool := &mathEvalTool{}
	cases := []struct {
		expr string
		want float64
	}{
		{"7 % 3", 1},
		{"10 % 5", 0},
		{"17 % 4", 1},
		{"100 % 27", 19},
		{"(42 + 8) % 7", 1},
		{"0 % 5", 0},
		{"-7 % 3", -1},
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

func TestMathEval_ModuloByZero(t *testing.T) {
	tool := &mathEvalTool{}
	result := callJSON(t, tool, `{"expression":"5 % 0"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" || !strings.Contains(r.Error, "modulo by zero") {
		t.Errorf("expected modulo by zero error, got: %s", r.Error)
	}
}

func TestMathEval_NegativeNumbers(t *testing.T) {
	tool := &mathEvalTool{}
	cases := []struct {
		expr string
		want float64
	}{
		{"-5 + 3", -2},
		{"-10 * -2", 20},
		{"-(5 + 3)", -8},
		{"(-7 + 3) * 2", -8},
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

func TestMathEval_FloatPrecision(t *testing.T) {
	tool := &mathEvalTool{}
	result := callJSON(t, tool, `{"expression":"10 / 3"}`)
	var r struct {
		Result float64 `json:"result"`
		Error  string  `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error != "" {
		t.Fatalf("error: %s", r.Error)
	}
	expected := 10.0 / 3.0
	if math.Abs(r.Result-expected) > 1e-9 {
		t.Errorf("10/3 = %f, want %f", r.Result, expected)
	}
}

func TestMathEval_LargeNumbers(t *testing.T) {
	tool := &mathEvalTool{}
	result := callJSON(t, tool, `{"expression":"999999 * 999999"}`)
	var r struct {
		Result float64 `json:"result"`
		Error  string  `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error != "" {
		t.Fatalf("error: %s", r.Error)
	}
	expected := 999999.0 * 999999.0
	if r.Result != expected {
		t.Errorf("999999*999999 = %f, want %f", r.Result, expected)
	}
}

// ─── Diff Edge Cases ──────────────────────────────────────────────────

func TestDiff_LargeFileOOMGuard(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "big_a.txt")
	pathB := filepath.Join(dir, "big_b.txt")
	var linesA, linesB []string
	for i := 0; i < 10001; i++ {
		linesA = append(linesA, fmt.Sprintf("line %d", i))
		linesB = append(linesB, fmt.Sprintf("line %d", i+1))
	}
	os.WriteFile(pathA, []byte(strings.Join(linesA, "\n")), 0644)
	os.WriteFile(pathB, []byte(strings.Join(linesB, "\n")), 0644)

	tool := &diffTool{}
	args := fmt.Sprintf(`{"path_a":"%s","path_b":"%s"}`, pathA, pathB)
	result := callJSON(t, tool, args)

	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected OOM guard error for 10K+ line files")
	}
	if !strings.Contains(r.Error, "too large") {
		t.Errorf("expected 'too large' error, got: %s", r.Error)
	}
}

func TestDiff_CompletelyDifferent(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.txt")
	pathB := filepath.Join(dir, "b.txt")
	os.WriteFile(pathA, []byte("alpha\nbeta\n"), 0644)
	os.WriteFile(pathB, []byte("gamma\ndelta\n"), 0644)

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
		t.Fatal("expected hunks for completely different files")
	}
}

func TestDiff_SingleLineFiles(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.txt")
	pathB := filepath.Join(dir, "b.txt")
	os.WriteFile(pathA, []byte("only line"), 0644)
	os.WriteFile(pathB, []byte("changed line"), 0644)

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
		t.Fatal("expected hunks for changed single-line files")
	}
}

// ─── CountLines Edge Cases ────────────────────────────────────────────

func TestCountLines_NoTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "no_eol.txt")
	os.WriteFile(path, []byte("line1\nline2"), 0644)

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
	if r.Results[0].Lines != 2 {
		t.Errorf("lines = %d, want 2 for file without trailing newline", r.Results[0].Lines)
	}
}

func TestCountLines_UnicodeContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unicode.txt")
	os.WriteFile(path, []byte("日本語\n中文\nруский\n"), 0644)

	tool := &countLinesTool{}
	args := fmt.Sprintf(`{"files":[{"path":"%s"}]}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Lines int    `json:"lines"`
			Chars int    `json:"chars"`
			Bytes int64  `json:"bytes"`
			Error string `json:"error"`
		} `json:"results"`
		Total struct {
			Lines int   `json:"lines"`
			Chars int   `json:"chars"`
			Bytes int64 `json:"bytes"`
		} `json:"total"`
	}
	mustUnmarshal(t, result, &r)
	if r.Results[0].Error != "" {
		t.Fatalf("error: %s", r.Results[0].Error)
	}
	if r.Results[0].Lines != 3 {
		t.Errorf("lines = %d, want 3", r.Results[0].Lines)
	}
}

// ─── MultiGrep Edge Cases ─────────────────────────────────────────────

func TestMultiGrep_ZeroMatches(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("nothing here\n"), 0644)

	tool := &multiGrepTool{}
	args := fmt.Sprintf(`{"patterns":["ZZZZTOP"],"path":"%s"}`, dir)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Pattern string `json:"pattern"`
			Count   int    `json:"count"`
			Error   string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if r.Results[0].Count != 0 {
		t.Errorf("expected 0 matches, got %d", r.Results[0].Count)
	}
}

func TestMultiGrep_InvalidRegex(t *testing.T) {
	tool := &multiGrepTool{}
	result := callJSON(t, tool, `{"patterns":["[invalid"],"path":"."}`)
	var r struct {
		Results []struct {
			Pattern string `json:"pattern"`
			Count   int    `json:"count"`
			Error   string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if r.Results[0].Error == "" {
		t.Errorf("expected error for invalid regex")
	}
}

// ─── JSONQuery Edge Cases ─────────────────────────────────────────────

func TestJSONQuery_NestedObjects(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested.json")
	os.WriteFile(path, []byte(`{"a":{"b":{"c":"deep"}}}`), 0644)

	tool := &jsonQueryTool{}
	args := fmt.Sprintf(`{"path":"%s","query":"a.b.c"}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Value interface{} `json:"value"`
		Error string      `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error != "" {
		t.Fatalf("error: %s", r.Error)
	}
	if got, ok := r.Value.(string); !ok || unwrapUntrusted(got) != "deep" {
		t.Errorf("value = %v, want 'deep'", r.Value)
	}
}

func TestJSONQuery_ArrayOutOfBounds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	os.WriteFile(path, []byte(`{"items":[1,2,3]}`), 0644)

	tool := &jsonQueryTool{}
	args := fmt.Sprintf(`{"path":"%s","query":"items[5]"}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" || !strings.Contains(r.Error, "out of range") {
		t.Errorf("expected out of range error, got: %s", r.Error)
	}
}

func TestJSONQuery_InvalidQuerySyntax(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	os.WriteFile(path, []byte(`{"key":"value"}`), 0644)

	tool := &jsonQueryTool{}
	args := fmt.Sprintf(`{"path":"%s","query":"key[abc]"}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Errorf("expected error for invalid array index syntax")
	}
}

func TestJSONQuery_EmptyQueryReturnsAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	os.WriteFile(path, []byte(`{"a":1,"b":2}`), 0644)

	tool := &jsonQueryTool{}
	args := fmt.Sprintf(`{"path":"%s","query":""}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Value     map[string]interface{} `json:"value"`
		ValueType string                 `json:"value_type"`
		Error     string                 `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error != "" {
		t.Fatalf("error: %s", r.Error)
	}
	if r.Value["a"].(float64) != 1 || r.Value["b"].(float64) != 2 {
		t.Errorf("unexpected value: %v", r.Value)
	}
}

func TestJSONQuery_FileNotFound(t *testing.T) {
	tool := &jsonQueryTool{}
	result := callJSON(t, tool, `{"path":"/nonexistent/nope.json","query":"key"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Errorf("expected error for nonexistent file")
	}
}

// ─── HTTP Batch Edge Cases ────────────────────────────────────────────

func TestHTTPBatch_EmptyRequests(t *testing.T) {
	tool := newHTTPBatchTool(danger.DangerousConfig{})
	result := callJSON(t, tool, `{"requests":[]}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if !strings.Contains(r.Error, "at least one") {
		t.Errorf("expected 'at least one URL' error, got: %s", r.Error)
	}
}

func TestHTTPBatch_URLSchemes(t *testing.T) {
	tool := newHTTPBatchTool(danger.DangerousConfig{})
	result := callJSON(t, tool, `{"requests":[{"url":"ftp://example.com/file"}]}`)
	var r struct {
		Results []struct {
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Results) != 1 {
		t.Fatalf("Results = %d, want 1", len(r.Results))
	}
}

// ─── isBinary Edge Cases ──────────────────────────────────────────────

func TestIsBinary_NullByte_Edge(t *testing.T) {
	if !isBinary([]byte{0x00, 0x41, 0x42}) {
		t.Errorf("expected binary detection for null byte content")
	}
}

func TestIsBinary_HighNonPrintable_Edge(t *testing.T) {
	data := []byte{0x01, 0x02, 0x03, 0x41, 0x42, 0x43}
	if !isBinary(data) {
		t.Errorf("expected binary detection for high non-printable ratio")
	}
}

func TestIsBinary_LowNonPrintable_Edge(t *testing.T) {
	data := []byte{0x01, 0x41, 0x42, 0x43, 0x44, 0x45, 0x46}
	if isBinary(data) {
		t.Errorf("expected NOT binary for low non-printable ratio")
	}
}

func TestIsBinary_EmptyData(t *testing.T) {
	if isBinary([]byte{}) {
		t.Errorf("expected NOT binary for empty data")
	}
}

func TestIsBinary_NormalText(t *testing.T) {
	if isBinary([]byte("Hello, World! This is normal text.")) {
		t.Errorf("expected NOT binary for normal text")
	}
}
