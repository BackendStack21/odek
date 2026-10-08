package danger

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// TestOptSpecGoldenEffects pins the effect list of a corpus of command lines
// that exercise the option grammars the classifier adapters parse (wrappers,
// transfer clients, git, containers, kubectl, tar, chmod, sed, xargs, gh,
// curl, wget, hugo, ...). The golden file was generated from the classifier
// before its option parsers were unified, so a refactor of the parsing layer
// that changes any verdict on this corpus fails here. A line is the command,
// a tab, and the comma-separated effects in analysis order.
func TestOptSpecGoldenEffects(t *testing.T) {
	f, err := os.Open("testdata/optspec_golden.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	n := 0
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		cmd, want, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("malformed golden line %q", line)
		}
		n++
		var got []string
		for _, e := range Analyze(cmd).Effects {
			got = append(got, string(e))
		}
		if g := strings.Join(got, ","); g != want {
			t.Errorf("Analyze(%q).Effects = [%s], golden [%s]", cmd, g, want)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if n < 200 {
		t.Fatalf("golden corpus has only %d commands", n)
	}
}
