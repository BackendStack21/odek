package danger

import "testing"

// macOS spells /var and /tmp as symlinks into /private, and the classifier
// compares paths with the /private prefix stripped. A $HOME spelled through
// /private must still protect its own trust anchors and credential files.
func TestRED_ClassifyPath_HomeSpelledThroughPrivate(t *testing.T) {
	t.Setenv("HOME", "/private/var/folders/xy/abc/T/odek-home")
	for _, p := range []string{
		"/private/var/folders/xy/abc/T/odek-home/.odek/config.json",
		"/var/folders/xy/abc/T/odek-home/.odek/secrets.env",
		"/private/var/folders/xy/abc/T/odek-home/.ssh/id_ed25519",
		"/private/var/folders/xy/abc/T/odek-home/.zshrc",
	} {
		if got := ClassifyPath(p); got != SystemWrite {
			t.Errorf("ClassifyPath(%q) = %s, want system_write", p, got)
		}
	}
	// An ordinary file in that home stays a local write.
	if got := ClassifyPath("/private/var/folders/xy/abc/T/odek-home/notes.txt"); got != LocalWrite {
		t.Errorf("ordinary home file = %s, want local_write", got)
	}
}
