package tool

import "testing"

func TestFilterTools_DeduplicatesEnabledNames(t *testing.T) {
	tools := []Tool{fakeTool{name: "read_file"}, fakeTool{name: "shell"}}
	got := FilterTools(tools, []string{"read_file", "read_file", "shell", "read_file"}, nil, nil)
	if len(got) != 2 {
		t.Fatalf("got %d tools, want 2; duplicate whitelist entries must not duplicate definitions", len(got))
	}
	if got[0].Name() != "read_file" || got[1].Name() != "shell" {
		t.Fatalf("order/content = [%s, %s], want [read_file, shell]", got[0].Name(), got[1].Name())
	}
	if registry := NewRegistry(got); registry.Get("read_file") == nil || registry.Get("shell") == nil {
		t.Fatal("filtered tools should form a valid registry")
	}
}

func TestFilterTools_RequiredFalseDoesNotForceInclude(t *testing.T) {
	tools := []Tool{fakeTool{name: "shell"}}
	if got := FilterTools(tools, []string{}, nil, map[string]bool{"shell": false}); len(got) != 0 {
		t.Fatalf("false required flag forced %d tools, want none", len(got))
	}
	if got := FilterTools(tools, []string{}, []string{"shell"}, map[string]bool{"shell": true}); len(got) != 1 {
		t.Fatalf("true required flag did not preserve disabled tool: got %d", len(got))
	}
}
