package loop

import (
	"context"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

// Context trimming drops tool-call groups from the model's view, but the
// per-step persist snapshot must still carry them so the store can record the
// session's episode taint (session.Session.EpisodeUntrusted) from the call.
func TestTrimmedToolCallStillReachesPersistSnapshot(t *testing.T) {
	read := session.ToolCall{ID: "c1", Type: "function"}
	read.Function.Name = "read_file"
	read.Function.Arguments = `{"path":"/etc/hosts"}`
	msgs := []session.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
		{Role: "assistant", ToolCalls: []session.ToolCall{read}},
		{Role: "tool", ToolCallID: "c1", Content: strings.Repeat("hosts ", 200)},
	}
	// Later local tool batches push the read out of the protected tail.
	for i := 0; i < 4; i++ {
		id := "s" + string(rune('0'+i))
		local := session.ToolCall{ID: id, Type: "function"}
		local.Function.Name = "shell"
		local.Function.Arguments = `{"command":"go test ./..."}`
		msgs = append(msgs,
			session.Message{Role: "assistant", Content: strings.Repeat("reasoning ", 60), ToolCalls: []session.ToolCall{local}},
			session.Message{Role: "tool", ToolCallID: id, Content: "ok"})
	}
	e := &Engine{maxContext: 400}
	e.startTranscript(msgs)
	var snapshot []session.Message
	e.SetMessagesPersistCallback(func(m []session.Message) { snapshot = m })

	trimmed := e.trimContext(context.Background(), msgs, nil)
	if len(session.EpisodeTaintSources(trimmed)) != 0 {
		t.Fatal("fixture: trimming kept the outside read in the model context")
	}
	e.emitMessagesPersist(trimmed)
	if src := session.EpisodeTaintSources(snapshot); len(src) != 1 || src[0] != "read_file" {
		t.Fatalf("persist snapshot lost the trimmed outside read: sources %v", src)
	}
}
