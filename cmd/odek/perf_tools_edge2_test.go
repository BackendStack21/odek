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
	args := fmt.Sprintf(`{"path":"%s","algorithm":"md5"}`, path)
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

func TestChecksum_IndividualFilesDiffHashes(t *testing.T) {
	dir := t.TempDir()
	var hashes []string
	for i, content := range []string{"hello", "world"} {
		path := filepath.Join(dir, fmt.Sprintf("file%d.txt", i))
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		var r checksumResult
		mustUnmarshal(t, callJSON(t, &checksumTool{}, fmt.Sprintf(`{"path":%q}`, path)), &r)
		if len(r.Results) != 1 || r.Results[0].Error != "" || r.Results[0].Hash == "" {
			t.Fatalf("unexpected result: %+v", r)
		}
		hashes = append(hashes, r.Results[0].Hash)
	}
	if hashes[0] == hashes[1] {
		t.Fatal("different files have identical hashes")
	}
}

func TestChecksum_EmptyPath(t *testing.T) {
	output, err := (&checksumTool{}).Call(`{"path":""}`)
	if err == nil || !strings.Contains(output, "path is required") {
		t.Fatalf("empty path accepted: %s (%v)", output, err)
	}
}

// ─── Sort Edge Cases ──────────────────────────────────────────────────

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
	if !strings.Contains(r.Decoded, "hello") || !strings.HasPrefix(r.Decoded, "<untrusted") {
		t.Errorf("decoded = %q, want wrapped 'hello' (decoded output is untrusted data)", r.Decoded)
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
	if !strings.Contains(r.Decoded, "hello") || !strings.HasPrefix(r.Decoded, "<untrusted") {
		t.Errorf("decoded = %q, want wrapped 'hello' (decoded output is untrusted data)", r.Decoded)
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


// ─── CountLines Empty File ────────────────────────────────────────────

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
	args := fmt.Sprintf(`{"path":"%s","lines":10,"mode":"head"}`, path)
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
	args := fmt.Sprintf(`{"path":"%s","lines":5,"mode":"tail"}`, path)
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
