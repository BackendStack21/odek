package main

import (
	"context"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/maintenance"
	"github.com/BackendStack21/odek/internal/runtimelog"
	"github.com/BackendStack21/odek/internal/session"
)

// cleanupCmd implements `odek cleanup [--dry-run]`: a one-shot, operator-
// invoked storage sweep over ~/.odek (expired sessions, audit records, plans,
// oversized logs). It runs the same maintenance.Sweep the
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
section: old sessions, audit records, and plans, and rotate oversized
logs.

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
	cfg := resolved.Maintenance
	log := resolved.Logging
	path := expandHome(log.File)
	if path == "" {
		path = expandHome("~/.odek/runtime.log")
	}
	cfg.RuntimeLog = runtimelog.Options{Path: path, Level: log.Level, MaxFileMB: log.MaxFileMB, MaxFiles: log.MaxFiles, MaxAgeHours: log.MaxAgeHours}
	return cfg
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
		r.RuntimeLogRecordsRemoved == 0 && r.ArtifactsRemoved == 0 && r.MediaFreedBytes == 0 && len(r.LogsRotated) == 0 {
		fmt.Println("Storage is clean — nothing to remove.")
		return
	}
	fmt.Println("Cleanup complete:")
	fmt.Printf("  runtime records removed: %d\n", r.RuntimeLogRecordsRemoved)
	fmt.Printf("  sessions removed:      %d\n", r.SessionsRemoved)
	fmt.Printf("  audit records removed: %d\n", r.AuditRemoved)
	fmt.Printf("  plans removed:         %d\n", r.PlansRemoved)
	fmt.Printf("  artifacts removed:     %d\n", r.ArtifactsRemoved)
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
// retention policy lives inside the maintenance package. Artifact removals
// ARE previewed via maintenance.ArtifactsSweepCandidates (shared with the
// sweep itself); runtime records use the same runtimelog pruning API.

// cleanupCandidates lists what a sweep WOULD remove, per category.
type cleanupCandidates struct {
	sessions          []string
	audit             []string
	plans             []string
	artifacts         []string
	runtimeLogBackups []string
}

// collectCleanupCandidates enumerates expired files under home without
// removing anything.
func collectCleanupCandidates(home string, cfg maintenance.Config) cleanupCandidates {
	now := time.Now()
	var c cleanupCandidates

	if cfg.SessionsMaxAgeDays > 0 {
		c.sessions = sessionCandidates(home, maintenance.DaysAgo(now, cfg.SessionsMaxAgeDays))
	}
	if cfg.AuditMaxAgeDays > 0 {
		c.audit = filesOlderThan(filepath.Join(home, "sessions", "audit"), maintenance.DaysAgo(now, cfg.AuditMaxAgeDays), false)
	}
	if cfg.PlansMaxAgeDays > 0 {
		// Plans may be nested per chat (plans/chat<id>/), so walk recursively.
		c.plans = filesOlderThan(filepath.Join(home, "plans"), maintenance.DaysAgo(now, cfg.PlansMaxAgeDays), true)
	}
	if cfg.ArtifactsMaxAgeHours > 0 {
		// Duration-based cutoff, mirroring sweepArtifacts by construction
		// (both consume the same plan inside maintenance).
		c.artifacts = maintenance.ArtifactsSweepCandidates(home, time.Duration(cfg.ArtifactsMaxAgeHours)*time.Hour)
	}
	if cfg.RuntimeLog.Path != "" {
		c.runtimeLogBackups, _ = runtimelog.ExcessBackupPaths(cfg.RuntimeLog.Path, cfg.RuntimeLog.MaxFiles)
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

// printCleanupDryRun reports the candidate list without removing anything.
func printCleanupDryRun(home string, cfg maintenance.Config) {
	expired := 0
	rotateRuntimeLog := false
	if cfg.RuntimeLog.Path != "" && cfg.RuntimeLog.MaxAgeHours > 0 {
		n, err := runtimelog.PruneWithOptions(context.Background(), cfg.RuntimeLog.Path, time.Now().Add(-time.Duration(maintenance.ClampRetentionHours(cfg.RuntimeLog.MaxAgeHours))*time.Hour), true, cfg.RuntimeLog.MaxFiles)
		if err != nil {
			fmt.Fprintf(os.Stderr, "runtime log preview failed: %v\n", err)
		} else {
			expired = n
		}
	}
	if cfg.RuntimeLog.Path != "" && cfg.RuntimeLog.MaxFileMB > 0 {
		limit := int64(math.MaxInt64)
		if cfg.RuntimeLog.MaxFileMB <= math.MaxInt64/(1<<20) {
			limit = cfg.RuntimeLog.MaxFileMB << 20
		}
		if info, err := os.Lstat(cfg.RuntimeLog.Path); err == nil && info.Mode().IsRegular() && info.Size() > limit {
			rotateRuntimeLog = true
		}
	}

	c := collectCleanupCandidates(home, cfg)
	if expired == 0 && !rotateRuntimeLog && len(c.sessions) == 0 && len(c.audit) == 0 && len(c.plans) == 0 && len(c.artifacts) == 0 && len(c.runtimeLogBackups) == 0 {
		fmt.Println("Dry run: storage is clean — nothing would be removed.")
		return
	}
	fmt.Println("Dry run — nothing removed. Would remove:")
	fmt.Printf("  runtime records expired: %d\n", expired)
	if rotateRuntimeLog {
		fmt.Printf("  runtime log rotated: %s\n", cfg.RuntimeLog.Path)
	}
	if len(c.runtimeLogBackups) > 0 {
		fmt.Printf("  excess runtime log backups: %d\n", len(c.runtimeLogBackups))
	}
	fmt.Printf("  sessions:            %d\n", len(c.sessions))
	fmt.Printf("  audit records:       %d\n", len(c.audit))
	fmt.Printf("  plans:               %d\n", len(c.plans))
	for _, p := range c.artifacts {
		fmt.Printf("  artifact subtree:    %s\n", p)
	}
}
