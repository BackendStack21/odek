package loop

// Bug-hunt v3 residual fix — reserve-at-spawn for SIMULTANEOUS parallel
// sub-agent spawns. Charge-back lands only on completion, so N spawns fired
// in the same instant all snapshotted the full remaining headroom (N× cap).
// ReserveExternalUsage commits headroom at spawn; SettleExternalUsage
// releases the grant and charges the ACTUAL usage on completion.

import (
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/budget"
)

func TestReserveExternalUsage_BoundsParallelSpawns(t *testing.T) {
	e := &Engine{}
	e.budget = budget.NewChecker(budget.Limits{MaxInputTokens: 1000}, time.Now())

	// First spawn: full remaining headroom.
	if got := e.ReserveExternalUsage(600); got != 600 {
		t.Fatalf("first reservation = %d, want 600", got)
	}
	// Second SIMULTANEOUS spawn: only the uncommitted remainder — this is
	// the window the old snapshot-per-spawn behavior left open.
	if got := e.ReserveExternalUsage(600); got != 400 {
		t.Fatalf("second simultaneous reservation = %d, want 400 (clamped to uncommitted headroom)", got)
	}
	// Fully committed: refused.
	if got := e.ReserveExternalUsage(100); got != 0 {
		t.Fatalf("third reservation = %d, want 0 (headroom fully committed)", got)
	}

	// Settle: second child finished using only 100 of its 400 grant.
	e.SettleExternalUsage(400, 100)

	// Freed headroom (grant released, actual charged) is available again.
	if got := e.ReserveExternalUsage(600); got != 300 {
		t.Fatalf("post-settle reservation = %d, want 300 (cap 1000 - charged 100 - reserved 600)", got)
	}

	// Settle the rest: actual clamped to the grant (child cannot exceed
	// the cap it was handed) — charged = 100 + 600 = 700; the 300 grant
	// from the last Reserve is still outstanding.
	e.SettleExternalUsage(600, 99999)
	snap := e.BudgetSnapshot()
	if snap.RemainingInputTokens != 300 {
		t.Fatalf("after clamped settle, remaining = %d, want 300", snap.RemainingInputTokens)
	}
	if e.externalReserved != 300 {
		t.Fatalf("outstanding reservation = %d, want 300", e.externalReserved)
	}

	// The outstanding child finishes too: fully consumed.
	e.SettleExternalUsage(300, 300)
	if e.externalReserved != 0 {
		t.Fatalf("reservations leaked: externalReserved = %d, want 0", e.externalReserved)
	}
	if got := e.BudgetSnapshot().RemainingInputTokens; got != 0 {
		t.Fatalf("remaining = %d, want 0 (cap fully consumed)", got)
	}
}

func TestReserveExternalUsage_UnconfiguredPassThrough(t *testing.T) {
	e := &Engine{} // no budget limits
	if got := e.ReserveExternalUsage(500); got != 500 {
		t.Fatalf("unconfigured cap should grant in full, got %d", got)
	}
	e.SettleExternalUsage(500, 500) // must not panic or corrupt anything
	if e.externalReserved != 0 {
		t.Fatalf("pass-through settle left reservations: %d", e.externalReserved)
	}
}
