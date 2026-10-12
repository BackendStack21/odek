package telegram

import (
	"strings"
	"testing"
)

func TestStripMarkdownCodeAndEscapes(t *testing.T) {
	cases := map[string]string{
		"a \\*b\\* c":            "a *b* c",
		"`x_y\\`z\\\\w`":         "x_y`z\\w",
		"```go\nfoo_bar *\n```t": "go\nfoo_bar *\nt",
		"*b* _i_ ~s~":            "b i s",
		"trailing\\":             "trailing",
	}
	for in, want := range cases {
		if got := StripMarkdown(in); got != want {
			t.Errorf("StripMarkdown(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCutEntityBalancedReopensEntities(t *testing.T) {
	text := "*" + strings.Repeat("word ", 40) + "end*"
	head, rest, ok := cutEntityBalanced(text, 60)
	if !ok || len(head) > 60 || !strings.HasSuffix(head, "*") || !strings.HasPrefix(rest, "*") {
		t.Fatalf("unbalanced cut: ok=%v head=%q rest=%q", ok, head, rest)
	}
	if _, _, ok := cutEntityBalanced("abcdef", 0); ok {
		t.Error("cut with no room reported ok")
	}
}
