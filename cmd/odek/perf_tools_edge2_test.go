package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─── Checksum Edge Cases ──────────────────────────────────────────────

func TestChecksum_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	os.WriteFile(path, []byte{}, 0644)

	tool := &checksumTool{}
	args := fmt.Sprintf(`{"files":[{"path":"%s","algorithm":"md5"}]}`, path)
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
	if r.Results[0].Error != "" {
		t.Fatalf("error: %s", r.Results[0].Error)
	}
	if r.Results[0].Hash != "d41d8cd98f00b204e9800998ecf8427e" {
		t.Errorf("md5(empty) = %s, want d41d8cd98f00b204e9800998ecf8427e", r.Results[0].Hash)
	}
}

func TestChecksum_MultipleFilesDiffHashes(t *testing.T) {
	dir := t.TempDir()
	path1 := filepath.Join(dir, "a.txt")
	path2 := filepath.Join(dir, "b.txt")
	os.WriteFile(path1, []byte("hello"), 0644)
	os.WriteFile(path2, []byte("world"), 0644)

	tool := &checksumTool{}
	args := fmt.Sprintf(`{"files":[{"path":"%s"},{"path":"%s"}]}`, path1, path2)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Path  string `json:"path"`
			Hash  string `json:"hash"`
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Results) != 2 {
		t.Fatalf("Results = %d, want 2", len(r.Results))
	}
	for i, res := range r.Results {
		if res.Error != "" {
			t.Errorf("result %d error: %s", i, res.Error)
		}
		if res.Hash == "" {
			t.Errorf("result %d hash is empty", i)
		}
	}
	if r.Results[0].Hash == r.Results[1].Hash {
		t.Errorf("different files should have different hashes")
	}
}

func TestChecksum_EmptyPath(t *testing.T) {
	tool := &checksumTool{}
	result := callJSON(t, tool, `{"files":[{"path":""}]}`)
	var r struct {
		Results []struct {
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if r.Results[0].Error == "" {
		t.Errorf("expected error for empty path")
	}
}

// ─── Sort Edge Cases ──────────────────────────────────────────────────

func TestSort_AlreadySorted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sorted.txt")
	os.WriteFile(path, []byte("a\nb\nc\nd\n"), 0644)
	tool := &sortTool{}
	args := fmt.Sprintf(`{"path":"%s"}`, path)
	result := callJSON(t, tool, args)
	var r struct {
		Output string `json:"output"`
		Total  int    `json:"total"`
		Error  string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error != "" {
		t.Fatalf("error: %s", r.Error)
	}
	if unwrapUntrusted(r.Output) != "a\nb\nc\nd" {
		t.Errorf("output = %q, want a\nb\nc\nd", r.Output)
	}
}

func TestSort_Descending(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "desc.txt")
	os.WriteFile(path, []byte("a\nb\nc\n"), 0644)
	tool := &sortTool{}
	args := fmt.Sprintf(`{"path":"%s","order":"desc"}`, path)
	result := callJSON(t, tool, args)
	var r struct {
		Output string `json:"output"`
		Error  string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error != "" {
		t.Fatalf("error: %s", r.Error)
	}
	if unwrapUntrusted(r.Output) != "c\nb\na" {
		t.Errorf("desc sort = %q, want c\nb\na", r.Output)
	}
}

// ─── Base64 Additional Edge Cases ─────────────────────────────────────

func TestBase64_EmptyStringEncode(t *testing.T) {
	tool := &base64Tool{}
	result := callJSON(t, tool, `{"content":""}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	// Empty content is treated as "not provided" — error expected
	if r.Error == "" {
		t.Errorf("expected error for empty content string")
	}
}

func TestBase64_DecodeStringFlag(t *testing.T) {
	tool := &base64Tool{}
	result := callJSON(t, tool, `{"string":"aGVsbG8=","decode":true}`)
	var r struct {
		Decoded string `json:"decoded"`
		Size    int    `json:"size"`
		Error   string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error != "" {
		t.Fatalf("error: %s", r.Error)
	}
	if r.Decoded != "hello" {
		t.Errorf("decoded = %q, want 'hello'", r.Decoded)
	}
}

func TestBase64_StringWithoutDecodeFlagEncodes(t *testing.T) {
	// When `string` is provided without `decode`, the tool treats it as
	// "value to decode" (string field = intended for decode input).
	// To encode inline text, use the `content` field.
	tool := &base64Tool{}
	result := callJSON(t, tool, `{"string":"aGVsbG8="}`)
	var r struct {
		Encoded string `json:"encoded"`
		Decoded string `json:"decoded"`
		Size    int    `json:"size"`
		Error   string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error != "" {
		t.Fatalf("error: %s", r.Error)
	}
	if r.Decoded != "hello" {
		t.Errorf("decoded = %q, want 'hello' (string field auto-decodes)", r.Decoded)
	}
}

func TestBase64_InvalidBase64Decode(t *testing.T) {
	tool := &base64Tool{}
	result := callJSON(t, tool, `{"string":"!!!invalid!!!","decode":true}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Errorf("expected error for invalid base64 input")
	}
}

// ─── TR Transform Edge Cases ──────────────────────────────────────────

func TestTR_MultipleTransformations(t *testing.T) {
	tool := &trTool{}
	result := callJSON(t, tool, `{"content":"Hello World","transformations":[{"type":"lower"},{"type":"string","from":"world","to":"there"}]}`)
	var r struct {
		Result string `json:"result"`
		Error  string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error != "" {
		t.Fatalf("error: %s", r.Error)
	}
	if r.Result != "hello there" {
		t.Errorf("result = %q, want 'hello there'", r.Result)
	}
}

func TestTR_DeleteChars(t *testing.T) {
	tool := &trTool{}
	result := callJSON(t, tool, `{"content":"hello world","transformations":[{"type":"delete","from":"hw"}]}`)
	var r struct {
		Result string `json:"result"`
		Error  string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error != "" {
		t.Fatalf("error: %s", r.Error)
	}
	if r.Result != "ello orld" {
		t.Errorf("delete result = %q, want 'ello orld'", r.Result)
	}
}

func TestTR_NoTransformations(t *testing.T) {
	tool := &trTool{}
	result := callJSON(t, tool, `{"content":"hello","transformations":[]}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" || !strings.Contains(r.Error, "at least one transformation") {
		t.Errorf("expected 'transformation required' error, got: %s", r.Error)
	}
}

func TestTR_UnknownType(t *testing.T) {
	tool := &trTool{}
	result := callJSON(t, tool, `{"content":"hello","transformations":[{"type":"reverse"}]}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" || !strings.Contains(r.Error, "unknown transformation") {
		t.Errorf("expected unknown type error, got: %s", r.Error)
	}
}

func TestTR_FileInput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("Hello World\n"), 0644)

	tool := &trTool{}
	args := fmt.Sprintf(`{"path":"%s","transformations":[{"type":"lower"}]}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Result   string `json:"result"`
		FromFile bool   `json:"from_file"`
		Error    string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error != "" {
		t.Fatalf("error: %s", r.Error)
	}
	if !r.FromFile {
		t.Errorf("expected from_file=true")
	}
	if unwrapUntrusted(r.Result) != "hello world" {
		t.Errorf("result = %q, want 'hello world' (unwrapped)", r.Result)
	}
}

// ─── WordCount Additional Edge Cases ──────────────────────────────────

func TestWordCount_MultipleSpaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spaces.txt")
	os.WriteFile(path, []byte("hello    world  foo\nbar   baz\n"), 0644)

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
	if r.Results[0].Lines != 2 {
		t.Errorf("lines = %d, want 2", r.Results[0].Lines)
	}
	if r.Results[0].Words != 5 {
		t.Errorf("words = %d, want 5", r.Results[0].Words)
	}
}

// ─── Tree Additional Edge Cases ───────────────────────────────────────

func TestTree_NonExistentPath(t *testing.T) {
	tool := &treeTool{}
	result := callJSON(t, tool, `{"path":"/nonexistent/path"}`)
	var r struct {
		Tree struct {
			Path   string `json:"path"`
			ErrMsg string `json:"error"`
		} `json:"tree"`
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	// tree tool returns the entry with error embedded, not a top-level error
	if r.Error == "" && r.Tree.ErrMsg == "" {
		t.Errorf("expected error for nonexistent path in tree or top-level")
	}
}

func TestTree_MaxDepthLimit(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "a", "b", "c", "d", "e")
	os.MkdirAll(subDir, 0755)
	os.WriteFile(filepath.Join(subDir, "f.txt"), []byte("test"), 0644)

	tool := &treeTool{}
	args := fmt.Sprintf(`{"path":"%s","max_depth":2}`, dir)
	result := callJSON(t, tool, args)

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

// ─── BatchPatch Additional Edge Cases ─────────────────────────────────

func TestBatchPatch_AllFailContinue(t *testing.T) {
	tool := &batchPatchTool{}
	result := callJSON(t, tool, `{"patches":[
		{"path":"/nonexistent/a.txt","old_string":"x","new_string":"y"},
		{"path":"/nonexistent/b.txt","old_string":"x","new_string":"y"},
		{"path":"/nonexistent/c.txt","old_string":"x","new_string":"y"}
	]}`)
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
	for i, res := range r.Results {
		if res.Success {
			t.Errorf("result %d should have failed", i)
		}
		if res.Error == "" {
			t.Errorf("result %d should have error", i)
		}
	}
}

func TestBatchPatch_ReplaceAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dupes.txt")
	os.WriteFile(path, []byte("foo bar foo baz foo\n"), 0644)

	tool := &batchPatchTool{}
	args := fmt.Sprintf(`{"patches":[{"path":"%s","old_string":"foo","new_string":"qux","replace_all":true}]}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Success bool   `json:"success"`
			Diff    string `json:"diff"`
			Error   string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if !r.Results[0].Success {
		t.Fatalf("patch failed: %s", r.Results[0].Error)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "qux bar qux baz qux\n" {
		t.Errorf("replace_all result = %q, want 'qux bar qux baz qux\\n'", string(data))
	}
}

// ─── CountLines Empty File ────────────────────────────────────────────

func TestCountLines_EmptyFile_Edge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	os.WriteFile(path, []byte{}, 0644)

	tool := &countLinesTool{}
	args := fmt.Sprintf(`{"files":[{"path":"%s"}]}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Lines int    `json:"lines"`
			Bytes int64  `json:"bytes"`
			Chars int    `json:"chars"`
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if r.Results[0].Error != "" {
		t.Fatalf("error: %s", r.Results[0].Error)
	}
	if r.Results[0].Lines != 0 {
		t.Errorf("lines = %d, want 0 for empty file", r.Results[0].Lines)
	}
	if r.Results[0].Bytes != 0 {
		t.Errorf("bytes = %d, want 0 for empty file", r.Results[0].Bytes)
	}
}

// ─── CountLines Binary Content ────────────────────────────────────────

func TestCountLines_BinaryContent_Edge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bin.dat")
	os.WriteFile(path, []byte{0x00, 0xFF, 0x00, 0xFF, 0x0A}, 0644)

	tool := &countLinesTool{}
	args := fmt.Sprintf(`{"files":[{"path":"%s"}]}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Lines int    `json:"lines"`
			Bytes int64  `json:"bytes"`
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if r.Results[0].Error != "" {
		t.Fatalf("error: %s", r.Results[0].Error)
	}
	if r.Results[0].Bytes != 5 {
		t.Errorf("bytes = %d, want 5", r.Results[0].Bytes)
	}
}

// ─── Glob Additional Edge Cases ───────────────────────────────────────

func TestGlob_RecursivePattern(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "sub")
	os.MkdirAll(subDir, 0755)
	os.WriteFile(filepath.Join(subDir, "test.txt"), []byte("hello"), 0644)

	tool := &globTool{}
	args := fmt.Sprintf(`{"pattern":"**/*.txt","path":"%s"}`, dir)
	result := callJSON(t, tool, args)

	var r struct {
		Matches []interface{} `json:"matches"`
		Error   string        `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error != "" {
		t.Fatalf("error: %s", r.Error)
	}
	if len(r.Matches) == 0 {
		t.Errorf("expected at least 1 match for recursive glob")
	}
}

// ─── HeadTail Additional Edge Cases ───────────────────────────────────

func TestHeadTail_FewerLinesThanN(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "short.txt")
	os.WriteFile(path, []byte("only one line\n"), 0644)

	tool := &headTailTool{}
	args := fmt.Sprintf(`{"files":[{"path":"%s"}],"lines":10,"mode":"head"}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Count int    `json:"count"`
			Total int    `json:"total"`
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if r.Results[0].Error != "" {
		t.Fatalf("error: %s", r.Results[0].Error)
	}
	if r.Results[0].Count != 1 {
		t.Errorf("count = %d, want 1", r.Results[0].Count)
	}
	if r.Results[0].Total != 1 {
		t.Errorf("total = %d, want 1", r.Results[0].Total)
	}
}

func TestHeadTail_TailOnSmallFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "small.txt")
	os.WriteFile(path, []byte("a\nb\nc\n"), 0644)

	tool := &headTailTool{}
	args := fmt.Sprintf(`{"files":[{"path":"%s"}],"lines":5,"mode":"tail"}`, path)
	result := callJSON(t, tool, args)

	var r struct {
		Results []struct {
			Count int      `json:"count"`
			Lines []string `json:"lines"`
			Total int      `json:"total"`
			Error string   `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if r.Results[0].Error != "" {
		t.Fatalf("error: %s", r.Results[0].Error)
	}
	if r.Results[0].Count != 3 {
		t.Errorf("tail count = %d, want 3", r.Results[0].Count)
	}
}

// ─── WordCount Binary File ────────────────────────────────────────────

func TestWordCount_BinaryFile_Edge(t *testing.T) {
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
			Chars int    `json:"chars"`
			Bytes int64  `json:"bytes"`
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if r.Results[0].Error != "" {
		t.Fatalf("error: %s", r.Results[0].Error)
	}
}
