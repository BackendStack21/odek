package render

import "testing"

// RED #B5 (M5): FirstSentence iterates separator TYPES in order, not
// boundary positions — a ". " anywhere wins over an earlier "! "/"? ",
// so the "first" sentence can be the second one.
func TestRED_FirstSentencePicksEarliestBoundary(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Done! Next step. Then finish.", "Done!"},
		{"Really? Yes it is. More text follows here.", "Really?"},
	}
	for _, c := range cases {
		if got := FirstSentence(c.in); got != c.want {
			t.Errorf("FirstSentence(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
