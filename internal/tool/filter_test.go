package tool

import (
	"strings"
	"testing"
)

type fakeTool struct{ name string }

func (f fakeTool) Name() string        { return f.name }
func (f fakeTool) Description() string { return "desc of " + f.name }
func (f fakeTool) Schema() any         { return nil }
func (f fakeTool) Call(args string) (string, error) {
	return "", nil
}

// RED tests for the proposed ToolFilter contract.
// These tests will fail until ToolFilter is implemented.

func TestFilterTools_NoFilter(t *testing.T) {
	tools := []Tool{fakeTool{"shell"}, fakeTool{"read_file"}, fakeTool{"web_search"}}
	got := FilterTools(tools, nil, nil, nil)
	if len(got) != 3 {
		t.Fatalf("want 3 tools, got %d", len(got))
	}
}

func TestFilterTools_Whitelist(t *testing.T) {
	tools := []Tool{fakeTool{"shell"}, fakeTool{"read_file"}, fakeTool{"web_search"}}
	got := FilterTools(tools, []string{"web_search", "read_file"}, nil, nil)
	if len(got) != 2 {
		t.Fatalf("want 2 tools, got %d", len(got))
	}
	want := map[string]bool{"read_file": true, "web_search": true}
	for _, tt := range got {
		if !want[tt.Name()] {
			t.Errorf("unexpected tool %q", tt.Name())
		}
	}
}

func TestFilterTools_Blacklist(t *testing.T) {
	tools := []Tool{fakeTool{"shell"}, fakeTool{"read_file"}, fakeTool{"web_search"}}
	got := FilterTools(tools, nil, []string{"shell"}, nil)
	if len(got) != 2 {
		t.Fatalf("want 2 tools, got %d", len(got))
	}
	for _, tt := range got {
		if tt.Name() == "shell" {
			t.Errorf("shell should be disabled")
		}
	}
}

func TestFilterTools_WhitelistAndBlacklist(t *testing.T) {
	tools := []Tool{fakeTool{"shell"}, fakeTool{"read_file"}, fakeTool{"web_search"}}
	got := FilterTools(tools, []string{"web_search", "read_file", "shell"}, []string{"shell"}, nil)
	if len(got) != 2 {
		t.Fatalf("want 2 tools, got %d", len(got))
	}
	for _, tt := range got {
		if tt.Name() == "shell" {
			t.Errorf("shell should be removed from whitelist by blacklist")
		}
	}
}

func TestFilterTools_RequiredToolsPreserved(t *testing.T) {
	tools := []Tool{fakeTool{"shell"}, fakeTool{"send_message"}, fakeTool{"web_search"}}
	got := FilterTools(tools, []string{"web_search"}, []string{"send_message"}, map[string]bool{"send_message": true})
	if len(got) != 2 {
		t.Fatalf("want 2 tools, got %d", len(got))
	}
	found := false
	for _, tt := range got {
		if tt.Name() == "send_message" {
			found = true
		}
	}
	if !found {
		t.Errorf("required send_message must be preserved")
	}
}

func TestFilterTools_UnknownNamesIgnored(t *testing.T) {
	tools := []Tool{fakeTool{"shell"}, fakeTool{"read_file"}}
	got := FilterTools(tools, []string{"shell", "nonexistent"}, []string{"also_missing"}, nil)
	if len(got) != 1 || got[0].Name() != "shell" {
		t.Fatalf("want [shell], got %v", names(got))
	}
}

func TestFilterTools_EmptyEnabled(t *testing.T) {
	tools := []Tool{fakeTool{"shell"}, fakeTool{"read_file"}}
	got := FilterTools(tools, []string{}, nil, nil)
	if len(got) != 0 {
		t.Fatalf("want 0 tools when enabled is explicitly empty, got %d", len(got))
	}
}

func names(tools []Tool) string {
	var out []string
	for _, tt := range tools {
		out = append(out, tt.Name())
	}
	return strings.Join(out, ", ")
}
