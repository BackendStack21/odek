package danger

import (
	"testing"
)

// `-e` is errexit for shells, not an inline-code flag; `bash -e script.sh`
// still executes script.sh and must gate like `bash -x script.sh` does.
func TestRED_UnreadGateShellErrexitFlag(t *testing.T) {
	p := redUnreadScript(t)
	for _, c := range []string{
		"bash -e " + p, "sh -e " + p, "zsh -e " + p, "dash -e " + p,
		"bash -e -u " + p, "bash -u -e " + p,
	} {
		if !redGated(c) {
			t.Errorf("unread script run via errexit flag must gate: %s", c)
		}
	}
	// control: the cluster spelling already gates
	if !redGated("bash -ex " + p) {
		t.Fatalf("control failed")
	}
}
