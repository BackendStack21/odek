package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/loop"
	"github.com/BackendStack21/odek/internal/resource"
	"github.com/BackendStack21/odek/internal/session"
)

// answerFixtureLLM replays scripted replies by call kind: main think calls
// (they carry tools) take the next answer, verifier side calls the next
// verdict, and any other side call (memory, titles) gets a neutral reply.
// Replies stream as SSE when the request asked for it.
func answerFixtureLLM(t *testing.T, answers, verdicts []string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		content := "ok"
		queue := (*[]string)(nil)
		switch {
		case strings.Contains(string(body), "You are a strict verifier"):
			queue = &verdicts
		case strings.Contains(string(body), `"tools":`):
			queue = &answers
		}
		if queue != nil {
			if len(*queue) == 0 {
				mu.Unlock()
				t.Errorf("unexpected provider call: %.200s", body)
				http.Error(w, "script exhausted", http.StatusBadRequest)
				return
			}
			content, *queue = (*queue)[0], (*queue)[1:]
		}
		mu.Unlock()
		quoted, _ := json.Marshal(content)
		if strings.Contains(string(body), `"stream":true`) {
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

type frameLog struct {
	mu     sync.Mutex
	frames []map[string]any
}

func (l *frameLog) send(f map[string]any) {
	l.mu.Lock()
	l.frames = append(l.frames, f)
	l.mu.Unlock()
}

// sequence renders the frames a client uses to fold drafts, in order.
func (l *frameLog) sequence() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, f := range l.frames {
		switch f["type"] {
		case "token", "token_delta":
			out = append(out, fmt.Sprintf("%s:%v", f["type"], f["content"]))
		case "answer_superseded":
			out = append(out, fmt.Sprintf("answer_superseded:%v:%v", f["reason"], f["cycle"]))
		case "runtime_event":
			ev := f["event"].(events.Event)
			if strings.HasPrefix(ev.Type, "verification_") {
				s := ev.Type
				if v, ok := ev.Data["verdict"]; ok {
					s += ":" + v.(string)
				}
				out = append(out, s)
			}
		case "done":
			out = append(out, fmt.Sprintf("done:%v", f["verified"]))
		}
	}
	return out
}

func runAnswerTurn(t *testing.T, stream bool, verify *loop.VerifyConfig, answers, verdicts []string) (*frameLog, *session.Session) {
	t.Helper()
	llm := answerFixtureLLM(t, answers, verdicts)
	log := &frameLog{}
	sendAny := func(v any) error {
		if m, ok := v.(map[string]any); ok {
			log.send(m)
		}
		return nil
	}
	var deltas *wsDeltaCounters
	if stream {
		deltas = &wsDeltaCounters{}
	}
	var built *odek.Agent
	a, err := odek.New(odek.Config{
		Provider: "deepseek", BaseURL: llm.URL, APIKey: "test-key", Model: "test-model",
		SystemMessage: "Help", NoProjectFile: true, MemoryDir: t.TempDir(), MaxIterations: 6,
		Verify: verify, Stream: stream, DeltaHandler: serveDeltaHandler(sendAny, deltas),
		// serve always installs an event handler, which gives runs an id.
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
		wsClientMsg{Type: "prompt", Content: "describe the branch"}, new(int), new(int), nil, deltas, nil, nil)
	saved, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	return log, saved
}

func TestServeAnswerSuperseded_StreamedVerifyRetry(t *testing.T) {
	log, saved := runAnswerTurn(t, true, &loop.VerifyConfig{Enabled: true, Mode: loop.VerifyModeHint, MaxCycles: 2},
		[]string{"draft", "fixed"}, []string{`{"verdict":"fail","reasons":["x"]}`, `{"verdict":"pass"}`})
	want := "[token_delta:draft verification_started verification_completed:fail answer_superseded:verify_retry:1 " +
		"token_delta:fixed verification_started verification_completed:pass done:pass]"
	if got := fmt.Sprint(log.sequence()); got != want {
		t.Fatalf("frames = %s\nwant     %s", got, want)
	}
	if !turnTaggedFrames["answer_superseded"] {
		t.Fatal("answer_superseded must carry turn_id")
	}

	// History replay sees the same boundary the live stream did.
	var drafts, finals int
	for _, m := range saved.Messages {
		if m.Role != "assistant" {
			continue
		}
		if m.Superseded {
			drafts++
			if m.Content != "draft" || m.SupersededReason != loop.SupersededVerifyRetry {
				t.Errorf("draft record = %+v", m)
			}
		} else {
			finals++
		}
	}
	if drafts != 1 || finals != 1 {
		t.Fatalf("drafts=%d finals=%d, want 1/1", drafts, finals)
	}
}

// A buffered draft never reached the client: no answer_superseded, and the
// post-run bulk send carries only the replacement.
func TestServeAnswerSuperseded_BufferedDraftNotSent(t *testing.T) {
	log, _ := runAnswerTurn(t, false, &loop.VerifyConfig{Enabled: true, Mode: loop.VerifyModeHint, MaxCycles: 2},
		[]string{"draft", "fixed"}, []string{`{"verdict":"fail"}`, `{"verdict":"pass"}`})
	want := "[verification_started verification_completed:fail verification_started verification_completed:pass token:fixed done:pass]"
	if got := fmt.Sprint(log.sequence()); got != want {
		t.Fatalf("frames = %s\nwant     %s", got, want)
	}
}

func TestServeVerified_StrictFail(t *testing.T) {
	log, _ := runAnswerTurn(t, true, &loop.VerifyConfig{Enabled: true, Mode: loop.VerifyModeStrict, MaxCycles: 3},
		[]string{"answer"}, []string{`{"verdict":"fail","reasons":["unsupported claim"]}`})
	want := "[token_delta:answer verification_started verification_completed:fail done:fail]"
	if got := fmt.Sprint(log.sequence()); got != want {
		t.Fatalf("frames = %s\nwant     %s", got, want)
	}
	// Verifier prose never reaches the wire.
	raw, _ := json.Marshal(log.frames)
	if strings.Contains(string(raw), "unsupported claim") {
		t.Fatal("verifier reasons leaked into a frame")
	}
	for _, f := range log.frames {
		if f["type"] == "runtime_event" && f["event"].(events.Event).RunID == "" {
			t.Fatalf("verification runtime_event without run_id: %v", f)
		}
	}
}

// Verification off and no re-ask: frame shapes are unchanged.
func TestServeVerified_OmittedWhenDisabled(t *testing.T) {
	log, _ := runAnswerTurn(t, true, nil, []string{"answer"}, nil)
	if got := fmt.Sprint(log.sequence()); got != "[token_delta:answer done:<nil>]" {
		t.Fatalf("frames = %s", got)
	}
	for _, f := range log.frames {
		if f["type"] == "done" {
			if _, ok := f["verified"]; ok {
				t.Fatalf("done carries verified with verification off: %v", f)
			}
		}
	}
}

func TestAnswerEventFrame_Shapes(t *testing.T) {
	frame, ok := answerEventFrame(loop.AnswerEvent{Type: "answer_superseded", Reason: loop.SupersededCompletionNudge, Cycle: 1, Streamed: true}, "run1")
	if !ok || fmt.Sprint(frame) != "map[cycle:1 reason:completion_nudge type:answer_superseded]" {
		t.Fatalf("frame = %v ok=%v", frame, ok)
	}
	if _, ok := answerEventFrame(loop.AnswerEvent{Type: "answer_superseded", Reason: loop.SupersededCompletionNudge, Cycle: 1}, "run1"); ok {
		t.Fatal("a draft that never streamed must not produce answer_superseded")
	}
	frame, ok = answerEventFrame(loop.AnswerEvent{Type: events.TypeVerificationCompleted, Verdict: "skipped", SkippedReason: "budget"}, "run1")
	ev := frame["event"].(events.Event)
	if !ok || ev.RunID != "run1" || ev.Data["verdict"] != "skipped" || ev.Data["skipped_reason"] != "budget" || ev.Data["cycles_used"] != 0 {
		t.Fatalf("frame = %+v ok=%v", frame, ok)
	}
}

func TestAnswerEventFrame_UnknownTypeDropped(t *testing.T) {
	if frame, ok := answerEventFrame(loop.AnswerEvent{Type: "future_event"}, "run1"); ok || frame != nil {
		t.Fatalf("unknown answer event produced a frame: %v", frame)
	}
}

// REST runs accumulate the answer from token frames; a superseded draft
// must not stay glued in front of the revised answer.
func TestServeRunRecord_AnswerSupersededResetsResult(t *testing.T) {
	run := newTerminalTestRun()
	run.record(map[string]any{"type": "token_delta", "content": "Done."})
	run.record(map[string]any{"type": "answer_superseded", "reason": "verify_retry", "cycle": 1})
	run.record(map[string]any{"type": "token_delta", "content": "Revised answer."})
	if run.Result != "Revised answer." {
		t.Fatalf("result = %q, want only the revised answer", run.Result)
	}
	run.finish("completed", "")
	run.record(map[string]any{"type": "answer_superseded"})
	if run.Result != "Revised answer." {
		t.Fatalf("terminal run result changed: %q", run.Result)
	}
}

func TestTurnAnswer_SkipsSupersededDrafts(t *testing.T) {
	msgs := []session.Message{
		{Role: "assistant", Content: "Let me check."},
		{Role: "tool", Content: "result"},
		{Role: "assistant", Content: "draft", Superseded: true},
		{Role: "assistant", Content: "final"},
		{Role: "system", Content: "trailing"},
	}
	if got, ok := turnAnswer(msgs); !ok || got != "final" {
		t.Fatalf("turnAnswer = %q, %v", got, ok)
	}
	if _, ok := turnAnswer([]session.Message{{Role: "assistant", Content: "draft", Superseded: true}}); ok {
		t.Fatal("a turn holding only a draft has no answer")
	}
}

func TestExportSessionMarkdown_LabelsSupersededDrafts(t *testing.T) {
	out := exportSessionMarkdown(&session.Session{ID: "export-drafts", Messages: []session.Message{
		{Role: "user", Content: "q"},
		{Role: "assistant", Content: "nudged draft", Superseded: true, SupersededReason: loop.SupersededCompletionNudge},
		{Role: "assistant", Content: "verify draft", Superseded: true, SupersededReason: loop.SupersededVerifyRetry},
		{Role: "assistant", Content: "odd draft", Superseded: true, SupersededReason: "## injected heading"},
		{Role: "assistant", Content: "final"},
	}})
	for _, want := range []string{
		"## assistant (draft, revised after completion check)\n",
		"## assistant (draft, revised after verification)\n",
		"## assistant (draft, revised)\n",
		"## assistant\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("export missing %q", want)
		}
	}
	if strings.Contains(out, "injected heading") {
		t.Fatal("superseded_reason from the session file reached the export heading")
	}
}

func TestAnswerSuperseded_IsTurnTagged(t *testing.T) {
	var tag wsTurnAnnotator
	var got map[string]any
	send := tag.wrap(func(m map[string]any) { got = m })
	tag.begin("t_draft")
	send(map[string]any{"type": "answer_superseded", "reason": "completion_nudge", "cycle": 1})
	if got["turn_id"] != "t_draft" {
		t.Fatalf("answer_superseded not turn-tagged: %v", got)
	}
}
