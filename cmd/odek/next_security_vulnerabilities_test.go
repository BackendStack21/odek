package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/resource"
	"github.com/BackendStack21/odek/internal/session"
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


// ── 6. Shell / parallel_shell must cap command output ────────────────────

func TestShell_CapsOutputSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.txt")
	os.WriteFile(path, []byte(strings.Repeat("x", 15*1024*1024)), 0644)

	tool := &shellTool{}
	tool.SetContext(context.Background())
	result, err := tool.Call(fmt.Sprintf(`{"command":"cat %s","description":"read huge file"}`, path))
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}

	body := unwrapUntrusted(result)
	if len(body) > 1024*1024+200 {
		t.Fatalf("shell returned %d bytes, expected cap near 1 MiB", len(body))
	}
}

func TestParallelShell_CapsOutputSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.txt")
	os.WriteFile(path, []byte(strings.Repeat("x", 15*1024*1024)), 0644)

	tool := &parallelShellTool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"commands":[{"command":"cat %s"}]}`, path))
	var r struct {
		Results []struct {
			Stdout string `json:"stdout"`
			Stderr string `json:"stderr"`
			Error  string `json:"error,omitempty"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(r.Results))
	}
	out := r.Results[0].Stdout + r.Results[0].Stderr
	if len(out) > 1024*1024+200 {
		t.Fatalf("parallel_shell returned %d bytes, expected cap near 1 MiB", len(out))
	}
}

// ── 7. Browser must enforce an HTTP request timeout ──────────────────────

func TestBrowser_NavigateTimeout(t *testing.T) {
	orig := browserRequestTimeout
	browserRequestTimeout = 100 * time.Millisecond
	defer func() { browserRequestTimeout = orig }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		fmt.Fprint(w, "<html><body>page</body></html>")
	}))
	defer srv.Close()

	tool := newBrowserTool(danger.DangerousConfig{})
	result := callJSON(t, tool, fmt.Sprintf(`{"action":"navigate","url":%q}`, srv.URL))
	var r struct {
		Error string `json:"error,omitempty"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" || !strings.Contains(strings.ToLower(r.Error), "timeout") {
		t.Fatalf("browser should time out on a slow server, got: %q", r.Error)
	}
}

// ── 8. batch_patch must reject huge files and wrap diff output ───────────

func TestBatchPatch_RejectsHugeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.txt")
	os.WriteFile(path, []byte(strings.Repeat("x", 15*1024*1024)), 0644)

	tool := &batchPatchTool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"patches":[{"path":%q,"old_string":"xxx","new_string":"yyy"}]}`, path))
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
		t.Fatal("batch_patch should reject a 15 MiB file")
	}
	if r.Results[0].Error == "" {
		t.Fatal("batch_patch should return an error for a 15 MiB file")
	}
}

func TestBatchPatch_WrapsDiff(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello world\n"), 0644)

	tool := &batchPatchTool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"patches":[{"path":%q,"old_string":"hello","new_string":"goodbye"}]}`, path))
	var r struct {
		Results []struct {
			Diff string `json:"diff"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Results) == 0 || !strings.HasPrefix(r.Results[0].Diff, "<untrusted_content_") {
		t.Fatalf("batch_patch diff should be wrapped in untrusted_content, got: %q", r.Results[0].Diff)
	}
}

// ── 9. Transcribe must reject huge / symlinked audio inputs ──────────────

func TestTranscribe_RejectsHugeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.ogg")
	os.WriteFile(path, make([]byte, 15*1024*1024), 0644)

	tool := &transcribeTool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"path":%q}`, path))
	var r struct {
		Error string `json:"error,omitempty"`
	}
	mustUnmarshal(t, result, &r)
	if !strings.Contains(r.Error, "too large") {
		t.Fatalf("transcribe should reject a 15 MiB file with a size error, got: %q", r.Error)
	}
}

// ── 10. Tree must cap directory width ────────────────────────────────────

func TestTree_CapsDirectoryWidth(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 1500; i++ {
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%d.txt", i)), []byte("x"), 0644)
	}

	tool := &treeTool{dangerousConfig: danger.DangerousConfig{}}
	result := callJSON(t, tool, fmt.Sprintf(`{"path":%q}`, dir))
	var r struct {
		Tree struct {
			Children []any `json:"children"`
		} `json:"tree"`
		Error string `json:"error,omitempty"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error != "" {
		t.Fatalf("tree returned error: %s", r.Error)
	}
	if len(r.Tree.Children) > 1000 {
		t.Fatalf("tree did not cap directory width: got %d children", len(r.Tree.Children))
	}
}


// ── 11. patch must reject huge files and preserve original permissions ───

func TestPatch_RejectsHugeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.txt")
	os.WriteFile(path, []byte(strings.Repeat("x", 15*1024*1024)), 0644)

	tool := &patchTool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"path":%q,"old_string":"xxx","new_string":"yyy"}`, path))
	var r struct {
		Success bool   `json:"success"`
		Error   string `json:"error,omitempty"`
	}
	mustUnmarshal(t, result, &r)
	if r.Success {
		t.Fatal("patch should reject a 15 MiB file")
	}
	if !strings.Contains(r.Error, "too large") {
		t.Fatalf("patch should reject huge file with a size error, got: %q", r.Error)
	}
}

func TestPatch_PreservesFileMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "script.sh")
	os.WriteFile(path, []byte("#!/bin/sh\necho hello\n"), 0755)

	tool := &patchTool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"path":%q,"old_string":"hello","new_string":"world"}`, path))
	var r struct {
		Success bool `json:"success"`
	}
	mustUnmarshal(t, result, &r)
	if !r.Success {
		t.Fatal("patch failed")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatalf("patch changed mode from 0755 to %04o", info.Mode().Perm())
	}
}

// ── 12. glob must cap match count and wrap paths as untrusted ────────────

func TestGlob_CapsMatchCount(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 1500; i++ {
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%d.txt", i)), []byte("x"), 0644)
	}

	tool := &globTool{dangerousConfig: danger.DangerousConfig{}}
	result := callJSON(t, tool, fmt.Sprintf(`{"pattern":"*","path":%q,"limit":10000}`, dir))
	var r struct {
		Matches []struct {
			Path string `json:"path"`
		} `json:"matches"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Matches) > 1000 {
		t.Fatalf("glob did not cap match count: got %d", len(r.Matches))
	}
	if len(r.Matches) == 0 {
		t.Fatal("expected at least one match")
	}
	if !strings.HasPrefix(r.Matches[0].Path, "<untrusted_content_") {
		t.Fatalf("glob path should be wrapped in untrusted_content, got: %q", r.Matches[0].Path)
	}
}

// ── 13. subagent must reject a huge task file ────────────────────────────

func TestSubagent_RejectsHugeTaskFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "task.json")
	os.WriteFile(path, []byte(`{"goal":"`+strings.Repeat("x", 15*1024*1024)+`"}`), 0600)

	err := subagentCmd([]string{"--task", path})
	if err == nil {
		t.Fatal("subagent should reject a huge task file")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Fatalf("subagent should reject huge task file with a size error, got: %v", err)
	}
}

// ── 14. transcribe must cap whisper stdout ───────────────────────────────

func TestTranscribe_CapsWhisperOutput(t *testing.T) {
	dir := t.TempDir()
	fakeBinary := filepath.Join(dir, "whisper")
	fakeModel := filepath.Join(dir, "model.bin")
	// Fake whisper: streams valid-ish opening JSON then floods stdout.
	script := `#!/bin/sh
head -c 20000000 /dev/zero | tr '\0' 'x'
exit 0
`
	os.WriteFile(fakeBinary, []byte(script), 0755)
	os.WriteFile(fakeModel, []byte("fake model"), 0644)

	audioPath := filepath.Join(dir, "audio.wav")
	os.WriteFile(audioPath, []byte("fake wav"), 0644)

	tool := newTranscribeTool(danger.DangerousConfig{}, config.TranscriptionConfig{
		BinaryPath: fakeBinary,
		Model:      fakeModel,
	})
	result := callJSON(t, tool, fmt.Sprintf(`{"path":%q}`, audioPath))
	var r struct {
		Error string `json:"error,omitempty"`
	}
	mustUnmarshal(t, result, &r)
	if !strings.Contains(r.Error, "too large") {
		t.Fatalf("transcribe should cap whisper output, got: %q", r.Error)
	}
}

// ── 15. session_search get must cap/wrap returned messages ───────────────

func TestSessionSearchGet_CapsAndWrapsMessages(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	store, err := session.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	sess := &session.Session{
		ID:        "test-session",
		Task:      "test",
		Model:     "test-model",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	for i := 0; i < 150; i++ {
		sess.Messages = append(sess.Messages, llm.Message{Role: "assistant", Content: fmt.Sprintf("msg %d", i)})
	}
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	tool := &sessionSearchTool{store: store}
	result := callJSON(t, tool, `{"action":"get","query":"test-session"}`)
	t.Logf("session get result: %s", result)
	var r struct {
		Error           string `json:"error,omitempty"`
		SessionMessages []struct {
			Content string `json:"content"`
		} `json:"session_messages"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error != "" {
		t.Fatalf("session get error: %s", r.Error)
	}
	if len(r.SessionMessages) > 100 {
		t.Fatalf("session_search get did not cap messages: got %d", len(r.SessionMessages))
	}
	if len(r.SessionMessages) == 0 {
		t.Fatal("expected at least one message")
	}
	if !strings.HasPrefix(r.SessionMessages[0].Content, "<untrusted_content_") {
		t.Fatalf("session message should be wrapped in untrusted_content, got: %q", r.SessionMessages[0].Content)
	}
}


// ── 16. enrichTask must wrap @-resource / --ctx content ──────────────────

func TestEnrichTask_WrapsCtxContent(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "note.txt"), []byte("hello world"), 0644)

	enriched, err := enrichTask("check @note.txt", nil, dir)
	if err != nil {
		t.Fatalf("enrichTask error: %v", err)
	}
	if !strings.Contains(enriched, "<untrusted_content_") {
		t.Fatalf("enriched prompt should wrap file content in untrusted_content, got: %s", enriched)
	}
}

func TestEnrichTask_WrapsCtxFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "data.txt"), []byte("sensitive data"), 0644)

	enriched, err := enrichTask("analyze", []string{"data.txt"}, dir)
	if err != nil {
		t.Fatalf("enrichTask error: %v", err)
	}
	if !strings.Contains(enriched, "<untrusted_content_") {
		t.Fatalf("--ctx content should be wrapped in untrusted_content, got: %s", enriched)
	}
}

// ── 17. session_search list/search/find must wrap Task/Buffer ────────────

func TestSessionSearch_ListWrapsTask(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	store, err := session.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	sess := &session.Session{
		ID:        "list-test",
		Task:      "user task about go-vector",
		Model:     "test",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	tool := newSessionSearchTool(store)
	result := callJSON(t, tool, `{"action":"list"}`)
	var r struct {
		Sessions []struct {
			Task string `json:"task"`
		} `json:"sessions"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Sessions) == 0 || !strings.HasPrefix(r.Sessions[0].Task, "<untrusted_content_") {
		t.Fatalf("session list should wrap task in untrusted_content, got: %s", result)
	}
}

// ── 18. Resource resolver must reject huge files ─────────────────────────

func TestResourceResolver_RejectsHugeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.txt")
	os.WriteFile(path, make([]byte, 15*1024*1024), 0644)

	res := resource.NewFileResolver(dir)
	_, err := res.Load(context.Background(), "huge.txt")
	if err == nil {
		t.Fatal("resource resolver should reject a 15 MiB file")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected size error, got: %v", err)
	}
}

// ── 19. delegate_tasks must cap summary size ─────────────────────────────

func TestDelegateTasks_CapsSummarySize(t *testing.T) {
	if os.Getenv("ODEK_E2E") == "" {
		t.Skip("sub-agent spawning test; set ODEK_E2E=true to run")
	}
	fakeOdek := filepath.Join(t.TempDir(), "fake-odek")
	// Print a valid JSON result whose summary is ~6 MB.
	script := `#!/bin/sh
printf '{"status":"success","summary":"%s","files_changed":[],"iterations":1,"tokens_used":10}\n' "$(head -c 6000000 /dev/zero | tr '\0' 'x')"
`
	os.WriteFile(fakeOdek, []byte(script), 0755)

	tool := &delegateTasksTool{
		odekPath:       fakeOdek,
		maxConcurrency: 1,
		timeout:        30 * time.Second,
	}
	tool.SetContext(context.Background())
	result, err := tool.Call(`{"tasks":[{"goal":"a"},{"goal":"b"}],"description":"summary cap test"}`)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	if len(result) > 1024*1024+500 {
		t.Fatalf("delegate_tasks summary returned %d bytes, expected cap near 1 MiB", len(result))
	}
}

// ── 20. patch / batch_patch must cap ReplaceAll expansion ────────────────

func TestPatch_RejectsOutputExpansion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.txt")
	// 2,000 'a' chars. Replacing each with 10,000 'x' => ~20M chars.
	os.WriteFile(path, []byte(strings.Repeat("a", 2000)), 0644)

	tool := &patchTool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"path":%q,"old_string":"a","new_string":%q,"replace_all":true}`, path, strings.Repeat("x", 10000)))
	var r struct {
		Success bool   `json:"success"`
		Error   string `json:"error,omitempty"`
	}
	mustUnmarshal(t, result, &r)
	if r.Success {
		t.Fatal("patch should reject a ReplaceAll that explodes output size")
	}
	if !strings.Contains(r.Error, "too large") {
		t.Fatalf("expected size error, got: %q", r.Error)
	}
}

func TestBatchPatch_RejectsOutputExpansion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.txt")
	os.WriteFile(path, []byte(strings.Repeat("a", 2000)), 0644)

	tool := &batchPatchTool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"patches":[{"path":%q,"old_string":"a","new_string":%q,"replace_all":true}]}`, path, strings.Repeat("x", 10000)))
	var r struct {
		Results []struct {
			Success bool   `json:"success"`
			Error   string `json:"error,omitempty"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Results) != 1 || r.Results[0].Success {
		t.Fatal("batch_patch should reject a ReplaceAll that explodes output size")
	}
	if !strings.Contains(r.Results[0].Error, "too large") {
		t.Fatalf("expected size error, got: %q", r.Results[0].Error)
	}
}
