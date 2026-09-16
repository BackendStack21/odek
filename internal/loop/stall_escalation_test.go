package loop

import (
	"context"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// ── B2: stall-hint escalation ────────────────────────────────────────────
//
// The old implementation reset the fingerprint counter to 0 after every
// hint, so a hint-resistant loop re-hit the threshold every 3 iterations
// forever — identical hints, zero escalation, full budget burn. The hint
// cadence must escalate (3, then 6, 12, …) and later hints must say
// "again" so the model notices the loop is NOT self-correcting.

func TestStallHintEscalatesNotResets(t *testing.T) {
	// 12 identical successful calls: escalated cadence fires at 3, 6, 12
	// (3 signals); the old reset-to-zero cadence fired at 3, 6, 9, 12 with
	// byte-identical messages.
	var responses []string
	for i := 0; i < 12; i++ {
		responses = append(responses, toolCallResp("noop", `{"a":1}`, "c"+strings.Repeat("x", i)))
	}
	responses = append(responses, finalResp)

	var bodies []string
	server := captureServer(responses, &bodies)
	defer server.Close()

	client := testChatClient(t, server.URL)
	registry := tool.NewRegistry([]tool.Tool{&noopTool{}})
	engine := New(client, registry, 30, "", nil, 0)

	var recoveries []SignalEvent
	engine.SetSignalHandler(func(ev SignalEvent) {
		if ev.Type == "tool_recovery" && ev.Tool == "noop" {
			recoveries = append(recoveries, ev)
		}
	})

	if _, _, err := engine.RunWithMessages(context.Background(), []session.Message{
		{Role: "user", Content: "loop harder"},
	}); err != nil {
		t.Fatalf("RunWithMessages: %v", err)
	}

	if len(recoveries) != 3 {
		t.Fatalf("tool_recovery signals = %d, want 3 (escalating cadence 3/6/12 — reset-to-zero fired 4 identical hints)", len(recoveries))
	}
	if !strings.Contains(recoveries[1].Detail, "again") {
		t.Errorf("second stall hint must escalate (mention 'again'), got detail: %q", recoveries[1].Detail)
	}
}

// ── B2: bg-poll elevated threshold ──────────────────────────────────────
//
// bg_status/bg_output polling was FULLY exempt from stall detection, so a
// model pinning a dead job id burned the whole budget silently. Polling is
// legitimate — but bounded: after 3× the normal stall threshold of
// identical polls, the same corrective hint must fire.

func TestStallBGPollElevatedThreshold(t *testing.T) {
	var responses []string
	for i := 0; i < 9; i++ {
		responses = append(responses, toolCallResp("bg_status", `{"job_id":"bg_dead"}`, "p"+strings.Repeat("y", i)))
	}
	responses = append(responses, finalResp)

	var bodies []string
	server := captureServer(responses, &bodies)
	defer server.Close()

	client := testChatClient(t, server.URL)
	registry := tool.NewRegistry([]tool.Tool{&namedTool{name: "bg_status", out: `{"status":"running"}`}})
	engine := New(client, registry, 30, "", nil, 0)

	var recoveries []SignalEvent
	engine.SetSignalHandler(func(ev SignalEvent) {
		if ev.Type == "tool_recovery" {
			recoveries = append(recoveries, ev)
		}
	})

	if _, _, err := engine.RunWithMessages(context.Background(), []session.Message{
		{Role: "user", Content: "poll a dead job forever"},
	}); err != nil {
		t.Fatalf("RunWithMessages: %v", err)
	}

	if len(recoveries) == 0 {
		t.Fatal("9 identical bg_status polls never fired a stall hint — the bg-poll exemption is total and burns the budget silently")
	}
}

type namedTailTool struct{ name, out string }

func (t *namedTailTool) Name() string        { return t.name }
func (t *namedTailTool) Description() string { return "named test tool" }
func (t *namedTailTool) Schema() any         { return map[string]any{"type": "object"} }
func (t *namedTailTool) Call(args string) (string, error) {
	return t.out, nil
}

var _ = session.Message{} // keep import stable if assertions evolve
