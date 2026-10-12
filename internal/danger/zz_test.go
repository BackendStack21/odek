package danger

import "testing"

func TestZZ(t *testing.T) {
	for _, c := range []string{`trap git\ push EXIT`, "ev" + `al git\ push`} {
		m, _ := normalize(c)
		t.Logf("%q %q", m, tokenize(m))
	}
}
