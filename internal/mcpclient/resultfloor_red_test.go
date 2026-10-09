package mcpclient

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRED_ResultCharsFloor(t *testing.T) {
	_, _, maxChars, warnings, err := normalizeLimits("tiny-srv", ServerConfig{MaxResultChars: 10})
	if err != nil {
		t.Fatal(err)
	}
	floor := ResultCharsFloor("tiny-srv")
	if maxChars != floor {
		t.Fatalf("maxChars = %d, want floor %d", maxChars, floor)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "tiny-srv") || !strings.Contains(warnings[0], "max_result_chars") {
		t.Fatalf("warnings = %v, want one warning naming the server", warnings)
	}

	// The floor leaves room for the notice plus real content, and the cap holds.
	c := &Client{name: "tiny-srv", maxResultChars: maxChars}
	out := c.applyResultLimit(strings.Repeat("t", 64), strings.Repeat("a", 5000))
	if n := utf8.RuneCountInString(out); n > maxChars {
		t.Fatalf("result has %d runes, cap %d", n, maxChars)
	}
	if !strings.HasPrefix(out, "aaaa") {
		t.Fatalf("floor leaves no room for content: %q", out)
	}

	// At or above the floor: no clamp, no warning.
	_, _, got, warnings, _ := normalizeLimits("tiny-srv", ServerConfig{MaxResultChars: floor})
	if got != floor || len(warnings) != 0 {
		t.Fatalf("at floor: got %d, warnings %v", got, warnings)
	}
}
