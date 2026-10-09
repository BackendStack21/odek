package danger

import "testing"

// -t may sit at the end of a short-flag cluster (cp -at DIR) and the long
// spelling may be any unambiguous prefix (--target=DIR); both overwrite a
// licensed script just like the plain -t spelling.
func TestRED_LedgerRewriteViaTargetDirectoryClusterAndAbbrev(t *testing.T) {
	dir, licensed, other, _ := redRewriteSetup(t)
	for _, c := range []string{
		"cp -at " + dir + " " + other + " && bash " + licensed,
		"cp -rt " + dir + " " + other + " && bash " + licensed,
		"cp -bt " + dir + " " + other + " && bash " + licensed,
		"install -Dt " + dir + " " + other + " && bash " + licensed,
		"cp -at" + dir + " " + other + " && bash " + licensed,
		"cp --target-dir=" + dir + " " + other + " && bash " + licensed,
		"cp --target=" + dir + " " + other + " && bash " + licensed,
		"mv --t " + dir + " " + other + " && bash " + licensed,
		"ln -sft " + dir + " " + other + " && bash " + licensed,
	} {
		if !redGated(c) {
			t.Errorf("rewritten script must gate again: %s", c)
		}
	}
}
