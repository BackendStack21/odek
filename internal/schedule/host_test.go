package schedule

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHostHealthRejectsStaleFutureAndSymlink(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	for name, at := range map[string]time.Time{"recent": now.Add(-10 * time.Second), "stale": now.Add(-time.Minute), "future": now.Add(time.Minute)} {
		data, _ := json.Marshal(at)
		if err := os.WriteFile(filepath.Join(dir, "schedule-host-"+name+".json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "schedule-host-recent.json"), filepath.Join(dir, "schedule-host-link.json")); err != nil {
		t.Fatal(err)
	}
	got := ReadHostHealth(dir, now)
	if got.Status != "recent_heartbeat" || got.Hosts != 1 {
		t.Fatal(got)
	}
	if got := ReadHostHealth(dir, now.Add(2*time.Minute)); got.Status != "unavailable" {
		t.Fatal(got)
	}
}
func TestHeartbeatLifecycle(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStoreAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	scheduler := New(store, nil, nil, Options{})
	stop := scheduler.startHeartbeat(context.Background())
	if h := ReadHostHealth(dir, time.Now()); h.Hosts != 1 {
		t.Fatal(h)
	}
	stop()
	if h := ReadHostHealth(dir, time.Now()); h.Status != "unavailable" {
		t.Fatal(h)
	}
}
