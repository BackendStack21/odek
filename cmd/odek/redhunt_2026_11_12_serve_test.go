package main

// RED tests for the serve area, bug hunt 2026-11-12. Each test encodes the
// correct behaviour and fails on the current code for the reason named in
// its comment.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/resource"
	"github.com/BackendStack21/odek/internal/session"
)

// serveHuntUpload stores one PNG through the production upload handler for
// sess in workspace and returns the server-side upload record.
func serveHuntUpload(t *testing.T, store *session.Store, sess *session.Session, workspace string) (string, browserUpload) {
	t.Helper()
	png := []byte{137, 80, 78, 71, 13, 10, 26, 10, 0, 0, 0, 13, 'I', 'H', 'D', 'R'}
	req := httptest.NewRequest("POST", "/api/uploads?name=diagram.png&session_id="+sess.ID, bytes.NewReader(png))
	req.Header.Set("X-Session-Token", sess.AuthToken)
	rec := httptest.NewRecorder()
	handleBrowserUpload(store, "model", workspace)(rec, req)
	if rec.Code != 201 {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID string `json:"upload_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.ID == "" {
		t.Fatalf("upload response: %v %s", err, rec.Body.String())
	}
	item, ok := resolveBrowserUpload(out.ID, sess.ID)
	if !ok {
		t.Fatal("upload does not resolve")
	}
	t.Cleanup(func() {
		browserUploads.Lock()
		delete(browserUploads.entries, out.ID)
		browserUploads.Unlock()
		browserArtifacts.Lock()
		for id, a := range browserArtifacts.entries {
			if a.SessionID == sess.ID {
				browserArtifacts.bytes -= len(a.data)
				delete(browserArtifacts.entries, id)
			}
		}
		browserArtifacts.Unlock()
	})
	return out.ID, item
}

// Web UI uploads are stored in <workspace>/.odek-artifacts/uploads/<sid>/,
// inside the sub-agent staging root. Every artifact-bearing delegate_tasks
// child sweeps that root at start (sweepStagingOrphans) and RemoveAlls every
// sibling directory older than 24h except its own task dir — "uploads"
// included. The uploads directory's mtime only moves when a session
// subdirectory is created or removed, so a day after the first upload of a
// session (well inside the documented 7-day upload retention) any delegation
// in that workspace deletes every uploaded file, even one uploaded seconds
// ago and pinned by the running turn.
func TestRED_StagingSweepDeletesLiveBrowserUploads(t *testing.T) {
	workspace := t.TempDir()
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create(nil, "model", "uploads")
	if err != nil {
		t.Fatal(err)
	}

	// A fresh upload, accepted as an attachment by the running turn (pinned
	// against serve's own retention).
	id, item := serveHuntUpload(t, store, sess, workspace)
	_, release, ok := acquireBrowserUpload(id, sess.ID)
	if !ok {
		t.Fatal("upload cannot be pinned")
	}
	defer release()

	// The session's upload directory was created a day and a bit ago (its
	// first upload); the file itself is brand new.
	uploadsDir := filepath.Join(workspace, ".odek-artifacts", "uploads")
	aged := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(uploadsDir, aged, aged); err != nil {
		t.Fatal(err)
	}

	// A delegate_tasks child with an artifact root starts in the workspace.
	ensureStagingRoot(workspace)
	sweepStagingOrphans(workspace, "task-new", stagingSweepMaxAge)

	if _, err := os.Stat(item.path); err != nil {
		t.Fatalf("sub-agent staging sweep deleted a live, pinned Web UI upload (%s): %v", item.path, err)
	}
}

// serveHuntWriteTool is a stand-in write_file that always succeeds, so the
// run's mutation ledger records a completed write.
type serveHuntWriteTool struct{}

func (serveHuntWriteTool) Name() string        { return "write_file" }
func (serveHuntWriteTool) Description() string { return "write a file" }
func (serveHuntWriteTool) Schema() any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}}}
}
func (serveHuntWriteTool) Call(string) (string, error) { return `{"success":true}`, nil }

// serveHuntLedgerLLM scripts a streaming provider: the first main call writes
// a file, every later main call denies having changed anything. Side calls
// (memory, titles) get a neutral reply.
func serveHuntLedgerLLM(t *testing.T) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	mainCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		stream := strings.Contains(string(body), `"stream":true`)
		mu.Lock()
		main := strings.Contains(string(body), `"tools":`)
		if main {
			mainCalls++
		}
		n := mainCalls
		mu.Unlock()
		if main && n == 1 {
			args, _ := json.Marshal(`{"path":"notes.txt","content":"x"}`)
			call := `[{"index":0,"id":"call_1","type":"function","function":{"name":"write_file","arguments":` + string(args) + `}}]`
			if stream {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":%s}}]}\n\n", call)
				fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":%s},"finish_reason":"tool_calls"}]}`, call)
			return
		}
		content := "ok"
		if main {
			content = "All set. No changes were made."
		}
		quoted, _ := json.Marshal(content)
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s}}]}\n\n", quoted)
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%s},"finish_reason":"stop"}]}`, quoted)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// When a final reply denies side effects the run's ledger shows completed
// ("no changes were made" after a write), the loop appends the runtime's
// consistency notice to the answer (docs/SECURITY.md: "a confident all-clear
// actively stops the user from looking"). On serve the answer streams live
// as token_delta frames; handlePrompt then skips the bulk resend because the
// final call streamed, so the notice — appended after streaming — reaches
// the session file but never the live Web UI (or a REST run's result). The
// reply_ledger_mismatch agent_signal is also ignored by the Web UI. The
// operator watching the turn sees only the model's false all-clear.
func TestRED_StreamedAnswerDropsConsistencyNotice(t *testing.T) {
	llm := serveHuntLedgerLLM(t)
	log := &frameLog{}
	sendAny := func(v any) error {
		if m, ok := v.(map[string]any); ok {
			log.send(m)
		}
		return nil
	}
	deltas := &wsDeltaCounters{}
	var built *odek.Agent
	a, err := odek.New(odek.Config{
		Provider: "deepseek", BaseURL: llm.URL, APIKey: "test-key", Model: "test-model",
		SystemMessage: "Help", NoProjectFile: true, MemoryDir: t.TempDir(), MaxIterations: 6,
		Stream: true, DeltaHandler: serveDeltaHandler(sendAny, deltas),
		Tools:              []odek.Tool{serveHuntWriteTool{}},
		EventHandler:       func(events.Event) {},
		AnswerEventHandler: serveAnswerEventHandler(sendAny, func() string { return built.RunID() }),
	})
	if err != nil {
		t.Fatal(err)
	}
	built = a
	t.Cleanup(func() { a.Close() })
	store := newTestSessionStore(t)
	sess, err := store.Create(nil, "test-model", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	handlePrompt(context.Background(), log.send, store, resource.NewRegistry(), loadJSONMockResolved(), a, nil, sess,
		wsClientMsg{Type: "prompt", Content: "set up the notes file"}, new(int), new(int), nil, deltas, nil, nil)

	saved, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	persisted, ok := turnAnswer(saved.Messages)
	if !ok || !strings.Contains(persisted, "odek consistency notice") {
		t.Fatalf("precondition: the persisted answer carries no consistency notice: %q", persisted)
	}

	// The notice must reach the live client in some frame of the final
	// answer (a token frame, the done frame, ...) after the last tool call or
	// superseded draft. shown is the answer text the client streamed.
	shown, delivered := "", false
	log.mu.Lock()
	for _, f := range log.frames {
		switch f["type"] {
		case "tool_call", "answer_superseded":
			shown, delivered = "", false
		case "token", "token_delta":
			shown += fmt.Sprint(f["content"])
		}
		if raw, err := json.Marshal(f); err == nil && strings.Contains(string(raw), "odek consistency notice") {
			delivered = true
		}
	}
	log.mu.Unlock()
	if !delivered {
		t.Fatalf("the live client never receives the runtime's consistency notice; it shows only %q while the session stores %q", shown, persisted)
	}
}

// The staging sweep owns only task-shaped directories: every other entry in
// .odek-artifacts/ survives, aged or not, symlinks are never followed, and
// genuine stale staging orphans are still removed.
func TestSweepStagingOrphans_OnlyRemovesOwnStagingDirs(t *testing.T) {
	ws := t.TempDir()
	root := filepath.Join(ws, stagingDirName)
	old := time.Now().Add(-72 * time.Hour)
	mk := func(name string) string {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Join(p, "sub"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "sub", "f"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
		return p
	}
	orphanHex := mk("task-0123456789abcdef")
	orphanB36 := mk("task-lq3k9z0a1")
	keep := []string{mk("uploads"), mk("other"), mk("task-"), mk("task-UPPER"), mk("Task-abc"), mk("task-dead.bak")}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "task-feedbeef")); err != nil {
		t.Fatal(err)
	}
	loose := filepath.Join(root, "loose-file")
	if err := os.WriteFile(loose, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if n := sweepStagingOrphans(ws, "task-cur", stagingSweepMaxAge); n != 2 {
		t.Fatalf("removed = %d, want 2", n)
	}
	for _, p := range []string{orphanHex, orphanB36} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("stale staging orphan %s must be removed", p)
		}
	}
	for _, p := range append(keep, loose, filepath.Join(outside, "f")) {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("non-staging entry %s must survive: %v", p, err)
		}
	}
}
