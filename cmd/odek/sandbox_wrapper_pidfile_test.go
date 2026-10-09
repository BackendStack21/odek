package main

import (
	"strings"
	"testing"
)

// A foreground sandboxed command cleans up its own pidfile on a natural
// exit; only background jobs leave it for the follow-up to remove.
func TestRED_ForegroundSandboxWrapperRemovesPidfile(t *testing.T) {
	argv, _ := wrapSandboxCommand("odek-c", "true")
	wrapper := argv[6]
	i := strings.Index(wrapper, "rm -f /tmp/.odek-cmd-")
	j := strings.Index(wrapper, `sh -c "$1"`)
	if i < 0 || j < 0 || i < j {
		t.Fatalf("foreground wrapper must remove the pidfile after the command: %q", wrapper)
	}
	if !strings.Contains(wrapper, "rc=$?") || !strings.HasSuffix(wrapper, "exit $rc") {
		t.Fatalf("foreground wrapper must preserve the command's exit code: %q", wrapper)
	}
	bg, _ := wrapSandboxCommandKeepPidfile("odek-c", "true")
	if strings.Contains(bg[6], "rm -f") {
		t.Fatalf("background wrapper must keep the pidfile for the follow-up: %q", bg[6])
	}
	if argv[7] != "odek-cmd" || argv[8] != "true" {
		t.Fatalf("command must travel as a positional argument: %v", argv)
	}
}
