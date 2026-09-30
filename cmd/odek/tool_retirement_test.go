package main

import (
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
)

func TestRetiredToolsAbsentFromRegistryAndPrompt(t *testing.T) {
	names := map[string]bool{}
	for _, tool := range builtinTools(danger.DangerousConfig{}, nil, nil, 1, "", toolConfig{}, nil) {
		names[tool.Name()] = true
	}
	for _, name := range []string{"parallel_shell", "batch_patch", "batch_read", "multi_grep", "http_batch"} {
		if names[name] {
			t.Errorf("retired tool %s is registered", name)
		}
		if strings.Contains(defaultSystem, name) {
			t.Errorf("system prompt recommends retired tool %s", name)
		}
	}
	for _, name := range []string{"shell", "patch", "read_file", "search_files", "http_request", "delegate_tasks"} {
		if !names[name] {
			t.Errorf("replacement or retained tool %s is missing", name)
		}
	}
}

func TestRetirementPreservesBackgroundTools(t *testing.T) {
	rt := newBackgroundRuntime(BackgroundSettings{Enabled: true}, t.Name(), "", nil)
	defer rt.Shutdown()
	names := map[string]bool{}
	for _, tool := range builtinTools(danger.DangerousConfig{}, nil, nil, 1, "", toolConfig{}, nil, rt) {
		names[tool.Name()] = true
	}
	for _, name := range []string{"delegate_tasks", "bg_start", "bg_list", "bg_status", "bg_output", "bg_stop"} {
		if !names[name] {
			t.Errorf("retirement removed %s", name)
		}
	}
}
