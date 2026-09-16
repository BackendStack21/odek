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

// ── B3: side-call plan prefix carries step TITLES ────────────────────────
//
// sideCallPlanPrefix feeds the compaction digest and budget-summary
// payloads — contexts where the wrapped plan message itself may be the
// dropped content. "s1=pending" without a title tells the summarizer
// nothing about what remains. Titles are model-authored and already on
// the main transcript, so including them adds no new exposure. (The
// anti-title pin applies to stall HINTS — a different path, unchanged.)

func TestSideCallPlanPrefix_IncludesTitles(t *testing.T) {
	engine := New(testChatClient(t, newIdleServer()), tool.NewRegistry(nil), 10, "sys", nil, 0)
	store := NewPlanStore(0, 0)
	if _, err := store.Execute(`{"verb":"create","steps":[` +
		`{"id":"s1","title":"Ship the parser"},` +
		`{"id":"s2","title":"Write the docs"}]}`); err != nil {
		t.Fatalf("plan create: %v", err)
	}
	engine.planStore = store

	prefix := engine.sideCallPlanPrefix()
	if prefix == "" {
		t.Fatal("sideCallPlanPrefix = empty, want remaining steps")
	}
	if !strings.Contains(prefix, "s1=pending") {
		t.Errorf("prefix lost the id=status contract: %q", prefix)
	}
	if !strings.Contains(prefix, "Ship the parser") || !strings.Contains(prefix, "Write the docs") {
		t.Errorf("prefix must include step titles (the summarizer cannot know what 's1' means): %q", prefix)
	}
}

// ── Measurement: side-call usage is observable ───────────────────────────
//
// recordSideCallUsage merges compaction/summary tokens into the run totals
// with no separate accounting, so the side-call cost is invisible to
// /api/usage and events. Every side call must emit a side_call_usage
// signal carrying its token split so run events (--events-jsonl) can
// quantify it.

func TestSideCallUsageSignal_EmittedOnCompaction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"summary"}}],"usage":{"prompt_tokens":120,"completion_tokens":15}}`)
	}))
	defer server.Close()

	client := testChatClient(t, server.URL)
	engine := New(client, tool.NewRegistry(nil), 10, "sys", nil, 0)
	engine.SetCompaction(true)

	var usageSignals []SignalEvent
	engine.SetSignalHandler(func(ev SignalEvent) {
		if ev.Type == "side_call_usage" {
			usageSignals = append(usageSignals, ev)
		}
	})

	msgs := []session.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
	}
	dropped := []session.Message{{Role: "tool", Content: "dropped output"}}
	out := engine.refreshDigest(context.Background(), msgs, dropped)
	engine.waitDigestSideCall(context.Background())
	engine.applyPendingDigest(context.Background(), out)
	_ = out

	if len(usageSignals) == 0 {
		t.Fatal("no side_call_usage signal after compaction side call — side-call cost is unobservable")
	}
	ev := usageSignals[0]
	if ev.Tool != "compaction" {
		t.Errorf("side_call_usage Tool = %q, want kind %q", ev.Tool, "compaction")
	}
	if !strings.Contains(ev.Detail, "in=") || !strings.Contains(ev.Detail, "out=") {
		t.Errorf("side_call_usage Detail must carry the in/out token split: %q", ev.Detail)
	}
}

func TestRecordSideCallUsage_NilResultNoSignal(t *testing.T) {
	engine := New(testChatClient(t, newIdleServer()), tool.NewRegistry(nil), 10, "sys", nil, 0)
	var n int
	engine.SetSignalHandler(func(ev SignalEvent) {
		if ev.Type == "side_call_usage" {
			n++
		}
	})
	engine.recordSideCallUsage("compaction", nil)
	if n != 0 {
		t.Errorf("nil result must not emit a signal, got %d", n)
	}
}

var _ = session.Message{}
