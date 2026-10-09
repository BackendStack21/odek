package render

import (
	"strings"
	"testing"
)

func refTruncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

func TestTruncate_EquivalentToRuneSlice(t *testing.T) {
	r := &Renderer{}
	corpus := []string{
		"", "a", "abc", "héllo wörld", "日本語のテキスト", "a\xffb\xfe\xc3", "\xff\xff\xff\xff",
		"ok\xe2\x82", "😀😀😀😀", strings.Repeat("x", 1000), strings.Repeat("é\xff", 300),
	}
	for _, s := range corpus {
		for _, n := range []int{0, 1, 2, 3, 5, 10, len(s), len(s) + 1, 400} {
			if got, want := r.truncate(s, n), refTruncate(s, n); got != want {
				t.Errorf("truncate(%q,%d)=%q want %q", s, n, got, want)
			}
		}
	}
}

func TestRED_Render_TruncateAllocs(t *testing.T) {
	r := &Renderer{}
	big := strings.Repeat("a", 1<<20)
	if a := testing.AllocsPerRun(5, func() { _ = r.truncate(big, 100) }); a > 1 {
		t.Fatalf("truncate allocs = %v, want <= 1", a)
	}
}
