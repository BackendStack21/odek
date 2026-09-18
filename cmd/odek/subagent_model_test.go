package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDelegateTasksPerTaskModelInTaskEnvelope(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "envelopes")
	if err := os.Mkdir(marker, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ODEK_MODEL_MARKER", marker)
	script := filepath.Join(dir, "child")
	const body = `#!/bin/sh
task=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--task" ]; then shift; task="$1"; fi
  shift
done
id=$(sed -n 's/.*"task_id":"\([^"]*\)".*/\1/p' "$task")
cp "$task" "$ODEK_MODEL_MARKER/$id.json"
echo '{"status":"success","summary":"ok"}'
`
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	tool := &delegateTasksTool{maxConcurrency: 3, odekPath: script, timeout: 10 * time.Second, provider: "parent-provider", model: "parent-model", baseURL: "https://provider.invalid/v1"}
	args := `{"tasks":[{"goal":"inherited","provider":"evil","base_url":"https://evil.invalid"},{"goal":"chosen","model":"fast-child"},{"goal":"another","model":"cheap-child"}]}`
	if _, err := tool.Call(args); err != nil {
		t.Fatalf("delegate call failed: %v", err)
	}
	entries, err := os.ReadDir(marker)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("captured %d child envelopes, want 3", len(entries))
	}
	models := make(map[string]bool)
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(marker, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var envelope taskEnvelope
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Provider != "parent-provider" || envelope.BaseURL != "https://provider.invalid/v1" {
			t.Errorf("parent connection fields changed: %+v", envelope)
		}
		var expected string
		switch envelope.Goal {
		case "inherited":
			expected = "parent-model"
		case "chosen":
			expected = "fast-child"
		case "another":
			expected = "cheap-child"
		default:
			t.Errorf("unexpected child goal %q", envelope.Goal)
		}
		if envelope.Model != expected {
			t.Errorf("goal %q got model %q, want %q", envelope.Goal, envelope.Model, expected)
		}
		models[envelope.Model] = true
	}
	for _, model := range []string{"parent-model", "fast-child", "cheap-child"} {
		if !models[model] {
			t.Errorf("child envelopes missing model %q: %v", model, models)
		}
	}
	if tool.model != "parent-model" {
		t.Fatalf("parent model mutated: %q", tool.model)
	}
}

func TestDelegateTasksRejectsInvalidModelNamesBeforeSpawn(t *testing.T) {
	var spawned atomic.Int32
	tool := &delegateTasksTool{maxConcurrency: 1, runTaskFn: func(int, string, string, string, string, string, string, string, string) string {
		spawned.Add(1)
		return `{"status":"success"}`
	}}
	for _, model := range []string{"", "   ", "\nfast", "\tfast", "bad\x00name", strings.Repeat("x", 257)} {
		argsBytes, err := json.Marshal(map[string]any{"tasks": []any{map[string]any{"goal": "test", "model": model}}})
		if err != nil {
			t.Fatal(err)
		}
		result, callErr := tool.Call(string(argsBytes))
		if callErr != nil || !strings.Contains(result, `"error"`) {
			t.Errorf("model %q: result=%s err=%v", model, result, callErr)
		}
	}
	if spawned.Load() != 0 {
		t.Fatalf("invalid model names spawned %d children", spawned.Load())
	}
}
