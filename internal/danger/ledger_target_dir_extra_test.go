package danger

import "testing"

func TestTargetDirectoryOption(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		dir  string
		ok   bool
	}{
		{"cp", []string{"-t", "d", "a"}, "d", true},
		{"cp", []string{"-td", "a"}, "d", true},
		{"mv", []string{"--target-directory=d", "a"}, "d", true},
		{"install", []string{"--target-directory", "d", "a"}, "d", true},
		{"ln", []string{"-s", "-t", "d", "a"}, "d", true},
		{"cp", []string{"a", "b"}, "", false},
		{"cp", []string{"--", "-t", "a"}, "", false},
		{"cp", []string{"-t"}, "", false},
		{"rsync", []string{"-t", "a", "b"}, "", false},
	} {
		dir, ok := targetDirectoryOption(tc.name, tc.args)
		if dir != tc.dir || ok != tc.ok {
			t.Errorf("targetDirectoryOption(%q, %q) = %q, %v; want %q, %v", tc.name, tc.args, dir, ok, tc.dir, tc.ok)
		}
	}
}

// Several sources copied with -t each rewrite DIR/<base>.
func TestLedgerRewriteTargetDirectoryAttachedForms(t *testing.T) {
	dir, licensed, other, _ := redRewriteSetup(t)
	for _, c := range []string{
		"cp -t" + dir + " " + other + " && bash " + licensed,
		"cp -v -t " + dir + " /nonexistent/x " + other + " && bash " + licensed,
		"ln -sf -t " + dir + " " + other + " && bash " + licensed,
	} {
		if !redGated(c) {
			t.Errorf("rewritten script must gate again: %s", c)
		}
	}
	if redGated("cp -t " + dir + " /nonexistent/other.sh && bash " + licensed) {
		t.Errorf("a copy of an unrelated file must leave the licence intact")
	}
}
