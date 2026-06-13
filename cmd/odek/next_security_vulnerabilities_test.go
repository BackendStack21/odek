package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
)

// ── 1. Browser history must be capped to avoid memory DoS ────────────────

func TestBrowser_HistoryCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html><body>page</body></html>")
	}))
	defer srv.Close()

	tool := newBrowserTool(danger.DangerousConfig{})
	for i := 0; i < 55; i++ {
		callJSON(t, tool, fmt.Sprintf(`{"action":"navigate","url":%q}`, srv.URL))
	}

	if len(tool.state.history) > 50 {
		t.Fatalf("browser history grew unbounded: got %d snapshots (max expected 50)", len(tool.state.history))
	}
}

// ── 2. search_files / multi_grep must cap limit and result size ──────────

func TestSearchFiles_LimitCap(t *testing.T) {
	dir := t.TempDir()
	var lines []string
	for i := 0; i < 600; i++ {
		lines = append(lines, "match")
	}
	os.WriteFile(filepath.Join(dir, "data.txt"), []byte(strings.Join(lines, "\n")), 0644)

	tool := &searchFilesTool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"pattern":"match","path":%q,"limit":10000}`, dir))
	var r struct {
		Matches []any `json:"matches"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Matches) > 500 {
		t.Fatalf("search_files limit was not capped: got %d matches", len(r.Matches))
	}
}

func TestSearchFiles_ResultByteCap(t *testing.T) {
	dir := t.TempDir()
	line := strings.Repeat("x", 500*1024) + " MATCH"
	os.WriteFile(filepath.Join(dir, "big.txt"), []byte(line+"\n"+line+"\n"+line+"\n"), 0644)

	tool := &searchFilesTool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"pattern":"MATCH","path":%q,"limit":10}`, dir))
	var r struct {
		Matches []struct {
			Content string `json:"content"`
		} `json:"matches"`
	}
	mustUnmarshal(t, result, &r)

	total := 0
	for _, m := range r.Matches {
		total += len(unwrapUntrusted(m.Content))
	}
	if total > 1024*1024 {
		t.Fatalf("search_files returned %d bytes of content, expected cap near 1 MiB", total)
	}
}

func TestMultiGrep_LimitCap(t *testing.T) {
	dir := t.TempDir()
	var lines []string
	for i := 0; i < 600; i++ {
		lines = append(lines, "match")
	}
	os.WriteFile(filepath.Join(dir, "data.txt"), []byte(strings.Join(lines, "\n")), 0644)

	tool := &multiGrepTool{dangerousConfig: danger.DangerousConfig{}}
	result := callJSON(t, tool, fmt.Sprintf(`{"patterns":["match"],"path":%q,"limit":10000}`, dir))
	var r struct {
		Results []struct {
			Matches []any `json:"matches"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Results) != 1 {
		t.Fatalf("expected 1 pattern result, got %d", len(r.Results))
	}
	if len(r.Results[0].Matches) > 500 {
		t.Fatalf("multi_grep limit was not capped: got %d matches", len(r.Results[0].Matches))
	}
}

// ── 3. perf tools must reject (not load) huge files ──────────────────────

func TestBase64_RejectsHugeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.bin")
	os.WriteFile(path, make([]byte, 15*1024*1024), 0644)

	tool := &base64Tool{dangerousConfig: danger.DangerousConfig{}}
	result := callJSON(t, tool, fmt.Sprintf(`{"path":%q}`, path))
	var r struct {
		Encoded string `json:"encoded,omitempty"`
		Error   string `json:"error,omitempty"`
	}
	mustUnmarshal(t, result, &r)
	if r.Encoded != "" {
		t.Fatalf("base64 should reject a 15 MiB file, but returned encoded data")
	}
	if r.Error == "" {
		t.Fatalf("base64 should return an error for a 15 MiB file")
	}
}

func TestDiff_RejectsHugeFile(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.txt")
	pathB := filepath.Join(dir, "b.txt")
	os.WriteFile(pathA, make([]byte, 15*1024*1024), 0644)
	os.WriteFile(pathB, []byte("small"), 0644)

	tool := &diffTool{dangerousConfig: danger.DangerousConfig{}}
	result := callJSON(t, tool, fmt.Sprintf(`{"path_a":%q,"path_b":%q}`, pathA, pathB))
	var r struct {
		Error string `json:"error,omitempty"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatalf("diff should return an error for a 15 MiB file")
	}
}

func TestJsonQuery_RejectsHugeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.json")
	// Build a ~15 MB JSON object without newlines so it looks like one value.
	big := `{"x":"` + strings.Repeat("a", 15*1024*1024) + `"}`
	os.WriteFile(path, []byte(big), 0644)

	tool := &jsonQueryTool{dangerousConfig: danger.DangerousConfig{}}
	result := callJSON(t, tool, fmt.Sprintf(`{"path":%q,"query":"x"}`, path))
	var r struct {
		Error string `json:"error,omitempty"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatalf("json_query should return an error for a 15 MiB file")
	}
}

// ── 4. serve state-changing endpoints must require a local origin ────────

func TestServe_CSRF_RejectForeignOrigin(t *testing.T) {
	base := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := requireLocalOrigin(base)

	req := httptest.NewRequest(http.MethodPost, "/api/cancel", nil)
	req.Header.Set("Origin", "http://evil.example.com")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("foreign origin POST should be rejected (403), got %d", rr.Code)
	}
}

func TestServe_CSRF_AllowsEmptyOrigin(t *testing.T) {
	base := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := requireLocalOrigin(base)

	req := httptest.NewRequest(http.MethodPost, "/api/cancel", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("empty-origin POST should be allowed, got %d", rr.Code)
	}
}

func TestServe_CSRF_AllowsLocalhostOrigin(t *testing.T) {
	base := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := requireLocalOrigin(base)

	for _, origin := range []string{"http://localhost:8080", "http://127.0.0.1:8080"} {
		req := httptest.NewRequest(http.MethodPost, "/api/cancel", nil)
		req.Header.Set("Origin", origin)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("localhost origin %q should be allowed, got %d", origin, rr.Code)
		}
	}
}

func TestServe_StaticSecurityHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handleStatic().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("static handler returned %d", rr.Code)
	}
	if rr.Header().Get("X-Frame-Options") == "" {
		t.Error("static handler missing X-Frame-Options")
	}
	if rr.Header().Get("Content-Security-Policy") == "" {
		t.Error("static handler missing Content-Security-Policy")
	}
}

// ── 5. file-reading perf tools must wrap content as untrusted ────────────

func TestHeadTail_WrapsContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello world\n"), 0644)

	tool := &headTailTool{dangerousConfig: danger.DangerousConfig{}}
	result := callJSON(t, tool, fmt.Sprintf(`{"files":[{"path":%q}],"lines":10}`, path))
	var r struct {
		Results []struct {
			Lines []string `json:"lines"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Results) == 0 || len(r.Results[0].Lines) == 0 {
		t.Fatal("expected at least one line")
	}
	if !strings.HasPrefix(r.Results[0].Lines[0], "<untrusted_content_") {
		t.Fatalf("head_tail line should be wrapped in untrusted_content, got: %q", r.Results[0].Lines[0])
	}
}

func TestDiff_WrapsContent(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.txt")
	pathB := filepath.Join(dir, "b.txt")
	os.WriteFile(pathA, []byte("old line\n"), 0644)
	os.WriteFile(pathB, []byte("new line\n"), 0644)

	tool := &diffTool{dangerousConfig: danger.DangerousConfig{}}
	result := callJSON(t, tool, fmt.Sprintf(`{"path_a":%q,"path_b":%q}`, pathA, pathB))
	var r struct {
		Hunks []struct {
			Lines []struct {
				Content string `json:"content"`
			} `json:"lines"`
		} `json:"hunks"`
	}
	mustUnmarshal(t, result, &r)
	for _, h := range r.Hunks {
		for _, l := range h.Lines {
			if !strings.HasPrefix(l.Content, "<untrusted_content_") {
				t.Fatalf("diff line should be wrapped in untrusted_content, got: %q", l.Content)
			}
		}
	}
}

func TestSort_WrapsContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("zebra\napple\n"), 0644)

	tool := &sortTool{dangerousConfig: danger.DangerousConfig{}}
	result := callJSON(t, tool, fmt.Sprintf(`{"path":%q}`, path))
	var r struct {
		Output string `json:"output"`
	}
	mustUnmarshal(t, result, &r)
	if !strings.HasPrefix(r.Output, "<untrusted_content_") {
		t.Fatalf("sort output should be wrapped in untrusted_content, got: %q", r.Output)
	}
}

func TestTr_WrapsContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello\n"), 0644)

	tool := &trTool{dangerousConfig: danger.DangerousConfig{}}
	result := callJSON(t, tool, fmt.Sprintf(`{"path":%q,"transformations":[{"type":"upper"}]}`, path))
	var r struct {
		Result string `json:"result"`
	}
	mustUnmarshal(t, result, &r)
	if !strings.HasPrefix(r.Result, "<untrusted_content_") {
		t.Fatalf("tr result should be wrapped in untrusted_content, got: %q", r.Result)
	}
}

func TestJsonQuery_WrapsStringValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	os.WriteFile(path, []byte(`{"message":"hello"}`), 0644)

	tool := &jsonQueryTool{dangerousConfig: danger.DangerousConfig{}}
	result := callJSON(t, tool, fmt.Sprintf(`{"path":%q,"query":"message"}`, path))
	var r struct {
		Value string `json:"value"`
	}
	mustUnmarshal(t, result, &r)
	if !strings.HasPrefix(r.Value, "<untrusted_content_") {
		t.Fatalf("json_query string value should be wrapped in untrusted_content, got: %q", r.Value)
	}
}
