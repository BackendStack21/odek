package loop

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Reasons clamp must not cut inside a rune.
func TestRED_VerifyReasonClampSplitsRune(t *testing.T) {
	r := strings.Repeat("€", 100)
	v := parseVerifyVerdict(`{"verdict":"fail","reasons":["` + r + `"]}`)
	if !utf8.ValidString(v.Reasons[0]) {
		t.Fatalf("clamped reason is invalid UTF-8")
	}
}

// The missing list is clamped on rune boundaries too.
func TestVerifyMissingClampKeepsValidUTF8(t *testing.T) {
	r := strings.Repeat("€", 100)
	v := parseVerifyVerdict(`{"verdict":"fail","missing":["` + r + `"]}`)
	if !utf8.ValidString(v.Missing[0]) || !strings.HasSuffix(v.Missing[0], "…") {
		t.Fatalf("missing clamp = %q", v.Missing[0])
	}
}
