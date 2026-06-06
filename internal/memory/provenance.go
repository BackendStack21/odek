package memory

import (
	"encoding/json"
	"strings"

	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/llm"
)

// EpisodeProvenance carries the trust signals of the session that
// produced an episode. The default zero value means trusted.
//
// An untrusted episode is one whose originating session ingested
// content from outside the agent's trust boundary (fetched pages, MCP
// tool output, audio transcription, or reads of sensitive system/
// credential paths). Such episodes are stored on disk for audit but are
// NEVER auto-replayed into future sessions — they must be explicitly
// promoted (UserApproved=true) by the user first (see `odek memory
// promote`). This stops a one-shot prompt injection from becoming a
// persistent backdoor.
type EpisodeProvenance struct {
	Untrusted    bool     `json:"untrusted,omitempty"`
	Sources      []string `json:"sources,omitempty"`
	UserApproved bool     `json:"user_approved,omitempty"`
}

// AlwaysExternalTools are tools whose RESULT content originates outside the
// agent's trust boundary regardless of their arguments: network fetches and
// opaque transcribed audio. A session that used any of these always produces
// an untrusted episode.
var AlwaysExternalTools = map[string]bool{
	"browser":    true,
	"http_batch": true,
	"transcribe": true,
}

// PathScopedTools are local file tools that only cross the trust boundary when
// they touch a SENSITIVE path (system or credential locations). Reads confined
// to the workspace — or any other non-sensitive local path — are trusted, so
// ordinary coding sessions no longer taint their episode just for reading
// project files. The per-call decision lives in ToolCallTaints.
var PathScopedTools = map[string]bool{
	"read_file":    true,
	"search_files": true,
	"multi_grep":   true,
}

// UntrustedToolNames is the union of the two categories above. It is retained
// as the canonical "these tools can produce untrusted content" set for
// external references and documentation. The actual per-call decision is made
// by ToolCallTaints, which is argument-aware for the path-scoped tools.
var UntrustedToolNames = func() map[string]bool {
	m := make(map[string]bool, len(AlwaysExternalTools)+len(PathScopedTools))
	for k := range AlwaysExternalTools {
		m[k] = true
	}
	for k := range PathScopedTools {
		m[k] = true
	}
	return m
}()

// ToolCallTaints reports whether a single recorded tool call crossed the
// agent's trust boundary. It is the single source of truth shared by episode
// (memory) and skill (skills) provenance so the two stay in lockstep.
//
//   - MCP adapter calls (name contains "__") always taint — third-party servers
//     return arbitrary text.
//   - AlwaysExternalTools always taint, regardless of arguments.
//   - PathScopedTools taint only when their "path" argument resolves to a
//     sensitive location (danger.ClassifyPath → SystemWrite/Destructive). An
//     empty path means the tool defaults to the workspace (trusted); a
//     malformed argument string is treated conservatively as tainting.
//   - Everything else (shell, patch, write_file, …) is trusted.
func ToolCallTaints(name, argsJSON string) bool {
	if strings.Contains(name, "__") {
		return true
	}
	if AlwaysExternalTools[name] {
		return true
	}
	if PathScopedTools[name] {
		return pathArgIsSensitive(argsJSON)
	}
	return false
}

// pathArgIsSensitive extracts the "path" argument of a path-scoped tool call
// and reports whether it points outside the trust boundary. All three
// path-scoped tools (read_file, search_files, multi_grep) use the "path" key.
func pathArgIsSensitive(argsJSON string) bool {
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return true // can't determine the path → assume the worst
	}
	if strings.TrimSpace(a.Path) == "" {
		return false // empty path defaults to the workspace — trusted
	}
	switch danger.ClassifyPath(a.Path) {
	case danger.SystemWrite, danger.Destructive:
		return true
	default:
		// LocalWrite (in-workspace, /tmp, and other non-sensitive local
		// paths) and anything else is within the trust boundary.
		return false
	}
}

// DeriveProvenance walks a session's structured messages and returns
// the provenance an episode derived from those messages should carry.
// A message taints the episode if it contains a tool call that crossed
// the trust boundary per ToolCallTaints.
func DeriveProvenance(messages []llm.Message) EpisodeProvenance {
	prov := EpisodeProvenance{}
	seen := make(map[string]bool)
	for _, m := range messages {
		for _, tc := range m.ToolCalls {
			if !ToolCallTaints(tc.Function.Name, tc.Function.Arguments) {
				continue
			}
			prov.Untrusted = true
			name := tc.Function.Name
			if !seen[name] {
				seen[name] = true
				prov.Sources = append(prov.Sources, name)
			}
		}
	}
	return prov
}
