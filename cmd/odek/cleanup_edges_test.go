package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/maintenance"
)

// writeCleanupFile writes content to path (creating parents) and backdates
// its modtime by age.
func writeCleanupFile(t *testing.T, path string, content []byte, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	mod := time.Now().Add(-age)
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupCmd_Help(t *testing.T) {
	if err := cleanupCmd([]string{"--help"}); err != nil {
		t.Errorf("cleanupCmd(--help) error: %v", err)
	}
}

func TestCleanupCmd_UnknownFlag(t *testing.T) {
	if err := cleanupCmd([]string{"--bogus"}); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("cleanupCmd(--bogus) error = %v, want an unknown flag error", err)
	}
}

// TestCleanupCmd_SweepError forces the maintenance sweep to fail (the
// sessions path is a regular file) and verifies the command reports it.
func TestCleanupCmd_SweepError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	odekHome := filepath.Join(home, ".odek")
	if err := os.MkdirAll(odekHome, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(odekHome, "sessions"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cleanupCmd(nil); err == nil || !strings.Contains(err.Error(), "cleanup:") {
		t.Errorf("cleanupCmd() error = %v, want a cleanup: error", err)
	}
}

func TestPrintCleanupReport(t *testing.T) {
	// Nothing removed → quiet success line.
	printCleanupReport(maintenance.Report{})
	// Every category populated → full breakdown incl. rotated-log listing.
	printCleanupReport(maintenance.Report{
		SessionsRemoved: 2,
		AuditRemoved:    1,
		PlansRemoved:    3,
		SkipsRemoved:    1,
		MediaFreedBytes: 2048,
		LogsRotated:     []string{filepath.Join("/tmp", "telegram.log")},
	})
}

func TestHumanBytes(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{2048, "2.0 KB"},
		{5 << 20, "5.0 MB"},
		{3 << 30, "3.0 GB"},
		{2 << 40, "2.0 TB"},
	}
	for _, tc := range tests {
		if got := humanBytes(tc.n); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// TestCollectCleanupCandidates_Logs covers the oversized-log candidate branch.
func TestCollectCleanupCandidates_Logs(t *testing.T) {
	home := t.TempDir()
	big := make([]byte, 2<<20) // 2 MiB > 1 MB limit
	writeCleanupFile(t, filepath.Join(home, "schedule.log"), big, time.Hour)

	c := collectCleanupCandidates(home, maintenance.Config{LogMaxMB: 1})
	if len(c.logs) != 1 {
		t.Errorf("logs candidates = %d, want 1", len(c.logs))
	}
}

// TestSessionCandidates_StoreError covers the NewStoreWithDir failure branch:
// the sessions path exists as a regular file.
func TestSessionCandidates_StoreError(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "sessions"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := sessionCandidates(home, time.Now()); got != nil {
		t.Errorf("sessionCandidates = %v, want nil", got)
	}
}

// TestSessionCandidates_ListError covers the store.List failure branch via an
// unreadable sessions directory.
func TestSessionCandidates_ListError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission checks")
	}
	home := t.TempDir()
	sessionsDir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessionsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sessionsDir, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(sessionsDir, 0755) })

	if got := sessionCandidates(home, time.Now()); got != nil {
		t.Errorf("sessionCandidates = %v, want nil", got)
	}
}

// TestFilesOlderThan_NonRecursive covers the SkipDir branch for nested
// directories when recursive is false.
func TestFilesOlderThan_NonRecursive(t *testing.T) {
	dir := t.TempDir()
	writeCleanupFile(t, filepath.Join(dir, "chat1", "old.json"), []byte(`{}`), 48*time.Hour)

	if got := filesOlderThan(dir, time.Now(), false); len(got) != 0 {
		t.Errorf("non-recursive candidates = %v, want none (nested dir skipped)", got)
	}
	if got := filesOlderThan(dir, time.Now(), true); len(got) != 1 {
		t.Errorf("recursive candidates = %v, want 1", got)
	}
}

// TestFilesOlderThan_Symlink covers the symlink skip branch.
func TestFilesOlderThan_Symlink(t *testing.T) {
	dir := t.TempDir()
	writeCleanupFile(t, filepath.Join(dir, "old.json"), []byte(`{}`), 48*time.Hour)
	if err := os.Symlink(filepath.Join(dir, "old.json"), filepath.Join(dir, "link.json")); err != nil {
		t.Fatal(err)
	}

	got := filesOlderThan(dir, time.Now(), false)
	if len(got) != 1 || filepath.Base(got[0]) != "old.json" {
		t.Errorf("candidates = %v, want only old.json (symlink skipped)", got)
	}
}

// TestFilesOlderThan_NameFilter covers the index.json / non-json-md skip branch.
func TestFilesOlderThan_NameFilter(t *testing.T) {
	dir := t.TempDir()
	writeCleanupFile(t, filepath.Join(dir, "index.json"), []byte(`{}`), 48*time.Hour)
	writeCleanupFile(t, filepath.Join(dir, "notes.txt"), []byte("x"), 48*time.Hour)
	writeCleanupFile(t, filepath.Join(dir, "old.json"), []byte(`{}`), 48*time.Hour)

	got := filesOlderThan(dir, time.Now(), false)
	if len(got) != 1 || filepath.Base(got[0]) != "old.json" {
		t.Errorf("candidates = %v, want only old.json", got)
	}
}

// TestFilesOlderThan_UnreadableDir covers the walk-error branch via an
// unreadable subdirectory.
func TestFilesOlderThan_UnreadableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission checks")
	}
	dir := t.TempDir()
	writeCleanupFile(t, filepath.Join(dir, "chat1", "old.json"), []byte(`{}`), 48*time.Hour)
	if err := os.Chmod(filepath.Join(dir, "chat1"), 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(dir, "chat1"), 0755) })

	if got := filesOlderThan(dir, time.Now(), true); len(got) != 0 {
		t.Errorf("candidates = %v, want none (unreadable dir skipped)", got)
	}
}

// TestFilesOlderThan_InfoError covers the Info() failure branch by removing
// the search bit from the directory while keeping it readable.
func TestFilesOlderThan_InfoError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission checks")
	}
	dir := t.TempDir()
	writeCleanupFile(t, filepath.Join(dir, "old.json"), []byte(`{}`), 48*time.Hour)
	if err := os.Chmod(dir, 0400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0755) })

	if got := filesOlderThan(dir, time.Now(), false); len(got) != 0 {
		t.Errorf("candidates = %v, want none (unreadable entries skipped)", got)
	}
}

func TestStaleSkipEntries_Errors(t *testing.T) {
	// Missing file → zero candidates.
	if got := staleSkipEntries(filepath.Join(t.TempDir(), ".skipped.json"), time.Now()); got != 0 {
		t.Errorf("staleSkipEntries(missing) = %d, want 0", got)
	}
	// Malformed JSON → zero candidates.
	dir := t.TempDir()
	bad := filepath.Join(dir, ".skipped.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := staleSkipEntries(bad, time.Now()); got != 0 {
		t.Errorf("staleSkipEntries(malformed) = %d, want 0", got)
	}
}

func TestPrintCleanupDryRun(t *testing.T) {
	// Empty home → quiet "nothing would be removed" line.
	printCleanupDryRun(t.TempDir(), maintenance.DefaultConfig())

	// Oversized log → candidate summary incl. the rotated-log listing.
	home := t.TempDir()
	big := make([]byte, 2<<20)
	writeCleanupFile(t, filepath.Join(home, "telegram.log"), big, time.Hour)
	printCleanupDryRun(home, maintenance.Config{LogMaxMB: 1})
}
