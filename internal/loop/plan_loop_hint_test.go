package loop

// plan ↔ loop hints (docs/PLANNING.md Planned loop integrations):
// stall suffix names IDs only, blocked-step streak emits plan_blocked, and
// remaining-steps attach on exhaustion as wrapped derived context.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/budget"
	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

const secretPlanTitle = "TOPSECRET-title-wire"
const secretPlanNote = "TOPSECRET-note-do-not-hint"

func seedPlanMessage(t *testing.T, store *PlanStore) session.Message {
	t.Helper()
	mustExecute(t, store, `{"verb":"create","steps":[`+
		`{"id":"s1","title":"Done already"},`+
		`{"id":"s2","title":"`+secretPlanTitle+`","note":"`+secretPlanNote+`"},`+
		`{"id":"s3","title":"Pending next"}]}`)
	mustExecute(t, store, `{"verb":"update","updates":[{"id":"s1","status":"done"},{"id":"s2","status":"in_progress"}]}`)
	body, err := store.Execute(`{"verb":"get"}`)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	return session.Message{Role: "system", Content: body}
}

func stallHintFrom(messages []session.Message) string {
	var hints []string
	for _, m := range messages {
		if m.Role == "system" && strings.Contains(m.Content, "identical arguments") {
			hints = append(hints, m.Content)
		}
	}
	return strings.Join(hints, "\n")
}

func fakePathTool(name string) tool.Tool {
	return &namedTool{name: name, out: `{"ok":true}`}
}

// TestStallSuffix_IDsNotTitles: three identical read_file calls against a
// live plan name in_progress/next_pending IDs in the stall hint and never
// copy step titles or notes into that engine-trusted hint.
func TestStallSuffix_IDsNotTitles(t *testing.T) {
	args := `{"path":"README.md"}`
	responses := []string{
		toolCallResp("read_file", args, "c1"),
		toolCallResp("read_file", args, "c2"),
		toolCallResp("read_file", args, "c3"),
		finalResp,
	}
	var bodies []string
	server := captureServer(responses, &bodies)
	defer server.Close()

	store := NewPlanStore(12, 2000)
	planMsg := seedPlanMessage(t, store)
	engine := New(testChatClient(t, server.URL),
		tool.NewRegistry([]tool.Tool{fakePathTool("read_file"), NewPlanTool(store)}),
		10, "sys", nil, 0)
	engine.SetPlanStore(store)

	_, messages, err := engine.RunWithMessages(context.Background(), []session.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "loop the file"},
		planMsg,
	})
	if err != nil {
		t.Fatalf("RunWithMessages: %v", err)
	}

	hint := stallHintFrom(messages)
	if hint == "" {
		t.Fatal("stall hint never injected")
	}
	if !strings.Contains(hint, "in_progress=s2") {
		t.Errorf("stall hint missing in_progress=s2:\n%s", hint)
	}
	if !strings.Contains(hint, "next_pending=s3") && !strings.Contains(hint, "next_non_mutating=s3") {
		t.Errorf("stall hint missing next pending/non-mutating s3:\n%s", hint)
	}
	if strings.Contains(hint, secretPlanTitle) || strings.Contains(hint, secretPlanNote) {
		t.Errorf("stall hint leaked plan title/note:\n%s", hint)
	}
}

// TestStallSuffix_EscalateLocalWrite: a local_write+ stall tells the model
// to stop retrying that class and points at the next non-mutating step id.
func TestStallSuffix_EscalateLocalWrite(t *testing.T) {
	args := `{"path":"/tmp/odek-stall-write.txt","content":"x"}`
	responses := []string{
		toolCallResp("write_file", args, "c1"),
		toolCallResp("write_file", args, "c2"),
		toolCallResp("write_file", args, "c3"),
		finalResp,
	}
	var bodies []string
	server := captureServer(responses, &bodies)
	defer server.Close()

	store := NewPlanStore(12, 2000)
	planMsg := seedPlanMessage(t, store)
	engine := New(testChatClient(t, server.URL),
		tool.NewRegistry([]tool.Tool{fakePathTool("write_file"), NewPlanTool(store)}),
		10, "sys", nil, 0)
	engine.SetPlanStore(store)

	_, messages, err := engine.RunWithMessages(context.Background(), []session.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "write it"},
		planMsg,
	})
	if err != nil {
		t.Fatalf("RunWithMessages: %v", err)
	}
	hint := stallHintFrom(messages)
	if hint == "" {
		t.Fatal("stall hint never injected")
	}
	if !strings.Contains(hint, "stop retrying") {
		t.Errorf("local_write stall did not escalate:\n%s", hint)
	}
	if !strings.Contains(hint, "next_non_mutating=s3") {
		t.Errorf("escalated stall missing next_non_mutating=s3:\n%s", hint)
	}
	if strings.Contains(hint, secretPlanTitle) {
		t.Errorf("escalated stall leaked title:\n%s", hint)
	}
}

// TestBlockedStreak_FiresOnceThenResets: three consecutive blocked
// transitions emit one plan_blocked (counts+version only) and a decompose
// hint; a wholesale create resets the streak so a later triple can fire again.
func TestBlockedStreak_FiresOnceThenResets(t *testing.T) {
	responses := []string{
		planTC("c1", `{"verb":"create","steps":[{"id":"s1","title":"`+secretPlanTitle+`"},{"id":"s2","title":"Two"},{"id":"s3","title":"Three"}]}`),
		planTC("c2", `{"verb":"update","updates":[{"id":"s1","status":"blocked"}]}`),
		planTC("c3", `{"verb":"update","updates":[{"id":"s2","status":"blocked"}]}`),
		planTC("c4", `{"verb":"update","updates":[{"id":"s3","status":"blocked"}]}`),
		planTC("c5", `{"verb":"create","steps":[{"id":"n1","title":"`+secretPlanTitle+`"},{"id":"n2","title":"N2"},{"id":"n3","title":"N3"}]}`),
		planTC("c6", `{"verb":"update","updates":[{"id":"n1","status":"blocked"}]}`),
		planTC("c7", `{"verb":"update","updates":[{"id":"n2","status":"blocked"}]}`),
		planTC("c8", `{"verb":"update","updates":[{"id":"n3","status":"blocked"}]}`),
		finalResp,
	}
	var bodies []string
	server := captureServer(responses, &bodies)
	defer server.Close()

	store := NewPlanStore(12, 2000)
	engine := New(testChatClient(t, server.URL),
		tool.NewRegistry([]tool.Tool{NewPlanTool(store)}),
		20, "sys", nil, 0)
	engine.SetPlanStore(store)
	col := &eventCollector{}
	engine.SetEventHandler(col.handle)

	_, messages, err := engine.RunWithMessages(context.Background(), []session.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "plan then block"},
	})
	if err != nil {
		t.Fatalf("RunWithMessages: %v", err)
	}

	var blocked []events.Event
	for _, ev := range col.all() {
		if ev.Type == events.TypePlanBlocked {
			blocked = append(blocked, ev)
		}
	}
	if len(blocked) != 2 {
		t.Fatalf("plan_blocked count = %d, want 2 (triple, create-reset, triple)", len(blocked))
	}

	wantKeys := map[string]bool{"steps": true, "blocked": true, "version": true}
	for i, ev := range blocked {
		if len(ev.Data) != len(wantKeys) {
			t.Errorf("plan_blocked[%d] keys = %v, want %v", i, ev.Data, wantKeys)
		}
		for k := range wantKeys {
			if _, ok := ev.Data[k]; !ok {
				t.Errorf("plan_blocked[%d] missing %q: %v", i, k, ev.Data)
			}
		}
		blob, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(blob), secretPlanTitle) {
			t.Errorf("plan_blocked payload leaked title: %s", blob)
		}
	}

	var sawDecompose bool
	for _, m := range messages {
		if m.Role == "system" && strings.Contains(strings.ToLower(m.Content), "decompose") {
			sawDecompose = true
			if strings.Contains(m.Content, secretPlanTitle) {
				t.Errorf("decompose hint leaked title:\n%s", m.Content)
			}
		}
	}
	if !sawDecompose {
		t.Fatal("blocked streak did not inject a decompose/`create` hint")
	}
}

// TestRemainingSteps_OnBudgetExceededWithoutSummary: runtime exhaustion
// skips the progress-summary side call but still appends remaining step
// IDs+statuses as wrapped, ingest-recorded derived context — never titles.
func TestRemainingSteps_OnBudgetExceededWithoutSummary(t *testing.T) {
	store := NewPlanStore(12, 2000)
	seedPlanMessage(t, store)

	engine := New(nil, tool.NewRegistry(nil), 10, "sys", nil, 0)
	engine.SetPlanStore(store)
	engine.SetUntrustedWrapper(func(source, content string) string {
		return "<UNTRUSTED-" + source + ">" + content + "</UNTRUSTED>"
	})
	var recorded []string
	ctx := WithIngestRecorder(context.Background(), func(source, content string) {
		recorded = append(recorded, source+":"+content)
	})

	_, messages, err := engine.budgetExceeded(ctx, []session.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
	}, &budget.Error{Limit: budget.LimitRuntime, Observed: 60, Maximum: 30}, 2)
	if err == nil {
		t.Fatal("budgetExceeded returned nil error")
	}

	var remaining string
	for _, m := range messages {
		if strings.Contains(m.Content, "<UNTRUSTED-plan_remaining>") {
			remaining = m.Content
		}
		if strings.Contains(m.Content, "progress_summary") {
			t.Errorf("runtime exhaustion must skip the summary side call, found: %s", m.Content)
		}
	}
	if remaining == "" {
		t.Fatal("remaining-steps block missing when summarizer skipped")
	}
	if !strings.Contains(remaining, "s2=in_progress") || !strings.Contains(remaining, "s3=pending") {
		t.Errorf("remaining-steps missing IDs/statuses: %s", remaining)
	}
	if strings.Contains(remaining, "s1=") {
		t.Errorf("done steps must not appear in remaining-steps: %s", remaining)
	}
	if strings.Contains(remaining, secretPlanTitle) || strings.Contains(remaining, secretPlanNote) {
		t.Errorf("remaining-steps leaked title/note: %s", remaining)
	}
	foundIngest := false
	for _, r := range recorded {
		if strings.HasPrefix(r, "plan_remaining:") {
			foundIngest = true
			if strings.Contains(r, secretPlanTitle) {
				t.Errorf("plan_remaining ingest leaked title: %s", r)
			}
		}
	}
	if !foundIngest {
		t.Fatal("plan_remaining ingest not recorded")
	}
}

// TestRemainingSteps_OnIterationCap attaches remaining IDs when the loop
// exhausts max iterations (including when the summarizer is skipped).
func TestRemainingSteps_OnIterationCap(t *testing.T) {
	// maxIter=1 + a tool call: the loop acts once then hits the cap.
	responses := []string{
		toolCallResp("noop", "{}", "c1"),
		finalResp, // unused if summarizer skipped
	}
	var bodies []string
	server := captureServer(responses, &bodies)
	defer server.Close()

	store := NewPlanStore(12, 2000)
	planMsg := seedPlanMessage(t, store)
	engine := New(testChatClient(t, server.URL),
		tool.NewRegistry([]tool.Tool{&noopTool{}, NewPlanTool(store)}),
		1, "sys", nil, 0)
	engine.SetPlanStore(store)
	engine.SetUntrustedWrapper(func(source, content string) string {
		return "<UNTRUSTED-" + source + ">" + content + "</UNTRUSTED>"
	})
	var recorded []string
	ctx := WithIngestRecorder(context.Background(), func(source, content string) {
		recorded = append(recorded, source+":"+content)
	})

	_, messages, err := engine.RunWithMessages(ctx, []session.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "one step"},
		planMsg,
	})
	if err != nil && !strings.Contains(err.Error(), "max iterations") {
		t.Fatalf("RunWithMessages: %v", err)
	}

	found := false
	for _, m := range messages {
		if strings.Contains(m.Content, "<UNTRUSTED-plan_remaining>") &&
			strings.Contains(m.Content, "s2=in_progress") {
			found = true
		}
	}
	if !found {
		t.Fatal("iteration-cap path did not append remaining-steps IDs")
	}
	foundIngest := false
	for _, r := range recorded {
		if strings.HasPrefix(r, "plan_remaining:") {
			foundIngest = true
		}
	}
	if !foundIngest {
		t.Fatal("iteration-cap remaining-steps ingest not recorded")
	}
}
