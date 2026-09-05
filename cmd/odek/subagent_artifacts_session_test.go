package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/danger"
)

// F1 (Bug A): delegate_tasks must file per-task artifact dirs under
// artifacts/<session_id>/ once the parent session id is wired, so the
// session-delete cascade owns the artifact lifecycle. Before the fix the
// session id never reached the tool and every task landed under unfiled/.
func TestDelegateTasks_SessionIDFilesUnderSessionDir(t *testing.T) {
	root := t.TempDir()
	var artifactDir string
	tool := &delegateTasksTool{
		maxConcurrency: 1,
		odekPath:       "unused",
		timeout:        time.Second,
		artifactsRoot:  root,
		runTaskFn: func(_ int, _, _, _, _, _, _, _, dir string) string {
			artifactDir = dir
			return `{"status":"success","summary":"ok"}`
		},
	}
	tool.SetContext(context.Background())
	tool.SetSessionID("20260905-abcdef1234")
	if _, err := tool.Call(`{"tasks":[{"goal":"g"}]}`); err != nil {
		t.Fatal(err)
	}
	if artifactDir == "" {
		t.Fatal("runTaskFn did not receive an artifact dir")
	}
	rel, err := filepath.Rel(root, artifactDir)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(filepath.ToSlash(rel), "/", 2)
	if len(parts) != 2 || parts[0] != "20260905-abcdef1234" || !strings.HasPrefix(parts[1], "task-") {
		t.Errorf("artifact dir filed under %q, want <root>/20260905-abcdef1234/task-*", rel)
	}
	if fi, err := os.Stat(artifactDir); err != nil || !fi.IsDir() {
		t.Errorf("task artifact dir must exist: %v", err)
	}
}

// F1 serve wrinkle: a Web UI connection can switch sessions mid-flight, so
// the session id must rebind between delegations — earlier filings stay
// under the session they were created for. Each Call is synchronous, so
// captures are naturally ordered.
func TestDelegateTasks_SessionIDRebindsBetweenDelegations(t *testing.T) {
	root := t.TempDir()
	var dirs []string
	tool := &delegateTasksTool{
		maxConcurrency: 1,
		odekPath:       "unused",
		timeout:        time.Second,
		artifactsRoot:  root,
		runTaskFn: func(_ int, _, _, _, _, _, _, _, dir string) string {
			dirs = append(dirs, dir)
			return `{"status":"success","summary":"ok"}`
		},
	}
	tool.SetContext(context.Background())

	tool.SetSessionID("sess-first")
	if _, err := tool.Call(`{"tasks":[{"goal":"a"}]}`); err != nil {
		t.Fatal(err)
	}
	tool.SetSessionID("sess-second")
	if _, err := tool.Call(`{"tasks":[{"goal":"b"}]}`); err != nil {
		t.Fatal(err)
	}

	if len(dirs) != 2 {
		t.Fatalf("want 2 artifact dirs, got %d", len(dirs))
	}
	for i, want := range []string{"sess-first", "sess-second"} {
		rel, err := filepath.Rel(root, dirs[i])
		if err != nil {
			t.Fatal(err)
		}
		if parent := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]; parent != want {
			t.Errorf("delegation %d filed under %q, want %q", i+1, parent, want)
		}
	}
}

// F1 wiring seam: Agent.SetToolSessionID must stamp the id onto the very
// tool instances builtinTools registered (serve/run/repl/telegram/continue
// all bind through this public method, serve per prompt).
func TestAgentSetToolSessionID_StampsDelegateTool(t *testing.T) {
	tools := builtinTools(danger.DangerousConfig{}, nil, nil, 2, "", toolConfig{}, nil)
	dt := findDelegateTool(t, tools)

	agent, err := odek.New(odek.Config{
		Model:           "test-model",
		APIKey:          "sk-test",
		NoProjectFile:   true,
		Tools:           tools,
		DangerousConfig: &danger.DangerousConfig{},
	})
	if err != nil {
		t.Fatalf("odek.New: %v", err)
	}
	defer agent.Close()

	if got := dt.getSessionID(); got != "" {
		t.Fatalf("precondition: session id should start empty, got %q", got)
	}
	agent.SetToolSessionID("sess-agent-level")
	if got := dt.getSessionID(); got != "sess-agent-level" {
		t.Errorf("Agent.SetToolSessionID did not reach delegate_tasks: got %q", got)
	}
}

// F1 defensive fallback: an unset or invalid session id still files under
// unfiled/ (the janitor sweeps that bucket per task).
func TestDelegateTasks_InvalidSessionIDFallsBackToUnfiled(t *testing.T) {
	root := t.TempDir()
	var artifactDir string
	tool := &delegateTasksTool{
		maxConcurrency: 1,
		odekPath:       "unused",
		timeout:        time.Second,
		artifactsRoot:  root,
		runTaskFn: func(_ int, _, _, _, _, _, _, _, dir string) string {
			artifactDir = dir
			return `{"status":"success","summary":"ok"}`
		},
	}
	tool.SetContext(context.Background())
	tool.SetSessionID("../escape-attempt")
	if _, err := tool.Call(`{"tasks":[{"goal":"g"}]}`); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(root, artifactDir)
	if err != nil {
		t.Fatal(err)
	}
	if parent := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]; parent != "unfiled" {
		t.Errorf("invalid session id must file under unfiled, got %q", rel)
	}
	if strings.Contains(rel, "..") {
		t.Errorf("path traversal in artifact dir: %q", rel)
	}
}
