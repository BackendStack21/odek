package mcpclient

import (
	"strings"
	"testing"
)

func TestTruncateRunes_Equivalent(t *testing.T) {
	ref := func(s string, n int) string {
		r := []rune(s)
		if len(r) <= n {
			return s
		}
		return string(r[:n])
	}
	corpus := []string{"", "abc", "héllo", "日本語", "a\xffb\xc3", strings.Repeat("😀", 50)}
	for _, s := range corpus {
		for _, n := range []int{0, 1, 2, 4, 100} {
			if got, want := truncateRunes(s, n), ref(s, n); got != want {
				t.Errorf("truncateRunes(%q,%d)=%q want %q", s, n, got, want)
			}
		}
	}
}

func TestRED_MCPClient_TruncateRunesAllocs(t *testing.T) {
	big := strings.Repeat("a", 1<<20)
	if a := testing.AllocsPerRun(5, func() { _ = truncateRunes(big, 100) }); a > 0 {
		t.Fatalf("allocs = %v, want 0", a)
	}
}
