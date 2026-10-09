package loop

import (
	"context"
	"testing"

	"github.com/BackendStack21/odek/internal/tool"
)

// LastPartialReason is documented as per-run. Run (unlike RunWithMessages)
// does not clear it, so a clean run after a partial one still reports the
// previous run's partial classification.
func TestRED_RunClearsLastPartialReason(t *testing.T) {
	srv := answerScriptServer(t,
		writeReply, textReply("partial summary"), textReply("all done"),
	)
	w := &contractTool{name: "write_file", run: func(string) (string, error) { return `{"success":true}`, nil }}
	e := New(testChatClient(t, srv.URL), tool.NewRegistry([]tool.Tool{w}), 1, "sys", nil, 0)
	if _, err := e.Run(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.LastPartialReason(); !ok {
		t.Fatal("setup: first run should have ended with a partial summary")
	}
	if _, err := e.Run(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	if r, ok := e.LastPartialReason(); ok {
		t.Fatalf("second run completed normally but LastPartialReason still reports %q from the previous run", r)
	}
}
