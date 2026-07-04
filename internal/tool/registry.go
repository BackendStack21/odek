// Package tool defines the Tool interface and a thread-safe registry.
package tool

// Tool is the interface the agent uses to discover and invoke capabilities.
// It's identical to the top-level odek.Tool — re-exported here for internal use.
type Tool interface {
	Name() string
	Description() string
	Schema() any // JSON Schema for tool parameters
	Call(args string) (string, error)
}

// Registry holds Tools and provides thread-safe lookup.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry creates a Registry from a slice of Tools.
// Duplicate names cause a panic.
func NewRegistry(tools []Tool) *Registry {
	r := &Registry{tools: make(map[string]Tool, len(tools))}
	for _, t := range tools {
		if _, ok := r.tools[t.Name()]; ok {
			panic("tool: duplicate name: " + t.Name())
		}
		r.tools[t.Name()] = t
	}
	return r
}

// FilterTools applies an enabled whitelist and a disabled blacklist to a slice
// of Tools. Required tools are always preserved, even if listed in disabled.
// Unknown names in enabled/disabled are silently ignored.
//
// Resolution:
//   - If enabled is non-nil, the result is the intersection of tools and
//     enabled, minus disabled, plus any required tools.
//   - If enabled is nil (not set), the result is tools minus disabled, plus any
//     required tools that were removed.
func FilterTools(tools []Tool, enabled, disabled []string, required map[string]bool) []Tool {
	enabledSet := make(map[string]bool, len(enabled))
	for _, name := range enabled {
		enabledSet[name] = true
	}
	disabledSet := make(map[string]bool, len(disabled))
	for _, name := range disabled {
		disabledSet[name] = true
	}

	// Build name -> tool map so unknown names don't create placeholder entries.
	byName := make(map[string]Tool, len(tools))
	for _, tt := range tools {
		byName[tt.Name()] = tt
	}

	var out []Tool
	if enabled != nil {
		for name := range enabledSet {
			if tt, ok := byName[name]; ok {
				out = append(out, tt)
			}
		}
	} else {
		out = append(out, tools...)
	}

	// Remove disabled tools (except required ones).
	filtered := out[:0]
	for _, tt := range out {
		name := tt.Name()
		if disabledSet[name] && !required[name] {
			continue
		}
		filtered = append(filtered, tt)
	}

	// Ensure required tools are present even if they were filtered out.
	present := make(map[string]bool, len(filtered))
	for _, tt := range filtered {
		present[tt.Name()] = true
	}
	for name := range required {
		if !present[name] {
			if tt, ok := byName[name]; ok {
				filtered = append(filtered, tt)
			}
		}
	}

	return filtered
}

// Get returns a Tool by name, or nil if not found.
func (r *Registry) Get(name string) Tool {
	return r.tools[name]
}

// Tools returns all registered tools.
func (r *Registry) Tools() []Tool {
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}
