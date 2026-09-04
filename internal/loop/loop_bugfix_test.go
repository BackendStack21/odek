package loop

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// Bug-sweep 2026-08-31, wave 2 (internal/loop):
//
//  1. Failure classification sniffed output text for the literal `"error":`.
//     A successful read/grep whose result legitimately contains that string
//     counted as a tool failure; 3 in a row fired a false keep-failing hint
//     and a false tool_recovery signal. Classification now uses the real
//     execution outcome recorded in Phase 2.
//  2. refreshDigest inserted the compaction digest at headLen without
//     shifting the droppable boundary, so the freshly inserted digest was
//     dropped by the next trim while buildTrimWarning kept advertising it.

// fakeTool lives in loop_test.go.

func runTwoTurnToolEngine(t *testing.T, toolOutput string) *Engine {
	t.Helper()
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"checking","tool_calls":[`+
				`{"id":"call_1","function":{"name":"echo","arguments":"{}"}}]}}]}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
	}))
	t.Cleanup(server.Close)

	echoTool := &fakeTool{name: "echo", description: "echoes", output: toolOutput}
	registry := tool.NewRegistry([]tool.Tool{echoTool})
	client := testChatClient(t, server.URL)
	engine := New(client, registry, 10, "", nil, 0)
	if _, err := engine.Run(context.Background(), "run the tool"); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	return engine
}

func TestEngine_Run_ErrorLiteralOutputNotAFailure(t *testing.T) {
	// The tool SUCCEEDS but its output legitimately contains the literal
	// `"error":` (reading an error log, searching code matching the JSON
	// key). It must not count as a tool failure.
	engine := runTwoTurnToolEngine(t, `scan ok; matched line: {"error": "boom"}`)
	if got := engine.maxConsecutiveToolErrors["echo"]; got != 0 {
		t.Errorf("maxConsecutiveToolErrors[echo] = %d, want 0 — output-text sniffing counted a successful result as a failure", got)
	}
}

func TestEngine_Run_RealToolErrorStillCounts(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"checking","tool_calls":[`+
				`{"id":"call_1","function":{"name":"echo","arguments":"{}"}}]}}]}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
	}))
	t.Cleanup(server.Close)

	registry := tool.NewRegistry([]tool.Tool{&failTool{name: "echo"}})
	client := testChatClient(t, server.URL)
	engine := New(client, registry, 10, "", nil, 0)
	if _, err := engine.Run(context.Background(), "run the tool"); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if got := engine.maxConsecutiveToolErrors["echo"]; got != 1 {
		t.Errorf("maxConsecutiveToolErrors[echo] = %d, want 1 — real failures must still count", got)
	}
}

func TestTrimContext_DigestSurvivesSuccessiveTrims(t *testing.T) {
	// Live LLM endpoint: refreshDigest's summarizer side-call must succeed
	// for the digest message to be created at all (summarizer failure leaves
	// the previous digest untouched by design).
	summarizer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"compressed summary of earlier turns"}}]}`)
	}))
	t.Cleanup(summarizer.Close)
	client := testChatClient(t, summarizer.URL)
	engine := New(client, tool.NewRegistry(nil), 10, "", nil, 3000)
	engine.SetCompaction(true)

	engine.ctxLeadDroppableFrom = -1
	msgs := []session.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
	}
	skillMsg := session.Message{Role: "system", Content: strings.Repeat("SKILL ", 400)}
	msgs = append(msgs[:1], append([]session.Message{skillMsg}, msgs[1:]...)...)
	engine.noteLeadingInjection(msgs, 1)

	heavy := func(msgs []session.Message) []session.Message {
		for i := 0; i < 5; i++ {
			tc := session.ToolCall{ID: fmt.Sprintf("c%d-%d", i, len(msgs)), Type: "function"}
			tc.Function.Name = "echo"
			tc.Function.Arguments = "{}"
			msgs = append(msgs,
				session.Message{Role: "assistant", Content: strings.Repeat("x", 2000), ToolCalls: []session.ToolCall{tc}},
				session.Message{Role: "tool", Content: strings.Repeat("y", 2000), ToolCallID: tc.ID},
			)
		}
		return msgs
	}

	got1 := engine.trimContext(context.Background(), heavy(msgs), nil)
	digestSeen := false
	for _, m := range got1 {
		if isDigestMessage(m) {
			digestSeen = true
		}
	}
	if !digestSeen {
		t.Fatal("setup: first trim with compaction produced no digest message")
	}

	// Second trim under fresh pressure: the digest must survive — it is
	// part of the protected head (boundary shifted past it at insertion).
	got2 := engine.trimContext(context.Background(), heavy(got1), nil)
	for _, m := range got2 {
		if isDigestMessage(m) {
			return // survived
		}
	}
	t.Fatal("compaction digest dropped by the second trim while buildTrimWarning still advertises it")
}
