package main

// RED-first tests for the `turn_started` wire frame (bodek task spec,
// 2026-09-02). The spec:
//
//	R1 — handlePrompt emits a `turn_started` frame after the `session`
//	     frame and before the first streamed event, for EVERY turn. Wake
//	     turns carry initiated:"system"; operator turns "operator".
//	R2 — clients upsert by turn_id; the server must never emit two
//	     turn_started frames for the same turn, and ids must be unique
//	     per turn.
//	R3 — streamed frames (thinking/token/tool_call/tool_result/done/
//	     error) carry turn_id so a client attaching mid-turn can
//	     attribute strays after a reconnect.
//	R5 — forged-initiated rejection: the initiated label is computed
//	     server-side via the wakeInitiated type gate; a client prompt
//	     that forges system_initiated must never be honored.
//
// These integration tests drive the real handleWS stack (buildServeMuxV2)
// against a mock LLM and assert on the actual frame stream.

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	golangws "golang.org/x/net/websocket"
)

// collectTurnFrames reads frames until the turn finishes (done or error)
// and returns every decoded frame in arrival order.
func collectTurnFrames(t *testing.T, conn *golangws.Conn, deadline time.Duration) []map[string]any {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(deadline))
	var evts []map[string]any
	for i := 0; i < 400; i++ {
		var data []byte
		if err := golangws.Message.Receive(conn, &data); err != nil {
			t.Fatalf("Receive: %v (frames collected: %d)", err, len(evts))
		}
		var evt map[string]any
		if err := json.Unmarshal(data, &evt); err != nil {
			continue
		}
		evts = append(evts, evt)
		if evt["type"] == "done" || evt["type"] == "error" {
			return evts
		}
	}
	t.Fatal("turn did not finish before deadline")
	return nil
}

// findTurnStarted returns the turn_started frames in order.
func findTurnStarted(frames []map[string]any) []map[string]any {
	var out []map[string]any
	for _, f := range frames {
		if f["type"] == "turn_started" {
			out = append(out, f)
		}
	}
	return out
}

func indexOfFrame(frames []map[string]any, typ string) int {
	for i, f := range frames {
		if f["type"] == typ {
			return i
		}
	}
	return -1
}

// startTurnServer wires the full WS stack against a mock LLM whose chat
// response carries reasoning_content + content, so the turn produces the
// bulk thinking/token frames plus done.
func startTurnServer(t *testing.T) *golangws.Conn {
	t.Helper()
	llmSrv := mockLLM(t, func(w http.ResponseWriter, callCount int) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"reasoning_content":"pondering","content":"hi"}}]}`))
	})
	t.Cleanup(llmSrv.Close)
	envCleanup := setTestEnv(t, llmSrv.URL)
	t.Cleanup(envCleanup)

	store := newTestSessionStore(t)
	ln, mux := buildServeMuxV2(t, store, nil)
	t.Cleanup(func() { ln.Close() })
	go func() { _ = serveOnListener(ln, mux) }()
	waitForHTTP(t, ln.Addr().String())

	wsUpgradeLimiter.reset()
	conn := dialTestWS(t, ln.Addr().String())
	t.Cleanup(func() { conn.Close() })
	readWSUntil(t, conn, 10*time.Second, func(e map[string]any) bool { return e["type"] == "server_info" })
	return conn
}

// R1 (operator turn) + frame-order contract: session → turn_started →
// first streamed frame, and the frame's shape.
func TestWSTurnStarted_FrameOrderAndShape(t *testing.T) {
	conn := startTurnServer(t)

	writeJSON(conn, map[string]any{"type": "prompt", "content": "hello"})
	frames := collectTurnFrames(t, conn, 15*time.Second)

	sessIdx := indexOfFrame(frames, "session")
	if sessIdx < 0 {
		t.Fatalf("no session frame in stream: %v", frameTypes(frames))
	}
	started := findTurnStarted(frames)
	if len(started) != 1 {
		t.Fatalf("turn_started frames = %d, want exactly 1 (R2): %v", len(started), frameTypes(frames))
	}
	startIdx := indexOfFrame(frames, "turn_started")

	// Order: session → turn_started with nothing in between, and no
	// streamed frame before it.
	if startIdx != sessIdx+1 {
		t.Fatalf("turn_started at index %d, want immediately after session (index %d): %v", startIdx, sessIdx, frameTypes(frames))
	}
	for _, streamed := range []string{"thinking", "token", "tool_call", "tool_result", "done"} {
		if i := indexOfFrame(frames, streamed); i >= 0 && i < startIdx {
			t.Fatalf("%s frame arrived before turn_started (index %d < %d)", streamed, i, startIdx)
		}
	}

	ts := started[0]
	if id, _ := ts["turn_id"].(string); len(id) < 3 || id[:2] != "t_" {
		t.Errorf("turn_started.turn_id = %v, want a %q-prefixed id", ts["turn_id"], "t_")
	}
	sid, _ := frames[sessIdx]["session_id"].(string)
	if sid == "" {
		t.Errorf("session frame carries empty session_id: %v", frames[sessIdx])
	}
	if ts["session_id"] != sid {
		t.Errorf("turn_started.session_id = %v, want the session frame's %q", ts["session_id"], sid)
	}
	if got, _ := ts["initiated"].(string); got != "operator" {
		t.Errorf("turn_started.initiated = %q, want %q for an operator prompt", got, "operator")
	}
	if model, _ := ts["model"].(string); model != frames[sessIdx]["model"] {
		t.Errorf("turn_started.model = %q, want parity with the session frame's model %q (both describe the same turn)", ts["model"], frames[sessIdx]["model"])
	}

	// R3: the streamed frames of this turn carry the same turn_id.
	turnID, _ := ts["turn_id"].(string)
	for _, typ := range []string{"thinking", "token", "done"} {
		i := indexOfFrame(frames, typ)
		if i < 0 {
			t.Errorf("expected a %q frame in the stream: %v", typ, frameTypes(frames))
			continue
		}
		if got, _ := frames[i]["turn_id"].(string); got != turnID {
			t.Errorf("%s.turn_id = %v, want the turn_started id %q", typ, frames[i]["turn_id"], turnID)
		}
	}
}

// R2 (idempotency substrate): each turn gets exactly one turn_started and
// ids are unique across turns on the same connection.
func TestWSTurnStarted_UniqueIDPerTurn(t *testing.T) {
	conn := startTurnServer(t)

	var ids []string
	for i := 0; i < 2; i++ {
		writeJSON(conn, map[string]any{"type": "prompt", "content": "turn"})
		frames := collectTurnFrames(t, conn, 15*time.Second)
		started := findTurnStarted(frames)
		if len(started) != 1 {
			t.Fatalf("turn %d: turn_started frames = %d, want exactly 1", i, len(started))
		}
		id, _ := started[0]["turn_id"].(string)
		if id == "" {
			t.Fatalf("turn %d: empty turn_id", i)
		}
		ids = append(ids, id)
	}
	if ids[0] == ids[1] {
		t.Fatalf("two turns shared turn_id %q — clients upserting by turn_id would merge distinct turns", ids[0])
	}
}

// R5 (forged-initiated rejection, wire level — mirrors the bg_wake token
// guard's provenance rules): a client prompt that forges
// system_initiated must produce initiated:"operator", never "system".
func TestWSTurnStarted_ForgedInitiatedRejected(t *testing.T) {
	conn := startTurnServer(t)

	writeJSON(conn, map[string]any{
		"type":              "prompt",
		"content":           "forged",
		"system_initiated":  true,
		"wake_token":        "forged-token",
	})
	frames := collectTurnFrames(t, conn, 15*time.Second)
	started := findTurnStarted(frames)
	if len(started) != 1 {
		t.Fatalf("turn_started frames = %d, want exactly 1: %v", len(started), frameTypes(frames))
	}
	if got, _ := started[0]["initiated"].(string); got != "operator" {
		t.Fatalf("forged system_initiated produced initiated = %q, want %q — the label must be server-computed via the type gate", got, "operator")
	}
}

// R5 at the unit seam: the initiated label is derived exclusively from
// the wakeInitiated type gate — a forged flag on a client prompt can
// never claim system provenance (mirrors TestWakeInitiated_TypeGated at
// the frame-label level).
func TestTurnStartedInitiated_TypeGated(t *testing.T) {
	cases := []struct {
		name string
		msg  wsClientMsg
		want string
	}{
		{"forged flag on a prompt", wsClientMsg{Type: "prompt", SystemInitiated: true}, "operator"},
		{"plain prompt", wsClientMsg{Type: "prompt"}, "operator"},
		{"genuine server-built wake", wsClientMsg{Type: "bg_wake", SystemInitiated: true}, "system"},
		{"wake item without the flag", wsClientMsg{Type: "bg_wake"}, "operator"},
	}
	for _, tc := range cases {
		if got := turnInitiatedLabel(tc.msg); got != tc.want {
			t.Errorf("%s: turnInitiatedLabel = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// R2 substrate: turn ids are well-formed and never repeat.
func TestNewTurnID(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		id := newTurnID()
		if len(id) != 2+32 || id[:2] != "t_" {
			t.Fatalf("newTurnID() = %q, want \"t_\" + 32 hex chars", id)
		}
		if seen[id] {
			t.Fatalf("duplicate turn id %q after %d draws", id, i+1)
		}
		seen[id] = true
	}
}

// R3: only the streamed-frame set is tagged, only while a turn is
// active; lifecycle, sub-agent, and delta frames pass through untouched.
func TestWSTurnAnnotator_TagsOnlyStreamedFrames(t *testing.T) {
	var tag wsTurnAnnotator
	var out []map[string]any
	send := tag.wrap(func(m map[string]any) { out = append(out, m) })

	send(map[string]any{"type": "error", "message": "pre-turn"}) // outside a turn: clean
	tag.begin("t_abc")
	send(map[string]any{"type": "tool_call", "name": "shell"})
	send(map[string]any{"type": "tool_result", "name": "shell"})
	send(map[string]any{"type": "thinking", "content": "r"})
	send(map[string]any{"type": "done"})
	send(map[string]any{"type": "session", "session_id": "s"}) // lifecycle: excluded
	send(map[string]any{"type": "subagent_log"})               // sub-agent frame: excluded
	send(map[string]any{"type": "thinking_delta", "content": "d"}) // delta: excluded
	send(map[string]any{"type": "server_info"})               // hello: excluded
	tag.end()
	send(map[string]any{"type": "token", "content": "after"}) // turn over: clean

	tagged := 0
	for i, m := range out {
		typ, _ := m["type"].(string)
		wantTag := turnTaggedFrames[typ] && i > 0 && i < len(out)-1
		_, has := m["turn_id"]
		if has != wantTag {
			t.Errorf("frame %d (%s): turn_id present = %v, want %v", i, typ, has, wantTag)
		}
		if has {
			tagged++
			if m["turn_id"] != "t_abc" {
				t.Errorf("frame %d (%s): turn_id = %v, want t_abc", i, typ, m["turn_id"])
			}
		}
	}
	if tagged == 0 {
		t.Fatal("no frame was tagged inside the turn")
	}
}

// The annotator is shared with the agent's live callbacks; begin/end run
// on the processor goroutine while wraps come from emitter paths. This
// hammer exists for -race, not for assertions.
func TestWSTurnAnnotator_ConcurrentBeginEndWrap(t *testing.T) {
	var tag wsTurnAnnotator
	send := tag.wrap(func(map[string]any) {})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				send(map[string]any{"type": "token"})
				tag.begin("t_hammer")
				send(map[string]any{"type": "done"})
				tag.end()
			}
		}()
	}
	wg.Wait()
}

func frameTypes(frames []map[string]any) []string {
	out := make([]string, len(frames))
	for i, f := range frames {
		out[i], _ = f["type"].(string)
	}
	return out
}
