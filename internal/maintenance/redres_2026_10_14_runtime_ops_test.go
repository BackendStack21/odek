package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRED_StaleScheduleHeartbeatsSwept(t *testing.T) {
	home := t.TempDir()
	stale := filepath.Join(home, "schedule-host-4242.json")
	fresh := filepath.Join(home, "schedule-host-4243.json")
	other := filepath.Join(home, "schedule-hostile.txt")
	for _, p := range []string{stale, fresh, other} {
		if err := os.WriteFile(p, []byte(`"2026-01-01T00:00:00Z"`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(other, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := Sweep(context.Background(), home, DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("stale heartbeat file survived the sweep")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("fresh heartbeat file was removed")
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("unrelated file was removed")
	}
}

func TestRED_ScheduleHeartbeatSweepEdges(t *testing.T) {
	if err := sweepScheduleHeartbeats(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Errorf("missing home: %v", err)
	}
	home := t.TempDir()
	dir := filepath.Join(home, "schedule-host-1.json")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(dir, old, old)
	if err := sweepScheduleHeartbeats(home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Error("non-regular entry was removed")
	}
	file := filepath.Join(home, "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sweepScheduleHeartbeats(file); err == nil {
		t.Error("expected error when home is not a directory")
	}
}
