package artifact

import (
	"strings"
	"testing"
)

func TestBoundOneLine_EquivalentToBoundFieldOneLine(t *testing.T) {
	corpus := append(truncCorpus(),
		"line1\nline2\ttab‮bidi sep",
		strings.Repeat("a\n", MaxFieldRunes+50),
		strings.Repeat("‮\xff", MaxFieldRunes+5),
		strings.Repeat("é", MaxFieldRunes),
		strings.Repeat("é", MaxFieldRunes+1),
	)
	for _, s := range corpus {
		if got, want := boundOneLine(s), boundField(oneLine(s)); got != want {
			t.Errorf("boundOneLine(%.40q) differs", s)
		}
	}
}

func TestRED_Artifact_RenderDoesNotMapFullSummary(t *testing.T) {
	env := &Envelope{Artifacts: []Ref{{ID: "x", MediaType: "text/plain", Summary: strings.Repeat("s\n", 1<<19)}}}
	if a := testing.AllocsPerRun(3, func() { _ = Render(env) }); a > 40 {
		t.Fatalf("allocs = %v", a)
	}
	var total uint64
	allocBytes := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = Render(env)
		}
	}).AllocedBytesPerOp()
	total = uint64(allocBytes)
	if total > 64<<10 {
		t.Fatalf("Render allocated %d bytes for a 1MiB summary", total)
	}
}
