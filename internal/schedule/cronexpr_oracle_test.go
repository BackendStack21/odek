package schedule

import (
	"testing"
	"time"
)

// Compare coarse calendar jumps with an independent minute walk around clock
// changes, including zones with a half-hour transition or a skipped midnight.
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
			for _, expression := range []string{"* * * * *", "5/1 * * * *", "0 0 * * *", "0 2 * * *", "15 1,2,3 * * *"} {
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
