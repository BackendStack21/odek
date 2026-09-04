package config

import "testing"

// An unterminated ${VAR consumed the ENTIRE remaining value: "a${b"
// expanded to "a$" — silent config-value corruption. The '$' plus the
// rest must survive verbatim (an unterminated brace is not a variable
// reference).
func TestExpandEnv_UnterminatedBracePreservesTail(t *testing.T) {
	cases := map[string]string{
		"a${b":       "a${b",
		"pre ${tail": "pre ${tail",
		"${only":     "${only",
		"$":          "$",
		"${}":        "${}",
	}
	for in, want := range cases {
		if got := expandEnv(in); got != want {
			t.Errorf("expandEnv(%q) = %q, want %q", in, got, want)
		}
	}
}
