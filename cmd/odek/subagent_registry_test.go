package main

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

// ── Registry: record / update / snapshot / bounds ────────────────────

func resetSubagentRegistry() {
	subagentReg.mu.Lock()
	subagentReg.entries = nil
	subagentReg.byID = map[string]*subagentEntry{}
	subagentReg.mu.Unlock()
}

func TestSubagentRegistry_RecordUpdateSnapshot(t *testing.T) {
	resetSubagentRegistry()

	subagentRegistryRecord(&subagentEntry{TaskID: "task-1", RunKey: "conn-1", Goal: "write tests", Phase: "started", Status: "running"})
	subagentRegistryUpdate("task-1", "", func(e *subagentEntry) {
		e.Phase = "active"
		e.LastTool = "read_file"
		e.Step = 2
	})

	snap := subagentRegistrySnapshot("")
	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d, want 1", len(snap))
	}
	e := snap[0]
	if e.Phase != "active" || e.LastTool != "read_file" || e.Step != 2 {
		t.Errorf("update not applied: %+v", e)
	}
	if e.Goal != "write tests" {
		t.Errorf("goal = %q, want write tests", e.Goal)
	}

	// Filter by run key.
	if got := subagentRegistrySnapshot("no-such-key"); len(got) != 0 {
		t.Errorf("filter by unknown key returned %d entries, want 0", len(got))
	}
	if got := subagentRegistrySnapshot("conn-1"); len(got) != 1 {
		t.Errorf("filter by key returned %d entries, want 1", len(got))
	}
}

func TestSubagentRegistry_BoundedOldestEvicted(t *testing.T) {
	resetSubagentRegistry()
	for i := 0; i < maxSubagentRegistryEntries+10; i++ {
		subagentRegistryRecord(&subagentEntry{TaskID: "task-" + strconv.Itoa(i), RunKey: "k"})
	}
	snap := subagentRegistrySnapshot("")
	if len(snap) != maxSubagentRegistryEntries {
		t.Errorf("snapshot len = %d, want bounded %d", len(snap), maxSubagentRegistryEntries)
	}
	// Oldest evicted: the first-inserted id must be gone, the last present.
	if got := subagentRegistrySnapshot(""); got[0].TaskID != "task-10" {
		t.Errorf("oldest surviving entry = %q, want task-10", got[0].TaskID)
	}
}

func TestSubagentRegistry_ConcurrentAccess(t *testing.T) {
	resetSubagentRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "task-c"
			subagentRegistryRecord(&subagentEntry{TaskID: id, RunKey: "k"})
			for j := 0; j < 50; j++ {
				subagentRegistryUpdate(id, "", func(e *subagentEntry) { e.Step++ })
				_ = subagentRegistrySnapshot("")
			}
		}(i)
	}
	wg.Wait()
}

// ── Telemetry relay: log + state + registry flow ─────────────────────

type captureSink struct {
	mu     sync.Mutex
	imgs   []map[string]any
	filter func(m map[string]any) bool
}

func (c *captureSink) send(v any) error {
	m := v.(map[string]any)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.filter == nil || c.filter(m) {
		c.imgs = append(c.imgs, m)
	}
	return nil
}

func (c *captureSink) ofState() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []map[string]any
	for _, m := range c.imgs {
		if m["type"] == "subagent_state" {
			out = append(out, m)
		}
	}
	return out
}

func (c *captureSink) ofLog() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []map[string]any
	for _, m := range c.imgs {
		if m["type"] == "subagent_log" {
			out = append(out, m)
		}
	}
	return out
}

func TestSubagentTelemetryRelay_LifecycleFlow(t *testing.T) {
	resetSubagentRegistry()
	sink := &captureSink{}
	relay := newSubagentTelemetryRelay(sink.send, "run-1")

	lines := []string{
		`{"type":"subagent_started","task_id":"t1","pid":9,"goal":"deploy the thing","timeout_s":1800,"max_iter":15}`,
		`{"type":"tool_call","name":"shell","data":"ls","task_id":"t1"}`,
		`{"type":"subagent_progress","task_id":"t1","step":1,"tool":"shell"}`,
		`{"type":"subagent_finished","task_id":"t1","status":"success","iterations":3,"duration_s":9.5,"tokens_used":800}`,
	}
	for _, l := range lines {
		relay(0, "t1", l)
	}

	states := sink.ofState()
	if len(states) != 3 {
		t.Fatalf("subagent_state messages = %d, want 3 (started, active, finished)", len(states))
	}
	if states[0]["phase"] != "started" {
		t.Errorf("state[0] phase = %v, want started", states[0]["phase"])
	}
	if states[0]["task_idx"] != 0 {
		t.Errorf("state[0] task_idx = %v, want 0 (card correlation)", states[0]["task_idx"])
	}
	if states[1]["phase"] != "active" || states[1]["tool"] != "shell" {
		t.Errorf("state[1] = %v, want active/shell", states[1])
	}
	last := states[2]
	if last["phase"] != "finished" || last["status"] != "success" {
		t.Errorf("state[2] = %v, want finished/success", last)
	}

	// Registry snapshot carries the full lifecycle.
	snap := subagentRegistrySnapshot("run-1")
	if len(snap) != 1 {
		t.Fatalf("registry entries = %d, want 1", len(snap))
	}
	e := snap[0]
	if e.Phase != "finished" || e.Status != "success" || e.Iterations != 3 || e.TokensUsed != 800 {
		t.Errorf("final entry = %+v", e)
	}
	if !strings.Contains(e.Goal, "deploy the thing") {
		t.Errorf("goal = %q, want the started record's goal", e.Goal)
	}

	// tool_call lines relay as logs only — no state message for them.
	if n := len(sink.ofLog()); n < 1 {
		t.Errorf("tool_call should still produce a subagent_log, got %d logs", n)
	}
}

func TestSubagentTelemetryRelay_LogOnlyForToolEvents(t *testing.T) {
	resetSubagentRegistry()
	sink := &captureSink{}
	relay := newSubagentTelemetryRelay(sink.send, "run-2")

	relay(0, "t2", `{"type":"tool_call","name":"shell","data":"ls","task_id":"t2"}`)
	if got := len(sink.ofState()); got != 0 {
		t.Errorf("tool_call produced %d state messages, want 0 (log-only)", got)
	}
	if len(sink.ofLog()) != 1 {
		t.Error("tool_call should produce exactly one subagent_log")
	}
	if snap := subagentRegistrySnapshot("run-2"); len(snap) != 0 {
		t.Errorf("tool_call must not create registry entries, got %d", len(snap))
	}
}

// ── REST snapshot handler ────────────────────────────────────────────

func subagentTestSession(t *testing.T, store *session.Store, task string) *session.Session {
	t.Helper()
	sess, err := store.Create(nil, "m", task)
	if err != nil {
		t.Fatal(err)
	}
	if sess.AuthToken == "" {
		sess.AuthToken = session.GenerateAuthToken()
		if err := store.Save(sess); err != nil {
			t.Fatal(err)
		}
	}
	return sess
}

func TestHandleSubagentRegistry_ScopedToSession(t *testing.T) {
	resetSubagentRegistry()
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sessA := subagentTestSession(t, store, "A")
	sessB := subagentTestSession(t, store, "B")

	conn := &wsConnInfo{ID: "conn-scope-a"}
	conn.setLive(sessA.ID, true)
	wsConnRegister(conn)
	defer wsConnUnregister(conn.ID)

	subagentRegistryRecord(&subagentEntry{TaskID: "task-a", RunKey: "conn-scope-a", Phase: "active", Status: "running", Goal: "audit docs"})
	subagentRegistryRecord(&subagentEntry{TaskID: "task-other", RunKey: "someone-else", Phase: "active", Status: "running"})

	get := func(url, token string) (int, []subagentEntry) {
		req := httptest.NewRequest("GET", url, nil)
		if token != "" {
			req.Header.Set("X-Session-Token", token)
		}
		rec := httptest.NewRecorder()
		handleSubagentRegistry(store).ServeHTTP(rec, req)
		var out struct {
			Entries []subagentEntry `json:"entries"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out.Entries
	}

	code, entries := get("/api/subagents?session_id="+sessA.ID, sessA.AuthToken)
	if code != 200 || len(entries) != 1 || entries[0].TaskID != "task-a" {
		t.Fatalf("owner view = %d %+v, want only task-a", code, entries)
	}
	if code, entries = get("/api/subagents?session_id="+sessA.ID+"&key=conn-scope-a", sessA.AuthToken); code != 200 || len(entries) != 1 {
		t.Fatalf("key filter = %d %+v", code, entries)
	}
	if code, entries = get("/api/subagents?session_id="+sessB.ID, sessB.AuthToken); code != 200 || len(entries) != 0 {
		t.Fatalf("foreign session must see nothing, got %d %+v", code, entries)
	}
	if code, _ = get("/api/subagents?session_id="+sessA.ID, sessB.AuthToken); code != 401 {
		t.Fatalf("wrong token = %d, want 401", code)
	}
	if code, _ = get("/api/subagents", ""); code != 400 {
		t.Fatalf("missing session_id = %d, want 400", code)
	}
}

func TestSubagentTaskOwnedBySession(t *testing.T) {
	resetSubagentRegistry()
	conn := &wsConnInfo{ID: "conn-own-1"}
	conn.setLive("sess-own", true)
	wsConnRegister(conn)
	defer wsConnUnregister(conn.ID)
	run := &serveRun{ID: "run-own-1", SessionID: "sess-run"}
	registerRun(run)
	defer func() {
		serveRuns.mu.Lock()
		delete(serveRuns.runs, run.ID)
		serveRuns.mu.Unlock()
	}()
	subagentRegistryRecord(&subagentEntry{TaskID: "t-ws", RunKey: "conn-own-1"})
	subagentRegistryRecord(&subagentEntry{TaskID: "t-run", RunKey: "run-own-1"})

	for _, tc := range []struct {
		task, sess string
		want       bool
	}{
		{"t-ws", "sess-own", true},
		{"t-ws", "sess-run", false},
		{"t-run", "sess-run", true},
		{"t-run", "sess-own", false},
		{"missing", "sess-own", false},
		{"t-ws", "", false},
	} {
		if got := subagentTaskOwnedBySession(tc.task, tc.sess); got != tc.want {
			t.Errorf("owned(%s,%s) = %v, want %v", tc.task, tc.sess, got, tc.want)
		}
	}
}
