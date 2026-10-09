package main

import (
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
)

// In friction mode the UI hides the trust shortcut and the TTY approver only
// accepts the literal word "approve". The WS approver must not honor a forged
// "trust" action then: it would cache a class-wide grant that bypasses friction
// for every later prompt.
func TestRED_WSApprover_FrictionModeRejectsTrust(t *testing.T) {
	var a *wsApprover
	a = newWSApprover(func(v any) error {
		if req, ok := v.(approvalRequest); ok {
			go func() {
				time.Sleep(10 * time.Millisecond)
				a.HandleResponse(req.ID, "trust")
			}()
		}
		return nil
	})
	for i := 0; i < 3; i++ {
		a.recordApproval(danger.SystemWrite)
	}
	if f, _ := a.shouldFriction(danger.SystemWrite); !f {
		t.Fatal("precondition: friction should be engaged")
	}
	if err := a.PromptCommand(danger.SystemWrite, "touch /etc/x", ""); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got := a.TrustClasses(); len(got) != 0 {
		t.Fatalf("trust granted while in friction mode: %v", got)
	}
}
