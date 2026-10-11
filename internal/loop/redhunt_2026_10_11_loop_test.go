package loop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/BackendStack21/odek/internal/budget"
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

// Calls skipped because the tool-call budget ran out never execute, yet the
// result phase records them as failed tool runs: a skipped write_file
// invalidates the check that just passed in the same batch.
func TestRED_BudgetSkippedCallInvalidatesPassedCheck(t *testing.T) {
	planArgs := `{"verb":"create","steps":[{"id":"fix","title":"Fix","checks":[{"id":"listing","description":"listing works","tool":"shell","arguments":{"command":"ls"}}]}]}`
	batches := [][]session.ToolCall{
		{acceptanceCall("plan", "plan", planArgs)},
		{
			acceptanceCall("check", "shell", `{"command":"ls"}`),
			acceptanceCall("w", "write_file", `{"path":"/tmp/redhunt-never-written","content":"x"}`),
		},
	}
	srv := redhuntBatchServer(t, batches)
	store := NewPlanStore(12, 4000)
	shell := &contractTool{name: "shell", run: func(string) (string, error) { return "ran", nil }}
	var wrote atomic.Bool
	wf := &contractTool{name: "write_file", run: func(string) (string, error) { wrote.Store(true); return `{"success":true}`, nil }}
	e := New(testChatClient(t, srv.URL), tool.NewRegistry([]tool.Tool{NewPlanTool(store), shell, wf}), 12, "sys", nil, 0)
	e.SetPlanStore(store)
	e.SetLimits(budget.Limits{MaxToolCalls: 2}, "test-model")
	answer, _, err := e.RunWithMessages(context.Background(), []session.Message{{Role: "user", Content: "go"}})
	if err != nil {
		if _, ok := budget.As(err); !ok {
			t.Fatal(err)
		}
	}
	if wrote.Load() {
		t.Fatal("fixture: write_file should have been skipped by the tool-call budget")
	}
	st, _ := store.Snapshot()
	if got := st.Steps[0].Checks[0].Status; got != PlanCheckPassed {
		t.Fatalf("check status = %q; want passed — the only later call in the batch was skipped by the budget and never ran (answer=%q)", got, answer)
	}
}

// A check call skipped by the tool-call budget never ran, but it is
// recorded as a FAILED check (false negative evidence).
func TestRED_BudgetSkippedCheckRecordedFailed(t *testing.T) {
	planArgs := `{"verb":"create","steps":[{"id":"fix","title":"Fix","checks":[{"id":"listing","description":"listing works","tool":"shell","arguments":{"command":"ls"}}]}]}`
	batches := [][]session.ToolCall{
		{acceptanceCall("plan", "plan", planArgs)},
		{
			acceptanceCall("r", "math_eval", `{"expression":"1+1"}`),
			acceptanceCall("check", "shell", `{"command":"ls"}`),
		},
	}
	srv := redhuntBatchServer(t, batches)
	store := NewPlanStore(12, 4000)
	var ran atomic.Bool
	shell := &contractTool{name: "shell", run: func(string) (string, error) { ran.Store(true); return "ran", nil }}
	me := &contractTool{name: "math_eval", run: func(string) (string, error) { return "2", nil }}
	e := New(testChatClient(t, srv.URL), tool.NewRegistry([]tool.Tool{NewPlanTool(store), shell, me}), 12, "sys", nil, 0)
	e.SetPlanStore(store)
	e.SetLimits(budget.Limits{MaxToolCalls: 2}, "test-model")
	_, _, err := e.RunWithMessages(context.Background(), []session.Message{{Role: "user", Content: "go"}})
	if err != nil {
		if _, ok := budget.As(err); !ok {
			t.Fatal(err)
		}
	}
	if ran.Load() {
		t.Fatal("fixture: the check call should have been skipped by the tool-call budget")
	}
	st, _ := store.Snapshot()
	if got := st.Steps[0].Checks[0].Status; got == PlanCheckFailed {
		t.Fatalf("check status = %q although the check call was skipped by the budget and never executed; want pending", got)
	}
}

type redhuntNamedTool struct{ name string }

func (t redhuntNamedTool) Name() string                { return t.name }
func (t redhuntNamedTool) Description() string         { return "fixture " + t.name }
func (t redhuntNamedTool) Schema() any                 { return map[string]any{"type": "object"} }
func (t redhuntNamedTool) Call(string) (string, error) { return "", nil }

// buildToolDefs is called once per run and its order is the request's tool
// order. Ranging over the registry map makes it differ between runs of a
// reused engine (every REPL/serve/Telegram turn), which changes the cached
// prompt prefix (tools are the first cached block; markLastToolCache puts a
// breakpoint on the last tool) and busts the provider prompt cache.
func TestRED_ToolDefsOrderUnstableAcrossRuns(t *testing.T) {
	var tools []tool.Tool
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "p"} {
		tools = append(tools, redhuntNamedTool{name: "tool_" + n})
	}
	e := New(nil, tool.NewRegistry(tools), 3, "sys", nil, 0)
	order := func() string {
		var names []string
		for _, d := range e.buildToolDefs() {
			names = append(names, d.Name)
		}
		return strings.Join(names, ",")
	}
	first := order()
	for i := 0; i < 50; i++ {
		if got := order(); got != first {
			t.Fatalf("tool definition order changed between runs:\n first: %s\n later: %s", first, got)
		}
	}
}

// redhuntResume renders the store's plan as the persisted plan message and
// parses it back the way `odek continue` does (syncPlanFromMessages).
func redhuntResume(t *testing.T, store *PlanStore) error {
	t.Helper()
	st, ok := store.Snapshot()
	if !ok {
		t.Fatal("no plan")
	}
	_, err := parsePlanState(renderPlan(st, store.maxRenderChars), store.maxSteps)
	return err
}

func redhuntCheckedStore(t *testing.T, desc string) *PlanStore {
	t.Helper()
	store := NewPlanStore(12, 16000)
	args := map[string]any{
		"verb": "create",
		"steps": []any{
			map[string]any{"id": "s1", "title": "Build", "checks": []any{
				map[string]any{"id": "c1", "description": desc, "tool": "shell", "arguments": map[string]any{"command": "make"}},
				map[string]any{"id": "c2", "description": "tests pass", "tool": "shell", "arguments": map[string]any{"command": "make test"}},
			}},
			map[string]any{"id": "s2", "title": "Ship"},
		},
	}
	raw, _ := json.Marshal(args)
	if _, err := store.Execute(string(raw)); err != nil {
		t.Fatal(err)
	}
	if err := redhuntResume(t, store); err != nil {
		t.Fatalf("fixture plan does not resume: %v", err)
	}
	return store
}

func redhuntExec(t *testing.T, store *PlanStore, v map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(v)
	if _, err := store.Execute(string(raw)); err != nil {
		t.Fatalf("plan call rejected: %v", err)
	}
}

// check_replace accepts mutations whose result the strict resume parser
// rejects, so the whole plan (every step, every status) is silently dropped
// the next time the session is resumed.
func TestRED_CheckReplaceEvidenceNoteBreaksResume(t *testing.T) {
	store := redhuntCheckedStore(t, strings.Repeat("d", 150))
	redhuntExec(t, store, map[string]any{
		"verb": "check_replace", "step_id": "s1", "check_id": "c1",
		"justification": "verified via CI", "evidence_note": strings.Repeat("e", 100),
	})
	if err := redhuntResume(t, store); err != nil {
		t.Fatalf("plan accepted by check_replace(evidence_note) cannot be resumed: %v", err)
	}
}

// Both parts at their own caps still produce a description the resume
// parser accepts, and a short description keeps the whole note.
func TestRED_CheckReplaceEvidenceNoteBoundedAtCaps(t *testing.T) {
	store := redhuntCheckedStore(t, strings.Repeat("d", maxPlanCheckDescChars))
	redhuntExec(t, store, map[string]any{
		"verb": "check_replace", "step_id": "s1", "check_id": "c1",
		"justification": "verified via CI", "evidence_note": strings.Repeat("e", maxPlanCheckDescChars),
	})
	if err := redhuntResume(t, store); err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	st, _ := store.Snapshot()
	got := st.Steps[0].Checks[0]
	if n := len([]rune(got.Description)); n > maxPlanCheckDescChars || !strings.Contains(got.Description, "(evidence: eee") || got.Status != PlanCheckPassed {
		t.Fatalf("description = %q (%d runes), status %q", got.Description, n, got.Status)
	}
	if d := evidenceCheckDescription("builds", "ci green"); d != "builds (evidence: ci green)" {
		t.Fatalf("short description changed: %q", d)
	}
	if d := evidenceCheckDescription(strings.Repeat("d", 190), "ok"); !strings.HasSuffix(d, " (evidence: ok)") || len([]rune(d)) != maxPlanCheckDescChars {
		t.Fatalf("short note not kept whole: %q", d)
	}
}
