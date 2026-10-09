package danger

import (
	"strings"
	"testing"
)

// A chain of nested find -exec operands must cost a number of stage scans
// linear in the chain length: the payload after one -exec already carries
// every later -exec, so the outer loop must resume after that payload, not
// re-scan each inner position. Quadratic work made the 300-deep chain take
// seconds under the race detector and time out on CI.
func TestRED_DenylistFindExecScansLinear(t *testing.T) {
	const n = 300
	cmd := strings.Repeat("find . -exec ", n) + `rm {} \;`
	dc := &denyCtx{entries: [][]string{{"git", "push"}}, seen: map[string]int{}}
	if denyScan(cmd, dc, 0) {
		t.Fatalf("chain without a denied command must not match")
	}
	t.Logf("stage entries %d, scans %d for a chain of %d", dc.stageEntries, dc.stageScans, n)
	if dc.stageEntries > 3*n || dc.stageScans > 3*n {
		t.Fatalf("find -exec chain of %d entered denyStage %d times and scanned %d; want at most %d each (linear)", n, dc.stageEntries, dc.stageScans, 3*n)
	}
	// A denied command at the bottom of a short chain is still found.
	denied := strings.Repeat("find . -exec ", 5) + `git push \;`
	if !denyScan(denied, &denyCtx{entries: [][]string{{"git", "push"}}, seen: map[string]int{}}, 0) {
		t.Fatalf("denied command at the bottom of a find -exec chain must be denied")
	}
}
