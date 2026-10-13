package main

import (
	"strings"
	"testing"
)

// From the 2026-10-13 CLI contract audit: every command was run against
// its documented contract.

// `odek help`, `--help` and `-h` print the usage and exit 0; they were
// "unknown command", exit 1.
func TestRED_Dispatch_HelpAliasesExitZero(t *testing.T) {
	for _, arg := range []string{"help", "--help", "-h"} {
		flush := captureStderr(t)
		out := captureStdout(func() {
			if code := dispatch([]string{arg}); code != 0 {
				t.Errorf("odek %s: exit %d, want 0", arg, code)
			}
		})
		if stderr := flush(); strings.Contains(stderr, "unknown command") {
			t.Errorf("odek %s treated as unknown command:\n%s", arg, stderr)
		}
		if !strings.Contains(out, "Usage:") {
			t.Errorf("odek %s printed no usage:\n%s", arg, out)
		}
	}
}
