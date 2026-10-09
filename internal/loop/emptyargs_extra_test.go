package loop

import (
	"context"
	"testing"

	"github.com/BackendStack21/odek/internal/tool"
)

// A blank arguments string is normalized to "{}" and handed to the tool,
// while siblings in the same batch still run.
func TestEmptyArgumentsNormalizedToEmptyObject(t *testing.T) {
	srv := answerScriptServer(t,
		scriptedReply{json: `{"choices":[{"message":{"tool_calls":[{"id":"c1","type":"function","function":{"name":"ping","arguments":"  "}},{"id":"c2","type":"function","function":{"name":"ping","arguments":"{\"a\":1}"}}]},"finish_reason":"tool_calls"}]}`},
		textReply("done"),
	)
	var got []string
	pt := &contractTool{name: "ping", run: func(a string) (string, error) { got = append(got, a); return "pong", nil }}
	e := New(testChatClient(t, srv.URL), tool.NewRegistry([]tool.Tool{pt}), 4, "sys", nil, 0)
	if _, err := e.Run(context.Background(), "ping"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("tool ran %d times, want 2: %v", len(got), got)
	}
	foundEmpty := false
	for _, a := range got {
		if a == "{}" {
			foundEmpty = true
		}
	}
	if !foundEmpty {
		t.Fatalf("blank arguments not normalized to {}: %v", got)
	}
}

// Genuinely truncated arguments still abort the batch.
func TestTruncatedArgumentsStillAbort(t *testing.T) {
	srv := answerScriptServer(t,
		scriptedReply{json: `{"choices":[{"message":{"tool_calls":[{"id":"c1","type":"function","function":{"name":"ping","arguments":"{\"a\":"}}]},"finish_reason":"tool_calls"}]}`},
	)
	ran := false
	pt := &contractTool{name: "ping", run: func(string) (string, error) { ran = true; return "pong", nil }}
	e := New(testChatClient(t, srv.URL), tool.NewRegistry([]tool.Tool{pt}), 4, "sys", nil, 0)
	if _, err := e.Run(context.Background(), "ping"); err == nil {
		t.Fatal("expected partial-response error")
	}
	if ran {
		t.Fatal("tool must not run on truncated arguments")
	}
}
