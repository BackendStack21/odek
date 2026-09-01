package main

// Bug-sweep batch 2 — B7 cutoff-parity regression test.
//
// The real sweep computes day-based cutoffs with duration arithmetic
// (maintenance daysAgo: now - N*24h), explicitly to avoid DST-sensitive
// calendar arithmetic. The dry-run preview used time.AddDate, so after a
// DST transition the previewed deletion set diverged from the sweep's by
// up to an hour of files. The helper is now exported and shared, making
// the divergence impossible by construction.
//
// RED observation: this file failed to compile before the fix —
// maintenance.DaysAgo did not exist (capability-absent RED, same class as
// the dry-run artifacts test in batch 1).

import (
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/maintenance"
)

func TestDaysAgo_MatchesSweepDurationMath(t *testing.T) {
	now := time.Date(2026, 9, 1, 6, 0, 0, 0, time.UTC)
	want := now.Add(-3 * 24 * time.Hour)
	if got := maintenance.DaysAgo(now, 3); !got.Equal(want) {
		t.Fatalf("DaysAgo(now, 3) = %v, want %v (pure duration arithmetic)", got, want)
	}
}
