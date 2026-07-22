package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/maintenance"
)

// seedCleanupHome creates a temp HOME with an ~/.odek tree containing old and
// new files in each category the janitor manages. It returns the home path.
func seedCleanupHome(t *testing.T) (home, odekHome string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	odekHome = filepath.Join(home, ".odek")

	old := time.Now().AddDate(0, 0, -120) // older than every default retention
	writeFile := func(rel string, mod time.Time) string {
		p := filepath.Join(odekHome, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(`{"id":"x"}`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// Sessions are swept by Store.Cleanup, which reads each file's embedded
	// updated_at (and requires the embedded ID to match the filename).
	writeSession := func(id string, updated time.Time) string {
		p := filepath.Join(odekHome, "sessions", id+".json")
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(map[string]any{
			"id":         id,
			"updated_at": updated.UTC().Format(time.RFC3339),
			"messages":   []any{},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// Old entries (must be swept) and new entries (must survive).
	writeSession("20250101-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", old)
	writeSession("29990101-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", time.Now())
	writeFile("sessions/audit/20250101-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.json", old)
	writeFile("sessions/audit/29990101-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.json", time.Now())
	writeFile("plans/chat1/old-plan.md", old)
	writeFile("plans/chat1/new-plan.md", time.Now())

	// Skill skips: one stale, one fresh.
	skillsDir := filepath.Join(odekHome, "skills")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		t.Fatal(err)
	}
	skips := map[string]any{
		"skipped": map[string]any{
			"old-skill": map[string]any{"skipped_at": old.Format(time.RFC3339), "times_skipped": 3},
			"new-skill": map[string]any{"skipped_at": time.Now().Format(time.RFC3339), "times_skipped": 1},
		},
	}
	data, err := json.Marshal(skips)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, ".skipped.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return home, odekHome
}

// TestCleanupCmd_Sweep removes only expired entries and keeps fresh ones.
func TestCleanupCmd_Sweep(t *testing.T) {
	_, odekHome := seedCleanupHome(t)

	if err := cleanupCmd(nil); err != nil {
		t.Fatalf("cleanupCmd() error: %v", err)
	}

	for _, rel := range []string{
		"sessions/20250101-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.json",
		"sessions/audit/20250101-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.json",
		"plans/chat1/old-plan.md",
	} {
		if _, err := os.Stat(filepath.Join(odekHome, rel)); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed, stat err = %v", rel, err)
		}
	}
	for _, rel := range []string{
		"sessions/29990101-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.json",
		"sessions/audit/29990101-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.json",
		"plans/chat1/new-plan.md",
	} {
		if _, err := os.Stat(filepath.Join(odekHome, rel)); err != nil {
			t.Errorf("expected %s to survive: %v", rel, err)
		}
	}
}

// TestCleanupCmd_DryRun reports candidates but removes nothing.
func TestCleanupCmd_DryRun(t *testing.T) {
	_, odekHome := seedCleanupHome(t)

	if err := cleanupCmd([]string{"--dry-run"}); err != nil {
		t.Fatalf("cleanupCmd(--dry-run) error: %v", err)
	}

	for _, rel := range []string{
		"sessions/20250101-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.json",
		"sessions/audit/20250101-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.json",
		"plans/chat1/old-plan.md",
		"plans/chat1/new-plan.md",
	} {
		if _, err := os.Stat(filepath.Join(odekHome, rel)); err != nil {
			t.Errorf("dry-run must not remove %s: %v", rel, err)
		}
	}
}

// TestCleanupDryRun_Candidates exercises the local candidate enumeration used
// by --dry-run against the seeded tree.
func TestCleanupDryRun_Candidates(t *testing.T) {
	_, odekHome := seedCleanupHome(t)

	// Default-shaped config: 30d sessions, 14d audit, 30d plans, 90d skips.
	cfg := maintenanceConfig(config.ResolvedConfig{Maintenance: maintenance.Config{
		SessionsMaxAgeDays:   30,
		AuditMaxAgeDays:      14,
		PlansMaxAgeDays:      30,
		SkillsSkipMaxAgeDays: 90,
	}})
	c := collectCleanupCandidates(odekHome, cfg)
	if len(c.sessions) != 1 {
		t.Errorf("sessions candidates = %d, want 1", len(c.sessions))
	}
	if len(c.audit) != 1 {
		t.Errorf("audit candidates = %d, want 1", len(c.audit))
	}
	if len(c.plans) != 1 {
		t.Errorf("plans candidates = %d, want 1", len(c.plans))
	}
	if c.skips != 1 {
		t.Errorf("skip candidates = %d, want 1", c.skips)
	}
}

// TestMaintenanceConfigMapping pins the resolved-config → maintenance.Config
// field mapping so a shape change in config.ResolvedConfig.Maintenance fails
// here first.
func TestMaintenanceConfigMapping(t *testing.T) {
	resolved := config.ResolvedConfig{Maintenance: maintenance.Config{
		Enabled:              true,
		IntervalMinutes:      60,
		SessionsMaxAgeDays:   30,
		AuditMaxAgeDays:      14,
		LogMaxMB:             50,
		PlansMaxAgeDays:      30,
		SkillsSkipMaxAgeDays: 90,
	}}
	cfg := maintenanceConfig(resolved)
	if !cfg.Enabled || cfg.IntervalMinutes != 60 || cfg.SessionsMaxAgeDays != 30 ||
		cfg.AuditMaxAgeDays != 14 || cfg.LogMaxMB != 50 || cfg.PlansMaxAgeDays != 30 ||
		cfg.SkillsSkipMaxAgeDays != 90 {
		t.Errorf("maintenanceConfig mapping wrong: %+v", cfg)
	}
}

// TestStartStorageMaintenance_Disabled is a no-op when the config disables the
// janitor — it must return immediately without starting anything.
func TestStartStorageMaintenance_Disabled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	resolved := config.ResolvedConfig{Maintenance: maintenance.Config{Enabled: false}}
	done := make(chan struct{})
	go func() {
		startStorageMaintenance(t.Context(), resolved)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("startStorageMaintenance did not return for disabled config")
	}
}

// TestStartStorageMaintenance_Enabled verifies the janitor starts and stops
// cleanly with the context. Behavioural sweep assertions live in the
// cleanupCmd tests; this only proves the wiring does not hang or panic.
func TestStartStorageMaintenance_Enabled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	resolved := config.ResolvedConfig{Maintenance: maintenance.Config{
		Enabled:         true,
		IntervalMinutes: 60,
	}}
	ctx, cancel := context.WithCancel(t.Context())
	startStorageMaintenance(ctx, resolved)
	cancel()
	// Give the janitor goroutine a moment to observe cancellation; there is
	// no join handle, so this is a smoke check only.
	time.Sleep(50 * time.Millisecond)
}
