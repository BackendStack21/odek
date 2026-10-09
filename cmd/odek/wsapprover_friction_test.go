package main

import (
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
)

// A class-wide grant cached before friction engaged must not short-circuit a
// prompt that arrives while friction is engaged.
func TestWSApprover_FrictionOverridesCachedClassTrust(t *testing.T) {
	prompted := make(chan approvalRequest, 1)
	var a *wsApprover
	a = newWSApprover(func(v any) error {
		if req, ok := v.(approvalRequest); ok {
			prompted <- req
			go a.HandleResponse(req.ID, "deny")
		}
		return nil
	})
	a.approveAll[danger.SystemWrite] = true
	for i := 0; i < 3; i++ {
		a.recordApproval(danger.SystemWrite)
	}
	if err := a.PromptCommand(danger.SystemWrite, "touch /etc/x", ""); err == nil {
		t.Fatal("expected denial from the interactive prompt, got auto-approve")
	}
	select {
	case req := <-prompted:
		if !req.Friction || req.AllowTrust {
			t.Fatalf("friction prompt must hide trust: %+v", req)
		}
	default:
		t.Fatal("no prompt was sent while friction was engaged")
	}
}
