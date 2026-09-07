package loop

// Bug-hunt v3 (fix/bughunt-v3) RED test — stall detection alternation gap.
//
// The pre-fix tracker kept a single lastToolFingerprint + streak: a model
// alternating two looping calls (A,B,A,B,…) never accumulated a repeat on
// either fingerprint, so a stalled loop burned the full iteration budget
// with zero tool_recovery hints. Per-fingerprint counters close the gap.

import (
	"context"
	"fmt"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

type altTool struct{}

func (altTool) Name() string { return "noop2" }
func (altTool) Description() string {
	return "Alternate no-op tool used to construct an interleaving loop."
}
func (altTool) Schema() any { return map[string]any{"type": "object"} }
func (altTool) Call(args string) (string, error) {
	return `{"ok":true}`, nil
}

// TestStallFiresForInterleavedIdenticalCalls: two distinct looping calls
// alternated 3× each must trip tool_recovery exactly like 3 consecutive
// identical calls do.
func TestStallFiresForInterleavedIdenticalCalls(t *testing.T) {
	responses := []string{
		toolCallResp("noop", `{"a":1}`, "c1"),
		toolCallResp("noop2", `{"b":1}`, "c2"),
		toolCallResp("noop", `{"a":1}`, "c3"),
		toolCallResp("noop2", `{"b":1}`, "c4"),
		toolCallResp("noop", `{"a":1}`, "c5"),
		toolCallResp("noop2", `{"b":1}`, "c6"),
		finalResp,
	}
	var bodies []string
	server := captureServer(responses, &bodies)
	defer server.Close()

	client := testChatClient(t, server.URL)
	registry := tool.NewRegistry([]tool.Tool{&noopTool{}, altTool{}})
	engine := New(client, registry, 10, "", nil, 0)

	fired := false
	engine.SetSignalHandler(func(ev SignalEvent) {
		if ev.Type == "tool_recovery" {
			fired = true
		}
	})

	_, _, err := engine.RunWithMessages(context.Background(), []session.Message{
		{Role: "user", Content: "loop it"},
	})
	if err != nil {
		t.Fatalf("RunWithMessages: %v", err)
	}
	if !fired {
		t.Fatal("interleaved identical calls (A,B,A,B,…) never fired tool_recovery — stall detection alternation gap burns the iteration budget unhinted")
	}
}

type boomTool struct{}

func (boomTool) Name() string        { return "boom" }
func (boomTool) Description() string { return "always fails" }
func (boomTool) Schema() any {
	return map[string]any{"type": "object"}
}
func (boomTool) Call(args string) (string, error) {
	return "", fmt.Errorf("boom")
}

// TestStallSurvivesInterleavedFailure: a looping successful call interleaved
// with a failing different tool must still reach stallThreshold. Wiping the
// whole fingerprint map on one error lets A,B-fail,A,A burn iterations.
func TestStallSurvivesInterleavedFailure(t *testing.T) {
	responses := []string{
		toolCallResp("noop", `{"a":1}`, "c1"),
		toolCallResp("boom", `{}`, "c2"),
		toolCallResp("noop", `{"a":1}`, "c3"),
		toolCallResp("noop", `{"a":1}`, "c4"),
		finalResp,
	}
	var bodies []string
	server := captureServer(responses, &bodies)
	defer server.Close()

	client := testChatClient(t, server.URL)
	registry := tool.NewRegistry([]tool.Tool{&noopTool{}, boomTool{}})
	engine := New(client, registry, 10, "", nil, 0)

	fired := false
	engine.SetSignalHandler(func(ev SignalEvent) {
		if ev.Type == "tool_recovery" && ev.Tool == "noop" {
			fired = true
		}
	})

	_, _, err := engine.RunWithMessages(context.Background(), []session.Message{
		{Role: "user", Content: "loop around a failure"},
	})
	if err != nil {
		t.Fatalf("RunWithMessages: %v", err)
	}
	if !fired {
		t.Fatal("successful identical calls interleaved with a failing other tool never stalled — one error wiped the whole fingerprint map")
	}
}
