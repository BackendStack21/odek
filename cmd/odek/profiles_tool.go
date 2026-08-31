package main

// list_subagent_profiles — built-in discovery tool for operator-defined
// sub-agent capability profiles (P4 follow-up).
//
// The delegate_tasks schema accepts a profile NAME, but profile names only
// exist in the operator's config — the model has no way to know what is
// available, let alone which envelope fits the task. This tool closes that
// gap: it renders the resolved profiles map (including the built-in default
// profile injected by the config pipeline) as JSON the model can pick from.
//
// Security posture: strictly read-only over operator config. Profiles are
// operator-authored only (project-level profiles are stripped at load), so
// the listing can never be poisoned by a cloned repository. The output
// carries permission metadata only — no secrets, no paths, no tool args.

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/BackendStack21/odek/internal/config"
)

// listSubagentProfilesTool renders the resolved capability profiles plus
// the effective default (subagent.default_profile) for model consumption.
type listSubagentProfilesTool struct {
	// profiles is the operator's resolved capability profiles map, as
	// handed to delegateTasksTool. After the config pipeline runs, it
	// always contains the built-in "default" profile unless the operator
	// disabled it (subagent.default_profile="none") or overrode the name.
	profiles map[string]config.ProfileConfig

	// defaultProfile is the resolved Subagent.DefaultProfile value: a
	// profile name, or config.DefaultProfileDisabled when the operator
	// opted out. It is echoed so the model knows which envelope applies
	// when delegate_tasks omits the profile field.
	defaultProfile string
}

func (t *listSubagentProfilesTool) Name() string { return "list_subagent_profiles" }

func (t *listSubagentProfilesTool) Description() string {
	return "List the sub-agent capability profiles available for delegate_tasks: the " +
		"operator-defined profiles (top-level profiles config) plus the built-in default. " +
		"Each entry carries name, description, max_risk ceiling, tool filters and an " +
		"is_default marker; default_profile reports which envelope applies when " +
		"delegate_tasks omits the profile field. Invoke this BEFORE delegate_tasks when " +
		"a task should run under a specific capability profile, then pass the chosen " +
		"name in the profile field. Unknown names fail the task."
}

func (t *listSubagentProfilesTool) Schema() any {
	// No parameters — the listing is static per-run config state.
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

// profileEntry is the JSON shape of one capability profile.
type profileEntry struct {
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	MaxRisk       string   `json:"max_risk,omitempty"`
	ToolsEnabled  []string `json:"tools_enabled,omitempty"`
	ToolsDisabled []string `json:"tools_disabled,omitempty"`
	IsDefault     bool     `json:"is_default,omitempty"`
}

func (t *listSubagentProfilesTool) Call(_ string) (string, error) {
	defaultActive := t.defaultProfile != "" && t.defaultProfile != config.DefaultProfileDisabled

	out := struct {
		Profiles       []profileEntry `json:"profiles"`
		DefaultProfile string         `json:"default_profile"`
	}{
		Profiles:       make([]profileEntry, 0, len(t.profiles)),
		DefaultProfile: t.defaultProfile,
	}

	names := make([]string, 0, len(t.profiles))
	for name := range t.profiles {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		prof := t.profiles[name]
		entry := profileEntry{
			Name:        name,
			Description: prof.Description,
			MaxRisk:     prof.MaxRisk,
			IsDefault:   defaultActive && name == t.defaultProfile,
		}
		if prof.Tools != nil {
			entry.ToolsEnabled = prof.Tools.Enabled
			entry.ToolsDisabled = prof.Tools.Disabled
		}
		out.Profiles = append(out.Profiles, entry)
	}

	buf, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("list_subagent_profiles: %w", err)
	}
	return string(buf), nil
}
