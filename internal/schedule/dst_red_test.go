package schedule

import (
	"testing"
	"time"
)

// A fixed-time daily job must fire once on a DST fall-back day, not once per
// occurrence of the repeated wall-clock hour.
func TestRED_FixedDailyFiresTwiceOnFallBack(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	s, err := ParseInLocation("30 1 * * *", loc)
	if err != nil {
		t.Fatal(err)
	}
	first := s.Next(time.Date(2026, 11, 1, 0, 0, 0, 0, loc))
	second := s.Next(first)
	if second.Sub(first) < 12*time.Hour {
		t.Fatalf("daily job fired at %v and again at %v on the same civil day", first, second)
	}
}

// A fixed-time daily job whose wall time falls in a spring-forward gap must
// still run that day (cron implementations run it right after the gap), not
// be skipped for a whole day.
func TestRED_FixedDailySkippedOnSpringForward(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	s, err := ParseInLocation("30 2 * * *", loc)
	if err != nil {
		t.Fatal(err)
	}
	got := s.Next(time.Date(2026, 3, 8, 0, 0, 0, 0, loc))
	if got.In(loc).Day() != 8 {
		t.Fatalf("daily 02:30 job on 2026-03-08 (gap day) next fires %v; the day's run was skipped", got)
	}
}
