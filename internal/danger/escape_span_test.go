package danger

import "testing"

func TestEscapeSpanForms(t *testing.T) {
	cases := []struct {
		in           string
		n, intro, tm int
	}{
		{"\x1b[31mX", 5, 5, 0},
		{"\x1b[", 2, 2, 0},
		{"\x1b]0;t\x07X", 6, 2, 1},
		{"\x1b]0;t\x1b\\X", 7, 2, 2},
		{"\x1b]0;t\u009cX", 7, 2, 2},
		{"\x1b]unterminated", 2, 2, 0},
		{"\x1bcX", 2, 2, 0},
		{"\x1b", 1, 1, 0},
		{"\x1b\x01", 1, 1, 0},
		{"\u009b31mX", 5, 5, 0},
		{"\u009b\x01", 2, 2, 0},
	}
	for _, c := range cases {
		n, intro, tm := escapeSpan(c.in)
		if n != c.n || intro != c.intro || tm != c.tm {
			t.Errorf("escapeSpan(%q) = %d,%d,%d want %d,%d,%d", c.in, n, intro, tm, c.n, c.intro, c.tm)
		}
	}
}

func TestControlVariantsReadings(t *testing.T) {
	if got := controlVariants("plain text", "plain text"); got != nil {
		t.Fatalf("no controls: %v", got)
	}
	got := controlVariants("a\x1b]0;pay\x07b", "ab")
	want := map[string]bool{"a 0;pay b": true, "ab": false}
	found := false
	for _, v := range got {
		if want[v] {
			found = true
		}
	}
	if !found {
		t.Fatalf("variants %q lack the spaced reading with the payload", got)
	}
}
