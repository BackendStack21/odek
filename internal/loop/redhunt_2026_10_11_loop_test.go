package loop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// redhuntBatchServer replays the given tool-call batches, then answers
// "All done." for every later request.
func redhuntBatchServer(t *testing.T, batches [][]session.ToolCall) *httptest.Server {
	t.Helper()
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
	t.Cleanup(srv.Close)
	return srv
}

// A batch denial refuses every call in the batch, including a check call
// the operator's DangerousConfig auto-allows and that was never shown on the
// approval card. The environment did not refuse that check; the user only
// refused the risky sibling. Marking it "blocked" makes it non-gating, so
// the run finishes with no "verification incomplete" notice although the
// check never produced evidence.
func TestRED_BatchDenialBlocksAutoAllowedCheck(t *testing.T) {
	planArgs := `{"verb":"create","steps":[{"id":"fix","title":"Fix","checks":[{"id":"listing","description":"listing works","tool":"shell","arguments":{"command":"ls"}}]}]}`
	batches := [][]session.ToolCall{
		{acceptanceCall("plan", "plan", planArgs)},
		{
			acceptanceCall("check", "shell", `{"command":"ls"}`),
			acceptanceCall("risky", "shell", `{"command":"rm -rf /etc/test1"}`),
		},
	}
	srv := redhuntBatchServer(t, batches)
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
	answer, _, err := e.RunWithMessages(context.Background(), []session.Message{{Role: "user", Content: "go"}})
	if err != nil {
		t.Fatal(err)
	}
	st, _ := store.Snapshot()
	if got := st.Steps[0].Checks[0].Status; got == PlanCheckBlocked {
		t.Fatalf("auto-allowed check status = %q after the user denied only its risky sibling; want pending (it never ran, the environment did not refuse it)", got)
	}
	if !strings.Contains(answer, "verification incomplete") {
		t.Fatalf("final answer lacks the verification-incomplete notice although the check never ran: %q", answer)
	}
}
