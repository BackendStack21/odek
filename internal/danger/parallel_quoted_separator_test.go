package danger

import "testing"

// parallel and sem join their command words and run the line through a shell
// unless -q/--quote is given, so a quoted separator word is a real separator.
func TestRED_ParallelQuotedSeparatorIsShellSeparator(t *testing.T) {
	for _, head := range []string{"parallel", "sem", "sem --id job", "parallel -j2"} {
		for _, sep := range []string{";", "&&", "|"} {
			for _, cmd := range []string{
				head + " echo '" + sep + "' rm -rf ~ ::: 1",
				"ls | " + head + " echo '" + sep + "' rm -rf ~",
			} {
				if got := Classify(cmd); Rank(got) < Rank(Destructive) {
					t.Errorf("Classify(%q) = %v, want destructive", cmd, got)
				}
			}
		}
	}
	// -q/--quote keeps the words literal.
	for _, cmd := range []string{
		"parallel -q echo ';' rm -rf ~ ::: 1",
		"parallel --quote echo '&&' rm -rf ~ ::: 1",
		"ls | parallel -q echo '|' rm -rf ~",
	} {
		if got := Classify(cmd); Rank(got) >= Rank(Destructive) {
			t.Errorf("Classify(%q) = %v, want below destructive", cmd, got)
		}
	}
	// watch composes its words the same way.
	if got := Classify("watch echo ';' rm -rf ~"); Rank(got) < Rank(Destructive) {
		t.Errorf("watch control = %v, want destructive", got)
	}
	if got := Classify("parallel 'echo; rm -rf ~' ::: 1"); Rank(got) < Rank(Unknown) {
		t.Errorf("embedded separator = %v, want at least unknown", got)
	}
}
