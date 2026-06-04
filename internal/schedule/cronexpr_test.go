package schedule

import (
	"testing"
	"time"
)

// mustParse fails the test if expr does not parse.
func mustParse(t *testing.T, expr string) *Schedule {
	t.Helper()
	s, err := Parse(expr)
	if err != nil {
		t.Fatalf("Parse(%q): unexpected error: %v", expr, err)
	}
	return s
}

// ── Next: basic stepping ────────────────────────────────────────────────

func TestNext_EveryMinute(t *testing.T) {
	s := mustParse(t, "* * * * *")
	after := time.Date(2026, 6, 4, 10, 30, 15, 0, time.UTC)
	got := s.Next(after)
	want := time.Date(2026, 6, 4, 10, 31, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("Next = %v, want %v", got, want)
	}
}

func TestNext_TopOfNextHour(t *testing.T) {
	s := mustParse(t, "0 * * * *")
	after := time.Date(2026, 6, 4, 10, 30, 0, 0, time.UTC)
	got := s.Next(after)
	want := time.Date(2026, 6, 4, 11, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("Next = %v, want %v", got, want)
	}
}

func TestNext_WeekdayNineAM(t *testing.T) {
	s := mustParse(t, "0 9 * * 1-5")
	// Friday 2026-06-05 10:00 → next is Monday 2026-06-08 09:00.
	after := time.Date(2026, 6, 5, 10, 0, 0, 0, time.UTC)
	got := s.Next(after)
	want := time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("Next = %v (weekday %s), want %v", got, got.Weekday(), want)
	}
}

func TestNext_StepMinutes(t *testing.T) {
	s := mustParse(t, "*/15 * * * *")
	after := time.Date(2026, 6, 4, 10, 7, 0, 0, time.UTC)
	want := []time.Time{
		time.Date(2026, 6, 4, 10, 15, 0, 0, time.UTC),
		time.Date(2026, 6, 4, 10, 30, 0, 0, time.UTC),
		time.Date(2026, 6, 4, 10, 45, 0, 0, time.UTC),
		time.Date(2026, 6, 4, 11, 0, 0, 0, time.UTC),
	}
	for i, w := range want {
		after = s.Next(after)
		if !after.Equal(w) {
			t.Fatalf("step %d: Next = %v, want %v", i, after, w)
		}
	}
}

func TestNext_List(t *testing.T) {
	s := mustParse(t, "0,30 * * * *")
	after := time.Date(2026, 6, 4, 10, 10, 0, 0, time.UTC)
	got := s.Next(after)
	if want := time.Date(2026, 6, 4, 10, 30, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("Next = %v, want %v", got, want)
	}
	got = s.Next(got)
	if want := time.Date(2026, 6, 4, 11, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("Next = %v, want %v", got, want)
	}
}

func TestNext_StrictlyAfter(t *testing.T) {
	// When `after` is exactly a firing instant, Next must return the FOLLOWING
	// one, never the same instant (prevents double-fire).
	s := mustParse(t, "30 9 * * *")
	at := time.Date(2026, 6, 4, 9, 30, 0, 0, time.UTC)
	got := s.Next(at)
	want := time.Date(2026, 6, 5, 9, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("Next = %v, want %v (must be strictly after)", got, want)
	}
}

// ── Vixie dom/dow coupling ──────────────────────────────────────────────

func TestNext_DomDowUnion(t *testing.T) {
	// Both fields restricted → union: 13th OR any Friday.
	s := mustParse(t, "0 0 13 * 5")
	// Start 2026-06-01 (Mon). June 5 is a Friday → first hit.
	after := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	got := s.Next(after)
	want := time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC) // Friday, before the 13th
	if !got.Equal(want) {
		t.Errorf("Next = %v (%s), want %v", got, got.Weekday(), want)
	}
}

func TestNext_DomRestrictedDowStar(t *testing.T) {
	// dow is "*" → intersection: only the 1st and 15th.
	s := mustParse(t, "0 0 1,15 * *")
	after := time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)
	got := s.Next(after)
	if want := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("Next = %v, want %v", got, want)
	}
	got = s.Next(got)
	if want := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("Next = %v, want %v", got, want)
	}
}

// ── Names and macros ────────────────────────────────────────────────────

func TestParse_MonthAndDowNames(t *testing.T) {
	s := mustParse(t, "0 0 * JAN MON")
	// First Monday in Jan 2027 at 00:00. 2027-01-01 is a Friday; first Mon is 4th.
	after := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	got := s.Next(after)
	want := time.Date(2027, 1, 4, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("Next = %v (%s), want %v", got, got.Weekday(), want)
	}
}

func TestParse_Macros(t *testing.T) {
	cases := map[string]string{
		"@yearly":  "0 0 1 1 *",
		"@monthly": "0 0 1 * *",
		"@weekly":  "0 0 * * 0",
		"@daily":   "0 0 * * *",
		"@hourly":  "0 * * * *",
	}
	for macro, equiv := range cases {
		ms := mustParse(t, macro)
		es := mustParse(t, equiv)
		after := time.Date(2026, 6, 4, 12, 34, 0, 0, time.UTC)
		if !ms.Next(after).Equal(es.Next(after)) {
			t.Errorf("%s and %q disagree: %v vs %v", macro, equiv,
				ms.Next(after), es.Next(after))
		}
	}
}

func TestNext_DowSevenIsSunday(t *testing.T) {
	s7 := mustParse(t, "0 0 * * 7")
	s0 := mustParse(t, "0 0 * * 0")
	after := time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)
	if !s7.Next(after).Equal(s0.Next(after)) {
		t.Errorf("dow 7 != dow 0: %v vs %v", s7.Next(after), s0.Next(after))
	}
	if got := s7.Next(after); got.Weekday() != time.Sunday {
		t.Errorf("Next weekday = %s, want Sunday", got.Weekday())
	}
}

// ── Leap day ────────────────────────────────────────────────────────────

func TestNext_LeapDay(t *testing.T) {
	s := mustParse(t, "0 0 29 2 *")
	// 2026 and 2027 are not leap years; next Feb 29 is 2028.
	after := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	got := s.Next(after)
	want := time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("Next = %v, want %v", got, want)
	}
}

func TestNext_ImpossibleReturnsZero(t *testing.T) {
	// Feb 30 never exists → Next exhausts the horizon and returns zero.
	s := mustParse(t, "0 0 30 2 *")
	got := s.Next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if !got.IsZero() {
		t.Errorf("Next = %v, want zero time", got)
	}
}

// ── Timezone ────────────────────────────────────────────────────────────

func TestNext_InLocation(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skipf("tz data unavailable: %v", err)
	}
	s, err := ParseInLocation("0 9 * * *", berlin)
	if err != nil {
		t.Fatalf("ParseInLocation: %v", err)
	}
	// 08:00 UTC in summer (CEST = UTC+2) is 10:00 Berlin → already past 09:00,
	// so next fire is 09:00 Berlin tomorrow = 07:00 UTC next day.
	after := time.Date(2026, 6, 4, 8, 0, 0, 0, time.UTC)
	got := s.Next(after)
	wantBerlin := time.Date(2026, 6, 5, 9, 0, 0, 0, berlin)
	if !got.Equal(wantBerlin) {
		t.Errorf("Next = %v, want %v", got.In(berlin), wantBerlin)
	}
}

// ── Matches ─────────────────────────────────────────────────────────────

func TestMatches(t *testing.T) {
	s := mustParse(t, "30 9 * * 1-5")
	tests := []struct {
		t    time.Time
		want bool
	}{
		{time.Date(2026, 6, 4, 9, 30, 0, 0, time.UTC), true},   // Thu 09:30
		{time.Date(2026, 6, 4, 9, 31, 0, 0, time.UTC), false},  // wrong minute
		{time.Date(2026, 6, 4, 10, 30, 0, 0, time.UTC), false}, // wrong hour
		{time.Date(2026, 6, 6, 9, 30, 0, 0, time.UTC), false},  // Saturday
	}
	for _, tc := range tests {
		if got := s.Matches(tc.t); got != tc.want {
			t.Errorf("Matches(%v) = %v, want %v", tc.t, got, tc.want)
		}
	}
}

// ── Parse errors ────────────────────────────────────────────────────────

func TestParse_Errors(t *testing.T) {
	bad := []string{
		"",            // empty
		"* * * *",     // 4 fields
		"* * * * * *", // 6 fields
		"60 * * * *",  // minute out of range
		"* 24 * * *",  // hour out of range
		"* * 0 * *",   // dom below range
		"* * 32 * *",  // dom above range
		"* * * 13 *",  // month out of range
		"* * * * 8",   // dow above range (7 max)
		"*/0 * * * *", // zero step
		"5-1 * * * *", // inverted range
		"a * * * *",   // non-numeric
		"* * * FOO *", // bad month name
		"@every 5m",   // unsupported macro
		"@reboot",     // explicitly rejected
		"@bogus",      // unknown macro
	}
	for _, expr := range bad {
		if _, err := Parse(expr); err == nil {
			t.Errorf("Parse(%q): expected error, got nil", expr)
		}
	}
}

func TestParse_StepFromValue(t *testing.T) {
	// "a/n" means from a to max stepping n: 5,25,45 for minutes.
	s := mustParse(t, "5/20 * * * *")
	after := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	for _, w := range []int{5, 25, 45} {
		after = s.Next(after)
		if after.Minute() != w {
			t.Fatalf("Next minute = %d, want %d", after.Minute(), w)
		}
	}
	// After 45 the next is 05 of the following hour.
	after = s.Next(after)
	if after.Minute() != 5 || after.Hour() != 11 {
		t.Errorf("Next = %02d:%02d, want 11:05", after.Hour(), after.Minute())
	}
}

func TestString(t *testing.T) {
	s := mustParse(t, "0 9 * * 1-5")
	if s.String() != "0 9 * * 1-5" {
		t.Errorf("String = %q", s.String())
	}
}
