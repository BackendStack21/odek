package agent

import (
	"context"
	"syscall"
	"testing"

	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/session"
)

func TestInvocationOwnsPersistenceOutcomeAndTranscriptIdentity(t *testing.T) {
	server := eventsTestServer(t)
	defer server.Close()
	col := &eventList{}
	a, err := New(Config{APIKey: "test", BaseURL: server.URL, Model: "test", NoProjectFile: true, InteractionMode: "off", EventHandler: col.handle, Tools: []Tool{echoTool{}}})
	if err != nil {
		t.Fatal(err)
	}
	a.SetEventSessionID("session")
	a.BeginRun("rest-id", "ui-turn")
	a.BeginRun("ignored", "ignored")
	_, messages, err := a.RunWithMessages(t.Context(), []session.Message{{Role: "user", Content: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range messages {
		if m.Role != "system" && m.TurnID != "ui-turn" {
			t.Fatalf("transcript lost turn identity: %+v", m)
		}
	}
	a.FinishRun(syscall.ENOSPC) // final persistence fails after a successful model result
	a.FinishRun(nil)
	if _, err := a.Run(t.Context(), "next"); err != nil {
		t.Fatal(err)
	}
	_ = a.Close()
	starts, finishes := map[string]int{}, map[string]events.Event{}
	for _, ev := range col.all() {
		if ev.Type == events.TypeRunStarted {
			starts[ev.RunID]++
		}
		if ev.Type == events.TypeRunCompleted || ev.Type == events.TypeRunFailed {
			if _, ok := finishes[ev.RunID]; ok {
				t.Fatalf("duplicate terminal event: %+v", ev)
			}
			finishes[ev.RunID] = ev
		}
	}
	if len(starts) != 2 || starts["rest-id"] != 1 || len(finishes) != 2 {
		t.Fatalf("starts=%v finishes=%v", starts, finishes)
	}
	if ev := finishes["rest-id"]; ev.Type != events.TypeRunFailed || ev.Data["error_class"] != "disk_full" || ev.TurnID != "ui-turn" {
		t.Fatal(ev)
	}
}

func TestInvocationInheritsTurnAndAncestry(t *testing.T) {
	server := eventsTestServer(t)
	defer server.Close()
	col := &eventList{}
	a, err := New(Config{APIKey: "test", BaseURL: server.URL, Model: "test", NoProjectFile: true, InteractionMode: "off", EventHandler: col.handle, Tools: []Tool{echoTool{}}, EventContext: events.Context{SessionID: "session", TurnID: "root-turn", ParentRunID: "parent", RootRunID: "root", TaskID: "task"}})
	if err != nil {
		t.Fatal(err)
	}
	a.BeginRun("child", "ignored-child-turn")
	_, _, err = a.RunWithMessages(t.Context(), []session.Message{{Role: "user", Content: "task", TurnID: "different"}})
	a.FinishRun(err)
	_ = a.Close()
	for _, ev := range col.all() {
		if ev.TurnID != "root-turn" || ev.RunID != "child" || ev.ParentRunID != "parent" || ev.RootRunID != "root" || ev.TaskID != "task" {
			t.Fatal(ev)
		}
	}
}

func TestInvocationPanicFinishesAndAllowsNextRun(t *testing.T) {
	server := eventsTestServer(t)
	defer server.Close()
	col := &eventList{}
	a, err := New(Config{APIKey: "test", BaseURL: server.URL, Model: "test", NoProjectFile: true, InteractionMode: "off", EventHandler: col.handle, Tools: []Tool{echoTool{}}})
	if err != nil {
		t.Fatal(err)
	}
	a.SetMessagesPersistCallback(func([]session.Message) { panic("private panic") })
	func() {
		defer func() {
			if recover() != "private panic" {
				t.Error("panic changed or swallowed")
			}
		}()
		_, _ = a.Run(context.Background(), "test")
	}()
	a.SetMessagesPersistCallback(nil)
	if _, err := a.Run(t.Context(), "next"); err != nil {
		t.Fatal(err)
	}
	_ = a.Close()
	failed, complete := 0, 0
	for _, ev := range col.all() {
		if ev.Type == events.TypeRunFailed {
			failed++
		}
		if ev.Type == events.TypeRunCompleted {
			complete++
		}
	}
	if failed != 1 || complete != 1 {
		t.Fatalf("failed=%d complete=%d", failed, complete)
	}
}

func TestInvocationEarlyFailureDoesNotReusePreviousUsage(t *testing.T) {
	server := eventsTestServer(t)
	defer server.Close()
	col := &eventList{}
	a, err := New(Config{APIKey: "test", BaseURL: server.URL, Model: "test", NoProjectFile: true, InteractionMode: "off", EventHandler: col.handle, Tools: []Tool{echoTool{}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(t.Context(), "first"); err != nil {
		t.Fatal(err)
	}
	a.BeginRun("early", "early-turn")
	a.FinishRun(syscall.ENOSPC)
	_ = a.Close()
	for _, ev := range col.all() {
		if ev.Type == events.TypeRunFailed && ev.RunID == "early" {
			if _, ok := ev.Data["input_tokens"]; ok {
				t.Fatalf("stale usage: %+v", ev)
			}
			return
		}
	}
	t.Fatal("missing early failure")
}
