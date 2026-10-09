package danger

import (
	"testing"
)

// sed -i on a path containing a space is skipped when collecting write
// targets, so the in-command rewrite is not noticed.
func TestRED_LedgerRewriteSedInPlaceSpacedPath(t *testing.T) {
	_, _, _, spaced := redRewriteSetup(t)
	c := "sed -i 's/a/b/' '" + spaced + "' && bash '" + spaced + "'"
	if !redGated(c) {
		t.Errorf("sed -i rewritten script must gate: %s", c)
	}
}

// perl/ruby -i rewrite files in place exactly like sed -i.
func TestRED_LedgerRewritePerlInPlace(t *testing.T) {
	_, licensed, _, _ := redRewriteSetup(t)
	c := "perl -pi -e 's/hi/evil/' " + licensed + " && bash " + licensed
	if !redGated(c) {
		t.Errorf("perl -pi rewritten script must gate: %s", c)
	}
}
