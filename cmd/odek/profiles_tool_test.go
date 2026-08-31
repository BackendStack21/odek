package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/config"
)

// Tests for the list_subagent_profiles built-in tool (P4 follow-up): the
// parent LLM must be able to discover the operator-defined capability
// profiles (plus the built-in default) on demand, so it can pick the right
// profile name for delegate_tasks. The tool is read-only and argument-free.

func newProfilesToolForTest(profiles map[string]config.ProfileConfig, defaultProfile string) *listSubagentProfilesTool {
	return &listSubagentProfilesTool{
		profiles:       profiles,
		defaultProfile: defaultProfile,
	}
}

func decodeProfilesToolOutput(t *testing.T, out string) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("Call output is not valid JSON: %v\nraw: %s", err, out)
	}
	return decoded
}

// TestListSubagentProfiles_NameAndSchema pins the tool's registration
// surface: the name the model invokes and an empty parameter schema (the
// tool takes no arguments).
func TestListSubagentProfiles_NameAndSchema(t *testing.T) {
	tool := newProfilesToolForTest(nil, config.DefaultProfileName)
	if tool.Name() != "list_subagent_profiles" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "list_subagent_profiles")
	}
	if strings.TrimSpace(tool.Description()) == "" {
		t.Error("Description() must be non-empty — it is the model's only usage guide")
	}
	schema, ok := tool.Schema().(map[string]any)
	if !ok {
		t.Fatalf("Schema() = %T, want map[string]any", tool.Schema())
	}
	if schema["type"] != "object" {
		t.Errorf("schema type = %v, want object", schema["type"])
	}
}

// TestListSubagentProfiles_ListsOperatorProfiles covers the core render:
// operator-defined profiles appear with name, description, max_risk and
// tool filters, sorted by name for deterministic output.
func TestListSubagentProfiles_ListsOperatorProfiles(t *testing.T) {
	profiles := map[string]config.ProfileConfig{
		"judge": {
			Description: "Read-only reviewer",
			MaxRisk:     "safe",
			Tools:       &config.ToolConfig{Disabled: []string{"shell", "write_file"}},
		},
		"builder": {
			MaxRisk: "local_write",
			Tools:   &config.ToolConfig{Enabled: []string{"shell", "read_file"}},
		},
	}
	tool := newProfilesToolForTest(profiles, "default")
	out, err := tool.Call("{}")
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	decoded := decodeProfilesToolOutput(t, out)

	if got := decoded["default_profile"]; got != "default" {
		t.Errorf("default_profile = %v, want %q", got, "default")
	}
	raw, ok := decoded["profiles"].([]any)
	if !ok {
		t.Fatalf("profiles = %T, want array", decoded["profiles"])
	}
	if len(raw) != 2 {
		t.Fatalf("profiles len = %d, want 2", len(raw))
	}
	// Sorted by name: builder < judge.
	first, _ := raw[0].(map[string]any)
	second, _ := raw[1].(map[string]any)
	if first["name"] != "builder" || second["name"] != "judge" {
		t.Errorf("profile order = [%v, %v], want [builder judge]", first["name"], second["name"])
	}
	if first["max_risk"] != "local_write" {
		t.Errorf("builder max_risk = %v, want local_write", first["max_risk"])
	}
	if second["description"] != "Read-only reviewer" {
		t.Errorf("judge description = %v, want %q", second["description"], "Read-only reviewer")
	}
	if second["max_risk"] != "safe" {
		t.Errorf("judge max_risk = %v, want safe", second["max_risk"])
	}
	disabled, ok := second["tools_disabled"].([]any)
	if !ok || !reflect.DeepEqual(disabled, []any{"shell", "write_file"}) {
		t.Errorf("judge tools_disabled = %v, want [shell write_file]", second["tools_disabled"])
	}
	enabled, ok := first["tools_enabled"].([]any)
	if !ok || !reflect.DeepEqual(enabled, []any{"shell", "read_file"}) {
		t.Errorf("builder tools_enabled = %v, want [shell read_file]", first["tools_enabled"])
	}
}

// TestListSubagentProfiles_MarksEffectiveDefault covers the is_default
// marker: exactly the entry matching the resolved subagent.default_profile
// is flagged, so the model knows which envelope applies when delegate_tasks
// omits the profile field.
func TestListSubagentProfiles_MarksEffectiveDefault(t *testing.T) {
	profiles := map[string]config.ProfileConfig{
		"default": {MaxRisk: "local_write"},
		"judge":   {MaxRisk: "safe"},
	}
	tool := newProfilesToolForTest(profiles, "default")
	out, err := tool.Call("{}")
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	decoded := decodeProfilesToolOutput(t, out)
	raw := decoded["profiles"].([]any)
	defaults := 0
	for _, e := range raw {
		entry := e.(map[string]any)
		isDefault, _ := entry["is_default"].(bool)
		switch entry["name"] {
		case "default":
			if !isDefault {
				t.Error("built-in default entry must carry is_default=true")
			}
			defaults++
		case "judge":
			if isDefault {
				t.Error("non-default entry must not carry is_default=true")
			}
		}
	}
	if defaults != 1 {
		t.Errorf("is_default marked on %d entries, want exactly 1", defaults)
	}
}

// TestListSubagentProfiles_DisabledDefault covers subagent.default_profile
// = "none": the echo reports "none" and no entry is flagged as default.
func TestListSubagentProfiles_DisabledDefault(t *testing.T) {
	profiles := map[string]config.ProfileConfig{
		"judge": {MaxRisk: "safe"},
	}
	tool := newProfilesToolForTest(profiles, config.DefaultProfileDisabled)
	out, err := tool.Call("{}")
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	decoded := decodeProfilesToolOutput(t, out)
	if got := decoded["default_profile"]; got != "none" {
		t.Errorf("default_profile = %v, want %q", got, "none")
	}
	for _, e := range decoded["profiles"].([]any) {
		entry := e.(map[string]any)
		if isDefault, _ := entry["is_default"].(bool); isDefault {
			t.Errorf("entry %v must not be flagged default when the default is disabled", entry["name"])
		}
	}
}

// TestListSubagentProfiles_EmptyProfilesRendersArray pins that an operator
// with no profiles (and a disabled or absent built-in) still gets a JSON
// array, not null — models handle [] more reliably.
func TestListSubagentProfiles_EmptyProfilesRendersArray(t *testing.T) {
	tool := newProfilesToolForTest(nil, config.DefaultProfileDisabled)
	out, err := tool.Call("{}")
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.Contains(out, `"profiles": []`) && !strings.Contains(out, "\"profiles\":[]") {
		t.Errorf("empty profiles must render as [], got: %s", out)
	}
}

// TestListSubagentProfiles_IgnoresArgs pins argument tolerance: the tool
// takes no parameters, so any args payload (valid or not) must be accepted
// rather than rejected.
func TestListSubagentProfiles_IgnoresArgs(t *testing.T) {
	tool := newProfilesToolForTest(nil, config.DefaultProfileName)
	for _, args := range []string{"{}", "", "not json at all", `{"unexpected":1}`} {
		if _, err := tool.Call(args); err != nil {
			t.Errorf("Call(%q) = %v, want nil (args are ignored)", args, err)
		}
	}
}
