package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed cron expression bound to a timezone. It answers one
// question — "given an instant, when does this next fire?" — via Next.
//
// Supported syntax is standard 5-field Vixie cron:
//
//	┌ minute        0-59
//	│ ┌ hour        0-23
//	│ │ ┌ dom        1-31
//	│ │ │ ┌ month    1-12 or JAN-DEC
//	│ │ │ │ ┌ dow    0-6 or SUN-SAT (0 and 7 both mean Sunday)
//	* * * * *
//
// Each field accepts: a wildcard "*", a single value, a range "a-b", a step
// "*/n" / "a-b/n" / "a/n" (from a to the field max), and comma-separated lists
// of any of those. Month and day-of-week also accept three-letter names.
//
// Macros: @yearly (@annually), @monthly, @weekly, @daily (@midnight), @hourly.
//
// Day-of-month / day-of-week coupling follows Vixie semantics: when BOTH
// fields are restricted (neither is "*"), a day matches if EITHER field
// matches (union). When at least one is a wildcard, the usual intersection
// applies. This is why "0 0 13 * 5" fires on the 13th OR any Friday, not only
// Friday-the-13th.
type Schedule struct {
	minute uint64 // bitset over 0..59
	hour   uint64 // bitset over 0..23
	dom    uint64 // bitset over 1..31
	month  uint64 // bitset over 1..12
	dow    uint64 // bitset over 0..6 (Sunday=0)

	domStar bool // dom field was a wildcard ("*" or "*/n")
	dowStar bool // dow field was a wildcard

	loc  *time.Location
	expr string // original expression, for String()
}

// matchHorizon bounds Next's search. The rarest legitimate expression is
// Feb 29, whose gap can reach 8 years across a non-leap century boundary
// (e.g. 2096 → 2104, since 2100 is not a leap year); 9 years clears it with
// margin. Stepping past the horizon means the expression matches nothing
// reachable (e.g. "0 0 30 2 *", Feb 30) and Next returns the zero time. Even
// then the coarse unit-stepping gives up in a few thousand iterations.
const matchHorizon = 9 * 366 * 24 * time.Hour

var monthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var dowNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

// Parse compiles a cron expression in UTC.
func Parse(expr string) (*Schedule, error) {
	return ParseInLocation(expr, time.UTC)
}

// ParseInLocation compiles a cron expression bound to loc. A nil loc defaults
// to UTC. The location only affects Next/Matches, not parsing.
func ParseInLocation(expr string, loc *time.Location) (*Schedule, error) {
	if loc == nil {
		loc = time.UTC
	}
	raw := strings.TrimSpace(expr)
	if raw == "" {
		return nil, fmt.Errorf("cron: empty expression")
	}

	if strings.HasPrefix(raw, "@") {
		expanded, err := expandMacro(raw)
		if err != nil {
			return nil, err
		}
		raw = expanded
	}

	fields := strings.Fields(raw)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron: expected 5 fields, got %d in %q", len(fields), expr)
	}

	s := &Schedule{loc: loc, expr: strings.TrimSpace(expr)}
	var err error

	if s.minute, _, err = parseField(fields[0], 0, 59, nil); err != nil {
		return nil, fmt.Errorf("cron: minute: %w", err)
	}
	if s.hour, _, err = parseField(fields[1], 0, 23, nil); err != nil {
		return nil, fmt.Errorf("cron: hour: %w", err)
	}
	if s.dom, s.domStar, err = parseField(fields[2], 1, 31, nil); err != nil {
		return nil, fmt.Errorf("cron: day-of-month: %w", err)
	}
	if s.month, _, err = parseField(fields[3], 1, 12, monthNames); err != nil {
		return nil, fmt.Errorf("cron: month: %w", err)
	}
	// Day-of-week accepts 0..7; 7 is a second spelling of Sunday (0).
	dowMask, dowStar, err := parseField(fields[4], 0, 7, dowNames)
	if err != nil {
		return nil, fmt.Errorf("cron: day-of-week: %w", err)
	}
	if dowMask&(1<<7) != 0 {
		dowMask |= 1 << 0 // fold 7 → 0
		dowMask &^= 1 << 7
	}
	s.dow, s.dowStar = dowMask, dowStar

	return s, nil
}

// expandMacro rewrites a @macro into its 5-field equivalent.
func expandMacro(m string) (string, error) {
	switch strings.ToLower(m) {
	case "@yearly", "@annually":
		return "0 0 1 1 *", nil
	case "@monthly":
		return "0 0 1 * *", nil
	case "@weekly":
		return "0 0 * * 0", nil
	case "@daily", "@midnight":
		return "0 0 * * *", nil
	case "@hourly":
		return "0 * * * *", nil
	case "@reboot":
		// @reboot has no meaning for a persistent scheduler — the catchup
		// flag covers "run if a fire was missed while down". Reject it
		// explicitly rather than silently never firing.
		return "", fmt.Errorf("cron: @reboot is not supported (use the job's catchup option)")
	default:
		return "", fmt.Errorf("cron: unknown macro %q", m)
	}
}

// parseField parses one cron field into a bitset over [min,max]. star reports
// whether the field began with "*" (a wildcard), which the caller needs for
// the dom/dow union rule. names, if non-nil, maps lowercased symbolic names
// (e.g. "mon") to values.
func parseField(field string, min, max int, names map[string]int) (mask uint64, star bool, err error) {
	if field == "" {
		return 0, false, fmt.Errorf("empty field")
	}
	star = strings.HasPrefix(field, "*")
	for item := range strings.SplitSeq(field, ",") {
		m, err := parseItem(item, min, max, names)
		if err != nil {
			return 0, false, err
		}
		mask |= m
	}
	return mask, star, nil
}

// parseItem parses a single comma-separated element: "*", "*/n", "a", "a-b",
// "a-b/n", or "a/n".
func parseItem(item string, min, max int, names map[string]int) (uint64, error) {
	rng := item
	step := 1
	if before, stepStr, found := strings.Cut(item, "/"); found {
		rng = before
		n, err := strconv.Atoi(stepStr)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid step %q in %q", stepStr, item)
		}
		step = n
	}

	var lo, hi int
	switch {
	case rng == "*":
		lo, hi = min, max
	case strings.ContainsRune(rng, '-'):
		parts := strings.SplitN(rng, "-", 2)
		var err error
		if lo, err = parseValue(parts[0], names); err != nil {
			return 0, err
		}
		if hi, err = parseValue(parts[1], names); err != nil {
			return 0, err
		}
	default:
		v, err := parseValue(rng, names)
		if err != nil {
			return 0, err
		}
		lo = v
		// "a/n" means "from a to the maximum, stepping n"; a bare "a" is just a.
		if step > 1 {
			hi = max
		} else {
			hi = v
		}
	}

	if lo < min || hi > max || lo > hi {
		return 0, fmt.Errorf("value out of range [%d,%d] in %q", min, max, item)
	}

	var mask uint64
	for v := lo; v <= hi; v += step {
		mask |= 1 << uint(v)
	}
	return mask, nil
}

// parseValue resolves a single token to an int, accepting symbolic names when
// names is non-nil.
func parseValue(tok string, names map[string]int) (int, error) {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return 0, fmt.Errorf("empty value")
	}
	if names != nil {
		if v, ok := names[strings.ToLower(tok)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(tok)
	if err != nil {
		return 0, fmt.Errorf("invalid value %q", tok)
	}
	return v, nil
}

// Matches reports whether t (in the schedule's location, to the minute) is a
// firing time.
func (s *Schedule) Matches(t time.Time) bool {
	t = t.In(s.loc)
	if s.minute&(1<<uint(t.Minute())) == 0 {
		return false
	}
	if s.hour&(1<<uint(t.Hour())) == 0 {
		return false
	}
	if s.month&(1<<uint(int(t.Month()))) == 0 {
		return false
	}
	return s.dayMatches(t)
}

// dayMatches applies the Vixie dom/dow coupling rule.
func (s *Schedule) dayMatches(t time.Time) bool {
	domMatch := s.dom&(1<<uint(t.Day())) != 0
	dowMatch := s.dow&(1<<uint(int(t.Weekday()))) != 0
	if s.domStar || s.dowStar {
		return domMatch && dowMatch
	}
	return domMatch || dowMatch
}

// Next returns the first firing time strictly after the given instant, or the
// zero time if none occurs within the search horizon. The result is in the
// schedule's location and has zero seconds/nanoseconds.
//
// It advances by the coarsest non-matching unit (month → day → hour → minute)
// so even rare expressions converge in a handful of iterations rather than
// stepping minute-by-minute across years.
func (s *Schedule) Next(after time.Time) time.Time {
	after = after.In(s.loc)
	limit := after.Add(matchHorizon)

	// Start at the top of the next minute, seconds/nanoseconds zeroed. Rebuild
	// via time.Date rather than Truncate so non-whole-minute historical zone
	// offsets can't misalign the boundary.
	y, mo, d := after.Date()
	h, mi, _ := after.Clock()
	t := time.Date(y, mo, d, h, mi, 0, 0, s.loc).Add(time.Minute)

	for t.Before(limit) {
		if s.month&(1<<uint(int(t.Month()))) == 0 {
			// Jump to the first day of next month at 00:00.
			t = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, s.loc).AddDate(0, 1, 0)
			continue
		}
		if !s.dayMatches(t) {
			// Jump to 00:00 of the next day.
			t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, s.loc).AddDate(0, 0, 1)
			continue
		}
		if s.hour&(1<<uint(t.Hour())) == 0 {
			// Jump to the top of the next hour.
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, s.loc).Add(time.Hour)
			continue
		}
		if s.minute&(1<<uint(t.Minute())) == 0 {
			t = t.Add(time.Minute) // seconds already zero
			continue
		}
		return t
	}
	return time.Time{}
}

// String returns the original expression the schedule was parsed from.
func (s *Schedule) String() string { return s.expr }
