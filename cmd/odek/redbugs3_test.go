package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/bgproc"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/session"
)

// persistPartialMessages strips only a trailing assistant message that
// still has unanswered tool calls. An interrupted batch that already
// appended some tool results ends on a tool message, so the unmatched
// calls stay in the transcript — resume then sends an invalid
// OpenAI-compatible request (N calls, N-1 results).
func TestRED_DropDanglingToolCallsRemovesUnansweredCalls(t *testing.T) {
	msgs := []session.Message{
		{Role: "user", Content: "do work"},
		{Role: "assistant", ToolCalls: []session.ToolCall{{ID: "c1"}, {ID: "c2"}}},
		{Role: "tool", ToolCallID: "c1", Content: "1"},
	}
	got := dropDanglingToolCalls(msgs)
	if unanswered := unansweredToolCalls(got); unanswered != 0 {
		t.Fatalf("unanswered tool calls = %d after dropDanglingToolCalls; want 0 (partial batch must not be resumed)", unanswered)
	}
}

func unansweredToolCalls(msgs []session.Message) int {
	seen := map[string]int{}
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			if tc.ID != "" {
				seen[tc.ID]++
			}
		}
		if m.Role == "tool" && m.ToolCallID != "" {
			seen[m.ToolCallID]--
		}
	}
	n := 0
	for _, v := range seen {
		if v > 0 {
			n += v
		}
	}
	return n
}

func TestRED_ParallelShellRejectsEmptyCommand(t *testing.T) {
	result, _ := (&parallelShellTool{}).Call(`{"commands":[{"command":""},{"command":"echo hi"}]}`)
	var r struct{ Error string }
	mustUnmarshal(t, result, &r)
	if !strings.Contains(r.Error, "empty") {
		t.Fatalf("parallel_shell empty command error = %q, want it to name empty", r.Error)
	}
}

func TestRED_ReadFileCapsFullFileScan(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(strings.Repeat("x", 99) + "\n")
	written := 0
	for written <= maxFileReadBytes+1<<20 {
		n, err := f.Write(line)
		written += n
		if err != nil {
			f.Close()
			t.Fatal(err)
		}
	}
	f.Close()

	wantLines := written / len(line)
	result := callJSON(t, &readFileTool{}, fmt.Sprintf(`{"path":%q,"offset":1,"limit":3}`, path))
	var r struct {
		TotalLines int    `json:"total_lines"`
		Error      string `json:"error,omitempty"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error != "" {
		t.Fatalf("read_file error: %s", r.Error)
	}
	if r.TotalLines >= wantLines {
		t.Fatalf("total_lines = %d, want a capped count below the full-file %d (scan must stop at the byte cap)", r.TotalLines, wantLines)
	}
}

func TestRED_BindBGRuntimeStopsPreviousSessionJobs(t *testing.T) {
	mgr := bgproc.NewManager(bgproc.Config{MaxJobsPerSession: 2, MaxOutputBytes: 4096}, nil)
	defer mgr.Shutdown()
	job, err := mgr.Start("old-sess", "sleep 30", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	rt := &bgRuntime{mgr: mgr, session: "old-sess"}
	bindBGRuntime(rt, "new-sess")
	if rt.session != "new-sess" {
		t.Fatalf("session = %q, want new-sess", rt.session)
	}
	snap, ok := mgr.Get("old-sess", job.ID)
	if ok && snap.Status == bgproc.StatusRunning {
		t.Fatalf("old-session job still running after bind to a new session")
	}
}

func TestRED_StartServeRunLegacyEmptyTokenFailsClosed(t *testing.T) {
	store := newTestSessionStore(t)
	sess, err := store.Create([]session.Message{{Role: "user", Content: "hi"}}, "m", "legacy")
	if err != nil {
		t.Fatal(err)
	}
	sess.AuthToken = ""
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	_, err = startServeRun(config.ResolvedConfig{}, "system", store, nil, promptRequest{
		Content: "hello", SessionID: sess.ID, AuthToken: "",
	})
	if err == nil {
		t.Fatal("startServeRun accepted a legacy session without presenting the minted token")
	}
	if !strings.Contains(err.Error(), "session token") {
		t.Fatalf("error = %v, want session token rejection", err)
	}
}

func TestRED_ReadKeyFromInheritedFDRejectsOverflow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("inherited-FD handoff is POSIX")
	}
	f, err := os.CreateTemp("", "odek-key-overflow-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		f.Close()
		os.Remove(f.Name())
	}()
	if _, err := f.Write(bytesRepeat(4097, 'A')); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	// Dup so the helper can close its FD without invalidating ours.
	fd, err := syscall.Dup(int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(keyFDEnvVar, fmt.Sprintf("%d", fd))
	if got := readKeyFromInheritedFD(); got != "" {
		t.Fatalf("overflow key = %d bytes, want empty (fail closed, no silent truncate)", len(got))
	}
}

func bytesRepeat(n int, b byte) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

func TestRED_WithSessionExecutionReleasesOnPanic(t *testing.T) {
	store := newTestSessionStore(t)
	sess, err := store.Create([]session.Message{{Role: "user", Content: "hi"}}, "m", "lock")
	if err != nil {
		t.Fatal(err)
	}
	panicked := false
	func() {
		defer func() {
			if recover() != nil {
				panicked = true
			}
		}()
		_ = withSessionExecution(context.Background(), store, sess.ID, func() error {
			panic("turn boom")
		})
	}()
	if !panicked {
		t.Fatal("expected panic to propagate")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	release, err := store.AcquireExecution(ctx, sess.ID)
	if err != nil {
		t.Fatalf("execution lock still held after panic: %v", err)
	}
	release()
}

func TestRED_SearchFiles_SymlinkDirectoryTraversal(t *testing.T) {
	skipIfSymlinksUnsupported(t)
	cwd := t.TempDir()
	origDir, _ := os.Getwd()
	os.Chdir(cwd)
	defer os.Chdir(origDir)

	outsideDir := symlinkSensitiveDir(t)
	outsideFile := filepath.Join(outsideDir, "secret-search.txt")
	os.WriteFile(outsideFile, []byte("unique-secret-token-xyz"), 0600)
	t.Cleanup(func() { os.Remove(outsideFile) })

	link := filepath.Join(cwd, "link")
	if err := os.Symlink(outsideDir, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	dc := danger.DangerousConfig{
		Classes: map[danger.RiskClass]danger.Action{
			danger.SystemWrite: danger.Deny,
		},
	}
	result := callJSON(t, &searchFilesTool{dangerousConfig: dc}, `{"path":".","pattern":"unique-secret-token-xyz","target":"content"}`)
	if strings.Contains(result, "unique-secret-token-xyz") && !strings.Contains(result, "denied") {
		t.Fatalf("search_files returned symlink-traversal content:\n%s", result)
	}
}

func TestRED_Transcribe_SymlinkDirectoryTraversal(t *testing.T) {
	skipIfSymlinksUnsupported(t)
	cwd := t.TempDir()
	origDir, _ := os.Getwd()
	os.Chdir(cwd)
	defer os.Chdir(origDir)

	outsideDir := symlinkSensitiveDir(t)
	outsideFile := filepath.Join(outsideDir, "secret.wav")
	os.WriteFile(outsideFile, []byte("RIFF"), 0600)
	t.Cleanup(func() { os.Remove(outsideFile) })

	link := filepath.Join(cwd, "link")
	if err := os.Symlink(outsideDir, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	dc := danger.DangerousConfig{
		Classes: map[danger.RiskClass]danger.Action{
			danger.SystemWrite: danger.Deny,
		},
	}
	result := callJSON(t, &transcribeTool{dangerousConfig: dc}, fmt.Sprintf(`{"path":%q}`, filepath.Join(link, "secret.wav")))
	if !strings.Contains(result, "denied") {
		t.Fatalf("transcribe should deny symlink directory traversal, got: %s", result)
	}
}

func TestRED_Vision_SymlinkDirectoryTraversal(t *testing.T) {
	skipIfSymlinksUnsupported(t)
	cwd := t.TempDir()
	origDir, _ := os.Getwd()
	os.Chdir(cwd)
	defer os.Chdir(origDir)

	outsideDir := symlinkSensitiveDir(t)
	outsideFile := filepath.Join(outsideDir, "secret.png")
	os.WriteFile(outsideFile, []byte("PNG"), 0600)
	t.Cleanup(func() { os.Remove(outsideFile) })

	link := filepath.Join(cwd, "link")
	if err := os.Symlink(outsideDir, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	dc := danger.DangerousConfig{
		Classes: map[danger.RiskClass]danger.Action{
			danger.SystemWrite: danger.Deny,
		},
	}
	result := callJSON(t, &visionTool{dangerousConfig: dc}, fmt.Sprintf(`{"path":%q}`, filepath.Join(link, "secret.png")))
	if !strings.Contains(result, "denied") {
		t.Fatalf("vision should deny symlink directory traversal, got: %s", result)
	}
}

func TestRED_TTYApproverHonorsContextCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fifo TTY cancel test is POSIX")
	}
	fifo := filepath.Join(t.TempDir(), "tty")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	w, err := os.OpenFile(fifo, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	ctx, cancel := context.WithCancel(context.Background())
	a := danger.NewTTYApprover(&danger.DangerousConfig{NonInteractive: strPtrDanger("deny")})
	a.TTYPath = fifo
	a.Ctx = ctx

	done := make(chan error, 1)
	go func() {
		done <- a.PromptCommand(danger.SystemWrite, "rm x", "test")
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("PromptCommand succeeded after cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PromptCommand did not return after context cancel")
	}
}

func strPtrDanger(s string) *string { return &s }
