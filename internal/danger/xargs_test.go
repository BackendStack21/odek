package danger

import "testing"

// xargs -d $'\n' is the idiomatic way to split on newlines. The decoded
// newline must not hide the composed destructive command.
func TestRED_XargsDelimiterNewline(t *testing.T) {
	cases := map[string]RiskClass{
		`echo / | xargs -d $'\n' rm -rf`:       Destructive,
		`printf '/\n' | xargs -d $'\n' rm -rf`: Destructive,
		"echo / | xargs -d '\n' rm -rf":        Destructive,
	}
	for cmd, want := range cases {
		if got := Classify(cmd); got != want {
			t.Errorf("Classify(%q) = %s, want %s", cmd, got, want)
		}
	}
	// Non-static upstream must fail closed (unknown), as it does without -d.
	for _, cmd := range []string{`ls / | xargs -d $'\n' rm -rf`, `find . | xargs -d $'\n' rm -rf`} {
		if got := Classify(cmd); got != Unknown && got != Destructive {
			t.Errorf("Classify(%q) = %s, want unknown or destructive", cmd, got)
		}
	}
}

// A quoted separator word is an argument everywhere the token stream is split,
// while the unquoted spelling still splits.
func TestQuotedSeparatorWordsStayArguments(t *testing.T) {
	segs := splitSegments(markLiteralOperators([]string{"grep", ";", "x", ";", "ls"}, []bool{false, false, false, true, false}))
	if len(segs) != 2 || len(segs[0]) != 3 || segs[0][1] != ";" {
		t.Errorf("splitSegments = %q, want a quoted ; kept as an argument and an operator ; splitting", segs)
	}
	stages := splitPipes(markLiteralOperators([]string{"grep", "|", "x", "|", "wc"}, []bool{false, false, false, true, false}))
	if len(stages) != 2 || stages[0][1] != "|" || stages[1][0] != "wc" {
		t.Errorf("splitPipes = %q, want a quoted | kept as an argument", stages)
	}
	if got := markLiteralOperators([]string{"a", ";"}, nil); got[1] != ";" {
		t.Errorf("nil ops must leave tokens unmarked, got %q", got)
	}
	if got := unmarkLiteralOperators([]string{"a", "b"}); len(got) != 2 {
		t.Errorf("unmark of plain tokens = %q", got)
	}
	for cmd, want := range map[string]RiskClass{
		`find . -name x -exec rm {} ';'`: CodeExecution,
		`echo a '|' b | cat`:             Safe,
		`echo 'a && b'; echo '&&'`:       Safe,
	} {
		if got := Classify(cmd); Rank(got) > Rank(want) {
			t.Errorf("Classify(%q) = %s, want at most %s", cmd, got, want)
		}
	}
	// The destructive tail behind a quoted separator is still seen.
	for _, cmd := range []string{
		`echo ';' ; rm -rf /`,
		`grep ';' x | xargs rm -rf`,
	} {
		if got := Classify(cmd); Rank(got) < Rank(Unknown) {
			t.Errorf("Classify(%q) = %s, want unknown or worse", cmd, got)
		}
	}
}
