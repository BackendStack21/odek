package danger

import (
	"testing"
)

// A licensed script overwritten through the -t / --target-directory spelling
// of cp/mv/install earlier in the same command is new, unread content.
func TestRED_LedgerRewriteViaTargetDirectory(t *testing.T) {
	dir, licensed, other, _ := redRewriteSetup(t)
	for _, c := range []string{
		"cp -t " + dir + " " + other + " && bash " + licensed,
		"cp --target-directory=" + dir + " " + other + " && bash " + licensed,
		"mv -t " + dir + " " + other + " && bash " + licensed,
		"install -t " + dir + " " + other + " && bash " + licensed,
	} {
		if !redGated(c) {
			t.Errorf("rewritten script must gate again: %s", c)
		}
	}
}
