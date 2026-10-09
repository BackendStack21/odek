package resource

import "testing"

// Sentence punctuation directly after a file reference is not part of the path.
func TestRED_ParseRefsTrailingSentencePunctuation(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"please review @main.go.", "@main.go"},
		{"is it fine @main.go?", "@main.go"},
	} {
		refs := ParseRefs(tc.text)
		if len(refs) != 1 {
			t.Fatalf("ParseRefs(%q) returned %d refs", tc.text, len(refs))
		}
		if refs[0].Raw != tc.want {
			t.Errorf("ParseRefs(%q) Raw = %q, want %q", tc.text, refs[0].Raw, tc.want)
		}
	}
}

// Punctuation inside a token and repeated trailing punctuation are handled
// without dropping the path or the reference span.
func TestParseRefsTrailingPunctuationEdges(t *testing.T) {
	text := "see @pkg/main.go:10, then @a.b.c... and @?"
	refs := ParseRefs(text)
	// "@?" trims down to a bare "@" and is not a reference.
	if len(refs) != 2 {
		t.Fatalf("ParseRefs returned %d refs, want 2: %+v", len(refs), refs)
	}
	if refs[0].Raw != "@pkg/main.go:10" {
		t.Errorf("first ref Raw = %q, want %q", refs[0].Raw, "@pkg/main.go:10")
	}
	if refs[1].Raw != "@a.b.c" || refs[1].Path != "a.b.c" {
		t.Errorf("second ref = %+v, want Raw @a.b.c", refs[1])
	}
	if text[refs[1].Start:refs[1].End] != "@a.b.c" {
		t.Errorf("second ref span = %q, want %q", text[refs[1].Start:refs[1].End], "@a.b.c")
	}
}
