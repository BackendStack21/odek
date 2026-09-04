package loop

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// Bug-sweep 2026-08-31: when a leading injection (skill/episode/extended-
// memory block) sets ctxLeadDroppableFrom, headLen stops BEFORE the first
// user message — so pass 2 of trimContext dropped the original task as the
// first standalone group, violating the documented protected-head invariant
// ("the first user message — the original task — is never dropped").
//
// Mirrors TestTrimContext_PlanProtectedAfterLeadingInjection's setup.

func TestTrimContext_OriginalTaskProtectedAfterLeadingInjection(t *testing.T) {
	client := testChatClient(t, "http://unused")
	engine := New(client, tool.NewRegistry(nil), 10, "", nil, 3000)

	engine.ctxLeadDroppableFrom = -1
	msgs := []session.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
	}
	skillMsg := session.Message{Role: "system", Content: strings.Repeat("SKILL ", 400)}
	msgs = append(msgs[:1], append([]session.Message{skillMsg}, msgs[1:]...)...)
	engine.noteLeadingInjection(msgs, 1)
	if engine.ctxLeadDroppableFrom != 1 {
		t.Fatalf("setup: ctxLeadDroppableFrom = %d, want 1", engine.ctxLeadDroppableFrom)
	}

	// Heavy old groups force pass-2 group drops. Contents stay below
	// toolTruncateMinBytes (2000) so pass 1 cannot absorb the pressure —
	// pass 2 must be the one that drops groups here.
	for i := 0; i < 40; i++ {
		tc := session.ToolCall{ID: fmt.Sprintf("c%d", i), Type: "function"}
		tc.Function.Name = "echo"
		tc.Function.Arguments = "{}"
		msgs = append(msgs,
			session.Message{Role: "assistant", Content: strings.Repeat("x", 300), ToolCalls: []session.ToolCall{tc}},
			session.Message{Role: "tool", Content: strings.Repeat("y", 900), ToolCallID: fmt.Sprintf("c%d", i)},
		)
	}
	got := engine.trimContext(context.Background(), msgs, nil)

	taskSurvived := false
	skillBlockDropped := true
	for _, m := range got {
		if m.Role == "user" && m.Content == "task" {
			taskSurvived = true
		}
		if m.Role == "system" && strings.HasPrefix(m.Content, "SKILL SKILL") {
			skillBlockDropped = false
		}
	}
	if !taskSurvived {
		t.Fatal("original task user message dropped by trimContext — protected-head violation")
	}
	if !skillBlockDropped {
		t.Error("injected skill block must be dropped ahead of the task (droppable boundary)")
	}
}

// The injected block itself must still be droppable ahead of the task —
// that is the whole point of the droppable boundary (an oversized injected
// block must be trimmable before its first API call).
func TestTrimContext_InjectedBlockDroppableBeforeTask(t *testing.T) {
	client := testChatClient(t, "http://unused")
	engine := New(client, tool.NewRegistry(nil), 10, "", nil, 3000)

	engine.ctxLeadDroppableFrom = -1
	msgs := []session.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
	}
	skillMsg := session.Message{Role: "system", Content: strings.Repeat("SKILL ", 400)}
	msgs = append(msgs[:1], append([]session.Message{skillMsg}, msgs[1:]...)...)
	engine.noteLeadingInjection(msgs, 1)

	// Force trimming: with enough pressure the injected skill block and
	// old groups are droppable — but the task itself must survive.
	for i := 0; i < 5; i++ {
		tc := session.ToolCall{ID: fmt.Sprintf("c%d", i), Type: "function"}
		tc.Function.Name = "echo"
		tc.Function.Arguments = "{}"
		msgs = append(msgs,
			session.Message{Role: "assistant", Content: strings.Repeat("x", 2000), ToolCalls: []session.ToolCall{tc}},
			session.Message{Role: "tool", Content: strings.Repeat("y", 2000), ToolCallID: fmt.Sprintf("c%d", i)},
		)
	}
	got := engine.trimContext(context.Background(), msgs, nil)
	for _, m := range got {
		if m.Role == "system" && strings.HasPrefix(m.Content, "SKILL SKILL") {
			t.Error("injected skill block should be trimmable ahead of the task")
		}
	}
	taskSurvived := false
	for _, m := range got {
		if m.Role == "user" && m.Content == "task" {
			taskSurvived = true
		}
	}
	if !taskSurvived {
		t.Fatal("task dropped while only an injected block preceded it")
	}
}
