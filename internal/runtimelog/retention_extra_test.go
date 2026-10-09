package runtimelog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeOldRecord(t *testing.T, path string) {
	t.Helper()
	old := time.Now().Add(-48 * time.Hour).Format(time.RFC3339Nano)
	if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"timestamp":%q}`+"\n", old)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPruneAtStartupVariants(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	writeOldRecord(t, path)
	if n, err := PruneAtStartup(context.Background(), Options{}); err != nil || n != 0 {
		t.Fatalf("empty path n=%d err=%v", n, err)
	}
	if n, err := PruneAtStartup(context.Background(), Options{Path: path, MaxFiles: 0, MaxAgeHours: -5}); err != nil || n != 0 {
		t.Fatalf("no age n=%d err=%v", n, err)
	}
	if n, err := PruneAtStartup(context.Background(), Options{Path: path, MaxFiles: 99, MaxAgeHours: 1}); err != nil || n != 1 {
		t.Fatalf("age n=%d err=%v", n, err)
	}
}

func TestPruneCancelledAndTempFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	writeOldRecord(t, path)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Prune(ctx, path, time.Now(), false); err == nil {
		t.Fatal("expected cancellation error")
	}
	orig := createPruneTemp
	createPruneTemp = func(string, string) (*os.File, error) { return nil, fmt.Errorf("no temp") }
	defer func() { createPruneTemp = orig }()
	if _, err := Prune(context.Background(), path, time.Now(), false); err == nil {
		t.Fatal("expected temp creation error")
	}
}
