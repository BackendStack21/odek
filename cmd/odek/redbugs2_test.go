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

	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/session"
)

// ────────────────────────────────────────────────────────────────────────
// RED #B1 (U2): The sandbox kill follow-up runs `docker exec` with no
// deadline, synchronously after the command's own timeout already fired.
// A hung Docker daemon wedges the tool call forever — voiding the shell
// tool's contract that a stuck command can never wedge the agent.
func TestRED_ShellSandboxKillFollowUpHasDeadline(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	os.MkdirAll(binDir, 0o755)
	// Fake docker: any `exec` subcommand hangs (simulates a dockerd that
	// accepts connections but never responds). Everything else succeeds.
	script := "#!/bin/sh\n" +
		`if [ "$1" = "exec" ]; then sleep 299; fi` + "\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	allow := "allow"
	tool := &shellTool{
		containerName:   "red-test-container",
		dangerousConfig: danger.DangerousConfig{DefaultAction: &allow},
	}

	type callRes struct {
		result string
		err    error
	}
	done := make(chan callRes, 1)
	go func() {
		res, err := tool.Call(`{"command":"sleep 30","timeout_seconds":1}`)
		done <- callRes{res, err}
	}()

	select {
	case r := <-done:
		// The command itself must time out; the kill follow-up must not add
		// an unbounded delay of its own.
		if r.err == nil || !strings.Contains(r.err.Error()+" "+r.result, "timed out") {
			t.Errorf("expected timeout error, got result=%q err=%v", r.result, r.err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("shell tool call wedged past its own timeout — the docker kill follow-up has no deadline")
	}
}

// ────────────────────────────────────────────────────────────────────────
// RED #B2 (V3): /api/sessions search filters against the pre-fetched
// recency window, so matches deeper in the store are silently dropped:
// a query whose only match is older than limit+offset returns count=0
// even though the session exists.
func TestRED_SessionSearchSearchesWholeStore(t *testing.T) {
	dir := t.TempDir()
	store, err := session.NewStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Save oldest-first so the matching session is LAST in recency order.
	for i := 4; i >= 0; i-- {
		task := fmt.Sprintf("filler session %d", i)
		if i == 0 {
			task = "the needle-in-the-haystack session"
		}
		s := &session.Session{
			ID:       session.GenerateID(),
			Task:     task,
			Messages: llmMessage("hi"),
		}
		if err := store.Save(s); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond) // distinct UpdatedAt ordering
	}

	handler := handleSessionListPaged(store)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions?q=needle&limit=2&offset=0", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	var out struct {
		Sessions []struct {
			ID   string `json:"id"`
			Task string `json:"task"`
		} `json:"sessions"`
		Count int `json:"count"`
	}
	mustUnmarshal(t, rec.Body.String(), &out)

	if out.Count == 0 {
		t.Fatalf("search returned %d results for 'needle'; the matching session is older than the fetch window but must still be found (got %+v)", out.Count, out.Sessions)
	}
	found := false
	for _, s := range out.Sessions {
		if strings.Contains(s.Task, "needle") {
			found = true
		}
	}
	if !found {
		t.Errorf("search results missing the matching session: %+v", out.Sessions)
	}
}

// ────────────────────────────────────────────────────────────────────────
// RED #B3 (V4): Concurrent prompts on one session overwrite each other's
// cancel registration and unregisterPromptCancel deletes unconditionally —
// when the FIRST prompt finishes it removes the SECOND prompt's cancel
// func, making /api/cancel a silent no-op while the newer prompt runs.
func TestRED_PromptCancelSurvivesEarlierPromptFinishing(t *testing.T) {
	calledSecond := false
	registerPromptCancel("red-b3-sess", func() {})                      // prompt 1 starts
	registerPromptCancel("red-b3-sess", func() { calledSecond = true }) // prompt 2 starts

	unregisterPromptCancel("red-b3-sess") // prompt 1 finishes first

	if !cancelPrompt("red-b3-sess") {
		t.Fatal("cancelPrompt found no registration after the earlier prompt finished — the second prompt can no longer be cancelled")
	}
	if !calledSecond {
		t.Fatal("cancelPrompt did not invoke the second (live) prompt's cancel function")
	}
	unregisterPromptCancel("red-b3-sess")
	if cancelPrompt("red-b3-sess") {
		t.Error("cancelPrompt should report false once the live prompt unregisters")
	}
}

// ────────────────────────────────────────────────────────────────────────
// RED #B6 (M6): The REPL editor advertises /sandbox /model /session via
// tab-completion, but handleREPLCommand doesn't implement them — completing
// then pressing enter yields "Unknown command".
func TestRED_REPLCompletionsAreImplemented(t *testing.T) {
	advertised := []string{
		"/exit", "/quit", "/help", "/info",
		"/sandbox", "/model", "/session",
	}
	sess := &session.Session{ID: "red-b6"}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()

	var sb strings.Builder
	done := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			sb.Write(buf[:n])
			if err != nil {
				close(done)
				return
			}
		}
	}()

	for _, cmd := range advertised {
		handleREPLCommand(cmd, sess)
	}
	w.Close()
	<-done

	if strings.Contains(sb.String(), "Unknown command") {
		t.Errorf("REPL advertises commands it does not implement: %s", sb.String())
	}
}

// llmMessage builds a single user message for session fixtures.
func llmMessage(content string) []llm.Message {
	return []llm.Message{{Role: "user", Content: content}}
}

var _ = context.Background // keep context imported for future cases

// ────────────────────────────────────────────────────────────────────────
// RED #B9 (T5): base64 decode-from-file returns raw file bytes to the
// model OUTSIDE the <untrusted_content_*> wrapper, while every other
// externally-sourced tool result is wrapped — a hole in the untrusted-
// content boundary (SECURITY.md invariant).
func TestRED_Base64DecodeFromFileIsWrapped(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "blob.txt")
	os.WriteFile(p, []byte("external file payload"), 0o644)

	tool := &base64Tool{}
	res := callJSON(t, tool, fmt.Sprintf(`{"path":%q,"decode":true}`, p))
	if !strings.Contains(res, "untrusted_content_") {
		t.Errorf("decoded file content returned unwrapped: %s", res)
	}
}
