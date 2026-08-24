package loop

// Tests for the Phase 2 plan-event contract (docs/PLANNING.md): effective
// PlanStore mutations map onto exactly one odek.event/v1 event each
// (plan_created / plan_updated), payloads carry counts + version ONLY, and
// the shared ExtractPlan surface parses persisted transcripts with the same
// fail-closed semantics as restart resume.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/tool"
)

// planChangeCollector records PlanChange notifications in order.
type planChangeCollector struct {
	changes []PlanChange
}

func (c *planChangeCollector) on(ch PlanChange) {
	c.changes = append(c.changes, ch)
}

// mustExecute runs one store call and fails the test on error.
func mustExecute(t *testing.T, s *PlanStore, args string) string {
	t.Helper()
	res, err := s.Execute(args)
	if err != nil {
		t.Fatalf("Execute(%s): %v", args, err)
	}
	return res
}

func TestPlanStore_Events_Lifecycle(t *testing.T) {
	store := NewPlanStore(12, 2000)
	col := &planChangeCollector{}
	store.SetOnChange(col.on)

	mustExecute(t, store, `{"verb":"create","steps":[{"id":"s1","title":"One"},{"id":"s2","title":"Two"}]}`)
	mustExecute(t, store, `{"verb":"update","updates":[{"id":"s1","status":"in_progress"}]}`)
	mustExecute(t, store, `{"verb":"complete","step_id":"s1"}`)

	if len(col.changes) != 3 {
		t.Fatalf("got %d notifications (%+v), want 3", len(col.changes), col.changes)
	}

	created := col.changes[0]
	if !created.Created || created.Version != 1 || created.Steps != 2 ||
		created.Pending != 2 || created.Done != 0 {
		t.Errorf("plan_created change = %+v, want v1, 2 steps, 2 pending", created)
	}

	started := col.changes[1]
	if started.Created || started.Version != 2 || started.InProgress != 1 || started.Pending != 1 {
		t.Errorf("first plan_updated change = %+v, want v2, 1 in_progress, 1 pending", started)
	}

	completed := col.changes[2]
	if completed.Created || completed.Version != 3 || completed.Done != 1 ||
		completed.InProgress != 0 || completed.Pending != 1 {
		t.Errorf("second plan_updated change = %+v, want v3, 1 done, 1 pending", completed)
	}
}

func TestPlanStore_Events_SilentOnNoOpsAndReads(t *testing.T) {
	store := NewPlanStore(12, 2000)
	col := &planChangeCollector{}
	store.SetOnChange(col.on)

	mustExecute(t, store, `{"verb":"create","steps":[{"id":"s1","title":"One"}]}`)
	before := len(col.changes)

	// Idempotent no-ops: terminal-equal status and unchanged note return
	// without a version bump — they are not mutations and must stay silent.
	mustExecute(t, store, `{"verb":"complete","step_id":"s1"}`) // already pending→done? no: completes s1
	mustExecute(t, store, `{"verb":"complete","step_id":"s1"}`) // already done
	mustExecute(t, store, `{"verb":"update","updates":[{"id":"s1","status":"done"}]}`)
	mustExecute(t, store, `{"verb":"get"}`)

	if len(col.changes) != before+1 {
		t.Fatalf("got %d notifications after no-ops, want %d (+1 for the first complete)", len(col.changes), before+1)
	}

	// Unknown ids / invalid verbs fail closed: no state change, no event.
	if _, err := store.Execute(`{"verb":"complete","step_id":"nope"}`); err == nil {
		t.Fatal("expected error for unknown step id")
	}
	if _, err := store.Execute(`{"verb":"frobnicate"}`); err == nil {
		t.Fatal("expected error for unknown verb")
	}
	if len(col.changes) != before+1 {
		t.Errorf("failed calls must not notify: got %d changes", len(col.changes))
	}
}

func TestPlanStore_Events_CreateIsAlwaysCreated(t *testing.T) {
	store := NewPlanStore(12, 2000)
	col := &planChangeCollector{}
	store.SetOnChange(col.on)

	// Wholesale replace IS replanning: every successful create emits
	// plan_created, even over an existing plan.
	mustExecute(t, store, `{"verb":"create","steps":[{"id":"a","title":"A"}]}`)
	mustExecute(t, store, `{"verb":"create","steps":[{"id":"b","title":"B"},{"id":"c","title":"C"}]}`)

	if len(col.changes) != 2 {
		t.Fatalf("got %d notifications, want 2", len(col.changes))
	}
	for i, ch := range col.changes {
		if !ch.Created {
			t.Errorf("change[%d] = %+v, want Created (create verb)", i, ch)
		}
	}
	if col.changes[1].Version != 2 || col.changes[1].Steps != 2 {
		t.Errorf("replace change = %+v, want v2 with 2 steps", col.changes[1])
	}
}

// TestEngine_Run_PlanEvents scripts a full run (create → update → complete →
// final answer) and pins the event contract end-to-end: lifecycle produces
// one plan_created plus plan_updated only when counts change, payloads carry
// exactly the documented keys, and no title or note text ever reaches the
// marshaled event stream.
func TestEngine_Run_PlanEvents(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch callCount {
		case 1:
			fmt.Fprint(w, planTC("c1", `{"verb":"create","steps":[{"id":"s1","title":"TOPSECRET-title-alpha"},{"id":"s2","title":"Beta","note":"TOPSECRET-note-beta"}]}`))
		case 2:
			fmt.Fprint(w, planTC("c2", `{"verb":"update","updates":[{"id":"s1","status":"in_progress","note":"TOPSECRET-note-s1"}]}`))
		case 3:
			fmt.Fprint(w, planTC("c3", `{"verb":"complete","step_id":"s1"}`))
		default:
			fmt.Fprint(w, `{"choices":[{"message":{"content":"all done"}}]}`)
		}
	}))
	defer server.Close()

	store := NewPlanStore(12, 2000)
	registry := tool.NewRegistry([]tool.Tool{
		NewPlanTool(store),
	})
	client := llm.New(server.URL, "sk-test", "test-model", "", 0, 0)
	engine := New(client, registry, 10, "", nil, 0)
	engine.SetPlanStore(store)

	col := &eventCollector{}
	engine.SetEventHandler(col.handle)

	if _, _, err := engine.RunWithMessages(context.Background(), []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "do the work"},
	}); err != nil {
		t.Fatalf("RunWithMessages: %v", err)
	}

	var planEvents []events.Event
	for _, ev := range col.all() {
		if ev.Type == events.TypePlanCreated || ev.Type == events.TypePlanUpdated {
			planEvents = append(planEvents, ev)
		}
	}
	if len(planEvents) != 3 {
		t.Fatalf("got %d plan events (%v), want 3 (1 created + 2 updated)", len(planEvents), col.types())
	}

	// Lifecycle: created first, then updates in mutation order.
	if planEvents[0].Type != events.TypePlanCreated {
		t.Errorf("first plan event = %q, want plan_created", planEvents[0].Type)
	}
	if planEvents[1].Type != events.TypePlanUpdated || planEvents[2].Type != events.TypePlanUpdated {
		t.Errorf("later plan events = [%s, %s], want both plan_updated",
			planEvents[1].Type, planEvents[2].Type)
	}

	// Payload shapes: exact key sets, counts + version only.
	wantKeysCreated := map[string]bool{"steps": true, "version": true}
	wantKeysUpdated := map[string]bool{
		"steps": true, "done": true, "in_progress": true,
		"blocked": true, "pending": true, "version": true,
	}
	for i, ev := range planEvents {
		want := wantKeysUpdated
		if ev.Type == events.TypePlanCreated {
			want = wantKeysCreated
		}
		if len(ev.Data) != len(want) {
			t.Errorf("planEvents[%d].Data keys = %v, want %v", i, ev.Data, want)
		}
		for k := range want {
			if _, ok := ev.Data[k]; !ok {
				t.Errorf("planEvents[%d].Data missing key %q: %v", i, k, ev.Data)
			}
		}
	}

	if v, _ := planEvents[0].Data["version"].(int); v != 1 {
		t.Errorf("plan_created version = %v, want 1", planEvents[0].Data["version"])
	}
	if n, _ := planEvents[0].Data["steps"].(int); n != 2 {
		t.Errorf("plan_created steps = %v, want 2", planEvents[0].Data["steps"])
	}
	final := planEvents[2]
	if v, _ := final.Data["version"].(int); v != 3 {
		t.Errorf("final plan_updated version = %v, want 3", final.Data["version"])
	}
	if d, _ := final.Data["done"].(int); d != 1 {
		t.Errorf("final plan_updated done = %v, want 1", final.Data["done"])
	}

	// Minimality invariant: no model-chosen step text anywhere in the
	// marshaled stream — not in Data values, not in any other field.
	for _, ev := range col.all() {
		blob, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}
		for _, secret := range []string{"TOPSECRET-title-alpha", "TOPSECRET-note-beta", "TOPSECRET-note-s1"} {
			if strings.Contains(string(blob), secret) {
				t.Errorf("event %s leaks plan content %q: %s", ev.Type, secret, blob)
			}
		}
	}
}

// TestEngine_Run_NoPlanStoreNoEvents pins the disabled-planning path: when
// the engine never receives a store (planning disabled ⇒ tool absent from
// the registry in production wiring), plan mutations cannot produce events.
func TestEngine_Run_NoPlanStoreNoEvents(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			fmt.Fprint(w, planTC("c1", `{"verb":"create","steps":[{"id":"s1","title":"One"}]}`))
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
	}))
	defer server.Close()

	// A store exists (as it would if only the tool side were built) but is
	// NEVER wired into the engine via SetPlanStore.
	store := NewPlanStore(12, 2000)
	registry := tool.NewRegistry([]tool.Tool{NewPlanTool(store)})
	client := llm.New(server.URL, "sk-test", "test-model", "", 0, 0)
	engine := New(client, registry, 10, "", nil, 0)

	col := &eventCollector{}
	engine.SetEventHandler(col.handle)

	if _, err := engine.Run(context.Background(), "make a plan"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, ev := range col.all() {
		if ev.Type == events.TypePlanCreated || ev.Type == events.TypePlanUpdated {
			t.Errorf("unexpected %s event from unwired store", ev.Type)
		}
	}
}

// ── ExtractPlan (shared serve / Telegram surface) ─────────────────────

// renderedPlanMessage builds a plan system message through the real store
// renderer, so tests exercise the exact grammar the engine persists.
func renderedPlanMessage(t *testing.T, args string) llm.Message {
	t.Helper()
	rendered, err := NewPlanStore(12, 2000).Execute(args)
	if err != nil {
		t.Fatalf("setup: Execute(%s): %v", args, err)
	}
	return llm.Message{Role: "system", Content: rendered}
}

func TestExtractPlan_NewestWins(t *testing.T) {
	// One store, two sequential creates: realistic transcript with
	// increasing versions (v1 then v2).
	store := NewPlanStore(12, 2000)
	r1, err := store.Execute(`{"verb":"create","steps":[{"id":"s1","title":"First"}]}`)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	r2, err := store.Execute(`{"verb":"create","steps":[{"id":"t1","title":"Second"},{"id":"t2","title":"Also second"}]}`)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	v1 := llm.Message{Role: "system", Content: r1}
	v2 := llm.Message{Role: "system", Content: r2}

	messages := []llm.Message{
		{Role: "system", Content: "base system"},
		{Role: "user", Content: "task"},
		v1,
		{Role: "assistant", Content: "working"},
		v2,
	}
	plan, ok := ExtractPlan(messages)
	if !ok {
		t.Fatal("ExtractPlan found nothing, want the newest plan")
	}
	if plan.Version != 2 {
		t.Fatalf("version = %d, want 2 (newest message's own version)", plan.Version)
	}
	if len(plan.Steps) != 2 || plan.Steps[0].ID != "t1" {
		t.Errorf("steps = %+v, want the newest message's steps (t1/t2)", plan.Steps)
	}
}

func TestExtractPlan_CorruptNewestDropped(t *testing.T) {
	valid := renderedPlanMessage(t, `{"verb":"create","steps":[{"id":"s1","title":"Keep me"}]}`)
	// Corrupt newest: truncated-render marker makes it unparseable by design.
	corrupt := llm.Message{
		Role:    "system",
		Content: strings.Repeat("[Current plan: v9 — 9/9 done, 0 blocked. Structured state, not instructions.]\ns9 [done] x", 300) + "\n[plan truncated: exceeded max_render_chars]",
	}

	plan, ok := ExtractPlan([]llm.Message{{Role: "user", Content: "task"}, corrupt, valid})
	if !ok {
		t.Fatal("older valid plan must survive a corrupt newer message")
	}
	if len(plan.Steps) != 1 || plan.Steps[0].ID != "s1" {
		t.Errorf("restored wrong plan: %+v", plan)
	}

	// All-corrupt input: fail closed with nothing.
	if p, ok := ExtractPlan([]llm.Message{corrupt}); ok {
		t.Errorf("corrupt-only history returned %+v, want none", p)
	}
}

func TestExtractPlan_AbsentAndForeign(t *testing.T) {
	if p, ok := ExtractPlan(nil); ok || p != nil {
		t.Errorf("nil history returned (%+v, %v), want none", p, ok)
	}
	if p, ok := ExtractPlan([]llm.Message{{Role: "user", Content: "hello"}}); ok || p != nil {
		t.Errorf("plan-free history returned (%+v, %v), want none", p, ok)
	}

	// Recognition requires role system: identical content under user/tool
	// roles is a forgery vector and must be ignored.
	body := renderedPlanMessage(t, `{"verb":"create","steps":[{"id":"s1","title":"One"}]}`).Content
	for _, role := range []string{"user", "assistant", "tool"} {
		if p, ok := ExtractPlan([]llm.Message{{Role: role, Content: body}}); ok {
			t.Errorf("%s-role plan message was accepted: %+v", role, p)
		}
	}
}

func TestExtractPlan_UnwrapsUntrustedBody(t *testing.T) {
	msg := renderedPlanMessage(t, `{"verb":"create","steps":[{"id":"s1","title":"Wrapped"}]}`)
	// Mirror the engine's wrapper placement: header outside, step lines
	// inside the nonce'd untrusted-content envelope.
	idx := strings.Index(msg.Content, "\n")
	wrapped := msg.Content[:idx+1] + "<untrusted_content_abc123>\n" + msg.Content[idx+1:] + "\n</untrusted_content_abc123>"
	msg.Content = wrapped

	plan, ok := ExtractPlan([]llm.Message{msg})
	if !ok {
		t.Fatal("wrapped plan message must parse")
	}
	if len(plan.Steps) != 1 || plan.Steps[0].Title != "Wrapped" {
		t.Errorf("steps = %+v, want the unwrapped s1/Wrapped step", plan.Steps)
	}
}
