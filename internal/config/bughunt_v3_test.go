package config

// Bug-hunt v3 (fix/bughunt-v3) RED test — maintenance retention clamping.
//
// The maintenance section copies day/hour values from config verbatim (unlike
// the limits section, which clamps). An overflow-sized retention value (or a
// negative one) flows into duration math that wraps and produces a FUTURE
// cutoff, which makes the janitor delete every unpinned session on its first
// tick. The resolver must clamp to sane bounds.

import (
	"testing"
)

func TestResolveMaintenance_ClampsRetentionBounds(t *testing.T) {
	huge := int(999999)            // ~2740 years — wraps int64 ns duration math
	neg := int(-5)                 // future cutoff by construction
	tiny := int64(0)               // explicit 0 = disabled / keep forever, preserved
	bigHours := int(99999999)      // hours also overflow at ~2.6e9

	got := resolveMaintenance(&MaintenanceConfig{
		Enabled:              boolPtr(true),
		SessionsMaxAgeDays:   &huge,
		AuditMaxAgeDays:      &neg,
		PlansMaxAgeDays:      &huge,
		ArtifactsMaxAgeHours: &bigHours,
		LogMaxMB:             &tiny,
	})

	if got.SessionsMaxAgeDays < 0 || got.SessionsMaxAgeDays > 36500 {
		t.Fatalf("SessionsMaxAgeDays = %d passed through unclamped (overflow → future cutoff → mass deletion); want within [0, 36500]", got.SessionsMaxAgeDays)
	}
	if got.AuditMaxAgeDays < 0 {
		t.Fatalf("AuditMaxAgeDays = %d negative (future cutoff); want clamped to ≥ 0", got.AuditMaxAgeDays)
	}
	if got.PlansMaxAgeDays < 0 || got.PlansMaxAgeDays > 36500 {
		t.Fatalf("PlansMaxAgeDays = %d passed through unclamped; want within [0, 36500]", got.PlansMaxAgeDays)
	}
	if got.ArtifactsMaxAgeHours < 0 || got.ArtifactsMaxAgeHours > 24*36500 {
		t.Fatalf("ArtifactsMaxAgeHours = %d passed through unclamped; want within [0, %d]", got.ArtifactsMaxAgeHours, 24*36500)
	}
	if got.LogMaxMB != 0 {
		t.Fatalf("explicit LogMaxMB=0 (disable) must be preserved, got %d", got.LogMaxMB)
	}
}

