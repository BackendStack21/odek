package danger

import "testing"

// Every home-relative path ClassifyPath protects must also be refused as an
// @-reference, even when the workspace root is the home directory itself.
func TestIsSensitiveReadPath_HomeCredentialStores(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	for _, rel := range []string{
		".my.cnf", ".mylogin.cnf", ".terraform.d/credentials.tfrc.json",
		".config/hub", ".config/rclone/rclone.conf", ".config/github-copilot/hosts.json",
		".config/op/config", ".gitconfig", ".netrc", ".pgpass", ".vault-token",
		".cargo/credentials.toml", ".gem/credentials", ".npmrc", ".pypirc",
		".git-credentials", ".azure/accessTokens.json", ".kube/config",
	} {
		p := "/home/user/" + rel
		if c := ClassifyPath(p); c != SystemWrite {
			t.Fatalf("precondition: ClassifyPath(%q) = %v", p, c)
		}
		if !IsSensitiveReadPath("/home/user", p) {
			t.Errorf("%q is gated by ClassifyPath but loads as an @-reference", p)
		}
	}
	// Every entry of the shared table, so a new entry cannot drift.
	for _, sub := range homeSensitivePrefixes {
		p := "/home/user" + sub + "/x"
		if !IsSensitiveReadPath("/home/user", p) {
			t.Errorf("%q (table entry %q) loads as an @-reference", p, sub)
		}
	}
}
