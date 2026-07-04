package main

import (
	"reflect"
	"testing"
)

// RED tests for the proposed --tool / --no-tool CLI flags.
// These will fail until parseRunFlags supports them.

func TestParseRunFlags_ToolWhitelist(t *testing.T) {
	f, err := parseRunFlags([]string{
		"--tool", "web_search",
		"--tool", "transcribe",
		"--tool", "vision",
		"hello",
	})
	if err != nil {
		t.Fatalf("parseRunFlags error: %v", err)
	}
	want := []string{"web_search", "transcribe", "vision"}
	if !reflect.DeepEqual(f.ToolsEnabled, want) {
		t.Errorf("ToolsEnabled = %v, want %v", f.ToolsEnabled, want)
	}
}

func TestParseRunFlags_ToolBlacklist(t *testing.T) {
	f, err := parseRunFlags([]string{
		"--no-tool", "shell",
		"--no-tool", "write_file",
		"hello",
	})
	if err != nil {
		t.Fatalf("parseRunFlags error: %v", err)
	}
	want := []string{"shell", "write_file"}
	if !reflect.DeepEqual(f.ToolsDisabled, want) {
		t.Errorf("ToolsDisabled = %v, want %v", f.ToolsDisabled, want)
	}
}

func TestParseRunFlags_ToolMixed(t *testing.T) {
	f, err := parseRunFlags([]string{
		"--tool", "web_search",
		"--no-tool", "shell",
		"--tool", "vision",
		"--no-tool", "delegate_tasks",
		"hello",
	})
	if err != nil {
		t.Fatalf("parseRunFlags error: %v", err)
	}
	wantEnabled := []string{"web_search", "vision"}
	wantDisabled := []string{"shell", "delegate_tasks"}
	if !reflect.DeepEqual(f.ToolsEnabled, wantEnabled) {
		t.Errorf("ToolsEnabled = %v, want %v", f.ToolsEnabled, wantEnabled)
	}
	if !reflect.DeepEqual(f.ToolsDisabled, wantDisabled) {
		t.Errorf("ToolsDisabled = %v, want %v", f.ToolsDisabled, wantDisabled)
	}
}

func TestParseRunFlags_ToolRequiresValue(t *testing.T) {
	_, err := parseRunFlags([]string{"--tool"})
	if err == nil {
		t.Fatal("expected error for --tool without value")
	}
	_, err = parseRunFlags([]string{"--no-tool"})
	if err == nil {
		t.Fatal("expected error for --no-tool without value")
	}
}

func TestParseRunFlags_ToolDefaults(t *testing.T) {
	f, err := parseRunFlags([]string{"hello"})
	if err != nil {
		t.Fatalf("parseRunFlags error: %v", err)
	}
	if len(f.ToolsEnabled) != 0 {
		t.Errorf("ToolsEnabled should default to empty, got %v", f.ToolsEnabled)
	}
	if len(f.ToolsDisabled) != 0 {
		t.Errorf("ToolsDisabled should default to empty, got %v", f.ToolsDisabled)
	}
}
