package loop

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// P4 — soft runtime enforcement RED tests: reminder injection at N=3 tool
// calls without a plan (opt-in via SetPlanRemind, default OFF), no reminder
// for quick tasks, and late/gate-triggered creates flagged provisional.

// toolTC builds a tool_calls response body for an arbitrary tool.
func toolTC(id, name, args string) string {
	return `{"choices":[{"message":{"content":"","tool_calls":[{"id":"` + id +
		`","function":{"name":"` + name + `","arguments":` + strconv.Quote(args) + `}}]}}]}`
}

// When remind is enabled and 3 non-plan tool calls pass without any plan,
// the third result carries a bounded reminder; a plan created afterwards is
// flagged provisional in its receipt.
func TestP4_ReminderAtThreeCallsAndProvisionalFlag(t *testing.T) {
	callCount := 0
	var lastToolOutput string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch callCount {
		case 1, 2, 3:
			fmt.Fprint(w, toolTC("c"+strconv.Itoa(callCount), "echo", `{}`))
		case 4:
			// Model heeds the reminder and creates a (late) plan.
			fmt.Fprint(w, planTC("c4", `{"verb":"create","steps":[{"id":"s1","title":"One"}]}`))
		default:
			fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
		}
	}))
	defer server.Close()

	store := NewPlanStore(12, 2000)
	registry := tool.NewRegistry([]tool.Tool{
		&fakeTool{name: "echo", description: "echo", output: "ok"},
		NewPlanTool(store),
	})
	client := testChatClient(t, server.URL)
	engine := New(client, registry, 10, "", nil, 0)
	engine.SetPlanStore(store)
	engine.SetPlanRemind(true)

	result, messages, err := engine.RunWithMessages(context.Background(), []session.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "do the work"},
	})
	if err != nil || result != "done" {
		t.Fatalf("run: %q %v", result, err)
	}

	for _, m := range messages {
		if m.Role == "tool" {
			lastToolOutput = m.Content
		}
	}
	// The plan receipt is the last tool output: it must carry the
	// provisional marker because work preceded the plan.
	if !strings.Contains(lastToolOutput, "provisional") {
		t.Errorf("late plan receipt should be flagged provisional, last tool output: %s", lastToolOutput)
	}

	sawReminder := false
	for _, m := range messages {
		if m.Role == "tool" && strings.Contains(m.Content, "consider creating a plan") {
			sawReminder = true
		}
	}
	if !sawReminder {
		t.Error("expected plan reminder injected after 3 plan-less tool calls")
	}
}

// Default OFF: no reminder even after many plan-less calls.
func TestP4_ReminderDefaultOff(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount <= 5 {
			fmt.Fprint(w, toolTC("c"+strconv.Itoa(callCount), "echo", `{}`))
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
	}))
	defer server.Close()

	store := NewPlanStore(12, 2000)
	registry := tool.NewRegistry([]tool.Tool{
		&fakeTool{name: "echo", description: "echo", output: "ok"},
		NewPlanTool(store),
	})
	client := testChatClient(t, server.URL)
	engine := New(client, registry, 10, "", nil, 0)
	engine.SetPlanStore(store)

	_, messages, err := engine.RunWithMessages(context.Background(), []session.Message{
		{Role: "system", Content: "sys"}, {Role: "user", Content: "quick"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range messages {
		if strings.Contains(m.Content, "consider creating a plan") {
			t.Error("reminder must not fire when SetPlanRemind was not enabled")
		}
	}
}

// Quick-task exemption: a single tool call never triggers the reminder even
// when remind is on (threshold is 3).
func TestP4_QuickTaskExempt(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			fmt.Fprint(w, toolTC("c1", "echo", `{}`))
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
	}))
	defer server.Close()

	store := NewPlanStore(12, 2000)
	registry := tool.NewRegistry([]tool.Tool{
		&fakeTool{name: "echo", description: "echo", output: "ok"},
		NewPlanTool(store),
	})
	client := testChatClient(t, server.URL)
	engine := New(client, registry, 10, "", nil, 0)
	engine.SetPlanStore(store)
	engine.SetPlanRemind(true)

	_, messages, err := engine.RunWithMessages(context.Background(), []session.Message{
		{Role: "system", Content: "sys"}, {Role: "user", Content: "one thing"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range messages {
		if strings.Contains(m.Content, "consider creating a plan") {
			t.Error("single-tool-call quick task must not be nagged")
		}
	}
}
