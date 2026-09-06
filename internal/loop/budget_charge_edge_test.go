package loop

import "testing"

func TestSettleExternalUsageEdgeBranches(t *testing.T) {
	t.Run("negative granted clamps to zero", func(t *testing.T) {
		e := &Engine{}
		// granted<0 clamps to 0; actual(50) then clamps down to the grant (0) — nothing charged.
		e.SettleExternalUsage(-100, 50)
		if e.TotalInputTokens != 0 {
			t.Errorf("TotalInputTokens = %d, want 0 (actual clamped to grant 0)", e.TotalInputTokens)
		}
		if e.externalReserved != 0 {
			t.Errorf("externalReserved = %d, want 0", e.externalReserved)
		}
	})

	t.Run("negative actual clamps to zero", func(t *testing.T) {
		e := &Engine{}
		e.SettleExternalUsage(50, -10)
		if e.TotalInputTokens != 0 {
			t.Errorf("TotalInputTokens = %d, want 0", e.TotalInputTokens)
		}
		if e.externalReserved != 0 {
			t.Errorf("externalReserved = %d, want 0 (grant released)", e.externalReserved)
		}
	})

	t.Run("actual above granted clamps to grant", func(t *testing.T) {
		e := &Engine{}
		got := e.ReserveExternalUsage(100)
		if got != 100 {
			t.Fatalf("ReserveExternalUsage = %d, want 100 (unconfigured pass-through)", got)
		}
		e.SettleExternalUsage(100, 5000)
		if e.TotalInputTokens != 100 {
			t.Errorf("TotalInputTokens = %d, want 100 (clamped to grant)", e.TotalInputTokens)
		}
		if e.externalReserved != 0 {
			t.Errorf("externalReserved = %d, want 0 after settle", e.externalReserved)
		}
	})
}

func TestReserveExternalUsageEdgeBranches(t *testing.T) {
	e := &Engine{}
	if got := e.ReserveExternalUsage(0); got != 0 {
		t.Errorf("ReserveExternalUsage(0) = %d, want 0", got)
	}
	if got := e.ReserveExternalUsage(-5); got != 0 {
		t.Errorf("ReserveExternalUsage(-5) = %d, want 0", got)
	}
}

func TestExternalUsageNilEngine(t *testing.T) {
	var e *Engine
	e.SettleExternalUsage(10, 5) // must not panic
	if got := e.ReserveExternalUsage(10); got != 0 {
		t.Errorf("nil ReserveExternalUsage = %d, want 0", got)
	}
}
