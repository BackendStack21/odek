package loop

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

func reviewProbeCall(id string) session.ToolCall {
	c := session.ToolCall{ID: id, Type: "function"}
	c.Function.Name = "read_file"
	c.Function.Arguments = `{"path":"changed.go"}`
	return c
}

func TestTrimPreservesCurrentPrincipalInstruction(t *testing.T) {
	for _, compact := range []bool{false, true} {
		e := New(nil, tool.NewRegistry(nil), 10, "sys", nil, 1000)
		e.SetCompaction(compact)
		msgs := []session.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "original instruction"},
			{Role: "assistant", Content: "earlier response"},
			{Role: "user", Content: "CURRENT INSTRUCTION: only inspect, do not modify"},
		}
		for i := 0; i < 8; i++ {
			id := fmt.Sprintf("c%d", i)
			msgs = append(msgs, session.Message{Role: "assistant", ToolCalls: []session.ToolCall{reviewProbeCall(id)}}, session.Message{Role: "tool", ToolCallID: id, Content: strings.Repeat("x", 900)})
		}
		got := e.trimContext(context.Background(), msgs, nil)
		if latest := lastUserMessage(got); latest != "CURRENT INSTRUCTION: only inspect, do not modify" {
			t.Fatalf("compaction=%v lost current user instruction: %q", compact, latest)
		}
	}
}

func TestSurvivalPreservesCurrentCompletedActions(t *testing.T) {
	msgs := []session.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "write the requested file"},
		{Role: "assistant", ToolCalls: []session.ToolCall{reviewProbeCall("c1")}},
		{Role: "tool", ToolCallID: "c1", Content: "file already written successfully"},
		{Role: "assistant", ToolCalls: []session.ToolCall{reviewProbeCall("c2")}},
		{Role: "tool", ToolCallID: "c2", Content: "verified file contents"},
	}
	got := trimToSurvival(msgs)
	seen := map[string]bool{}
	for _, m := range got {
		if m.Role == "tool" {
			seen[m.ToolCallID] = true
		}
	}
	if !seen["c1"] || !seen["c2"] {
		t.Fatalf("completed evidence lost: %+v", got)
	}
}

func TestEffectEvidenceSurvivesEveryTrimMode(t *testing.T) {
	e := New(nil, tool.NewRegistry(nil), 10, "sys", nil, 500)
	e.SetCompaction(false)
	e.runMutations = []string{"write_file already-completed.go"}
	messages := []session.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "inspect after previous changes"}, {Role: "assistant", Content: strings.Repeat("padding ", 1000)}}
	messages = e.trimContext(context.Background(), messages, nil)
	for _, got := range [][]session.Message{messages, trimToSurvival(messages)} {
		found := false
		for _, m := range got {
			if isEffectEvidence(m) && strings.Contains(m.Content, "already-completed.go") {
				found = true
			}
		}
		if !found {
			t.Fatal("completed effects disappeared from model context")
		}
	}
}
