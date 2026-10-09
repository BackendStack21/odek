package artifact

import (
	"strings"
	"testing"
)

func refTruncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func truncCorpus() []string {
	return []string{
		"", "a", "abc", "héllo wörld", "日本語のテキスト", "a\xffb\xfe\xc3", "\xff\xff\xff\xff",
		"ok\xe2\x82", "😀😀😀😀", strings.Repeat("x", 1000), strings.Repeat("é\xff", 300),
	}
}

func TestTruncateRunes_EquivalentToRuneSlice(t *testing.T) {
	for _, s := range truncCorpus() {
		for _, n := range []int{0, 1, 2, 3, 5, 10, len(s), len(s) + 1, 400} {
			if got, want := TruncateRunes(s, n), refTruncateRunes(s, n); got != want {
				t.Errorf("TruncateRunes(%q,%d)=%q want %q", s, n, got, want)
			}
		}
	}
}

func TestBoundField_EquivalentAndCheap(t *testing.T) {
	ref := func(s string) string {
		r := []rune(s)
		if len(r) <= MaxFieldRunes {
			return s
		}
		return string(r[:MaxFieldRunes]) + "…"
	}
	for _, s := range truncCorpus() {
		if boundField(s) != ref(s) {
			t.Errorf("boundField(%q) differs", s)
		}
	}
	big := strings.Repeat("a", MaxFieldRunes*50)
	if boundField(big) != ref(big) {
		t.Fatal("big differs")
	}
}

func TestRED_Artifact_BoundFieldAllocs(t *testing.T) {
	big := strings.Repeat("a", 1<<20)
	if a := testing.AllocsPerRun(5, func() { _ = boundField(big) }); a > 1 {
		t.Fatalf("boundField allocs = %v, want <= 1", a)
	}
	if a := testing.AllocsPerRun(5, func() { _ = TruncateRunes(big, 100) }); a > 0 {
		t.Fatalf("TruncateRunes allocs = %v, want 0", a)
	}
}
