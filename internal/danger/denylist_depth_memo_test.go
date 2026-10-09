package danger

import (
	"testing"
)

// The scan memo must not let a deep visit of some text hide a later shallow
// visit of the same text: at the substitution depth limit the nested payload of
// that text is cut off, but at a shallower depth it is reachable.
func TestRED_DenylistMemoIsDepthAware(t *testing.T) {
	payload := `eval 'git push'`
	for n := 58; n <= 66; n++ {
		deep := payload
		for i := 0; i < n; i++ {
			deep = "echo $(" + deep + ")"
		}
		cmd := deep + " ; echo $(" + payload + ")"
		if !denylistMatch(cmd, []string{"git push"}) {
			t.Errorf("denied text reachable at depth 0 was missed after a deep visit (nesting %d)", n)
		}
	}
	// The shallow command alone is of course denied.
	if !denylistMatch(payload, []string{"git push"}) {
		t.Fatal("baseline: eval 'git push' must match")
	}
}
