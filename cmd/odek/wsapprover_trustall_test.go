package main

import (
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
)

// trustAll must not auto-approve classes that never allow trust shortcuts
// (destructive, blocked, unknown, ...). A benign batch approval that grants
// SetTrustAll must still surface a per-call prompt for a destructive command.
func TestWSApprover_TrustAllGatedByTrustShortcutAllowed(t *testing.T) {
	a := newWSApprover(func(v any) error { return nil })
	a.SetTrustAll(true)

	prompted := make(chan struct{}, 1)
	go func() {
		// Wait for the prompt to be registered, then answer it.
		deadline := time.After(3 * time.Second)
		for {
			a.mu.Lock()
			var id string
			for k := range a.pending {
				id = k
			}
			a.mu.Unlock()
			if id != "" {
				a.HandleResponse(id, "approve")
				close(prompted)
				return
			}
			select {
			case <-deadline:
				return
			default:
				time.Sleep(5 * time.Millisecond)
			}
		}
	}()

	err := a.PromptCommand(danger.Destructive, "rm -rf /tmp/x", "test")
	if err != nil {
		t.Fatalf("PromptCommand returned error: %v", err)
	}

	select {
	case <-prompted:
		// Good: a real prompt was shown and answered.
	default:
		t.Fatal("destructive command was auto-approved by trustAll without prompting")
	}

	// And the destructive class must not have been silently trusted.
	a.mu.Lock()
	_, trusted := a.approveAll[danger.Destructive]
	a.mu.Unlock()
	if trusted {
		t.Error("destructive class must never be cached as approved")
	}
}
