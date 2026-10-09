package danger

import "testing"

// A quoted separator is an ordinary argument, never a command boundary. The
// classifier treats a standalone quoted ';', '|', '&&' or newline word as a
// separator, so benign commands go unknown (denied by default) and the
// split hides the real verb's arguments from adapters.
func TestRED_QuotedSeparatorIsArgument(t *testing.T) {
	for _, cmd := range []string{
		`grep ';' x`,
		`grep '|' x`,
		`cut -d ';' -f1 x`,
		`awk -F ';' '{print $1}' x`,
		`sort -t ';' x`,
		`tr ';' ' ' < x`,
		`cut -d $'\n' -f1 x`,
	} {
		if got := Classify(cmd); got != Safe {
			t.Errorf("Classify(%q) = %s, want safe", cmd, got)
		}
	}
}
