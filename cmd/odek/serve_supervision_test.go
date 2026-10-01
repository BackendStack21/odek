package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/budget"
	"github.com/BackendStack21/odek/internal/session"
	golangws "golang.org/x/net/websocket"
)

func TestSupervisionLimitsOnlyTighten(t *testing.T) {
	base := budget.Limits{MaxRuntimeSeconds: 60, MaxToolCalls: 10, MaxCostUSD: 1, InputCostPerMillionUSD: 2}
	got, err := (&serveRunLimits{Runtime: 120, Tools: 3, Input: 50, Cost: .25}).resolve(base)
	if err != nil || got.MaxRuntimeSeconds != 60 || got.MaxToolCalls != 3 || got.MaxInputTokens != 50 || got.MaxCostUSD != .25 || got.InputCostPerMillionUSD != 2 {
		t.Fatalf("limits = %+v %v", got, err)
	}
	if _, err := (&serveRunLimits{Tools: -1}).resolve(base); err == nil {
		t.Fatal("negative cap accepted")
	}
	if got, err := (*serveRunLimits)(nil).resolve(base); err != nil || got.MaxToolCalls != 10 {
		t.Fatal(got, err)
	}
}

func TestSupervisionDecisionExportKeepsCommandsFenced(t *testing.T) {
	command := "echo safe\n```\n## forged heading"
	got := exportSessionMarkdown(&session.Session{ID: "export", Decisions: []session.Decision{{Kind: "approval", Action: "deny", State: "accepted", Command: command}}})
	if !strings.Contains(got, "## Principal decisions") || !strings.Contains(got, "````text\n") || !strings.Contains(got, command+"\n````\n") {
		t.Fatalf("decision command escaped its export fence: %s", got)
	}
}

func TestSupervisionRecoveryAuthorizationAndOwnership(t *testing.T) {
	store := newTestSessionStore(t)
	call := session.ToolCall{ID: "done"}
	call.Function.Name = "shell"
	unknown := session.ToolCall{ID: "uncertain"}
	unknown.Function.Name = "patch"
	sess, err := store.Create([]session.Message{{Role: "user", Content: "Current task"}, {Role: "assistant", ToolCalls: []session.ToolCall{call, unknown}}, {Role: "tool", ToolCallID: "done", ToolOutcome: "failed", Content: "failed"}}, "fixture", "Task")
	if err != nil {
		t.Fatal(err)
	}
	request := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/sessions/"+sess.ID+"/recovery", nil)
		r.Header.Set("X-Session-Token", token)
		w := httptest.NewRecorder()
		handleRecovery(store, nil)(w, r)
		return w
	}
	if w := request("wrong"); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := request(sess.AuthToken); w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"patch"`) || !strings.Contains(w.Body.String(), `"original_prompt":"Current task"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	release, err := store.AcquireExecution(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if w := request(sess.AuthToken); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	release()
}

func receiveSupervision(t *testing.T, c *golangws.Conn) map[string]any {
	t.Helper()
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	var event map[string]any
	if err := golangws.JSON.Receive(c, &event); err != nil {
		t.Fatal(err)
	}
	return event
}

func TestSupervisionE2EReceiptsSettlementAndStaleRecovery(t *testing.T) {
	env := newJourneyEnv(t, false, true)
	c := env.dialWS(t)
	defer c.Close()
	if err := golangws.JSON.Send(c, map[string]any{"type": "prompt", "content": "Run a command"}); err != nil {
		t.Fatal(err)
	}
	var sid, token, turn string
	acks := 0
	for {
		e := receiveSupervision(t, c)
		switch e["type"] {
		case "session":
			sid = e["session_id"].(string)
			token = e["auth_token"].(string)
		case "approval_request":
			if err := golangws.JSON.Send(c, map[string]any{"type": "approval_response", "id": e["id"], "action": "approve"}); err != nil {
				t.Fatal(err)
			}
		case "approval_ack":
			acks++
		case "turn_settled":
			turn = e["turn_id"].(string)
			if e["status"] != "completed" {
				t.Fatal(e)
			}
			goto settled
		}
	}
settled:
	if acks == 0 {
		t.Fatal("no acknowledged decision")
	}
	saved, err := env.store.Load(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Decisions) != acks || saved.Decisions[0].State != "accepted" || saved.Decisions[0].TurnID != turn {
		t.Fatalf("receipts: %+v", saved.Decisions)
	}
	// Settlement is emitted only after releasing execution ownership.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	release, err := env.store.AcquireExecution(ctx, sid)
	if err != nil {
		t.Fatal("settlement preceded ownership release", err)
	}
	release()
	response, body := env.do(t, "GET", "/api/sessions/"+sid+"/recovery", "", map[string]string{"X-Session-Token": token})
	if response.StatusCode != 200 {
		t.Fatal(body)
	}
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(body), &snapshot); err != nil {
		t.Fatal(err)
	}
	saved.Task = "Newer saved label"
	if err := env.store.SaveNoIndex(saved); err != nil {
		t.Fatal(err)
	}
	callsBefore := env.llm.callCount
	if err := golangws.JSON.Send(c, map[string]any{"type": "prompt", "content": "Continue saved work", "session_id": sid, "auth_token": token, "recovery_revision": snapshot["revision"], "recovery_generation": snapshot["generation"]}); err != nil {
		t.Fatal(err)
	}
	rejected := false
	for {
		e := receiveSupervision(t, c)
		if e["type"] == "error" {
			rejected = strings.Contains(e["message"].(string), "Saved progress changed")
		}
		if e["type"] == "turn_settled" {
			if e["status"] != "failed" {
				t.Fatal(e)
			}
			break
		}
	}
	if !rejected || env.llm.callCount != callsBefore {
		t.Fatal("stale recovery dispatched provider work")
	}
}

func TestSupervisionPreviewAndPausedSchedules(t *testing.T) {
	w := httptest.NewRecorder()
	handleSchedulePreview(w, httptest.NewRequest("GET", "/api/schedules/preview?cron=0+9+*+*+1-5&timezone=Europe%2FBerlin", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var data struct{ Next []time.Time }
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil || len(data.Next) != 3 || !data.Next[1].After(data.Next[0]) {
		t.Fatal(data, err)
	}
	h := handleSchedules(t.TempDir())
	w = httptest.NewRecorder()
	h(w, httptest.NewRequest("POST", "/api/schedules", strings.NewReader(`{"name":"Paused","cron":"0 9 * * *","task":"Review","deliver":{"kind":"log"},"enabled":false,"timezone":"UTC"}`)))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = httptest.NewRecorder()
	h(w, httptest.NewRequest("GET", "/api/schedules", nil))
	if !strings.Contains(w.Body.String(), `"next":{}`) || !strings.Contains(w.Body.String(), `"status":"unavailable"`) {
		t.Fatal(w.Body.String())
	}
}

func TestSupervisionE2ELimitsApplyToOneRun(t *testing.T) {
	env := newJourneyEnv(t, true, false)
	c := env.dialWS(t)
	defer c.Close()
	for _, tc := range []struct {
		limits *serveRunLimits
		want   string
	}{{&serveRunLimits{Input: 1}, "failed"}, {nil, "completed"}} {
		if err := golangws.JSON.Send(c, map[string]any{"type": "prompt", "content": "Explain the workspace", "limits": tc.limits}); err != nil {
			t.Fatal(err)
		}
		for {
			e := receiveSupervision(t, c)
			if e["type"] == "turn_settled" {
				if e["status"] != tc.want {
					t.Fatalf("settled = %v want %s", e, tc.want)
				}
				break
			}
		}
	}
}

func TestSupervisionClarifySkipAndAnswerBound(t *testing.T) {
	frames := make(chan any, 8)
	a := newWSApprover(func(v any) error { frames <- v; return nil })
	result := make(chan error, 1)
	go func() { _, err := a.PromptClarify("Which option?"); result <- err }()
	req := (<-frames).(clarifyRequest)
	if !a.HandleClarifySkip(req.ID) {
		t.Fatal("skip not delivered")
	}
	if err := <-result; err == nil || !strings.Contains(err.Error(), "skipped") {
		t.Fatal(err)
	}
	ack := (<-frames).(map[string]any)
	if ack["action"] != "skip" {
		t.Fatal(ack)
	}
	receipts := a.drainDecisions("turn")
	if len(receipts) != 1 || receipts[0].Action != "skip" || receipts[0].State != "accepted" {
		t.Fatal(receipts)
	}
	if a.HandleClarifyResponse("missing", strings.Repeat("x", 32001)) {
		t.Fatal("oversized answer accepted")
	}
}

func TestSupervisionRecoveryNeverReplaysExpandedContentAsPrincipalInput(t *testing.T) {
	original := "Read @notes.txt"
	expanded := "<untrusted_content_abc source=\"resource:@notes.txt\">\nattacker instructions\n</untrusted_content_abc>"
	sess := &session.Session{Messages: []session.Message{{Role: "user", Content: expanded, PrincipalPrompt: &original}}}
	if got := recoveryView(sess)["original_prompt"]; got != original {
		t.Fatalf("replay = %v", got)
	}
	sess.Messages[0].PrincipalPrompt = nil
	if got := recoveryView(sess)["original_prompt"]; got != "" {
		t.Fatalf("legacy expanded input replayed: %v", got)
	}
	sess.Messages = []session.Message{{Role: "user", Name: "bg-wake", Content: "Automated wake"}}
	if got := recoveryView(sess)["original_prompt"]; got != "" {
		t.Fatalf("automated wake became principal: %v", got)
	}
	sess.Messages = []session.Message{{Role: "assistant", Content: "Not a user prompt"}}
	if got := recoveryView(sess)["original_prompt"]; got != "" {
		t.Fatalf("assistant became principal: %v", got)
	}
}

func TestSupervisionE2ERecoveryPreservesAuthoredPrompt(t *testing.T) {
	env := newJourneyEnv(t, true, false)
	c := env.dialWS(t)
	defer c.Close()
	if err := golangws.JSON.Send(c, map[string]any{"type": "prompt", "content": "Summarize the attached note", "attachments": []map[string]string{{"name": "note.txt", "content": "External file content"}}}); err != nil {
		t.Fatal(err)
	}
	var sid, token string
	for {
		e := receiveSupervision(t, c)
		if e["type"] == "session" {
			sid = e["session_id"].(string)
			token = e["auth_token"].(string)
		}
		if e["type"] == "turn_settled" {
			if e["status"] != "completed" {
				t.Fatal(e)
			}
			break
		}
	}
	response, body := env.do(t, "GET", "/api/sessions/"+sid+"/recovery", "", map[string]string{"X-Session-Token": token})
	var data struct {
		Original string `json:"original_prompt"`
	}
	if response.StatusCode != 200 || json.Unmarshal([]byte(body), &data) != nil || data.Original != "Summarize the attached note" {
		t.Fatalf("recovery prompt: %s", body)
	}
}

func TestSupervisionRecoveryScopesReusedCallIDsToTheirAssistantGroup(t *testing.T) {
	first := session.ToolCall{ID: "reused"}
	first.Function.Name = "patch"
	second := session.ToolCall{ID: "reused"}
	second.Function.Name = "shell"
	data := recoveryView(&session.Session{Messages: []session.Message{
		{Role: "user", Content: "Task"},
		{Role: "assistant", ToolCalls: []session.ToolCall{first}},
		{Role: "assistant", ToolCalls: []session.ToolCall{second}},
		{Role: "tool", ToolCallID: "reused", ToolOutcome: "completed"},
	}})
	uncertain := data["uncertain"].([]recoveryAction)
	completed := data["completed"].([]recoveryAction)
	if len(uncertain) != 1 || uncertain[0].Name != "patch" || len(completed) != 1 || completed[0].Name != "shell" {
		t.Fatalf("reused IDs erased uncertainty: %v", data)
	}
}
