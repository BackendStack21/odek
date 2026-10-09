package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
)

// A medium-sized file made of many short strings fits under the 10 MiB
// output bound once wrapped and must not be refused by an over-estimated
// wrap budget.
func TestRED_JSONQueryBudgetAcceptsMediumFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "many.json")
	vals := make([]string, 42000)
	for i := range vals {
		vals[i] = "ab"
	}
	raw, _ := json.Marshal(vals)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"path": path})
	out, err := (&jsonQueryTool{dangerousConfig: danger.DangerousConfig{}}).Call(string(args))
	if err != nil || strings.Contains(out, "narrow the query") {
		t.Fatalf("medium file refused: err=%v out=%.200s", err, out)
	}
	t.Logf("wrapped output: %d bytes for %d strings", len(out), len(vals))
	if len(out) > maxFileReadBytes {
		t.Fatalf("output exceeds the tool bound: %d bytes", len(out))
	}
}
