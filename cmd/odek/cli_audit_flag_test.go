package main

import (
	"strings"
	"testing"
)

// From the 2026-10-13 CLI contract audit: every command was run against
// its documented contract.

// `odek audit --bogus` is an unknown flag, not a session called "--bogus".
func TestRED_AuditCmd_UnknownFlagRejected(t *testing.T) {
	withTempHome(t)
	err := auditCmd([]string{"--bogus"})
	if err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("audit --bogus: %v; want an unknown-flag error", err)
	}
	if err := auditCmd([]string{"--list", "extra"}); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("audit --list extra: %v; want a usage error", err)
	}
}
