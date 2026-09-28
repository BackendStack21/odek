package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/maintenance"
	"github.com/BackendStack21/odek/internal/runtimelog"
)

func TestSubagentRuntimeRelaySessionAncestryAndPrivacy(t *testing.T) {
	var got []events.Event
	tool := &delegateTasksTool{}
	tool.SetEventContext(events.Context{RunID: "parent", RootRunID: "root", TurnID: "turn", SessionID: "session", TaskID: "parent-task"})
	tool.SetEventEmitter(func(ev events.Event) { got = append(got, ev) })
	a := newSubagentActivity()
	feed := func(ev events.Event) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"type": "runtime_event", "event": ev})
		if !tool.relayRuntimeRecord("child", string(b), a) {
			t.Fatal("record not consumed")
		}
	}
	feed(events.Event{Type: "tool_call_started", RunID: "child-run", TaskID: "child", SessionID: "forged-session", Data: map[string]any{"call_id": "one", "args": "private task content"}})
	feed(events.Event{Type: "tool_call_started", RunID: "grandchild-run", TaskID: "grandchild", ParentTaskID: "child", ParentRunID: "child-run", Data: map[string]any{"call_id": "two"}})
	for _, ev := range got {
		if ev.SessionID != "session" || ev.RootRunID != "root" || ev.SourceTaskID != "child" {
			t.Fatalf("lost verified source: %+v", ev)
		}
		if ev.Data["args"] != nil {
			t.Fatal("raw args forwarded")
		}
	}
	if got[0].ParentTaskID != "parent-task" || got[0].ParentRunID != "parent" || got[1].ParentTaskID != "child" || got[1].RunID != "grandchild-run" {
		t.Fatalf("ancestry: %+v", got)
	}
	if n := a.snapshot(time.Now())["pending_calls"]; n != 1 {
		t.Fatalf("descendant polluted direct activity: %v", n)
	}
}

func TestSubagentRuntimeRelayLegacyStartMetadata(t *testing.T) {
	var got []events.Event
	tool := &delegateTasksTool{}
	tool.SetEventEmitter(func(ev events.Event) { got = append(got, ev) })
	line := `{"type":"subagent_started","pid":123,"profile":"fast","max_risk":"read_only","budget_seconds":60,"budget_iterations":5,"budget_cost_usd":0.25,"goal":"private task","stderr":"private output"}`
	if tool.relayRuntimeRecord("child", line, newSubagentActivity()) {
		t.Fatal("legacy lifecycle frame must remain available to the UI")
	}
	if len(got) != 1 {
		t.Fatalf("got %d lifecycle events", len(got))
	}
	ev := got[0]
	if ev.Type != "subagent_started" || ev.TaskID != "child" || len(ev.Data) != 6 || ev.Data["pid"] != 123 || ev.Data["profile"] != "fast" || ev.Data["max_risk"] != "read_only" || ev.Data["budget_seconds"] != 60 || ev.Data["budget_iterations"] != 5 || ev.Data["budget_cost_usd"] != 0.25 {
		t.Fatalf("unexpected lifecycle metadata: %+v", ev)
	}
}
func TestSubagentActivityParallelExecution(t *testing.T) {
	a := newSubagentActivity()
	for _, id := range []string{"fast", "slow"} {
		a.observe(events.Event{Type: "tool_call_started", Data: map[string]any{"call_id": id}})
		a.observe(events.Event{Type: "tool_call_executing", Data: map[string]any{"call_id": id}})
	}
	a.observe(events.Event{Type: "tool_execution_completed", Data: map[string]any{"call_id": "fast"}})
	got := a.snapshot(time.Now())
	if got["active_calls"] != 1 || got["activity"] != "tool_execution" {
		t.Fatalf("completed tool still active: %v", got)
	}
}
func TestSubagentPeriodicObservationStops(t *testing.T) {
	tool := &delegateTasksTool{}
	got := make(chan events.Event, 10)
	tool.SetEventEmitter(func(ev events.Event) { got <- ev })
	stop := tool.monitorSubagent("child", newSubagentActivity(), time.Millisecond)
	select {
	case ev := <-got:
		if ev.TaskID != "child" || ev.Data["activity"] != "process_running" {
			t.Fatal(ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no activity summary")
	}
	stop()
	stop()
}
func TestSubagentTelemetryConcurrentFrames(t *testing.T) {
	var out bytes.Buffer
	w := newSubagentTelemetryWriter(&out, "child")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				w.emit(map[string]any{"type": "runtime_event", "event": events.Event{Type: "run_started"}})
			}
		}()
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 800 {
		t.Fatal(len(lines))
	}
	for _, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatalf("interleaved JSON: %s", line)
		}
	}
}

func TestE2E_SubagentRuntimeLogging(t *testing.T) {
	skipIfNoE2E(t)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"private answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":20}}`))
	}))
	defer provider.Close()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(home)
	_ = os.Mkdir(filepath.Join(home, ".odek"), 0700)
	_ = os.WriteFile(filepath.Join(home, ".odek", "config.json"), []byte(`{"logging":{"enabled":true},"memory":{"enabled":false},"limits":{"input_cost_per_million_usd":2,"output_cost_per_million_usd":4}}`), 0600)
	path := filepath.Join(home, ".odek", "runtime.log")
	logger, err := runtimelog.Open(path, "test", 50)
	if err != nil {
		t.Fatal(err)
	}
	emitter := events.NewEmitter(logger.Emit, "parent-run")
	emitter.SetContext(events.Context{SessionID: "session-1", TurnID: "turn-1"})
	tool := &delegateTasksTool{odekPath: e2eBinary, apiKey: "test-key", timeout: 20 * time.Second, provider: "deepseek", model: "test-model", baseURL: provider.URL}
	tool.SetEventEmitter(emitter.Emit)
	tool.SetEventContext(emitter.Context())
	var started map[string]any
	tool.OnSubagentLog = func(_ int, _ string, line string) {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil && rec["type"] == "subagent_started" {
			started = rec
		}
	}
	result := tool.runTask(0, "child-task", "private goal", "", "", "trusted", "safe", "", "")
	emitter.Close()
	logger.Close()
	var res map[string]any
	if json.Unmarshal([]byte(result), &res) != nil || res["status"] != "success" {
		t.Fatalf("child failed: %s", result)
	}
	if started["budget_seconds"] == nil || started["max_risk"] == nil {
		t.Fatalf("resolved live telemetry not wired: %v", started)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "private") || strings.Contains(string(b), "test-key") {
		t.Fatalf("private content leaked: %s", b)
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var ev events.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		if ev.SessionID != "session-1" || ev.TaskID != "child-task" {
			t.Fatalf("missing session/task: %+v", ev)
		}
		seen[ev.Type] = true
		if ev.Type == "subagent_completed" && (ev.Data["exit_status"] != "exited" || ev.Data["cost_usd"] == nil) {
			t.Fatalf("terminal diagnostics: %+v", ev)
		}
	}
	for _, typ := range []string{"subagent_spawned", "turn_started", "llm_call_started", "llm_call_completed", "run_completed", "subagent_completed"} {
		if !seen[typ] {
			t.Errorf("missing %s", typ)
		}
	}
}

func TestRuntimeExpirationDryRunReportsWork(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.log")
	b, _ := json.Marshal(events.Event{Type: "run_completed", Timestamp: time.Now().Add(-48 * time.Hour)})
	_ = os.WriteFile(path, append(b, '\n'), 0600)
	out := captureStdout(func() { printCleanupDryRun(dir, maintenance.Config{RuntimeLogMaxAgeHours: 24}) })
	if !strings.Contains(out, "runtime records expired: 1") || strings.Contains(out, "storage is clean") {
		t.Fatalf("misleading preview: %s", out)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, append(b, '\n')) {
		t.Fatal("dry run modified log")
	}
}

func TestSubagentActivityTransitionsAndTiming(t *testing.T) {
	a := newSubagentActivity()
	start := time.Now().Add(-time.Minute)
	a.mu.Lock()
	a.last = time.Now().Add(-time.Second)
	a.mu.Unlock()
	before := a.snapshot(start)
	if before["elapsed_seconds"].(float64) < 60 || before["last_event_age_seconds"].(float64) < 1 {
		t.Fatalf("missing timing: %v", before)
	}
	cases := []struct {
		event, id, want string
		pending, active int
	}{
		{"llm_call_started", "", "llm_request", 0, 0},
		{"llm_call_completed", "", "process_running", 0, 0},
		{"llm_call_started", "", "llm_request", 0, 0},
		{"llm_call_failed", "", "process_running", 0, 0},
		{"tool_call_started", "one", "tools_pending", 1, 0},
		{"tool_call_executing", "one", "tool_execution", 0, 1},
		{"tool_call_started", "two", "tool_execution", 1, 1},
		{"tool_call_failed", "two", "tool_execution", 0, 1},
		{"tool_call_completed", "one", "process_running", 0, 0},
	}
	for _, tc := range cases {
		a.observe(events.Event{Type: tc.event, Data: map[string]any{"call_id": tc.id}})
		got := a.snapshot(start)
		if got["activity"] != tc.want || got["pending_calls"] != tc.pending || got["active_calls"] != tc.active {
			t.Fatalf("after %s: %v", tc.event, got)
		}
	}
	if age := a.snapshot(start)["last_event_age_seconds"].(float64); age > 0.5 {
		t.Fatalf("new observations did not update event age: %v", age)
	}
}

func TestSubagentRuntimeRelayRejectsMalformedAndUnknown(t *testing.T) {
	tool := &delegateTasksTool{}
	var got []events.Event
	tool.SetEventEmitter(func(ev events.Event) { got = append(got, ev) })
	tool.SetEventContext(events.Context{RunID: "parent", SessionID: "session", TurnID: "turn", TaskID: "parent-task"})
	a := newSubagentActivity()
	for _, tc := range []struct {
		line     string
		consumed bool
	}{
		{`{broken`, false}, {`{"type":"tool_call","data":"private"}`, false},
		{`{"type":"runtime_event","event":{"type":"unknown","data":{"goal":"private"}}}`, true},
		{`{"type":"subagent_started","pid":"invalid"}`, false},
	} {
		if got := tool.relayRuntimeRecord("child", tc.line, a); got != tc.consumed {
			t.Fatalf("consumed=%v for %s", got, tc.line)
		}
	}
	if len(got) != 0 {
		t.Fatalf("malformed records emitted events: %v", got)
	}
	if !tool.relayRuntimeRecord("child", `{"type":"runtime_event","event":{"type":"llm_call_started","task_id":"","session_id":"forged"}}`, a) {
		t.Fatal("valid legacy event rejected")
	}
	if len(got) != 1 || got[0].TaskID != "child" || got[0].SessionID != "session" || got[0].ParentTurnID != "turn" {
		t.Fatalf("parent correlation not restored: %v", got)
	}
}

func TestSubagentStderrCountsWithoutRetainingContent(t *testing.T) {
	var b boundedStderr
	for _, p := range [][]byte{nil, []byte("private error"), bytes.Repeat([]byte("secret"), 1<<16)} {
		n, err := b.Write(p)
		if err != nil || n != len(p) {
			t.Fatalf("write=%d,%v", n, err)
		}
	}
	if b.n != int64(len("private error")+6*(1<<16)) {
		t.Fatal("wrong diagnostic byte count")
	}
}
