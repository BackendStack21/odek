package schedule

// Bug-sweep batch 2 — DST fall-back regression test.
//
// RED-first: Next() jumped hours via time.Date(...).Add(time.Hour). Across
// a DST fall-back, time.Date resolves an ambiguous wall time to its FIRST
// occurrence, so when the repeated wall hour is not in the hour mask the
// hop can stop advancing entirely — an infinite loop that wedges the
// scheduler (and `schedule add/list/next`, which validate through Next).

import (
	"testing"
	"time"

	// Hermetic zone database: the test must not depend on the host's
	// tzdata installation.
	_ "time/tzdata"
)

func TestNext_DstFallBackRepeatedHourNotInMask(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	s, err := ParseInLocation("0 3 * * *", loc)
	if err != nil {
		t.Fatalf("ParseInLocation: %v", err)
	}

	// 2026-11-01: 02:00 EDT falls back to 01:00 EST — wall hour 01 occurs
	// twice. Starting before the transition with hour 01 ∉ {03}: the scan
	// must cross the repeated hour and land on 03:00 EST.
	after := time.Date(2026, 11, 1, 0, 30, 0, 0, loc)
	want := time.Date(2026, 11, 1, 3, 0, 0, 0, loc)

	type result struct {
		t time.Time
	}
	ch := make(chan result, 1)
	go func() { ch <- result{s.Next(after)} }()

	select {
	case r := <-ch:
		if r.t.IsZero() {
			t.Fatal("Next returned zero time (no match within horizon)")
		}
		if !r.t.Equal(want) {
			t.Fatalf("Next = %v, want %v (first firing strictly after %v)", r.t, want, after)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Next did not return within 5s — infinite loop across DST fall-back " +
			"(time.Date pins the ambiguous wall time to its first occurrence, so the " +
			"+1h hour-jump stops advancing)")
	}
}
