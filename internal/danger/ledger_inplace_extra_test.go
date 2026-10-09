package danger

import (
	"reflect"
	"testing"
)

func TestSedInPlaceFiles(t *testing.T) {
	for _, tc := range []struct {
		tokens []string
		want   []string
	}{
		{[]string{"sed", "-i", "s/a/b/", "my s.sh"}, []string{"my s.sh"}},
		{[]string{"sed", "-i.bak", "-e", "s/a/b/", "x", "y z"}, []string{"x", "y z"}},
		{[]string{"sed", "-i", "-f", "script.sed", "x"}, []string{"x"}},
		{[]string{"sed", "--in-place=.b", "p;p", "x"}, []string{"x"}},
		{[]string{"sed", "-i"}, nil},
	} {
		if got := sedInPlaceFiles(tc.tokens); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("sedInPlaceFiles(%q) = %q, want %q", tc.tokens, got, tc.want)
		}
	}
}

func TestScriptInPlaceFiles(t *testing.T) {
	for _, tc := range []struct {
		tokens []string
		want   []string
	}{
		{[]string{"perl", "-pi", "-e", "s/a/b/", "f.sh"}, []string{"f.sh"}},
		{[]string{"perl", "-i.bak", "-pe", "s/a/b/", "f", "g h"}, []string{"f", "g h"}},
		{[]string{"perl", "-pie", "x", "f"}, []string{"f"}},
		{[]string{"perl", "-p", "-e", "s/a/b/", "f"}, nil},
		{[]string{"perl", "-Mstrict", "-e", "1", "f"}, nil},
		{[]string{"perl", "-I", "lib", "-i", "-e", "1", "f"}, []string{"f"}},
		{[]string{"ruby", "-i", "-pe", "gsub(/a/,'b')", "f"}, []string{"f"}},
		{[]string{"ruby", "-r", "lib", "-i", "prog.rb", "f"}, []string{"f"}},
		{[]string{"perl", "-i", "-e", "1", "--", "-weird"}, []string{"-weird"}},
	} {
		if got := scriptInPlaceFiles(tc.tokens); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("scriptInPlaceFiles(%q) = %q, want %q", tc.tokens, got, tc.want)
		}
	}
}

func TestPerlInPlaceWriteIsPathClassified(t *testing.T) {
	if got := Classify("perl -pi -e 's/a/b/' /etc/hosts"); Rank(got) < Rank(SystemWrite) {
		t.Errorf("perl -pi on /etc/hosts = %s, want at least system_write", got)
	}
	if len(scriptInPlaceFiles([]string{"perl", "-pe", "s/a/b/", "f"})) != 0 {
		t.Errorf("perl without -i writes nothing")
	}
}
