package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// From the 2026-10-13 CLI contract audit: every command was run against
// its documented contract.

// `odek telegram` takes no arguments; before, `odek telegram --bogus`
// ignored the argument and started polling with the configured token.
func TestRED_TelegramCmd_RejectsArguments(t *testing.T) {
	home := setupTestHome(t)
	err := telegramCmd([]string{"--bogus"})
	if err == nil || !strings.Contains(err.Error(), "takes no arguments") {
		t.Fatalf("telegram --bogus: %v; want a usage error", err)
	}
	// Rejected before the singleton lock and the config are touched.
	if _, statErr := os.Stat(filepath.Join(home, ".odek", "telegram.lock")); statErr == nil {
		t.Fatal("argument check ran after the singleton lock was taken")
	}
	out := captureStdout(func() {
		if err := telegramCmd([]string{"--help"}); err != nil {
			t.Errorf("telegram --help: %v", err)
		}
	})
	if !strings.Contains(out, "Usage: odek telegram") {
		t.Errorf("telegram --help usage:\n%s", out)
	}
}
