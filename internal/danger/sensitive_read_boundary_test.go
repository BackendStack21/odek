package danger

import "testing"

func TestIsSensitiveReadPath_HomeBoundariesAndSpecificChildren(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	for _, rel := range []string{
		".config/nvim/init.lua", ".cargo/config.toml", ".gemrc", ".configs/x",
		".sshfoo", ".docker/Dockerfile", ".gem/specs/x", ".terraform.d/plugins/x",
	} {
		if IsSensitiveReadPath("/home/user", "/home/user/"+rel) {
			t.Errorf("~/%s should load", rel)
		}
	}
	for _, rel := range []string{
		".config/gh/hosts.yml", ".config/gcloud/credentials.db", ".config/rclone/rclone.conf",
		".docker/config.json", ".cargo/credentials.toml", ".cargo/credentials", ".gem/credentials",
		".ssh/id_rsa", ".ssh", ".gitconfig", ".terraform.d/credentials.tfrc.json",
	} {
		if !IsSensitiveReadPath("/home/user", "/home/user/"+rel) {
			t.Errorf("~/%s should be refused", rel)
		}
	}
}
