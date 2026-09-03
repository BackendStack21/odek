package main

// Regression test for the batch_patch early-stop contract: the tool
// description has promised "at the first failing edit the remaining edits
// are skipped (early-stop)" since before v1.41.1. The loop previously
// continued past failures — this pins the documented behavior.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBatchPatchEarlyStopOnFirstFailure(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.txt")
	pathC := filepath.Join(dir, "c.txt")
	if err := os.WriteFile(pathA, []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathC, []byte("gamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &batchPatchTool{restrictToCWD: false}
	args, _ := json.Marshal(map[string]any{
		"patches": []map[string]any{
			{"path": pathA, "old_string": "alpha", "new_string": "ALPHA"},
			// Fails: old_string not present in a.txt after edit 1.
			{"path": pathA, "old_string": "nope", "new_string": "x"},
			{"path": pathC, "old_string": "gamma", "new_string": "GAMMA"},
		},
	})
	out, err := tool.Call(string(args))
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}

	var resp struct {
		Results []struct {
			Path    string `json:"path"`
			Success bool   `json:"success"`
			Error   string `json:"error,omitempty"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, out)
	}
	if len(resp.Results) != 3 {
		t.Fatalf("results = %d entries, want 3", len(resp.Results))
	}
	if !resp.Results[0].Success {
		t.Errorf("patch 1 should succeed: %+v", resp.Results[0])
	}
	if resp.Results[1].Success || resp.Results[1].Error == "" {
		t.Errorf("patch 2 should fail: %+v", resp.Results[1])
	}
	if !strings.Contains(resp.Results[2].Error, "skipped") {
		t.Errorf("patch 3 must be skipped (early-stop), got: %+v", resp.Results[2])
	}
	got, _ := os.ReadFile(pathC)
	if string(got) != "gamma\n" {
		t.Errorf("c.txt was modified despite early-stop: %q", got)
	}
}
