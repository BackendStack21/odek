package main

// Pure-output declarations for first-party built-ins (tool.PureOutput).
//
// A call is pure when its output is derived only from the model's own
// arguments or from operator-controlled runtime state. The loop keeps the
// output's untrusted boundary but labels it engine-derived: no audit ingest,
// no run taint, so trusted delegation stays available after, say, a
// math_eval. Only the types registered in init below are honoured; an
// embedder or MCP tool that implements PureOutputFor is still external.
//
// Audit of the built-ins (when in doubt, a tool is NOT pure):
//
//   - math_eval: pure. Evaluates the model's expression; nothing else is read.
//   - base64: pure only for inline encoding (content set, no decode, no
//     string). File mode reads a file; decoding is wrapped as external by the
//     tool itself (a classic carrier for obfuscated payloads).
//   - list_subagent_profiles: pure. Profiles are operator-only config
//     (project-level profiles are stripped at load).
//   - list_tools: pure only while no MCP server in the listing came from the
//     project ./odek.json; project entries carry repository-controlled
//     command/args strings. Tool names are this process's built-ins.
//   - plan (loop.PlanTool, registered in internal/loop): pure. Renders the
//     model-authored plan.
//
// Not pure: config_view (the resolved view includes values a project
// ./odek.json may set, e.g. tools.disabled names, sandbox image, model),
// diff, json_query, tree, checksum and head_tail (every mode reads the
// filesystem), clarify (a human answer, not model or operator state),
// send_message (relays platform API errors), speak (provider round trip),
// and every file, shell, network, session, memory, skill, background and
// MCP tool. Wrapped tools (untrustedToolWrapper) never forward the marker.

import (
	"encoding/json"

	toolpkg "github.com/BackendStack21/odek/internal/tool"
)

func init() {
	toolpkg.RegisterPureOutputType((*mathEvalTool)(nil))
	toolpkg.RegisterPureOutputType((*base64Tool)(nil))
	toolpkg.RegisterPureOutputType((*listSubagentProfilesTool)(nil))
	toolpkg.RegisterPureOutputType((*listToolsTool)(nil))
}

// PureOutputFor: math_eval reads nothing but its expression.
func (t *mathEvalTool) PureOutputFor(string) bool { return true }

// PureOutputFor mirrors the base64 Call dispatch: decode wins when decode or
// string is set, inline content wins over path, otherwise the file is read.
func (t *base64Tool) PureOutputFor(args string) bool {
	var a base64Args
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return false
	}
	return !a.Decode && a.String == "" && a.Content != ""
}

// PureOutputFor: profiles are operator-only config.
func (t *listSubagentProfilesTool) PureOutputFor(string) bool { return true }

// PureOutputFor: the listing is pure unless it shows a project-introduced
// MCP server, whose command and args come from the repository.
func (t *listToolsTool) PureOutputFor(string) bool {
	for _, s := range t.mcpServers {
		if s.Project {
			return false
		}
	}
	return true
}
