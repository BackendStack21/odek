package danger

import (
	"os"
	"strings"
	"testing"
)

// TestMain gives this package a non-temp HOME when the environment provides
// a temp one (sandboxed runs use HOME=/tmp). The classifier's temp-dir rule
// (local write) outranks the home-anchor patterns, so coverage for ~/.ssh,
// ~/.gnupg, shell rc files, and odek trust anchors is unrepresentable under
// a temp home — tests would fail on ClassifyPath("/tmp/.ssh/...") = local_write
// even though the classifier is behaving as designed for real homes.
//
// The replacement directory must (a) not start with a temp prefix and (b)
// already exist as a writable mount. /workspace satisfies both in odek
// sandbox images; on ordinary hosts (CI, dev machines) HOME is not a temp
// dir and is left untouched.
func TestMain(m *testing.M) {
	if home := os.Getenv("HOME"); strings.HasPrefix(home, "/tmp") || strings.HasPrefix(home, "/var/folders") {
		if st, err := os.Stat("/workspace"); err == nil && st.IsDir() {
			fake := "/workspace/.testhome"
			if err := os.MkdirAll(fake, 0o755); err == nil {
				_ = os.Setenv("HOME", fake)
			}
		}
	}
	os.Exit(m.Run())
}
