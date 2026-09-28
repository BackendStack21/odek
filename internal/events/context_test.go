package events

import (
	"reflect"
	"sync"
	"testing"
)

func TestEmitterContextSnapshotsAcrossTurnsAndSessions(t *testing.T) {
	handler, got := collect()
	em := NewEmitter(handler, "local-run")
	t.Cleanup(em.Close)
	em.SetSessionID("first-session")
	em.SetContext(Context{RunID: "ignored", TaskID: "task", ParentTaskID: "parent-task", ParentRunID: "parent-run", ParentTurnID: "parent-turn"})
	em.SetTurnID("first-turn")
	first := em.Context()
	if first.RunID != em.RunID() || first.RootRunID != "local-run" || first.SessionID != "first-session" {
		t.Fatalf("context defaults: %+v", first)
	}
	em.Emit(Event{Type: TypeRunStarted})
	em.SetTurnID("second-turn")
	em.SetSessionID("second-session")
	em.Emit(Event{Type: TypeRunStarted, RunID: em.RunID()})
	em.Close()
	evs := got()
	if len(evs) != 2 {
		t.Fatalf("got %d events", len(evs))
	}
	for i, ev := range evs {
		want := []string{"first", "second"}[i]
		if ev.TurnID != want+"-turn" || ev.SessionID != want+"-session" || ev.RunID != "local-run" || ev.RootRunID != "local-run" || ev.TaskID != "task" || ev.ParentTaskID != "parent-task" || ev.ParentRunID != "parent-run" || ev.ParentTurnID != "parent-turn" {
			t.Fatalf("event context changed after enqueue: %+v", ev)
		}
	}
	if first.TurnID != "first-turn" || first.SessionID != "first-session" {
		t.Fatalf("previous context snapshot changed: %+v", first)
	}
}

func TestEmitterPreservesRelayedChildContext(t *testing.T) {
	handler, got := collect()
	em := NewEmitter(handler, "parent-run")
	t.Cleanup(em.Close)
	em.SetContext(Context{RootRunID: "root", SessionID: "session", TurnID: "parent-turn", TaskID: "parent-task"})
	if c := em.Context(); c.RootRunID != "root" || c.SessionID != "session" {
		t.Fatalf("explicit context lost: %+v", c)
	}
	child := Event{Type: TypeRunStarted, RunID: "child-run", TurnID: "child-turn", RootRunID: "explicit-root", SessionID: "explicit-session", TaskID: "child-task", ParentRunID: "parent-run", ParentTurnID: "parent-turn", ParentTaskID: "parent-task", SourceTaskID: "verified-child"}
	em.Emit(child)
	em.Emit(Event{Type: TypeRunStarted, RunID: "legacy-child"})
	em.Close()
	evs := got()
	if len(evs) != 2 {
		t.Fatalf("got %d events", len(evs))
	}
	child.Schema, child.Timestamp = evs[0].Schema, evs[0].Timestamp
	if !reflect.DeepEqual(evs[0], child) {
		t.Fatalf("child context overwritten: %+v", evs[0])
	}
	legacy := evs[1]
	if legacy.RootRunID != "root" || legacy.SessionID != "session" || legacy.TurnID != "" || legacy.TaskID != "" || legacy.ParentRunID != "" || legacy.ParentTurnID != "" || legacy.ParentTaskID != "" {
		t.Fatalf("parent identity attributed to legacy child: %+v", legacy)
	}
}

func TestEmitterContextConcurrentAccess(t *testing.T) {
	em := NewEmitter(nil, "run")
	t.Cleanup(em.Close)
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Go(func() {
			for i := 0; i < 100; i++ {
				em.SetContext(Context{SessionID: "session", RootRunID: "root"})
				em.SetTurnID("turn")
				em.SetSessionID("session")
				em.Emit(Event{Type: TypeRunStarted})
				if c := em.Context(); c.RunID != em.RunID() || c.SessionID != "session" || c.RootRunID != "root" {
					t.Errorf("inconsistent context: %+v", c)
				}
			}
		})
	}
	wg.Wait()
}
