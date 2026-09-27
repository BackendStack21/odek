package config

import "testing"

// A '$' followed by a non-identifier byte must be emitted verbatim without
// consuming the following byte: "cost: $ 5" stays "cost: $ 5" (the space
// after the $ was previously eaten), "$9.99" stays "$9.99" (the '9' was
// eaten), and shell-sigil-lookalikes like "$?" emit "$?" unharmed.
func TestRED_ExpandEnvKeepsCharAfterBareDollar(t *testing.T) {
	cases := map[string]string{
		"cost: $ 5":   "cost: $ 5",
		"$9.99":       "$9.99",
		"$?":          "$?",
		"$$":          "$",
		"a$ b":        "a$ b",
		"plain":       "plain",
		"a$$b":        "a$b",
		"$$9.99":      "$9.99",
		"${UNSET_XY}": "",
		"$UNSET_XY":   "",
	}
	for in, want := range cases {
		if got := expandEnv(in); got != want {
			t.Errorf("expandEnv(%q) = %q, want %q", in, got, want)
		}
	}
}
