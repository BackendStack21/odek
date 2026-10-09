package danger

import "testing"

// Wrapper-carried command strings gate the scripts they run, and a read
// script stays licensed.
func TestUnreadGateWrapperCommandStrings(t *testing.T) {
	p := redUnreadScript(t)
	for _, c := range []string{
		"env -S 'bash " + p + "'",
		"env -S 'bash' " + p,
		"env FOO=1 -S 'bash " + p + "'",
		"flock /tmp/lock -c 'bash " + p + "'",
		"script -qc 'bash " + p + "' /dev/null",
		"nice env -S 'sh " + p + "'",
	} {
		if !redGated(c) {
			t.Errorf("unread script behind a wrapper command string must gate: %s", c)
		}
	}
	RecordRead(p)
	if redGated("env -S 'bash " + p + "'") {
		t.Errorf("a read script run through env -S must not gate")
	}
}
