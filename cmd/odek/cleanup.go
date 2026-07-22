package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/maintenance"
	"github.com/BackendStack21/odek/internal/session"
)

// cleanupCmd implements `odek cleanup [--dry-run]`: a one-shot, operator-
// invoked storage sweep over ~/.odek (expired sessions, audit records, plans,
// skill skip entries, oversized logs). It runs the same maintenance.Sweep the
// background janitor uses in long-lived processes (telegram, serve, schedule
// daemon). Like `odek session cleanup`, this deletes data without a
// confirmation prompt — it is a local, operator-run command.
func cleanupCmd(args []string) error {
	dryRun := false
	for _, a := range args {
		switch a {
		case "--dry-run":
			dryRun = true
		case "--help", "-h":
			fmt.Println(`Usage: odek cleanup [--dry-run]

Remove expired odek storage from ~/.odek per the [maintenance] config
section: old sessions, audit records, plans, and skill skip entries, and
rotate oversized logs.

  --dry-run   Show what would be removed without removing anything.`)
			return nil
		default:
			return fmt.Errorf("unknown flag %q for cleanup", a)
		}
	}

	resolved := config.LoadConfig(config.CLIFlags{})
	cfg := maintenanceConfig(resolved)
	home := expandHome("~/.odek")

	if dryRun {
		printCleanupDryRun(home, cfg)
		return nil
	}

	report, err := maintenance.Sweep(context.Background(), home, cfg)
	if err != nil {
		return fmt.Errorf("cleanup: %w", err)
	}
	printCleanupReport(report)
	return nil
}

// maintenanceConfig returns the resolved maintenance section as a
// maintenance.Config. resolved.Maintenance already carries the fully
// defaulted type; this helper is the single mapping point if the resolved
// shape ever diverges.
func maintenanceConfig(resolved config.ResolvedConfig) maintenance.Config {
	return resolved.Maintenance
}

// startStorageMaintenance starts the background storage janitor when the
// resolved maintenance config enables it. Long-lived processes (telegram bot,
// web UI server, schedule daemon) call this at startup; the janitor stops
// when ctx is cancelled.
func startStorageMaintenance(ctx context.Context, resolved config.ResolvedConfig) {
	cfg := maintenanceConfig(resolved)
	if !cfg.Enabled {
		return
	}
	maintenance.Start(ctx, expandHome("~/.odek"), cfg)
	fmt.Fprintf(os.Stderr, "odek: storage maintenance enabled (interval %dm)\n", cfg.IntervalMinutes)
}

// printCleanupReport prints the human-readable result of a sweep, or a quiet
// success line when there was nothing to do.
func printCleanupReport(r maintenance.Report) {
	if r.SessionsRemoved == 0 && r.AuditRemoved == 0 && r.PlansRemoved == 0 &&
		r.SkipsRemoved == 0 && r.MediaFreedBytes == 0 && len(r.LogsRotated) == 0 {
		fmt.Println("Storage is clean — nothing to remove.")
		return
	}
	fmt.Println("Cleanup complete:")
	fmt.Printf("  sessions removed:      %d\n", r.SessionsRemoved)
	fmt.Printf("  audit records removed: %d\n", r.AuditRemoved)
	fmt.Printf("  plans removed:         %d\n", r.PlansRemoved)
	fmt.Printf("  skip entries removed:  %d\n", r.SkipsRemoved)
	fmt.Printf("  media freed:           %s\n", humanBytes(r.MediaFreedBytes))
	for _, p := range r.LogsRotated {
		fmt.Printf("  log rotated:           %s\n", p)
	}
}

// humanBytes formats a byte count for human consumption (e.g. "12.3 MB").
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// ── Dry run ────────────────────────────────────────────────────────────
//
// maintenance.Sweep has no dry-run mode, so the CLI builds the same candidate
// list locally for display only. Media cleanup is not previewed — its
// retention policy lives inside the maintenance package.

// cleanupCandidates lists what a sweep WOULD remove, per category.
type cleanupCandidates struct {
	sessions []string
	audit    []string
	plans    []string
	skips    int
	logs     []string
}

// collectCleanupCandidates enumerates expired files under home without
// removing anything.
func collectCleanupCandidates(home string, cfg maintenance.Config) cleanupCandidates {
	now := time.Now()
	var c cleanupCandidates

	if cfg.SessionsMaxAgeDays > 0 {
		c.sessions = sessionCandidates(home, now.AddDate(0, 0, -cfg.SessionsMaxAgeDays))
	}
	if cfg.AuditMaxAgeDays > 0 {
		c.audit = filesOlderThan(filepath.Join(home, "sessions", "audit"), now.AddDate(0, 0, -cfg.AuditMaxAgeDays), false)
	}
	if cfg.PlansMaxAgeDays > 0 {
		// Plans may be nested per chat (plans/chat<id>/), so walk recursively.
		c.plans = filesOlderThan(filepath.Join(home, "plans"), now.AddDate(0, 0, -cfg.PlansMaxAgeDays), true)
	}
	if cfg.SkillsSkipMaxAgeDays > 0 {
		c.skips = staleSkipEntries(filepath.Join(home, "skills", ".skipped.json"), now.AddDate(0, 0, -cfg.SkillsSkipMaxAgeDays))
	}
	if cfg.LogMaxMB > 0 {
		for _, name := range []string{"schedule.log", "telegram.log"} {
			p := filepath.Join(home, name)
			if info, err := os.Stat(p); err == nil && info.Size() > cfg.LogMaxMB*1024*1024 {
				c.logs = append(c.logs, p)
			}
		}
	}
	return c
}

// sessionCandidates lists session files whose UpdatedAt is before cutoff,
// using the session store's own listing so the dry-run preview matches what
// Store.Cleanup (and therefore the sweep) would delete. Unreadable sessions
// are skipped, mirroring Cleanup.
func sessionCandidates(home string, cutoff time.Time) []string {
	store, err := session.NewStoreWithDir(filepath.Join(home, "sessions"))
	if err != nil {
		return nil
	}
	sessions, err := store.List(0)
	if err != nil {
		return nil
	}
	var out []string
	for _, s := range sessions {
		if s.UpdatedAt.Before(cutoff) {
			out = append(out, store.Path(s.ID))
		}
	}
	return out
}

// filesOlderThan returns the regular files under dir whose modification time
// is before cutoff. Session index/metadata files are excluded. Missing
// directories yield an empty list.
func filesOlderThan(dir string, cutoff time.Time, recursive bool) []string {
	var out []string
	walk := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are not candidates
		}
		if d.IsDir() {
			if path != dir && !recursive {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // never follow or report symlinks
		}
		name := d.Name()
		if name == "index.json" || !strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".md") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			out = append(out, path)
		}
		return nil
	}
	_ = filepath.WalkDir(dir, walk) // missing dir → no candidates
	return out
}

// staleSkipEntries counts entries in the skills .skipped.json file whose
// skipped_at timestamp is before cutoff. An unreadable or malformed file
// counts as zero — the sweep itself decides what to do with it.
func staleSkipEntries(path string, cutoff time.Time) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var file struct {
		Skipped map[string]struct {
			SkippedAt time.Time `json:"skipped_at"`
		} `json:"skipped"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return 0
	}
	n := 0
	for _, e := range file.Skipped {
		if e.SkippedAt.Before(cutoff) {
			n++
		}
	}
	return n
}

// printCleanupDryRun reports the candidate list without removing anything.
func printCleanupDryRun(home string, cfg maintenance.Config) {
	c := collectCleanupCandidates(home, cfg)
	if len(c.sessions) == 0 && len(c.audit) == 0 && len(c.plans) == 0 && c.skips == 0 && len(c.logs) == 0 {
		fmt.Println("Dry run: storage is clean — nothing would be removed.")
		return
	}
	fmt.Println("Dry run — nothing removed. Would remove:")
	fmt.Printf("  sessions:            %d\n", len(c.sessions))
	fmt.Printf("  audit records:       %d\n", len(c.audit))
	fmt.Printf("  plans:               %d\n", len(c.plans))
	fmt.Printf("  skip entries:        %d\n", c.skips)
	for _, p := range c.logs {
		fmt.Printf("  log rotated:         %s\n", p)
	}
}
