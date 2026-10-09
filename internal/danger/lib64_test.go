package danger

import "testing"

// /lib is a system prefix, and degenerateHomes already lists /lib64 as a
// system directory, but the system-prefix table omits /lib64 (and /lib32,
// /libx32), so writes into the dynamic-loader directory are plain local_write.
func TestRED_ClassifyPathLib64IsSystem(t *testing.T) {
	for _, p := range []string{"/lib/x.so", "/lib64/ld-linux-x86-64.so.2", "/lib32/libc.so.6", "/libx32/libc.so.6"} {
		if got := ClassifyPath(p); got != SystemWrite {
			t.Errorf("ClassifyPath(%q) = %s, want system_write", p, got)
		}
	}
}
