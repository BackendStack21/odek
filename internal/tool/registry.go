// Package tool defines the Tool interface and a thread-safe registry.
package tool

// Tool is the interface the agent uses to discover and invoke capabilities.
// It's identical to the top-level kode.Tool — re-exported here for internal use.
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
