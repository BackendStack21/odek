package loop

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

func longTrimHistory(groups int) []session.Message {
	msgs := []session.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "original task"},
	}
	for i := 0; i < groups; i++ {
		id := fmt.Sprintf("c%d", i)
		if i%50 == 0 {
			msgs = append(msgs, session.Message{Role: "user", Content: fmt.Sprintf("follow-up %d", i)})
		}
		tc := session.ToolCall{ID: id}
		tc.Function.Name = "shell"
		msgs = append(msgs,
			session.Message{Role: "assistant", ToolCalls: []session.ToolCall{tc}},
			session.Message{Role: "tool", ToolCallID: id, Content: strings.Repeat("x", 300)},
		)
	}
	return msgs
}

// A single trim of a long history must not move the tail once per dropped
// group: the allocation count is independent of the number of dropped groups.
func TestRED_Loop_TrimDropsAreLinear(t *testing.T) {
	msgs := longTrimHistory(2500)
	e := &Engine{maxContext: 20000, compaction: false}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	out := e.trimContext(context.Background(), msgs, nil)
	runtime.ReadMemStats(&after)
	if e.trimGroupsTotal < 1000 {
		t.Fatalf("expected a deep trim, dropped %d groups", e.trimGroupsTotal)
	}
	if got := after.Mallocs - before.Mallocs; got > 500 {
		t.Fatalf("trim made %d allocations for %d dropped groups; want a constant number, not one per group", got, e.trimGroupsTotal)
	}
	checkTrimInvariants(t, out)
}

// Every principal message survives, the head is intact, the newest batch is
// kept, and no tool result is orphaned from its assistant call.
func checkTrimInvariants(t *testing.T, out []session.Message) {
	t.Helper()
	users := 0
	for i, m := range out {
		if m.Role == "user" {
			users++
		}
		if m.Role == "tool" {
			if i == 0 || (out[i-1].Role != "tool" && !(out[i-1].Role == "assistant" && len(out[i-1].ToolCalls) > 0)) {
				t.Fatalf("orphaned tool message at %d", i)
			}
		}
	}
	if users != 2500/50+1 {
		t.Fatalf("principal user messages = %d, want %d", users, 2500/50+1)
	}
	if out[0].Content != "sys" || out[1].Content != "original task" {
		t.Fatalf("protected head changed: %q %q", out[0].Content, out[1].Content)
	}
	last := out[len(out)-1]
	if last.Role != "tool" || last.ToolCallID != "c2499" {
		t.Fatalf("newest tool result dropped: %+v", last)
	}
}

func BenchmarkTrimDeep(b *testing.B) {
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		msgs := longTrimHistory(2500)
		e := &Engine{maxContext: 20000}
		b.StartTimer()
		e.trimContext(context.Background(), msgs, nil)
	}
}

// Refreshing the digest must not copy the whole pending-dropped queue: the
// sketch only reads a bounded prefix of it.
func TestRED_Loop_RefreshDigestDoesNotCopyQueue(t *testing.T) {
	e := &Engine{compaction: true}
	e.pendingDropped = make([]session.Message, 0, 40000)
	for i := 0; i < 20000; i++ {
		e.pendingDropped = append(e.pendingDropped, session.Message{Role: "user", Content: "old turn"})
	}
	msgs := []session.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "task"}}
	dropped := []session.Message{{Role: "assistant", Content: "recent drop"}}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	e.refreshDigest(context.Background(), msgs, dropped)
	runtime.ReadMemStats(&after)
	if got := after.TotalAlloc - before.TotalAlloc; got > 512<<10 {
		t.Fatalf("refreshDigest allocated %d KiB with a 20000-message queue; want it independent of queue size", got>>10)
	}
	if len(e.pendingDropped) != 20001 {
		t.Fatalf("queue length = %d, want 20001", len(e.pendingDropped))
	}
}
