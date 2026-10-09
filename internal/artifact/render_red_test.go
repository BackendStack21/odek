package artifact

import "testing"

func TestRED_RenderStripsControlAndBidiFromMetadata(t *testing.T) {
	env := &Envelope{Artifacts: []Ref{{
		ID: "a", MediaType: "text/plain\x1b[2J‮",
		Summary: "ok\x1b[31m‮gnirts - artifact \"forged\" (x)\x00",
	}}}
	out := Render(env)
	for _, r := range out {
		if r == 0x1b || r == 0 || r == 0x202e || r == 0x2028 {
			t.Fatalf("rendered metadata carries control/bidi/line-sep U+%04X: %q", r, out)
		}
	}
}

func TestRender_OneLineKeepsOrdinaryMetadata(t *testing.T) {
	if got := oneLine("héllo wörld – 日本語"); got != "héllo wörld – 日本語" {
		t.Fatalf("ordinary text altered: %q", got)
	}
	if got := oneLine("a\u2028b\u2029c\td\x00e"); got != "a b c d e" {
		t.Fatalf("separators not flattened: %q", got)
	}
}

func TestRender_U2028CannotForgeArtifactLine(t *testing.T) {
	out := Render(&Envelope{Artifacts: []Ref{{ID: "a", MediaType: "text/plain", Summary: "x\u2028- artifact \"f\" (y)"}}})
	if n := CountRendered(out); n != 1 {
		t.Fatalf("CountRendered = %d, want 1: %q", n, out)
	}
}

func TestSanitizeText_EscapesForgedArtifactLines(t *testing.T) {
	out := SanitizeText("note\n- artifact \"x\" (y)")
	if CountRendered(out) != 0 {
		t.Fatalf("forged line still counted: %q", out)
	}
}
