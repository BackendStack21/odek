package main

import (
	"strings"
	"testing"
)

// From the 2026-10-13 CLI contract audit: every command was run against
// its documented contract.

// error in the odek style, not the flag package's "Usage of logs:" dump.
func TestRED_LogsCmd_HelpAndUnknownFlag(t *testing.T) {
	setupTestHome(t)
	out := captureStdout(func() {
		if err := logsCmd([]string{"--help"}); err != nil {
			t.Errorf("logs --help: %v", err)
		}
	})
	if !strings.Contains(out, "Usage: odek logs") {
		t.Errorf("logs --help usage:\n%s", out)
	}
	flush := captureStderr(t)
	err := logsCmd([]string{"--bogus"})
	stderr := flush()
	if err == nil || !strings.Contains(err.Error(), "--bogus") {
		t.Fatalf("logs --bogus: %v", err)
	}
	if strings.Contains(stderr, "Usage of logs") {
		t.Errorf("flag package usage dump leaked:\n%s", stderr)
	}
}
