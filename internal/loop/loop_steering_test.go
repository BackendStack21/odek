package loop

// loop steering: long-tool runtime footer and one-shot completion nudge.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

func TestLongToolRuntimeFooter_PresentAtThreshold(t *testing.T) {
	prev := longToolRuntimeMs
	longToolRuntimeMs = 20
	t.Cleanup(func() { longToolRuntimeMs = prev })

	responses := []string{
		toolCallResp("slow", "{}", "c1"),
		finalResp,
	}
	var bodies []string
	server := captureServer(responses, &bodies)
	defer server.Close()

	engine := New(testChatClient(t, server.URL),
		tool.NewRegistry([]tool.Tool{&slowTool{dur: 40 * time.Millisecond}}),
		5, "", nil, 0)

	_, messages, err := engine.RunWithMessages(context.Background(), []session.Message{
		{Role: "user", Content: "go slow"},
	})
	if err != nil {
		t.Fatalf("RunWithMessages: %v", err)
	}

	var result string
	for _, m := range messages {
		if m.Role == "tool" {
			result = m.Content
		}
	}
	if result == "" {
		t.Fatal("no tool result")
	}
	if !strings.Contains(result, "[runtime:") {
		t.Fatalf("missing runtime footer inside tool result:\n%s", result)
	}
	if !strings.Contains(result, "timeout_seconds") || !strings.Contains(result, "bg_start") {
		t.Errorf("footer missing timeout/bg_start guidance:\n%s", result)
	}
	// Footer must sit inside the DATA delimiter, not after END TOOL RESULT.
	end := strings.Index(result, "END TOOL RESULT")
	foot := strings.Index(result, "[runtime:")
	if end < 0 || foot < 0 || foot > end {
		t.Errorf("runtime footer must be inside the nonce'd delimiter:\n%s", result)
	}
}

func TestLongToolRuntimeFooter_AbsentBelowThreshold(t *testing.T) {
	prev := longToolRuntimeMs
	longToolRuntimeMs = 60_000
	t.Cleanup(func() { longToolRuntimeMs = prev })

	responses := []string{
		toolCallResp("noop", "{}", "c1"),
		finalResp,
	}
	var bodies []string
	server := captureServer(responses, &bodies)
	defer server.Close()

	engine := New(testChatClient(t, server.URL),
		tool.NewRegistry([]tool.Tool{&noopTool{}}),
		5, "", nil, 0)
	_, messages, err := engine.RunWithMessages(context.Background(), []session.Message{
		{Role: "user", Content: "quick"},
	})
	if err != nil {
		t.Fatalf("RunWithMessages: %v", err)
	}
	for _, m := range messages {
		if m.Role == "tool" && strings.Contains(m.Content, "[runtime:") {
			t.Fatalf("fast tool should not carry a runtime footer:\n%s", m.Content)
		}
	}
}

func TestCompletionNudge_OpenPlanBlocksFirstDone(t *testing.T) {
	store := NewPlanStore(12, 2000)
	mustExecute(t, store, `{"verb":"create","steps":[{"id":"s1","title":"TOPSECRET-nudge-title"},{"id":"s2","title":"Two"},{"id":"s3","title":"Three"}]}`)
	mustExecute(t, store, `{"verb":"update","updates":[{"id":"s1","status":"in_progress"}]}`)
	body, err := store.Execute(`{"verb":"get"}`)
	if err != nil {
		t.Fatal(err)
	}

	responses := []string{
		`{"choices":[{"message":{"role":"assistant","content":"TASK COMPLETE all done"}}]}`,
		`{"choices":[{"message":{"role":"assistant","content":"still working on remaining steps"}}]}`,
	}
	var bodies []string
	server := captureServer(responses, &bodies)
	defer server.Close()

	engine := New(testChatClient(t, server.URL),
		tool.NewRegistry([]tool.Tool{NewPlanTool(store)}),
		10, "sys", nil, 0)
	engine.SetPlanStore(store)

	result, messages, err := engine.RunWithMessages(context.Background(), []session.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "finish"},
		{Role: "system", Content: body},
	})
	if err != nil {
		t.Fatalf("RunWithMessages: %v", err)
	}
	if result == "TASK COMPLETE all done" {
		t.Fatal("first tool-less turn claimed done with open plan steps — completion nudge did not fire")
	}
	if !strings.Contains(result, "still working") {
		t.Errorf("result = %q, want the second (post-nudge) reply", result)
	}
	var sawNudge bool
	for _, m := range messages {
		if m.Role == "system" && strings.Contains(strings.ToLower(m.Content), "open steps") {
			sawNudge = true
			if strings.Contains(m.Content, "TOPSECRET-nudge-title") {
				t.Errorf("completion nudge leaked plan title:\n%s", m.Content)
			}
		}
	}
	if !sawNudge {
		t.Fatal("completion nudge missing from history")
	}
}

func TestCompletionNudge_SecondToolLessAccepted(t *testing.T) {
	store := NewPlanStore(12, 2000)
	mustExecute(t, store, `{"verb":"create","steps":[{"id":"s1","title":"One"}]}`)
	body, err := store.Execute(`{"verb":"get"}`)
	if err != nil {
		t.Fatal(err)
	}

	responses := []string{
		`{"choices":[{"message":{"role":"assistant","content":"done now"}}]}`,
		`{"choices":[{"message":{"role":"assistant","content":"really done"}}]}`,
	}
	var bodies []string
	server := captureServer(responses, &bodies)
	defer server.Close()

	engine := New(testChatClient(t, server.URL),
		tool.NewRegistry([]tool.Tool{NewPlanTool(store)}),
		10, "sys", nil, 0)
	engine.SetPlanStore(store)

	result, _, err := engine.RunWithMessages(context.Background(), []session.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "finish"},
		{Role: "system", Content: body},
	})
	if err != nil {
		t.Fatalf("RunWithMessages: %v", err)
	}
	if result != "really done" {
		t.Fatalf("second tool-less reply should be accepted, got %q", result)
	}
}

func TestCompletionNudge_IgnoresAssistantDoneTextWithoutLedger(t *testing.T) {
	responses := []string{
		`{"choices":[{"message":{"role":"assistant","content":"TASK COMPLETE all done"}}]}`,
	}
	var bodies []string
	server := captureServer(responses, &bodies)
	defer server.Close()

	engine := New(testChatClient(t, server.URL), tool.NewRegistry(nil), 5, "", nil, 0)
	result, _, err := engine.RunWithMessages(context.Background(), []session.Message{
		{Role: "user", Content: "hi"},
	})
	if err != nil {
		t.Fatalf("RunWithMessages: %v", err)
	}
	if result != "TASK COMPLETE all done" {
		t.Fatalf("without plan/ledger the first tool-less reply must be accepted, got %q", result)
	}
}

func TestDropStallFingerprints_OnlyNamedTool(t *testing.T) {
	m := map[string]int{
		"noop\x00{}": 2,
		"boom\x00{}": 1,
		"noop\x00x":  3,
	}
	dropStallFingerprints(m, "boom")
	if _, ok := m["boom\x00{}"]; ok {
		t.Fatal("boom fingerprint should be dropped")
	}
	if m["noop\x00{}"] != 2 || m["noop\x00x"] != 3 {
		t.Fatalf("noop fingerprints should survive: %v", m)
	}
}

func TestEvictLowestStallCount(t *testing.T) {
	m := map[string]int{"a": 3, "b": 1, "c": 2}
	evictLowestStallCount(m)
	if _, ok := m["b"]; ok {
		t.Fatalf("lowest count should be evicted, got %v", m)
	}
	if len(m) != 2 {
		t.Fatalf("len = %d, want 2", len(m))
	}
}

func TestFormatLongToolRuntime(t *testing.T) {
	if got := formatLongToolRuntime(40); got != "40ms" {
		t.Errorf("40ms = %q", got)
	}
	if got := formatLongToolRuntime(90_000); got != "1m30s" {
		t.Errorf("90s = %q", got)
	}
	if got := formatLongToolRuntime(192_000); got != "3m12s" {
		t.Errorf("3m12s = %q", got)
	}
}
