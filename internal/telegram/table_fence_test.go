package telegram

import (
	"strings"
	"testing"
)

// unescapedLinkOutsideFence reports whether s has a raw '[' outside ```
// fences, i.e. a MarkdownV2 link could be formed.
func unescapedLinkOutsideFence(s string) bool {
	in := false
	for i := 0; i < len(s); i++ {
		if strings.HasPrefix(s[i:], "```") {
			bs := 0
			for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
				bs++
			}
			if bs%2 == 0 {
				in = !in
				i += 2
				continue
			}
		}
		if !in && s[i] == '[' {
			bs := 0
			for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
				bs++
			}
			if bs%2 == 0 {
				return true
			}
		}
	}
	return false
}

func TestEscapeCodeLine(t *testing.T) {
	cases := map[string]string{
		"plain":       "plain",
		"a ``` b":     "a \\`\\`\\` b",
		`back\slash`:  `back\\slash`,
		"x \\``` [l]": "x \\\\\\`\\`\\` [l]",
	}
	for in, want := range cases {
		if got := escapeCodeLine(in); got != want {
			t.Errorf("escapeCodeLine(%q) = %q, want %q", in, got, want)
		}
	}
}

// A mid-line fence inside a fenced code block cannot close the block early.
func TestFormatResponse_CodeBlockLineCannotBreakOut(t *testing.T) {
	chunks, err := FormatResponse("```\nx ``` [click](http://evil.example)\n```")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range chunks {
		if unescapedLinkOutsideFence(c) {
			t.Fatalf("code line broke out of its fence: %q", c)
		}
	}
}

func TestRED_TableRowFenceBreakout(t *testing.T) {
	in := "a | b ``` [click](http://evil.example)"
	chunks, err := FormatResponse(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range chunks {
		if unescapedLinkOutsideFence(c) {
			t.Fatalf("untrusted text broke out of the table fence and formed a live link: %q", c)
		}
	}
}
