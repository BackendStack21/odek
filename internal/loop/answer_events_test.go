package loop

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/BackendStack21/odek/internal/llmclient"
	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// scriptedReply is one provider response: a buffered JSON body, or an SSE
// stream when the request asked for one.
type scriptedReply struct {
	json   string
	stream []string // SSE data payloads; "[DONE]" is appended
	status int
}

func answerScriptServer(t *testing.T, replies ...scriptedReply) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	next := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		if next >= len(replies) {
			mu.Unlock()
			t.Errorf("unexpected provider call %d: %s", next+1, body)
			http.Error(w, "script exhausted", http.StatusBadRequest)
			return
		}
		rep := replies[next]
		next++
		mu.Unlock()
		if rep.status != 0 {
			http.Error(w, `{"error":{"message":"scripted failure"}}`, rep.status)
			return
		}
		if rep.stream != nil && strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, d := range append(rep.stream, "[DONE]") {
				fmt.Fprintf(w, "data: %s\n\n", d)
			}
			return
		}
		fmt.Fprint(w, rep.json)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func textReply(content string) scriptedReply {
	return scriptedReply{json: fmt.Sprintf(`{"choices":[{"message":{"content":%q},"finish_reason":"stop"}]}`, content)}
}

func verdictReply(verdict string) scriptedReply {
	return textReply(fmt.Sprintf(`{"verdict":%q,"reasons":["checked"],"missing":[]}`, verdict))
}

// writeReply asks for a write_file call with no read-back, which leaves an
// uncaught mutation and triggers the completion nudge on the next answer.
var writeReply = scriptedReply{json: `{"choices":[{"message":{"tool_calls":[{"id":"w","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"a.go\"}"}}]},"finish_reason":"tool_calls"}]}`}

func answerEngine(t *testing.T, srv *httptest.Server) (*Engine, *[]AnswerEvent) {
	t.Helper()
	w := &contractTool{name: "write_file", run: func(string) (string, error) { return `{"success":true}`, nil }}
	e := New(testChatClient(t, srv.URL), tool.NewRegistry([]tool.Tool{w}), 8, "sys", nil, 0)
	var got []AnswerEvent
	e.SetAnswerEventHandler(func(ev AnswerEvent) { got = append(got, ev) })
	return e, &got
}

func eventTypes(evs []AnswerEvent) []string {
	out := make([]string, 0, len(evs))
	for _, ev := range evs {
		s := ev.Type
		switch {
		case ev.Reason != "":
			s += ":" + ev.Reason
		case ev.Verdict != "":
			s += ":" + ev.Verdict
		}
		out = append(out, s)
	}
	return out
}

func assistantReplies(msgs []session.Message) []session.Message {
	var out []session.Message
	for _, m := range msgs {
		if m.Role == "assistant" && len(m.ToolCalls) == 0 {
			out = append(out, m)
		}
	}
	return out
}

func TestAnswerSuperseded_CompletionNudge(t *testing.T) {
	srv := answerScriptServer(t, writeReply, textReply("draft"), textReply("final"))
	e, got := answerEngine(t, srv)
	answer, msgs, err := e.RunWithMessages(context.Background(), []session.Message{{Role: "user", Content: "edit a.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if answer != "final" {
		t.Fatalf("answer = %q", answer)
	}
	if fmt.Sprint(eventTypes(*got)) != "[answer_superseded:completion_nudge]" {
		t.Fatalf("events = %v", eventTypes(*got))
	}
	if ev := (*got)[0]; ev.Cycle != 1 || ev.Streamed {
		t.Fatalf("event = %+v, want cycle 1, not streamed", ev)
	}
	replies := assistantReplies(msgs)
	if len(replies) != 2 {
		t.Fatalf("assistant replies = %d, want 2", len(replies))
	}
	if !replies[0].Superseded || replies[0].SupersededReason != SupersededCompletionNudge || replies[0].Content != "draft" {
		t.Fatalf("draft record = %+v", replies[0])
	}
	if replies[1].Superseded {
		t.Fatalf("final record marked superseded: %+v", replies[1])
	}
	if e.VerifyOutcome() != "" {
		t.Fatalf("verify outcome = %q with verification disabled", e.VerifyOutcome())
	}
}

// The superseded notice must arrive after the draft's fragments and before
// the replacement's, so a live client can fold exactly the draft.
func TestAnswerSuperseded_OrderedWithStreamedDeltas(t *testing.T) {
	srv := answerScriptServer(t,
		scriptedReply{stream: []string{
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"w","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"a.go\"}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		}},
		scriptedReply{stream: []string{`{"choices":[{"delta":{"content":"draft"}}]}`, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`}},
		scriptedReply{stream: []string{`{"choices":[{"delta":{"content":"final"}}]}`, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`}},
	)
	e, _ := answerEngine(t, srv)
	var seq []string
	e.SetStream(true)
	e.SetDeltaHandler(func(d llmclient.Delta) error {
		if d.Kind == llmclient.DeltaContent {
			seq = append(seq, "delta:"+d.Text)
		}
		return nil
	})
	e.SetAnswerEventHandler(func(ev AnswerEvent) {
		seq = append(seq, fmt.Sprintf("%s:%s:streamed=%v", ev.Type, ev.Reason, ev.Streamed))
	})
	if _, err := e.Run(context.Background(), "edit a.go"); err != nil {
		t.Fatal(err)
	}
	want := "[delta:draft answer_superseded:completion_nudge:streamed=true delta:final]"
	if fmt.Sprint(seq) != want {
		t.Fatalf("sequence = %v\nwant       %s", seq, want)
	}
}

func TestAnswerSuperseded_VerifyHintRetry(t *testing.T) {
	srv := answerScriptServer(t, textReply("draft"), verdictReply("fail"), textReply("fixed"), verdictReply("pass"))
	e, got := answerEngine(t, srv)
	e.SetVerify(VerifyConfig{Enabled: true, Mode: VerifyModeHint, MaxCycles: 2})
	answer, msgs, err := e.RunWithMessages(context.Background(), []session.Message{{Role: "user", Content: "explain"}})
	if err != nil {
		t.Fatal(err)
	}
	if answer != "fixed" {
		t.Fatalf("answer = %q", answer)
	}
	want := "[verification_started verification_completed:fail answer_superseded:verify_retry verification_started verification_completed:pass]"
	if fmt.Sprint(eventTypes(*got)) != want {
		t.Fatalf("events = %v\nwant     %s", eventTypes(*got), want)
	}
	if ev := (*got)[2]; ev.Cycle != 1 {
		t.Fatalf("retry cycle = %d, want 1", ev.Cycle)
	}
	if ev := (*got)[4]; ev.CyclesUsed != 1 {
		t.Fatalf("cycles_used = %d, want 1", ev.CyclesUsed)
	}
	replies := assistantReplies(msgs)
	if len(replies) != 2 || !replies[0].Superseded || replies[0].SupersededReason != SupersededVerifyRetry || replies[1].Superseded {
		t.Fatalf("replies = %+v", replies)
	}
	if e.VerifyOutcome() != VerifyOutcomePass {
		t.Fatalf("verify outcome = %q, want pass", e.VerifyOutcome())
	}
}

func TestVerifyOutcome_StrictFail(t *testing.T) {
	srv := answerScriptServer(t, textReply("answer"), verdictReply("fail"))
	e, got := answerEngine(t, srv)
	e.SetVerify(VerifyConfig{Enabled: true, Mode: VerifyModeStrict, MaxCycles: 3})
	answer, msgs, err := e.RunWithMessages(context.Background(), []session.Message{{Role: "user", Content: "explain"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(answer, VerifyFailedMarker) {
		t.Fatalf("answer not marked: %q", answer)
	}
	if fmt.Sprint(eventTypes(*got)) != "[verification_started verification_completed:fail]" {
		t.Fatalf("events = %v", eventTypes(*got))
	}
	if e.VerifyOutcome() != VerifyOutcomeFail {
		t.Fatalf("verify outcome = %q, want fail", e.VerifyOutcome())
	}
	for _, m := range assistantReplies(msgs) {
		if m.Superseded {
			t.Fatalf("strict mode superseded a reply: %+v", m)
		}
	}
}

// Hint mode out of cycles ships the corrected answer marked; the stage
// reports skipped/exhausted, and the outcome is fail because of the marker.
func TestVerifyOutcome_HintExhaustedIsFail(t *testing.T) {
	srv := answerScriptServer(t, textReply("draft"), verdictReply("fail"), textReply("fixed"))
	e, got := answerEngine(t, srv)
	e.SetVerify(VerifyConfig{Enabled: true, Mode: VerifyModeHint, MaxCycles: 1})
	answer, err := e.Run(context.Background(), "explain")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(answer, VerifyFailedMarker) {
		t.Fatalf("answer not marked: %q", answer)
	}
	last := (*got)[len(*got)-1]
	if last.Type != "verification_completed" || last.Verdict != VerifyOutcomeSkipped || last.SkippedReason != "exhausted" {
		t.Fatalf("last event = %+v", last)
	}
	if e.VerifyOutcome() != VerifyOutcomeFail {
		t.Fatalf("verify outcome = %q, want fail", e.VerifyOutcome())
	}
}

func TestVerifyOutcome_SkippedOnVerifierError(t *testing.T) {
	srv := answerScriptServer(t, textReply("answer"), scriptedReply{status: http.StatusBadRequest})
	e, got := answerEngine(t, srv)
	e.SetVerify(VerifyConfig{Enabled: true, Mode: VerifyModeStrict})
	answer, err := e.Run(context.Background(), "explain")
	if err != nil {
		t.Fatal(err)
	}
	if answer != "answer" {
		t.Fatalf("answer = %q", answer)
	}
	last := (*got)[len(*got)-1]
	if last.Verdict != VerifyOutcomeSkipped || last.SkippedReason != "error" {
		t.Fatalf("last event = %+v", last)
	}
	if e.VerifyOutcome() != VerifyOutcomeSkipped {
		t.Fatalf("verify outcome = %q, want skipped", e.VerifyOutcome())
	}
}

// A reused engine must not report the previous run's verdict.
func TestVerifyOutcome_ResetPerRun(t *testing.T) {
	srv := answerScriptServer(t, textReply("one"), verdictReply("pass"), textReply("two"))
	e, _ := answerEngine(t, srv)
	e.SetVerify(VerifyConfig{Enabled: true, Mode: VerifyModeStrict})
	if _, err := e.Run(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	if e.VerifyOutcome() != VerifyOutcomePass {
		t.Fatalf("first outcome = %q", e.VerifyOutcome())
	}
	e.SetVerify(VerifyConfig{})
	if _, err := e.Run(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	if e.VerifyOutcome() != "" {
		t.Fatalf("second outcome = %q, want empty", e.VerifyOutcome())
	}
}

// With no client (or no budget headroom) the stage is skipped and says so.
func TestVerifyOutcome_SkippedOnBudget(t *testing.T) {
	e := &Engine{}
	var got []AnswerEvent
	e.SetAnswerEventHandler(func(ev AnswerEvent) { got = append(got, ev) })
	e.SetVerify(VerifyConfig{Enabled: true, Mode: VerifyModeStrict})
	if _, ran := e.runVerifyStage(context.Background(), nil, "answer"); ran {
		t.Fatal("stage ran without a client")
	}
	if len(got) != 1 || got[0].Verdict != VerifyOutcomeSkipped || got[0].SkippedReason != "budget" {
		t.Fatalf("events = %+v", got)
	}
	if e.VerifyOutcome() != VerifyOutcomeSkipped {
		t.Fatalf("verify outcome = %q, want skipped", e.VerifyOutcome())
	}
}

// A nudge on the last iteration extends the budget by one so the
// replacement reply can still be produced.
func TestAnswerSuperseded_NudgeOnLastIteration(t *testing.T) {
	srv := answerScriptServer(t, writeReply, textReply("draft"), textReply("final"))
	w := &contractTool{name: "write_file", run: func(string) (string, error) { return `{"success":true}`, nil }}
	e := New(testChatClient(t, srv.URL), tool.NewRegistry([]tool.Tool{w}), 2, "sys", nil, 0)
	var got []AnswerEvent
	e.SetAnswerEventHandler(func(ev AnswerEvent) { got = append(got, ev) })
	answer, err := e.Run(context.Background(), "edit a.go")
	if err != nil {
		t.Fatal(err)
	}
	if answer != "final" || len(got) != 1 || got[0].Reason != SupersededCompletionNudge {
		t.Fatalf("answer=%q events=%+v", answer, got)
	}
}

// No handler installed: re-asks still work and nothing panics.
func TestAnswerSuperseded_NoHandler(t *testing.T) {
	srv := answerScriptServer(t, writeReply, textReply("draft"), textReply("final"))
	e, _ := answerEngine(t, srv)
	e.SetAnswerEventHandler(nil)
	if answer, err := e.Run(context.Background(), "edit a.go"); err != nil || answer != "final" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
}

// A read-only shell call that discards stderr is not a mutation: the turn's
// answer stands without a completion nudge.
func TestAnswerSuperseded_NoNudgeForDiscardedStderr(t *testing.T) {
	read := scriptedReply{json: `{"choices":[{"message":{"tool_calls":[{"id":"r","type":"function","function":{"name":"shell","arguments":"{\"command\":\"git log --oneline main..HEAD 2>/dev/null | head -30\"}"}}]},"finish_reason":"tool_calls"}]}`}
	srv := answerScriptServer(t, read, textReply("summary"))
	sh := &contractTool{name: "shell", run: func(string) (string, error) { return "abc123 commit", nil }}
	e := New(testChatClient(t, srv.URL), tool.NewRegistry([]tool.Tool{sh}), 8, "sys", nil, 0)
	var got []AnswerEvent
	e.SetAnswerEventHandler(func(ev AnswerEvent) { got = append(got, ev) })
	answer, _, err := e.RunWithMessages(context.Background(), []session.Message{{Role: "user", Content: "describe branch changes"}})
	if err != nil {
		t.Fatal(err)
	}
	if answer != "summary" || len(got) != 0 || len(e.runMutations) != 0 {
		t.Fatalf("answer = %q, events = %v, mutations = %v", answer, eventTypes(got), e.runMutations)
	}
}
