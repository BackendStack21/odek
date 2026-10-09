package loop

import (
	"context"
	"testing"

	"github.com/BackendStack21/odek/internal/tool"
)

// Providers commonly send an empty arguments string for a tool that takes no
// parameters. That is a complete call, not truncated output: the tool must
// run instead of the whole turn being aborted as "incomplete tool arguments".
func TestRED_EmptyArgumentsForNoArgToolStillRuns(t *testing.T) {
	srv := answerScriptServer(t,
		scriptedReply{json: `{"choices":[{"message":{"tool_calls":[{"id":"c1","type":"function","function":{"name":"ping","arguments":""}}]},"finish_reason":"tool_calls"}]}`},
		textReply("pong received"),
	)
	ran := false
	pt := &contractTool{name: "ping", run: func(string) (string, error) { ran = true; return "pong", nil }}
	e := New(testChatClient(t, srv.URL), tool.NewRegistry([]tool.Tool{pt}), 4, "sys", nil, 0)
	ans, err := e.Run(context.Background(), "ping it")
	if err != nil {
		t.Fatalf("run aborted: %v (answer %q)", err, ans)
	}
	if !ran {
		t.Fatal("no-arg tool call with empty arguments string was not executed")
	}
}
