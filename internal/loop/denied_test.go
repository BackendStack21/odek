package loop

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// A failing check whose own output merely contains the text "approval denied"
// must stay a failed check; only an engine-observed denial may block it.
func TestRED_CheckBlockedByOutputText(t *testing.T) {
	batches := [][]session.ToolCall{
		{acceptanceCall("plan", "plan", acceptancePlanArgs)},
		{acceptanceCall("check", "shell", `{"command":"go test ./parser"}`)},
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
	shell := &contractTool{name: "shell", run: func(string) (string, error) {
		return "FAIL parser: test log says approval denied for fixture", errors.New("exit status 1")
	}}
	e := New(testChatClient(t, srv.URL), tool.NewRegistry([]tool.Tool{NewPlanTool(store), shell}), 12, "sys", nil, 0)
	e.SetPlanStore(store)
	if _, _, err := e.RunWithMessages(context.Background(), []session.Message{{Role: "user", Content: "go"}}); err != nil {
		t.Fatal(err)
	}
	if p := store.PendingChecks(); len(p) == 0 {
		st, _ := store.Snapshot()
		t.Fatalf("failing check was turned into blocked (non-gating) by output text: %+v", st.Steps[0].Checks)
	}
}
