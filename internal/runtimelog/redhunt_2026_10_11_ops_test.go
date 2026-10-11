package runtimelog

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// PreviewAtStartup must count exactly what PruneAtStartup removes, using the
// same MaxAgeHours ceiling, and change nothing.
func TestRED_PreviewAtStartupMatchesPrune(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.log")
	rec := func(age time.Duration, id string) string {
		ts := time.Now().Add(-age).UTC().Format(time.RFC3339Nano)
		return `{"schema":"odek.log/v1","timestamp":"` + ts + `","level":"info","event":"run.started","process_id":"p","record_id":"` + id + `","pid":1}` + "\n"
	}
	data := rec(50000*time.Hour, "p-1") + rec(90000*time.Hour, "p-2") + rec(time.Hour, "p-3")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, err := PreviewAtStartup(context.Background(), Options{}); n != 0 || err != nil {
		t.Fatalf("empty path: %d, %v", n, err)
	}
	if n, err := PreviewAtStartup(context.Background(), Options{Path: path, MaxAgeHours: -5}); n != 0 || err != nil {
		t.Fatalf("non-positive age: %d, %v", n, err)
	}
	for _, age := range []int{80000, 1 << 40} {
		opts := Options{Path: path, MaxFiles: 2, MaxAgeHours: age}
		n, err := PreviewAtStartup(context.Background(), opts)
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("max_age_hours=%d: preview counts %d, want 1 (only the record past the ceiling)", age, n)
		}
		if got, _ := os.ReadFile(path); string(got) != data {
			t.Fatal("preview modified the log")
		}
	}
	removed, err := PruneAtStartup(context.Background(), Options{Path: path, MaxFiles: 2, MaxAgeHours: 1 << 40})
	if err != nil || removed != 1 {
		t.Fatalf("PruneAtStartup removed %d, %v; want 1", removed, err)
	}
}
