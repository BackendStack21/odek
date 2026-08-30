package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHermetic_TestProcessIsolatedFromOperatorConfig pins the package-wide
// hermeticity guarantee: the test process runs against a fixture HOME, never
// the operator's real home. Without it, config.LoadConfig reads the real
// ~/.odek/config.json and ~/.odek/secrets.env — observed 2026-08-30, where
// tests logged the operator's glm-5.3-flash model and inherited operator
// dangerous/limits settings (the odek sandbox only masked this by setting
// HOME=/tmp; host runs were poisoned).
//
// Tests that need a specific home still t.Setenv("HOME", ...) locally; the
// escape hatch for debugging config interactions is
// ODEK_TEST_KEEP_REAL_HOME=1.
func TestHermetic_TestProcessIsolatedFromOperatorConfig(t *testing.T) {
	fixture := os.Getenv("ODEK_TEST_HOME")
	if fixture == "" {
		t.Fatal("ODEK_TEST_HOME not set — TestMain home isolation is missing")
	}
	got, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	if got != fixture {
		t.Fatalf("UserHomeDir = %q, want the TestMain fixture %q — operator home leaked into tests", got, fixture)
	}
	if _, err := os.Stat(filepath.Join(fixture, ".odek", "config.json")); err == nil {
		t.Fatal("fixture home contains .odek/config.json — operator config must never be copied into the fixture")
	}
	if os.Getenv("HOME") != fixture {
		t.Fatalf("HOME = %q, want fixture %q", os.Getenv("HOME"), fixture)
	}

	// No ambient ODEK_* variable may survive the TestMain scrub — the env
	// layer of config.LoadConfig applies them regardless of HOME.
	keep := map[string]bool{
		"ODEK_NO_SANDBOX":               true,
		"ODEK_E2E":                      true,
		"ODEK_TEST_HOME":                true,
		"ODEK_TEST_KEEP_REAL_HOME":      true,
		"ODEK_SUPPRESS_SANDBOX_WARNING": true,
	}
	for _, kv := range os.Environ() {
		key, val, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "ODEK_") && !keep[key] {
			t.Fatalf("ambient %s=%q leaked into the test process — TestMain scrub is missing it", key, val)
		}
	}
}
