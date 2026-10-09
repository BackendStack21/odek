package danger

import (
	"testing"
)

// env -S 'cmd' runs the command string; the classifier sees it, the unread
// script gate does not.
func TestRED_UnreadGateEnvSplitString(t *testing.T) {
	p := redUnreadScript(t)
	if !redGated("env bash " + p) {
		t.Fatalf("control failed")
	}
	if !redGated("env -S 'bash " + p + "'") {
		t.Errorf("env -S 'bash script' must gate an unread script")
	}
}
