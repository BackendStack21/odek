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

// RunWithMessages's documented contract: callers persist the returned
// history and feed it back for the next turn (REPL, Telegram, run — only
// serve filters dynamic injections). The memory slot was tracked ONLY by
// memMsgIdx, reset per run: every fed-back turn inserted a FRESH memory
// block while the stale one survived in the protected leading run — N
// turns ⇒ N memory blocks, the model reading stale facts.
func TestRunWithMessages_MemoryBlockNotDuplicatedAcrossTurns(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer server.Close()

	engine := New(testChatClient(t, server.URL),
		tool.NewRegistry(nil), 10, "sys", nil, 0)
	engine.SetMemoryPromptFunc(func() string { return "MEM" })

	msgs := []session.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "turn 1"}}
	_, msgs, err := engine.RunWithMessages(context.Background(), msgs)
	if err != nil {
		t.Fatalf("turn 1: %v", err)
	}

	// Turn 2: caller feeds the persisted history back plus a new user msg.
	msgs = append(msgs, session.Message{Role: "user", Content: "turn 2"})
	_, msgs, err = engine.RunWithMessages(context.Background(), msgs)
	if err != nil {
		t.Fatalf("turn 2: %v", err)
	}

	count := 0
	for _, m := range msgs {
		if m.Role == "system" && strings.Contains(m.Content, "\nMEM\n") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("memory system messages after 2 fed-back turns: %d, want exactly 1 (history: %v)", count, summarize(msgs))
	}
}

// Memory content changes between turns: the stale block in the fed-back
// history must be ADOPTED and updated, not joined by a second block.
func TestRunWithMessages_MemoryBlockUpdatedNotDuplicatedOnContentChange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer server.Close()

	engine := New(testChatClient(t, server.URL),
		tool.NewRegistry(nil), 10, "sys", nil, 0)
	mem := "mem-old"
	engine.SetMemoryPromptFunc(func() string { return mem })

	msgs := []session.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "turn 1"}}
	_, msgs, _ = engine.RunWithMessages(context.Background(), msgs)

	mem = "mem-new"
	msgs = append(msgs, session.Message{Role: "user", Content: "turn 2"})
	_, msgs, err := engine.RunWithMessages(context.Background(), msgs)
	if err != nil {
		t.Fatalf("turn 2: %v", err)
	}

	var old, new int
	for _, m := range msgs {
		if m.Role == "system" && strings.Contains(m.Content, "\nmem-old\n") {
			old++
		}
		if m.Role == "system" && strings.Contains(m.Content, "\nmem-new\n") {
			new++
		}
	}
	if old != 0 || new != 1 {
		t.Fatalf("memory blocks: old=%d new=%d, want old=0 new=1 (adopt+update)", old, new)
	}
}

func summarize(msgs []session.Message) string {
	out := ""
	for i, m := range msgs {
		if i > 0 {
			out += ", "
		}
		c := m.Content
		if len(c) > 20 {
			c = c[:20]
		}
		out += m.Role + "(" + c + ")"
	}
	return out
}
