package telegram

import (
	"testing"
)

// A run whose billing pushes the day's usage past daily_token_budget must
// leave the budget exhausted. CheckDailyBudget rejects the overshooting
// amount without recording it, so usage stays below the limit and every
// following pre-flight (CheckDailyBudget(1) / DailyTokenUsage) keeps letting
// runs through: the budget is never enforced once a run overshoots it.
func TestRED_DailyBudgetOvershootIsNeverRecorded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	b := &Bot{DailyTokenBudget: 10000, log: NewNopLogger()}

	if err := b.CheckDailyBudget(9000); err != nil {
		t.Fatalf("first run billing: %v", err)
	}
	// Second run actually consumed 3000 tokens (already spent at the provider).
	if err := b.CheckDailyBudget(3000); err == nil {
		t.Fatal("overshooting billing must still report the overshoot")
	}

	// The read-only pre-flight must see the real spend.
	if used, limit := b.DailyTokenUsage(); used != 12000 || limit != 10000 {
		t.Fatalf("DailyTokenUsage = %d/%d, want 12000/10000 (actual spend recorded)", used, limit)
	}
	// The day's real spend is 12000 > 10000: the next run must be refused.
	if err := b.CheckDailyBudget(1); err == nil {
		used, limit := b.DailyTokenUsage()
		t.Fatalf("pre-flight after overshoot passed: recorded usage %d/%d, real spend 12000 — budget never blocks further runs", used, limit)
	}
}
