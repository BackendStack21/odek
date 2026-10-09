package schedule

import (
	"testing"
	"time"
)

// Compare coarse calendar jumps with an independent minute walk around clock
// changes, including zones with a half-hour transition or a skipped midnight.
// Only wildcard expressions are compared: fixed time-of-day jobs deliberately
// follow cron wall-clock semantics instead (see TestNextFixedTimeAcrossClockChanges).
func TestNextMatchesMinuteWalkAcrossClockChanges(t *testing.T) {
	cases := []struct {
		zone, start string
	}{
		{"America/New_York", "2026-11-01T04:00:00Z"},
		{"Europe/Berlin", "2026-10-25T00:00:00Z"},
		{"Australia/Lord_Howe", "2026-04-04T14:00:00Z"},
		{"Australia/Lord_Howe", "2026-10-03T14:00:00Z"},
		{"America/Santiago", "2026-09-06T02:00:00Z"},
	}
	for _, tc := range cases {
		t.Run(tc.zone+"/"+tc.start, func(t *testing.T) {
			loc, err := time.LoadLocation(tc.zone)
			if err != nil {
				t.Fatal(err)
			}
			start, err := time.Parse(time.RFC3339, tc.start)
			if err != nil {
				t.Fatal(err)
			}
			for _, expression := range []string{"* * * * *", "5/1 * * * *", "0 * * * *", "*/30 * * * *"} {
				s, err := ParseInLocation(expression, loc)
				if err != nil {
					t.Fatal(err)
				}
				for offset := time.Duration(0); offset < 4*time.Hour; offset += 7 * time.Minute {
					after := start.Add(offset + 17*time.Second)
					want := after.Truncate(time.Minute).Add(time.Minute)
					limit := want.Add(48 * time.Hour)
					for !s.Matches(want) && want.Before(limit) {
						want = want.Add(time.Minute)
					}
					if !want.Before(limit) {
						t.Fatal("oracle horizon exhausted")
					}
					if got := s.Next(after); !got.Equal(want) {
						t.Fatalf("%q after %v: got %v; want %v", expression, after.In(loc), got, want.In(loc))
					}
				}
			}
		})
	}
}

// Fixed time-of-day jobs follow Vixie/cronie semantics: a repeated wall time
// fires once, a nonexistent wall time fires right after the gap.
func TestNextFixedTimeAcrossClockChanges(t *testing.T) {
	cases := []struct {
		name, zone, expr string
		after, want      time.Time
	}{
		{"berlin repeated hour once", "Europe/Berlin", "0 2 * * *",
			time.Date(2026, 10, 24, 22, 0, 0, 0, time.UTC), time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)},
		{"berlin after first occurrence goes to next day", "Europe/Berlin", "0 2 * * *",
			time.Date(2026, 10, 25, 0, 0, 17, 0, time.UTC), time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC)},
		{"new york gap runs after the gap", "America/New_York", "30 2 * * *",
			time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC), time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC)},
		{"two gap times collapse to one fire", "America/New_York", "0,30 2 * * *",
			time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC), time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC)},
		{"after the gap fire goes to next day", "America/New_York", "30 2 * * *",
			time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC), time.Date(2026, 3, 9, 6, 30, 0, 0, time.UTC)},
		{"lord howe half hour gap", "Australia/Lord_Howe", "0 2 * * *",
			time.Date(2026, 10, 3, 14, 0, 0, 0, time.UTC), time.Date(2026, 10, 3, 15, 30, 0, 0, time.UTC)},
		{"santiago skipped midnight", "America/Santiago", "0 0 * * *",
			time.Date(2026, 9, 5, 22, 0, 0, 0, time.UTC), time.Date(2026, 9, 6, 4, 0, 0, 0, time.UTC)},
		{"plain day unaffected", "America/New_York", "30 9 * * *",
			time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 6, 1, 13, 30, 0, 0, time.UTC)},
		{"month and weekday filters", "America/New_York", "0 3 * 3 0",
			time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loc, err := time.LoadLocation(tc.zone)
			if err != nil {
				t.Skipf("tzdata unavailable: %v", err)
			}
			s, err := ParseInLocation(tc.expr, loc)
			if err != nil {
				t.Fatal(err)
			}
			if got := s.Next(tc.after); !got.Equal(tc.want) {
				t.Fatalf("Next(%v) = %v, want %v", tc.after, got, tc.want)
			}
		})
	}
}

func TestNextFixedTimeUnreachableReturnsZero(t *testing.T) {
	s, err := Parse("0 0 30 2 *")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); !got.IsZero() {
		t.Fatalf("Feb 30 should never fire, got %v", got)
	}
}

// In a zone without clock changes both the fixed-time and the wildcard paths
// must agree with an independent minute walk over Matches.
func TestNextMatchesMinuteWalkInUTC(t *testing.T) {
	exprs := []string{
		"*/30 * * 6 *", "*/15 * 5 6 *", "0 */6 * * 1", "10 */4 * 2 *", "* 3 * * *",
		"30 9 * * *", "0,30 8-10 * * 1-5", "5 4 29 2 *", "0 0 1 1 *", "45 23 31 * *", "0 12 * * 0",
	}
	start := time.Date(2027, 12, 20, 13, 41, 17, 0, time.UTC)
	for _, expr := range exprs {
		s, err := Parse(expr)
		if err != nil {
			t.Fatal(err)
		}
		for step := 0; step < 4; step++ {
			after := start.Add(time.Duration(step) * 1900 * time.Hour)
			want := after.Truncate(time.Minute).Add(time.Minute)
			limit := want.Add(5 * 366 * 24 * time.Hour)
			for !s.Matches(want) && want.Before(limit) {
				want = want.Add(time.Minute)
			}
			if got := s.Next(after); !got.Equal(want) {
				t.Fatalf("%q after %v: got %v want %v", expr, after, got, want)
			}
		}
	}
}

// A repeated wall time on a half-hour DST zone still fires once.
func TestNextFixedTimeHalfHourFallBack(t *testing.T) {
	loc, err := time.LoadLocation("Australia/Lord_Howe")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	s, err := ParseInLocation("45 1 * * *", loc)
	if err != nil {
		t.Fatal(err)
	}
	// 2026-04-05 02:00 LHDT falls back to 01:30 LHST, so 01:45 occurs twice.
	first := s.Next(time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC))
	second := s.Next(first)
	if second.Sub(first) < 12*time.Hour {
		t.Fatalf("fired at %v and again at %v", first, second)
	}
}
