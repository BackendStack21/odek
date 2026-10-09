package loop

import (
	"context"
	"testing"

	"github.com/BackendStack21/odek/internal/tool"
)

// The completion nudge grants one extra iteration when it fires on the last
// slot. That grant must be scoped to the run: it mutates the engine's
// configured iteration cap, so every later run on a reused engine (REPL,
// serve, Telegram) silently gets a bigger cap, and each nudge-on-last-slot
// ratchets it further.
func TestRED_CompletionNudgeIterationGrantDoesNotLeakAcrossRuns(t *testing.T) {
	srv := answerScriptServer(t,
		writeReply, textReply("done"), textReply("done again"),
	)
	w := &contractTool{name: "write_file", run: func(string) (string, error) { return `{"success":true}`, nil }}
	e := New(testChatClient(t, srv.URL), tool.NewRegistry([]tool.Tool{w}), 2, "sys", nil, 0)
	if _, err := e.Run(context.Background(), "write a file"); err != nil {
		t.Fatal(err)
	}
	if e.maxIter != 2 {
		t.Fatalf("engine iteration cap changed from 2 to %d after one run; the nudge's extra slot leaks into later runs", e.maxIter)
	}
}
