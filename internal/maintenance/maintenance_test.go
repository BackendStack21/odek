package maintenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeFileAt writes content to path (creating parents) and sets its modtime.
func writeFileAt(t *testing.T, path string, content []byte, mod time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

// writeSessionFixture writes a minimal session JSON file with the given
// UpdatedAt so the store's no-index Cleanup fallback picks it up.
func writeSessionFixture(t *testing.T, home, id string, updatedAt time.Time) {
	t.Helper()
	sess := map[string]any{
		"id":         id,
		"created_at": updatedAt,
		"updated_at": updatedAt,
		"model":      "test-model",
		"turns":      1,
		"task":       "fixture",
		"messages":   []any{},
	}
	data, err := json.Marshal(sess)
	if err != nil {
		t.Fatal(err)
	}
	writeFileAt(t, filepath.Join(home, "sessions", id+".json"), data, updatedAt)
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	want := Config{
		Enabled:              true,
		IntervalMinutes:      60,
		SessionsMaxAgeDays:   30,
		AuditMaxAgeDays:      14,
		LogMaxMB:             50,
		PlansMaxAgeDays:      30,
		SkillsSkipMaxAgeDays: 90,
	}
	if cfg != want {
		t.Errorf("DefaultConfig() = %+v, want %+v", cfg, want)
	}
}

func TestSweepSessions(t *testing.T) {
	old := time.Now().Add(-60 * 24 * time.Hour)
	recent := time.Now().Add(-time.Hour)

	tests := []struct {
		name        string
		maxAgeDays  int
		wantRemoved int
		wantKept    []string
	}{
		{"old session removed, recent kept", 30, 1, []string{"20260201-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}},
		{"zero keeps everything", 0, 0, []string{"20200101-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "20260201-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writeSessionFixture(t, home, "20200101-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", old)
			writeSessionFixture(t, home, "20260201-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", recent)

			cfg := DefaultConfig()
			cfg.SessionsMaxAgeDays = tc.maxAgeDays
			// Isolate the session step from the others.
			cfg.AuditMaxAgeDays = 0
			cfg.LogMaxMB = 0
			cfg.PlansMaxAgeDays = 0
			cfg.SkillsSkipMaxAgeDays = 0

			rep, err := Sweep(context.Background(), home, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if rep.SessionsRemoved != tc.wantRemoved {
				t.Errorf("SessionsRemoved = %d, want %d", rep.SessionsRemoved, tc.wantRemoved)
			}
			for _, id := range tc.wantKept {
				if _, err := os.Stat(filepath.Join(home, "sessions", id+".json")); err != nil {
					t.Errorf("session %s should have been kept: %v", id, err)
				}
			}
		})
	}
}

func TestSweepSessionsIdempotent(t *testing.T) {
	home := t.TempDir()
	writeSessionFixture(t, home, "20200101-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", time.Now().Add(-60*24*time.Hour))

	cfg := DefaultConfig()
	cfg.AuditMaxAgeDays = 0
	cfg.LogMaxMB = 0
	cfg.PlansMaxAgeDays = 0
	cfg.SkillsSkipMaxAgeDays = 0

	rep1, err := Sweep(context.Background(), home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep1.SessionsRemoved != 1 {
		t.Fatalf("first sweep removed %d, want 1", rep1.SessionsRemoved)
	}
	rep2, err := Sweep(context.Background(), home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep2.SessionsRemoved != 0 {
		t.Errorf("second sweep removed %d, want 0 (idempotent)", rep2.SessionsRemoved)
	}
}

func TestSweepAudit(t *testing.T) {
	home := t.TempDir()
	auditDir := filepath.Join(home, "sessions", "audit")
	writeFileAt(t, filepath.Join(auditDir, "old.json"), []byte(`{}`), time.Now().Add(-30*24*time.Hour))
	writeFileAt(t, filepath.Join(auditDir, "new.json"), []byte(`{}`), time.Now())

	cfg := DefaultConfig()
	cfg.SessionsMaxAgeDays = 0
	cfg.LogMaxMB = 0
	cfg.PlansMaxAgeDays = 0
	cfg.SkillsSkipMaxAgeDays = 0

	rep, err := Sweep(context.Background(), home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.AuditRemoved != 1 {
		t.Errorf("AuditRemoved = %d, want 1", rep.AuditRemoved)
	}
	if _, err := os.Stat(filepath.Join(auditDir, "old.json")); !os.IsNotExist(err) {
		t.Error("old audit record should have been deleted")
	}
	if _, err := os.Stat(filepath.Join(auditDir, "new.json")); err != nil {
		t.Error("recent audit record should have been kept")
	}
}

func TestSweepAuditDisabledAndMissing(t *testing.T) {
	home := t.TempDir()
	auditDir := filepath.Join(home, "sessions", "audit")
	writeFileAt(t, filepath.Join(auditDir, "old.json"), []byte(`{}`), time.Now().Add(-30*24*time.Hour))

	cfg := DefaultConfig()
	cfg.AuditMaxAgeDays = 0 // keep forever
	cfg.SessionsMaxAgeDays = 0
	cfg.LogMaxMB = 0
	cfg.PlansMaxAgeDays = 0
	cfg.SkillsSkipMaxAgeDays = 0

	rep, err := Sweep(context.Background(), home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.AuditRemoved != 0 {
		t.Errorf("AuditRemoved = %d, want 0 (disabled)", rep.AuditRemoved)
	}

	// Missing audit dir entirely: no error, no removals.
	rep, err = Sweep(context.Background(), t.TempDir(), DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if rep.AuditRemoved != 0 {
		t.Errorf("AuditRemoved = %d, want 0 (missing dir)", rep.AuditRemoved)
	}
}

func TestRotateLogs(t *testing.T) {
	big := make([]byte, 2<<20) // 2 MiB
	for i := range big {
		big[i] = 'x'
	}

	tests := []struct {
		name        string
		logMaxMB    int64
		telegramLog []byte // nil = file absent
		scheduleLog []byte
		staleBackup bool // pre-create telegram.log.1 to verify replacement
		wantRotated int
	}{
		{"oversized telegram.log rotates", 1, big, nil, false, 1},
		{"small logs untouched", 50, []byte("small"), []byte("small"), false, 0},
		{"existing .1 replaced", 1, big, nil, true, 1},
		{"both logs rotate", 1, big, big, false, 2},
		{"zero disables rotation", 0, big, big, false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			now := time.Now()
			if tc.telegramLog != nil {
				writeFileAt(t, filepath.Join(home, "telegram.log"), tc.telegramLog, now)
			}
			if tc.scheduleLog != nil {
				writeFileAt(t, filepath.Join(home, "schedule.log"), tc.scheduleLog, now)
			}
			if tc.staleBackup {
				writeFileAt(t, filepath.Join(home, "telegram.log.1"), []byte("stale"), now)
			}

			cfg := DefaultConfig()
			cfg.LogMaxMB = tc.logMaxMB
			cfg.SessionsMaxAgeDays = 0
			cfg.AuditMaxAgeDays = 0
			cfg.PlansMaxAgeDays = 0
			cfg.SkillsSkipMaxAgeDays = 0

			rep, err := Sweep(context.Background(), home, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if len(rep.LogsRotated) != tc.wantRotated {
				t.Fatalf("LogsRotated = %v, want %d entries", rep.LogsRotated, tc.wantRotated)
			}
			for _, path := range rep.LogsRotated {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatalf("rotated log %s missing: %v", path, err)
				}
				if info.Size() != 0 {
					t.Errorf("rotated log %s not truncated: size %d", path, info.Size())
				}
				backup, err := os.ReadFile(path + ".1")
				if err != nil {
					t.Fatalf("backup %s.1 missing: %v", path, err)
				}
				if len(backup) != len(big) {
					t.Errorf("backup %s.1 size = %d, want %d", path, len(backup), len(big))
				}
			}
			if tc.staleBackup {
				backup, _ := os.ReadFile(filepath.Join(home, "telegram.log.1"))
				if string(backup) == "stale" {
					t.Error("stale .1 backup should have been replaced")
				}
			}
		})
	}
}

func TestSweepPlans(t *testing.T) {
	home := t.TempDir()
	old := time.Now().Add(-60 * 24 * time.Hour)
	recent := time.Now()
	writeFileAt(t, filepath.Join(home, "plans", "chat1", "old.md"), []byte("plan"), old)
	writeFileAt(t, filepath.Join(home, "plans", "chat2", "new.md"), []byte("plan"), recent)
	writeFileAt(t, filepath.Join(home, "plans", "chat2", "notes.txt"), []byte("keep"), old) // non-md untouched

	cfg := DefaultConfig()
	cfg.SessionsMaxAgeDays = 0
	cfg.AuditMaxAgeDays = 0
	cfg.LogMaxMB = 0
	cfg.SkillsSkipMaxAgeDays = 0

	rep, err := Sweep(context.Background(), home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.PlansRemoved != 1 {
		t.Errorf("PlansRemoved = %d, want 1", rep.PlansRemoved)
	}
	if _, err := os.Stat(filepath.Join(home, "plans", "chat1")); !os.IsNotExist(err) {
		t.Error("emptied chat dir should have been removed")
	}
	if _, err := os.Stat(filepath.Join(home, "plans", "chat2", "new.md")); err != nil {
		t.Error("recent plan should have been kept")
	}
	if _, err := os.Stat(filepath.Join(home, "plans", "chat2", "notes.txt")); err != nil {
		t.Error("non-markdown file should have been kept")
	}
}

func TestSweepPlansDisabled(t *testing.T) {
	home := t.TempDir()
	writeFileAt(t, filepath.Join(home, "plans", "chat1", "old.md"), []byte("plan"), time.Now().Add(-60*24*time.Hour))

	cfg := DefaultConfig()
	cfg.PlansMaxAgeDays = 0
	cfg.SessionsMaxAgeDays = 0
	cfg.AuditMaxAgeDays = 0
	cfg.LogMaxMB = 0
	cfg.SkillsSkipMaxAgeDays = 0

	rep, err := Sweep(context.Background(), home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.PlansRemoved != 0 {
		t.Errorf("PlansRemoved = %d, want 0 (disabled)", rep.PlansRemoved)
	}
}

func TestSweepMedia(t *testing.T) {
	home := t.TempDir()
	old := time.Now().Add(-2 * time.Hour)
	recent := time.Now()
	writeFileAt(t, filepath.Join(home, "media", "voice_chat1_x.ogg"), []byte("aaaa"), old)
	writeFileAt(t, filepath.Join(home, "media", "chat1", "photo.jpg"), []byte("bb"), old)
	writeFileAt(t, filepath.Join(home, "media", "chat1", "recent.jpg"), []byte("cc"), recent)

	cfg := DefaultConfig()
	cfg.SessionsMaxAgeDays = 0
	cfg.AuditMaxAgeDays = 0
	cfg.LogMaxMB = 0
	cfg.PlansMaxAgeDays = 0
	cfg.SkillsSkipMaxAgeDays = 0

	rep, err := Sweep(context.Background(), home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.MediaFreedBytes != 6 {
		t.Errorf("MediaFreedBytes = %d, want 6", rep.MediaFreedBytes)
	}
	// The per-chat subdirectory itself must survive.
	info, err := os.Stat(filepath.Join(home, "media", "chat1"))
	if err != nil || !info.IsDir() {
		t.Error("chat subdirectory should never be removed")
	}
	if _, err := os.Stat(filepath.Join(home, "media", "chat1", "recent.jpg")); err != nil {
		t.Error("recent media should have been kept")
	}
}

func TestGCSkipList(t *testing.T) {
	old := time.Now().Add(-120 * 24 * time.Hour).UTC()
	recent := time.Now().UTC()

	tests := []struct {
		name        string
		maxAgeDays  int
		wantRemoved int
		wantKept    []string
	}{
		{"expired entries removed", 90, 1, []string{"new-skill"}},
		{"zero keeps everything", 0, 0, []string{"old-skill", "new-skill"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			skipPath := filepath.Join(home, "skills", ".skipped.json")
			data, err := json.Marshal(map[string]any{
				"skipped": map[string]any{
					"old-skill": map[string]any{"skipped_at": old, "heuristic": "h", "times_skipped": 3},
					"new-skill": map[string]any{"skipped_at": recent, "heuristic": "h", "times_skipped": 1},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			writeFileAt(t, skipPath, data, recent)

			cfg := DefaultConfig()
			cfg.SkillsSkipMaxAgeDays = tc.maxAgeDays
			cfg.SessionsMaxAgeDays = 0
			cfg.AuditMaxAgeDays = 0
			cfg.LogMaxMB = 0
			cfg.PlansMaxAgeDays = 0

			rep, err := Sweep(context.Background(), home, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if rep.SkipsRemoved != tc.wantRemoved {
				t.Errorf("SkipsRemoved = %d, want %d", rep.SkipsRemoved, tc.wantRemoved)
			}

			raw, err := os.ReadFile(skipPath)
			if err != nil {
				t.Fatal(err)
			}
			var parsed struct {
				Skipped map[string]json.RawMessage `json:"skipped"`
			}
			if err := json.Unmarshal(raw, &parsed); err != nil {
				t.Fatal(err)
			}
			if len(parsed.Skipped) != len(tc.wantKept) {
				t.Errorf("skip list has %d entries, want %d", len(parsed.Skipped), len(tc.wantKept))
			}
			for _, name := range tc.wantKept {
				if _, ok := parsed.Skipped[name]; !ok {
					t.Errorf("entry %q should have been kept", name)
				}
			}
		})
	}
}

func TestGCSkipListMissingFile(t *testing.T) {
	rep, err := Sweep(context.Background(), t.TempDir(), DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if rep.SkipsRemoved != 0 {
		t.Errorf("SkipsRemoved = %d, want 0 (missing file)", rep.SkipsRemoved)
	}
}

func TestSweepEmptyHome(t *testing.T) {
	rep, err := Sweep(context.Background(), t.TempDir(), DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if rep.SessionsRemoved != 0 || rep.AuditRemoved != 0 || rep.PlansRemoved != 0 ||
		rep.SkipsRemoved != 0 || rep.MediaFreedBytes != 0 || len(rep.LogsRotated) != 0 {
		t.Errorf("empty home should produce a zero report, got %+v", rep)
	}
}

func TestSweepContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Sweep(ctx, t.TempDir(), DefaultConfig())
	if err == nil {
		t.Error("Sweep with cancelled context should return an error")
	}
}

func TestStartDisabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = false
	// Must return immediately without launching a sweep.
	Start(context.Background(), t.TempDir(), cfg)
}

func TestStartStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cfg := DefaultConfig()
	cfg.IntervalMinutes = 1
	Start(ctx, t.TempDir(), cfg)
	cancel() // janitor goroutine must exit (verified by -race leak detectors)
}
