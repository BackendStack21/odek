package mcpclient

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRED_ResultLimitHoldsForTinyCap(t *testing.T) {
	c := &Client{name: "srv", maxResultChars: 100}
	out := c.applyResultLimit("tool", strings.Repeat("a", 5000))
	if n := utf8.RuneCountInString(out); n > 100 {
		t.Fatalf("max_result_chars=100 but result has %d chars", n)
	}
}

func TestResultLimit_CapBelowNoticeStaysWithinCap(t *testing.T) {
	for _, limit := range []int{1, 10, 50, 150} {
		c := &Client{name: "srv", maxResultChars: limit}
		out := c.applyResultLimit("tool", strings.Repeat("é", 5000))
		if n := utf8.RuneCountInString(out); n > limit {
			t.Fatalf("limit %d: got %d runes", limit, n)
		}
	}
}
