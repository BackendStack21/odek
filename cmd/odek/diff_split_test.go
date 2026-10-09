package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
)

// diff splits both files into a []string BEFORE applying its 10k-line cap, so
// two 10 MiB newline-only files (allowed by the byte cap) allocate ~320 MiB
// of slice headers just to be rejected.
func TestRED_DiffSplitsBeforeLineCap(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	blob := []byte(strings.Repeat("\n", maxFileReadBytes))
	os.WriteFile(a, blob, 0o644)
	os.WriteFile(b, blob, 0o644)
	args, _ := json.Marshal(map[string]string{"path_a": a, "path_b": b})
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	out, _ := (&diffTool{dangerousConfig: danger.DangerousConfig{}}).Call(string(args))
	runtime.ReadMemStats(&m1)
	if !strings.Contains(out, "too large") {
		t.Fatalf("expected rejection, got %.200s", out)
	}
	if d := m1.TotalAlloc - m0.TotalAlloc; d > 150<<20 {
		t.Fatalf("diff allocated %d MiB for two 10 MiB inputs it rejects; cap must be checked before splitting", d>>20)
	}
}
