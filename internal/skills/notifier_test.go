package skills

import (
	"testing"
	"time"
)

// callbackNotifier is a test helper that delegates to a callback function.
type callbackNotifier struct {
	fn func(SkillEvent)
}

func (c *callbackNotifier) Notify(event SkillEvent) {
	if c.fn != nil {
		c.fn(event)
	}
}

func TestNoopNotifier(t *testing.T) {
	t.Run("notify does not panic", func(t *testing.T) {
		n := NoopNotifier{}
		n.Notify(SkillEvent{Type: "loaded", SkillName: "test"})
	})

	t.Run("notify with empty event", func(t *testing.T) {
		n := NoopNotifier{}
		n.Notify(SkillEvent{})
	})

	t.Run("notify with full event", func(t *testing.T) {
		n := NoopNotifier{}
		n.Notify(SkillEvent{
			Type:      "saved",
			SkillName: "docker-build",
			Skills:    []string{"docker-build", "go-test"},
			Heuristic: "multi-step",
			Body:      "# Skill\n\n## Overview\n...",
			Timestamp: time.Now().UTC(),
		})
	})
}

func TestNewMultiNotifier(t *testing.T) {
	t.Run("zero notifiers", func(t *testing.T) {
		mn := NewMultiNotifier()
		if mn == nil {
			t.Fatal("NewMultiNotifier() returned nil")
		}
		// Should not panic
		mn.Notify(SkillEvent{Type: "saved", SkillName: "test"})
	})

	t.Run("one notifier", func(t *testing.T) {
		var received []SkillEvent
		n1 := &callbackNotifier{fn: func(e SkillEvent) {
			received = append(received, e)
		}}
		mn := NewMultiNotifier(n1)
		if mn == nil {
			t.Fatal("NewMultiNotifier(n1) returned nil")
		}

		evt := SkillEvent{Type: "deleted", SkillName: "old-skill"}
		mn.Notify(evt)

		if len(received) != 1 {
			t.Fatalf("expected 1 event, got %d", len(received))
		}
		if received[0].Type != "deleted" {
			t.Errorf("expected type 'deleted', got %q", received[0].Type)
		}
		if received[0].SkillName != "old-skill" {
			t.Errorf("expected SkillName 'old-skill', got %q", received[0].SkillName)
		}
	})

	t.Run("multiple notifiers", func(t *testing.T) {
		var received1, received2 []SkillEvent

		n1 := &callbackNotifier{fn: func(e SkillEvent) { received1 = append(received1, e) }}
		n2 := &callbackNotifier{fn: func(e SkillEvent) { received2 = append(received2, e) }}

		mn := NewMultiNotifier(n1, n2)
		if mn == nil {
			t.Fatal("NewMultiNotifier(n1, n2) returned nil")
		}

		evt := SkillEvent{
			Type:      "loaded",
			Skills:    []string{"docker-build", "go-test"},
			Timestamp: time.Now().UTC(),
		}
		mn.Notify(evt)

		if len(received1) != 1 {
			t.Fatalf("notifier 1: expected 1 event, got %d", len(received1))
		}
		if received1[0].Type != "loaded" {
			t.Errorf("notifier 1: expected type 'loaded', got %q", received1[0].Type)
		}
		if len(received1[0].Skills) != 2 {
			t.Errorf("notifier 1: expected 2 skills, got %d", len(received1[0].Skills))
		}

		if len(received2) != 1 {
			t.Fatalf("notifier 2: expected 1 event, got %d", len(received2))
		}
		if received2[0].Type != "loaded" {
			t.Errorf("notifier 2: expected type 'loaded', got %q", received2[0].Type)
		}
	})
}

func TestMultiNotifier_FanOutToAll(t *testing.T) {
	t.Run("all notifiers receive the same event", func(t *testing.T) {
		var events1, events2 []SkillEvent
		var count3 int

		n1 := &callbackNotifier{fn: func(e SkillEvent) { events1 = append(events1, e) }}
		n2 := &callbackNotifier{fn: func(e SkillEvent) { events2 = append(events2, e) }}
		n3 := &callbackNotifier{fn: func(e SkillEvent) { count3++ }}

		mn := NewMultiNotifier(n1, n2, n3)

		evt := SkillEvent{
			Type:      "autoloaded",
			SkillName: "docker-build",
			Timestamp: time.Now().UTC(),
		}
		mn.Notify(evt)

		if len(events1) != 1 || len(events2) != 1 {
			t.Fatalf("each notifier should receive exactly 1 event: got %d, %d", len(events1), len(events2))
		}
		if count3 != 1 {
			t.Errorf("notifier 3: expected 1 call, got %d", count3)
		}

		// Verify all received the same event content
		if events1[0].Type != "autoloaded" || events2[0].Type != "autoloaded" {
			t.Errorf("all notifiers should receive the same event type")
		}
		if events1[0].SkillName != "docker-build" || events2[0].SkillName != "docker-build" {
			t.Errorf("all notifiers should receive the same skill name")
		}
	})

	t.Run("events are independent between calls", func(t *testing.T) {
		var events []SkillEvent
		n1 := &callbackNotifier{fn: func(e SkillEvent) { events = append(events, e) }}
		mn := NewMultiNotifier(n1)

		mn.Notify(SkillEvent{Type: "used", SkillName: "skill-a"})
		mn.Notify(SkillEvent{Type: "saved", SkillName: "skill-b"})

		if len(events) != 2 {
			t.Fatalf("expected 2 events, got %d", len(events))
		}
		if events[0].Type != "used" || events[0].SkillName != "skill-a" {
			t.Errorf("first event mismatch: got %q / %q", events[0].Type, events[0].SkillName)
		}
		if events[1].Type != "saved" || events[1].SkillName != "skill-b" {
			t.Errorf("second event mismatch: got %q / %q", events[1].Type, events[1].SkillName)
		}
	})
}
