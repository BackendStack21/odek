package redact

import (
	"math/rand"
	"regexp"
	"strings"
	"testing"
	"time"
)

// redhunt1109Words is ordinary English with no credential word, header name
// or command that any redaction rule keys on.
var redhunt1109Words = strings.Fields(`the of and to in is was for on that with as by at from it be this are or an
have not which has but were their all one more about also when there into some time new other than can over only
after first two most such people years used work made could many before during like through state then these both
where being well since three world may between under while however because each part found number would include
several those history system known later following different early called including city small long government
river north south music field during record series team album season school family members second national public`)

// redhunt1109Prose returns n bytes of deterministic secret-free prose with
// sentence punctuation, numbers and paragraph breaks.
func redhunt1109Prose(n int) string {
	r := rand.New(rand.NewSource(1109))
	var b strings.Builder
	for b.Len() < n {
		k := 8 + r.Intn(14)
		for i := 0; i < k; i++ {
			w := redhunt1109Words[r.Intn(len(redhunt1109Words))]
			if i == 0 {
				w = strings.ToUpper(w[:1]) + w[1:]
			}
			b.WriteString(w)
			switch {
			case i == k-1:
			case r.Intn(15) == 0:
				b.WriteString(", ")
			case r.Intn(25) == 0:
				b.WriteString(" 1987 ")
			default:
				b.WriteString(" ")
			}
		}
		b.WriteString(". ")
		if r.Intn(6) == 0 {
			b.WriteString("\n\n")
		}
	}
	return b.String()[:n]
}

// bestOf returns the shortest of runs timings of f.
func bestOf(runs int, f func()) time.Duration {
	best := time.Duration(1<<63 - 1)
	for i := 0; i < runs; i++ {
		t0 := time.Now()
		f()
		if d := time.Since(t0); d < best {
			best = d
		}
	}
	return best
}

// Every tool result, session save and runtime event is redacted, so text
// holding no secret must cost a few passes of the regexp engine, not one full
// automaton pass per pattern. The cost is compared with one full-automaton
// scan of the same text by a pattern without a literal prefix, measured in
// the same test, so the bound holds under -race and on a loaded runner.
func TestRED_RedactSecretFreeTextCostsFewRegexpPasses(t *testing.T) {
	ResetSecrets()
	text := redhunt1109Prose(256 << 10)
	if out := RedactSecrets(text); out != text {
		t.Fatal("secret-free prose must come back unchanged")
	}
	calibration := regexp.MustCompile(`\bqzx-[a-z]{32,}`)
	if calibration.MatchString(text) {
		t.Fatal("calibration pattern must not match")
	}
	scan := bestOf(5, func() { calibration.FindStringIndex(text) })
	redact := bestOf(5, func() { RedactSecrets(text) })
	ratio := float64(redact) / float64(scan)
	t.Logf("256 KiB prose: RedactSecrets %v, one full scan %v, ratio %.2f", redact, scan, ratio)
	if ratio > 6 {
		t.Fatalf("RedactSecrets on secret-free prose costs %.1f full regexp scans (%v vs %v), want at most 6", ratio, redact, scan)
	}
}

// The checks that decide which patterns can be skipped stay linear on input
// built to stress them: ten times the input may not cost much more than ten
// times the time (quadratic: 100x).
func TestRedactPatternSkipChecksScaleLinearly(t *testing.T) {
	gens := map[string]func(n int) string{
		"capitals":       func(n int) string { return strings.ToUpper(redhunt1109Prose(n)) },
		"digit colons":   func(n int) string { return strings.Repeat("12345:", n/6) },
		"colon runs":     func(n int) string { return strings.Repeat("12345:"+strings.Repeat("a", 29)+" ", n/36) },
		"dotted runs":    func(n int) string { return strings.Repeat(strings.Repeat("a", 22)+".", n/23) },
		"anchor bytes":   func(n int) string { return strings.Repeat("k", n) },
		"near literals":  func(n int) string { return strings.Repeat("k_liv tok_en pas_sw <tokn --secre ://x ", n/39) },
		"folding runes":  func(n int) string { return strings.Repeat("x \u212a \u017f ", n/9) },
		"folded secrets": func(n int) string { return strings.Repeat("to\u212aen ", n/8) },
		"command words":  func(n int) string { return strings.Repeat("curl a;", n/7) },
	}
	base := 64 << 10
	if raceEnabled {
		base = 16 << 10
	}
	for name, gen := range gens {
		small, large := finalRedactTime(gen(base), 3), finalRedactTime(gen(10*base), 1)
		if small < time.Millisecond {
			small = time.Millisecond
		}
		if large > 25*small {
			t.Errorf("%s: super-linear: %d bytes %v, x10 %v", name, base, small, large)
		}
	}
}
