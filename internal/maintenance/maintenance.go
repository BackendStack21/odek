// Package maintenance provides periodic storage hygiene for the odek home
// directory (~/.odek): session retention, audit-record retention, log
// rotation, Telegram plan/media cleanup, and skill skip-list garbage
// collection.
//
// The janitor is safe to run concurrently with a live agent: every pass is
// idempotent, individual steps are independent (one failing step does not
// abort the rest), and deletions are based on file modification times.
package maintenance

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/BackendStack21/odek/internal/session"
)

// mediaMaxAge is the fixed retention for downloaded Telegram media. The
// per-turn telegram.CleanupMedia already uses one hour; the maintenance
// sweep extends the same policy to the per-chat subdirectories it skips.
const mediaMaxAge = time.Hour

// Config controls the storage-maintenance janitor.
type Config struct {
	Enabled              bool
	IntervalMinutes      int   // janitor tick; default 60
	SessionsMaxAgeDays   int   // delete sessions older than this; default 30; 0 = keep forever
	AuditMaxAgeDays      int   // delete audit records older than this; default 14; 0 = keep
	LogMaxMB             int64 // rotate telegram/schedule logs larger than this; default 50; 0 = no rotation
	PlansMaxAgeDays      int   // delete telegram plans older than this; default 30; 0 = keep
	ArtifactsMaxAgeHours int   // delete delegate_tasks artifact subtrees older than this; default 24; 0 = keep
}

// DefaultConfig returns the out-of-the-box maintenance policy.
func DefaultConfig() Config {
	return Config{
		Enabled:              true,
		IntervalMinutes:      60,
		SessionsMaxAgeDays:   30,
		AuditMaxAgeDays:      14,
		LogMaxMB:             50,
		PlansMaxAgeDays:      30,
		ArtifactsMaxAgeHours: 24,
	}
}

// Report summarises what one Sweep pass removed.
type Report struct {
	SessionsRemoved    int
	AuditRemoved       int
	PlansRemoved       int
	ArtifactsRemoved   int
	ArtifactsFreed     int64
	MediaFreedBytes int64
	LogsRotated     []string
}

// Sweep runs one full maintenance pass over the odek home dir (e.g. ~/.odek).
// Idempotent, safe to call concurrently with a running agent.
//
// Every step is independent: a failing step records the first error and the
// remaining steps still run, so one corrupt subtree cannot block the others.
func Sweep(ctx context.Context, home string, cfg Config) (Report, error) {
	var rep Report
	var firstErr error
	fail := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	if cfg.SessionsMaxAgeDays > 0 {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		n, err := sweepSessions(home, cfg.SessionsMaxAgeDays)
		rep.SessionsRemoved = n
		fail(err)
	}

	if cfg.AuditMaxAgeDays > 0 {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		n, err := sweepAudit(home, cfg.AuditMaxAgeDays)
		rep.AuditRemoved = n
		fail(err)
	}

	if cfg.LogMaxMB > 0 {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		rotated, err := rotateLogs(home, cfg.LogMaxMB)
		rep.LogsRotated = rotated
		fail(err)
	}

	if cfg.PlansMaxAgeDays > 0 {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		n, err := sweepPlans(home, cfg.PlansMaxAgeDays)
		rep.PlansRemoved = n
		fail(err)
	}

	if cfg.ArtifactsMaxAgeHours > 0 {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		n, freed, err := sweepArtifacts(home, time.Duration(cfg.ArtifactsMaxAgeHours)*time.Hour)
		rep.ArtifactsRemoved = n
		rep.ArtifactsFreed = freed
		fail(err)
	}

	if err := ctx.Err(); err != nil {
		return rep, err
	}
	freed, err := sweepMedia(home)
	rep.MediaFreedBytes = freed
	fail(err)

	return rep, firstErr
}

// sweepArtifacts removes delegate_tasks artifact subtrees
// (<home>/artifacts/<session_id>/) whose modtime is older than maxAge.
// This is the BACKSTOP for subtrees whose session was already removed by
// other means (crash leftovers, hand deletion) — the primary lifecycle is
// the Store.OnDelete cascade. Returns (subtrees removed, bytes freed).
func sweepArtifacts(home string, maxAge time.Duration) (int, int64, error) {
	if maxAge <= 0 {
		return 0, 0, nil // 0 = keep forever
	}
	dir := filepath.Join(home, "artifacts")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("maintenance: read artifacts dir: %w", err)
	}
	cutoff := time.Now().Add(-maxAge)
	var removed int
	var freed int64
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		size := int64(0)
		_ = filepath.WalkDir(sub, func(_ string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() {
				if fi, fiErr := d.Info(); fiErr == nil {
					size += fi.Size()
				}
			}
			return nil
		})
		if err := os.RemoveAll(sub); err != nil {
			continue // one bad subtree shouldn't block the sweep
		}
		removed++
		freed += size
	}
	return removed, freed, nil
}

// tickInterval, when positive, overrides the configured janitor tick. It is
// a test hook so the janitor loop can be exercised without waiting a full
// minute; production code never sets it.
var tickInterval time.Duration

// Start runs Sweep on an interval until ctx is cancelled. It launches a
// background janitor goroutine and returns immediately. The first sweep runs
// after one interval (not immediately, so process startup isn't slowed).
// A disabled config or non-positive interval is a no-op / falls back to the
// default interval respectively.
func Start(ctx context.Context, home string, cfg Config) {
	if !cfg.Enabled {
		return
	}
	interval := tickInterval
	if interval <= 0 {
		interval = time.Duration(cfg.IntervalMinutes) * time.Minute
		if interval <= 0 {
			interval = time.Duration(DefaultConfig().IntervalMinutes) * time.Minute
		}
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := Sweep(ctx, home, cfg); err != nil && ctx.Err() == nil {
					fmt.Fprintf(os.Stderr, "odek: maintenance sweep: %v\n", err)
				}
			}
		}
	}()
}

// daysAgo returns the cutoff time for a day-based retention policy. Duration
// arithmetic (instead of AddDate) avoids DST-sensitive behaviour where a
// "day" isn't always 24 hours.
func daysAgo(days int) time.Time {
	return time.Now().Add(-time.Duration(days) * 24 * time.Hour)
}

// sweepSessions deletes sessions older than maxAgeDays via the session
// store's own Cleanup, which also scrubs index.json and the vector index.
func sweepSessions(home string, maxAgeDays int) (int, error) {
	store, err := session.NewStoreWithDir(filepath.Join(home, "sessions"))
	if err != nil {
		return 0, err
	}
	return store.Cleanup(daysAgo(maxAgeDays))
}

// sweepAudit deletes audit records (<home>/sessions/audit/*.json) whose
// modtime is older than maxAgeDays. Contents are never parsed.
func sweepAudit(home string, maxAgeDays int) (int, error) {
	dir := filepath.Join(home, "sessions", "audit")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("maintenance: read audit dir: %w", err)
	}
	cutoff := daysAgo(maxAgeDays)
	var removed int
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // skip unreadable entries
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				continue // one bad file shouldn't block the sweep
			}
			removed++
		}
	}
	return removed, nil
}

// rotateLogs rotates <home>/telegram.log (when it exists) and
// <home>/schedule.log when they exceed maxMB: the current log is renamed to
// <name>.1 (replacing any previous generation) and a fresh empty log is
// created. One backup generation only. Returns the rotated log paths.
func rotateLogs(home string, maxMB int64) ([]string, error) {
	limit := maxMB << 20
	var rotated []string
	for _, name := range []string{"telegram.log", "schedule.log", "serve.log"} {
		path := filepath.Join(home, name)
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return rotated, fmt.Errorf("maintenance: stat %s: %w", name, err)
		}
		if info.Size() <= limit {
			continue
		}
		// os.Rename replaces an existing <name>.1 on POSIX filesystems.
		if err := os.Rename(path, path+".1"); err != nil {
			return rotated, fmt.Errorf("maintenance: rotate %s: %w", name, err)
		}
		// Recreate an empty log with the same restrictive permissions the
		// appenders use, so they keep working on a fresh file.
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			return rotated, fmt.Errorf("maintenance: truncate %s: %w", name, err)
		}
		if err := f.Close(); err != nil {
			return rotated, fmt.Errorf("maintenance: truncate %s: %w", name, err)
		}
		rotated = append(rotated, path)
	}
	return rotated, nil
}

// sweepPlans deletes Telegram plan files (<home>/plans/**/*.md) older than
// maxAgeDays and removes chat directories left empty afterwards.
func sweepPlans(home string, maxAgeDays int) (int, error) {
	root := filepath.Join(home, "plans")
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("maintenance: stat plans dir: %w", err)
	}
	cutoff := daysAgo(maxAgeDays)
	var removed int
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries, keep sweeping
		}
		if d.IsDir() {
			if path != root {
				dirs = append(dirs, path)
			}
			return nil
		}
		if filepath.Ext(d.Name()) != ".md" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(path); err == nil {
				removed++
			}
		}
		return nil
	})
	if err != nil {
		return removed, fmt.Errorf("maintenance: walk plans dir: %w", err)
	}
	// Remove chat directories emptied by the sweep. os.Remove fails on
	// non-empty directories, so this is safe for dirs that still hold plans.
	// Deepest first so nested empties collapse in one pass.
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i])
	}
	return removed, nil
}

// sweepMedia deletes downloaded Telegram media files older than mediaMaxAge,
// including the per-chat chat<id>/ subdirectories that the per-turn
// telegram.CleanupMedia skips. Subdirectories themselves are never removed.
// Returns the total number of bytes freed.
func sweepMedia(home string) (int64, error) {
	root := filepath.Join(home, "media")
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("maintenance: stat media dir: %w", err)
	}
	cutoff := time.Now().Add(-mediaMaxAge)
	var freed int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil // never remove directories; skip unreadable entries
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(path); err == nil {
				freed += info.Size()
			}
		}
		return nil
	})
	if err != nil {
		return freed, fmt.Errorf("maintenance: walk media dir: %w", err)
	}
	return freed, nil
}
