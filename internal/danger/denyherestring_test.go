package danger

import "testing"

// The denylist scans heredoc bodies fed to a shell, but a here-string or a
// static echo/printf pipe into the same shell carries the same command line
// and is only prompted (code_execution), not denied.
func TestRED_DenylistSeesStaticShellInput(t *testing.T) {
	cfg := &DangerousConfig{Denylist: []string{"git push"}}
	for _, cmd := range []string{
		"cat <<X | sh\ngit push\nX",
		"sh <<< 'git push'",
		"bash <<< 'git push origin main'",
		"echo 'git push' | sh",
		"printf 'git push\\n' | bash",
	} {
		if got := cfg.ActionForCommand(cmd); got != Deny {
			t.Errorf("ActionForCommand(%q) = %s, want deny", cmd, got)
		}
	}
}
