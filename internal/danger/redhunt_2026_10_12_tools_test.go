package danger

import "testing"

// Unicode case-fold aliases (U+017F LONG S folds to 's', U+212A KELVIN SIGN
// to 'k') name the same files on APFS/NTFS and must classify like the
// canonical spelling.
func TestToolsFoldAliasHomeProtection(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	if got := ClassifyPath("/home/user/.ſsh/authorized_keys"); Rank(got) < Rank(SystemWrite) {
		t.Errorf("folded .ssh alias = %v, want >= system_write", got)
	}
	if got := ClassifyPath("/home/user/.baſhrc"); Rank(got) < Rank(SystemWrite) {
		t.Errorf("folded .bashrc alias = %v, want >= system_write", got)
	}
	if !IsPersistencePath("/home/user/.baſhrc") {
		t.Error("folded .bashrc alias must be a persistence path")
	}
	if !IsPersistencePath("/home/user/.local/share/ſystemd/user/x.service") {
		t.Error("folded systemd user unit dir must be a persistence path")
	}
}
