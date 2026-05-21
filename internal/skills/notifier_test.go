package skills

import (
	"testing"
	"time"
)

func TestNoopNotifier(t *testing.T) {
	n := NoopNotifier{}
	// Should not panic or error
	n.Notify(SkillEvent{Type: "loaded", SkillName: "test"})
}

func TestMultiNotifier_FanOut(t *testing.T) {
	var received1, received2 []SkillEvent

	n1 := &callbackNotifier{fn: func(e SkillEvent) { received1 = append(received1, e) }}
	n2 := &callbackNotifier{fn: func(e SkillEvent) { received2 = append(received2, e) }}

	mn := NewMultiNotifier(n1, n2)

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
}

func TestMultiNotifier_Empty(t *testing.T) {
	mn := NewMultiNotifier()
	// Should not panic
	mn.Notify(SkillEvent{Type: "saved", SkillName: "test"})
}

// callbackNotifier is a test helper that delegates to a callback function.
type callbackNotifier struct {
	fn func(SkillEvent)
}

func (c *callbackNotifier) Notify(event SkillEvent) {
	if c.fn != nil {
		c.fn(event)
	}
}
