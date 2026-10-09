package danger

import "testing"

func TestLibraryDirectoriesAreSystemPaths(t *testing.T) {
	for _, cmd := range []string{
		"cp x /lib64/ld-linux-x86-64.so.2",
		"cp x /lib32/libc.so.6",
		"echo x > /libx32/libc.so.6",
	} {
		if got := Classify(cmd); Rank(got) < Rank(SystemWrite) {
			t.Errorf("Classify(%q) = %s, want at least system_write", cmd, got)
		}
	}
	for _, p := range []string{"/lib64", "/lib32/sub/x", "/libx32"} {
		if got := ClassifyPath(p); got != SystemWrite {
			t.Errorf("ClassifyPath(%q) = %s, want system_write", p, got)
		}
	}
	for _, p := range []string{"/libfoo/x", "/lib640/x"} {
		if got := ClassifyPath(p); got == SystemWrite {
			t.Errorf("ClassifyPath(%q) = %s, only the library directories are system paths", p, got)
		}
	}
}
