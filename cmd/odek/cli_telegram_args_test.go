package main

import (
	"strings"
	"testing"
)

// From the 2026-10-13 CLI contract audit: every command was run against
// its documented contract.

// ignored the argument and started polling with the configured token.
func TestRED_TelegramCmd_RejectsArguments(t *testing.T) {
	setupTestHome(t)
	err := telegramCmd([]string{"--bogus"})
	if err == nil || !strings.Contains(err.Error(), "takes no arguments") {
		t.Fatalf("telegram --bogus: %v; want a usage error", err)
	}
}
