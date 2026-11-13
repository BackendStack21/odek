package danger

import "testing"

// FoldPathCase expands the characters whose full case folding is several
// ASCII letters, so filesystem aliases of an anchor name fold to the anchor.
func TestRED_FoldPathCaseFullFolding(t *testing.T) {
	cases := map[string]string{
		"conﬁg.json":         "config.json",
		"seßions":            "sessions",
		"seẞions":            "sessions",
		"reﬆart.json":        "restart.json",
		"schedule-ﬅate.json": "schedule-state.json",
		"ﬀ":                  "ff",
		"ﬃ":                  "ffi",
		"ﬄ":                  "ffl",
		"ﬂ":                  "fl",
		".ßh":                ".ssh",
		"ſecrets.env":        "secrets.env",
		"CONFIG.JSON":        "config.json",
		"KELVIN-K":           "kelvin-k",
		"plain-ascii.txt":    "plain-ascii.txt",
		"Ünïcode-ÄÖ":         "ünïcode-äö",
	}
	for in, want := range cases {
		if got := FoldPathCase(in); got != want {
			t.Errorf("FoldPathCase(%q) = %q, want %q", in, got, want)
		}
	}
}
