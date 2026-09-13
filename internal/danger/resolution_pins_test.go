package danger

import "testing"

// TestProbeLinuxResolutionPins pins the Linux-observed CI behavior that
// cannot reproduce on macOS: /dev/stdout stays LocalWrite regardless of
// where symlink resolution lands, and the systemd binary path is a plain
// system write, not a persistence unit directory.
func TestClassify_LinuxResolutionPins(t *testing.T) {
	if got := ClassifyPath("/dev/stdout"); got != LocalWrite {
		t.Errorf("/dev/stdout = %v, want local_write", got)
	}
	if got := Classify("dd of=/dev/stdout"); got == Destructive || got == SystemWrite {
		t.Errorf("dd of=/dev/stdout = %v, want < system_write", got)
	}
	if isPersistencePathLexical("/lib/systemd/systemd") {
		t.Errorf("/lib/systemd/systemd flagged persistence — binary, not unit dir")
	}
	if !isPersistencePathLexical("/lib/systemd/system/evil.service") {
		t.Errorf("/lib/systemd/system/evil.service not flagged persistence")
	}
	if !isPersistencePathLexical("/usr/lib/systemd/system/evil.service") {
		t.Errorf("/usr/lib/systemd/system/evil.service not flagged persistence")
	}
}
