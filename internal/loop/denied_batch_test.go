package loop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// A call the approval gate refused (observed by the engine, not inferred from
// output text) leaves its matching check blocked rather than failed.
func TestCheckBlockedByObservedBatchDenial(t *testing.T) {
	planArgs := `{"verb":"create","steps":[{"id":"fix","title":"Fix","checks":[{"id":"clean","description":"clean passes","tool":"shell","arguments":{"command":"rm -rf /etc/test0"}}]}]}`
	batches := [][]session.ToolCall{
		{acceptanceCall("plan", "plan", planArgs)},
		{
			acceptanceCall("check", "shell", `{"command":"rm -rf /etc/test0"}`),
			acceptanceCall("other", "shell", `{"command":"rm -rf /etc/test1"}`),
		},
	}
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(requests.Add(1)) - 1
		msg := map[string]any{"content": "All done."}
		finish := "stop"
		if i < len(batches) {
			msg = map[string]any{"tool_calls": batches[i]}
			finish = "tool_calls"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": msg, "finish_reason": finish}}})
	}))
	defer srv.Close()
	store := NewPlanStore(12, 4000)
	shell := &contractTool{name: "shell", run: func(string) (string, error) { return "ran", nil }}
	e := New(testChatClient(t, srv.URL), tool.NewRegistry([]tool.Tool{NewPlanTool(store), shell}), 12, "sys", nil, 0)
	e.SetPlanStore(store)
	e.SetApprover(&mockApprover{approved: false})
	allow := "allow"
	e.SetDangerousConfig(&danger.DangerousConfig{
		DefaultAction: &allow,
		Classes:       map[danger.RiskClass]danger.Action{danger.Destructive: danger.Prompt},
	})
	if _, _, err := e.RunWithMessages(context.Background(), []session.Message{{Role: "user", Content: "go"}}); err != nil {
		t.Fatal(err)
	}
	st, _ := store.Snapshot()
	if got := st.Steps[0].Checks[0].Status; got != "blocked" {
		t.Fatalf("check status = %q, want blocked after observed batch denial", got)
	}
}
