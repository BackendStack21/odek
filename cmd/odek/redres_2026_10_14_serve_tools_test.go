package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	golangws "golang.org/x/net/websocket"
)

// ── clarify question display sanitizing ─────────────────────────────────

func TestRED_ClarifyQuestionSanitizedForDisplay(t *testing.T) {
	var mu sync.Mutex
	var sent []clarifyRequest
	a := newWSApprover(func(v any) error {
		if cr, ok := v.(clarifyRequest); ok {
			mu.Lock()
			sent = append(sent, cr)
			mu.Unlock()
		}
		return nil
	})
	q := "proceed?‮ evil ​\x1b[31m red"
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = a.PromptClarify(q)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(sent)
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	a.Close()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(sent) == 0 {
		t.Fatal("no clarify_request sent")
	}
	got := sent[0].Question
	for _, bad := range []string{"‮", "​", "\x1b"} {
		if strings.Contains(got, bad) {
			t.Fatalf("clarify question carries raw %q: %q", bad, got)
		}
	}
	if !strings.Contains(got, "proceed?") {
		t.Fatalf("question text lost: %q", got)
	}
	for _, d := range a.drainDecisions("t") {
		if strings.ContainsAny(d.Command, "‮​\x1b") {
			t.Fatalf("recorded decision carries raw control characters: %q", d.Command)
		}
	}
}

// ── REST requires_confirmation mirrors the enforced setting ─────────────

func TestRED_RunSnapshotRequiresConfirmationMatchesEnforcement(t *testing.T) {
	prev := restApprovalFrictionEnabled
	t.Cleanup(func() { restApprovalFrictionEnabled = prev })
	restApprovalFrictionEnabled = false
	r := &serveRun{ID: "run-svtres", Status: "waiting_approval", pending: map[string]*approvalRequest{}}
	r.pending["a"] = &approvalRequest{ID: "a", Friction: true}
	pend := r.snapshot(false)["pending_approvals"].([]map[string]any)
	if pend[0]["requires_confirmation"] != false {
		t.Fatalf("requires_confirmation advertised but the answer endpoint does not enforce it: %v", pend[0])
	}
	if pend[0]["friction"] != true {
		t.Fatalf("friction flag must still be reported: %v", pend[0])
	}
	restApprovalFrictionEnabled = true
	pend = r.snapshot(false)["pending_approvals"].([]map[string]any)
	if pend[0]["requires_confirmation"] != true {
		t.Fatalf("enforced friction must be advertised: %v", pend[0])
	}
}

// ── rejected WS prompt must not switch the connection model ─────────────

func TestRED_WSRejectedTokenPromptDoesNotSwitchModel(t *testing.T) {
	var mu sync.Mutex
	var models []string
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		models = append(models, body.Model)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer llm.Close()
	defer setTestEnv(t, llm.URL)()

	store := newTestSessionStore(t)
	ln, mux := buildServeMuxPromptAll(t, store)
	defer startServeTest(t, ln, mux)()
	waitForHTTP(t, ln.Addr().String())
	wsUpgradeLimiter.reset()
	conn := dialTestWS(t, ln.Addr().String())
	defer conn.Close()

	send := func(m map[string]string) {
		b, _ := json.Marshal(m)
		if err := golangws.Message.Send(conn, string(b)); err != nil {
			t.Fatal(err)
		}
	}
	send(map[string]string{"type": "prompt", "content": "hello"})
	var sid, tok string
	readWSUntil(t, conn, 30*time.Second, func(e map[string]any) bool {
		if e["type"] == "session" {
			sid, _ = e["session_id"].(string)
			tok, _ = e["auth_token"].(string)
		}
		return e["type"] == "done"
	})
	other, err := store.Create(nil, "m", "svtres-other")
	if err != nil {
		t.Fatalf("create second session: %v", err)
	}
	send(map[string]string{"type": "prompt", "content": "x", "session_id": other.ID, "auth_token": "wrong", "model": "svtres-switched-model"})
	readWSUntil(t, conn, 30*time.Second, func(e map[string]any) bool { return e["type"] == "error" })
	send(map[string]string{"type": "prompt", "content": "again", "session_id": sid, "auth_token": tok})
	readWSUntil(t, conn, 30*time.Second, func(e map[string]any) bool { return e["type"] == "done" })

	mu.Lock()
	defer mu.Unlock()
	for _, m := range models {
		if m == "svtres-switched-model" {
			t.Fatalf("rejected prompt switched the connection model; LLM saw models %v", models)
		}
	}
}

// ── tree: skip list and total cap ───────────────────────────────────────

func TestRED_TreeSkipsDependencyDirs(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"node_modules", "vendor", "src"} {
		if err := os.MkdirAll(filepath.Join(dir, d, "pkg"), 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(dir, d, "pkg", "f.txt"), []byte("x"), 0o644)
	}
	out, _ := (&treeTool{}).Call(fmt.Sprintf(`{"path":%q,"max_depth":5}`, dir))
	if strings.Contains(out, "node_modules") || strings.Contains(out, "vendor") {
		t.Fatalf("tree descended into dependency directories: %s", out)
	}
	if !strings.Contains(out, "src") {
		t.Fatalf("tree lost ordinary directory: %s", out)
	}
	// An explicitly requested root inside a skipped directory is still listed.
	out, _ = (&treeTool{}).Call(fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "node_modules")))
	if !strings.Contains(out, "pkg") {
		t.Fatalf("explicit root under a skipped dir must still list: %s", out)
	}
}

func TestRED_TreeTotalEntryCap(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		sub := filepath.Join(dir, fmt.Sprintf("d%d", i))
		_ = os.Mkdir(sub, 0o755)
		for j := 0; j < 900; j++ {
			_ = os.WriteFile(filepath.Join(sub, fmt.Sprintf("f%d.txt", j)), nil, 0o644)
		}
	}
	out, _ := (&treeTool{}).Call(fmt.Sprintf(`{"path":%q,"max_depth":3}`, dir))
	if n := strings.Count(out, `"is_dir"`); n > maxTreeTotalEntries+10 {
		t.Fatalf("tree emitted %d entries, cap %d", n, maxTreeTotalEntries)
	}
	if !strings.Contains(out, "tree truncated") {
		t.Fatalf("missing truncation notice")
	}
	var r struct {
		Tree treeEntry `json:"tree"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
}

func TestRED_TreeErrorEntryOmitsFileName(t *testing.T) {
	dir := t.TempDir()
	e, _ := buildTree(filepath.Join(dir, "secret-name-svtres"), 1, 3, false, nil, nil)
	if strings.Contains(e.ErrMsg, "secret-name-svtres") {
		t.Fatalf("error text carries the file name outside the untrusted wrapper: %q", e.ErrMsg)
	}
	if e.ErrMsg == "" {
		t.Fatal("error entry lost its reason")
	}
}

// ── artifact_read JSON errors ───────────────────────────────────────────

func TestRED_ArtifactReadErrorsAreValidJSON(t *testing.T) {
	resetArtifactRegistryForTest()
	tl := &artifactReadTool{}
	tl.SetContext(t.Context())
	ids := []string{"a\"b", "x\\y", "tab\there", "nul\x00z", "uni \xff", "q\x1b[0m"}
	for _, id := range ids {
		b, _ := json.Marshal(map[string]any{"id": id})
		out, _ := tl.Call(string(b))
		var m map[string]any
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			t.Fatalf("id %q: invalid JSON result %q: %v", id, out, err)
		}
		if m["error"] == nil {
			t.Fatalf("id %q: no error field: %q", id, out)
		}
	}
	out, _ := tl.Call(`{"id": "x", "offset": "bad"}`)
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("parse-failure result is invalid JSON: %q", out)
	}
}

// ── artifact open must not block on a FIFO ──────────────────────────────

func TestRED_ArtifactOpenDoesNotBlockOnFIFO(t *testing.T) {
	p := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if f, err := openArtifactNoFollow(p); err == nil {
			f.Close()
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		// Unblock the stuck open so the test process can finish.
		if f, err := os.OpenFile(p, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			f.Close()
		}
		t.Fatal("opening a swapped-in FIFO blocks")
	}
}
