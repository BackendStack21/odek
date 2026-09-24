package loop

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// Unknown effects form a barrier. Native file operations use canonical path
// identity so a read after a write sees that write, including through aliases.
type callEffects struct {
	reads   []string
	writes  []string
	unknown bool
}

func resourceIdentity(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	probe := abs
	var tail []string
	for {
		if real, err := filepath.EvalSymlinks(probe); err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				real = filepath.Join(real, tail[i])
			}
			return real
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return abs
		}
		tail = append(tail, filepath.Base(probe))
		probe = parent
	}
}

func effectsFor(tc session.ToolCall) callEffects {
	name := tc.Function.Name
	var args struct {
		Path  string `json:"path"`
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
		Patches []struct {
			Path string `json:"path"`
		} `json:"patches"`
	}
	if json.Unmarshal([]byte(tc.Function.Arguments), &args) != nil {
		return callEffects{unknown: true}
	}
	var paths []string
	if args.Path != "" {
		paths = append(paths, resourceIdentity(args.Path))
	}
	for _, f := range args.Files {
		if f.Path != "" {
			paths = append(paths, resourceIdentity(f.Path))
		}
	}
	for _, f := range args.Patches {
		if f.Path != "" {
			paths = append(paths, resourceIdentity(f.Path))
		}
	}
	if mutatingToolNames[name] {
		if len(paths) == 0 {
			return callEffects{unknown: true}
		}
		return callEffects{writes: paths}
	}
	if readCheckToolNames[name] || name == "head_tail" || name == "word_count" || name == "base64" || name == "tr" || name == "sort" {
		// Queries without explicit paths can read the whole workspace.
		if len(paths) == 0 {
			return callEffects{reads: []string{"*"}}
		}
		return callEffects{reads: paths}
	}
	switch name {
	case "math_eval", "web_search", "browser", "config_view", "list_tools", "session_search", "bg_status", "bg_output", "bg_list", "bg_stop":
		return callEffects{}
	}
	return callEffects{unknown: true}
}

func (e *Engine) executionEffects(tc session.ToolCall) callEffects {
	if provider, ok := e.registry.Get(tc.Function.Name).(interface{ Effects(string) tool.Effects }); ok {
		declared := provider.Effects(tc.Function.Arguments)
		if declared.Unknown && len(declared.Reads) == 0 && len(declared.Writes) == 0 {
			return effectsFor(tc)
		}
		fx := callEffects{unknown: declared.Unknown}
		for _, p := range declared.Reads {
			if p == "" {
				fx.unknown = true
			} else {
				fx.reads = append(fx.reads, resourceIdentity(p))
			}
		}
		for _, p := range declared.Writes {
			if p == "" {
				fx.unknown = true
			} else {
				fx.writes = append(fx.writes, resourceIdentity(p))
			}
		}
		return fx
	}
	return effectsFor(tc)
}

func pathsOverlap(a, b string) bool {
	return a == "*" || b == "*" || a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator))
}
func effectConflict(a, b callEffects) bool {
	if a.unknown || b.unknown {
		return true
	}
	for _, w := range a.writes {
		for _, p := range append(append([]string{}, b.reads...), b.writes...) {
			if pathsOverlap(w, p) {
				return true
			}
		}
	}
	for _, w := range b.writes {
		for _, p := range a.reads {
			if pathsOverlap(w, p) {
				return true
			}
		}
	}
	return false
}

func (e *Engine) recordVerificationEffects(name, args string, failed bool) {
	if failed {
		return
	}
	tc := session.ToolCall{}
	tc.Function.Name = name
	tc.Function.Arguments = args
	fx := effectsFor(tc)
	if e.pendingVerification == nil {
		e.pendingVerification = make(map[string]bool)
	}
	if len(fx.writes) > 0 {
		for _, p := range fx.writes {
			e.pendingVerification[p] = true
		}
	}
	if fx.unknown && (name == "shell" || name == "parallel_shell" || name == "terminal") {
		e.pendingVerification["*"] = true
	}
	// Only checks of concrete changed resources satisfy verification. Broad
	// search or unrelated reads cannot erase unverified effects.
	for _, p := range fx.reads {
		if p != "*" {
			delete(e.pendingVerification, p)
		}
	}
	e.sawReadAfterMutation = len(e.pendingVerification) == 0
}
