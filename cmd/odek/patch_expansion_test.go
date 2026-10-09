package main

import (
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
)

// patch replace_all must reject an expanding result before allocating it.
func TestRED_PatchReplaceAllExpansionAllocatesBeforeCap(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	defer os.Chdir(old)
	os.Chdir(dir)
	if err := os.WriteFile("a.txt", []byte(strings.Repeat("a", 1<<20)), 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path": "a.txt", "old_string": "a", "new_string": strings.Repeat("b", 200), "replace_all": true,
	})
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	out, _ := (&patchTool{dangerousConfig: danger.DangerousConfig{}}).Call(string(args))
	runtime.ReadMemStats(&m1)
	if !strings.Contains(out, "too large") {
		t.Fatalf("expected too-large rejection, got %s", out)
	}
	if d := m1.TotalAlloc - m0.TotalAlloc; d > 64<<20 {
		t.Fatalf("patch allocated %d MiB for a result it rejects (cap is 10 MiB); expansion must be bounded before ReplaceAll", d>>20)
	}
}

func TestPatchReplaceAllWithinBoundStillWorks(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	defer os.Chdir(old)
	os.Chdir(dir)
	os.WriteFile("b.txt", []byte("a-a-a"), 0o644)
	args, _ := json.Marshal(map[string]any{
		"path": "b.txt", "old_string": "a", "new_string": "xyz", "replace_all": true,
	})
	if out, _ := (&patchTool{dangerousConfig: danger.DangerousConfig{}}).Call(string(args)); !strings.Contains(out, `"success":true`) {
		t.Fatalf("patch failed: %s", out)
	}
	got, _ := os.ReadFile("b.txt")
	if string(got) != "xyz-xyz-xyz" {
		t.Fatalf("got %q", got)
	}
}
