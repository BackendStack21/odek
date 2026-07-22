package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/BackendStack21/odek/internal/llm"
)

// EpisodeProvenance carries the trust signals of the session that
// produced an episode. The default zero value means trusted.
//
// An untrusted episode is one whose originating session ingested
// content from outside the agent's trust boundary (fetched pages, MCP
// tool output, audio transcription, prior-session recall, or reads of
// files outside the workspace). Such episodes are stored on disk for
// audit but are NEVER auto-replayed into future sessions — they must be
// explicitly promoted (UserApproved=true) by the user first (see
// `odek memory promote`). This stops a one-shot prompt injection from
// becoming a persistent backdoor.
//
// AutoApproved is the opt-in escape valve: when the operator sets
// memory.auto_approve_episodes=true, untrusted episodes are stamped
// AutoApproved at creation so they are recalled without a manual promote.
// It is kept distinct from UserApproved so the audit trail still shows the
// approval was automatic (policy) rather than a human decision; Untrusted and
// Sources remain recorded either way.
type EpisodeProvenance struct {
	Untrusted    bool     `json:"untrusted,omitempty"`
	Sources      []string `json:"sources,omitempty"`
	UserApproved bool     `json:"user_approved,omitempty"`
	AutoApproved bool     `json:"auto_approved,omitempty"`
}

// AlwaysExternalTools are tools whose RESULT content originates outside the
// agent's trust boundary regardless of their arguments: network fetches,
// search-engine results, opaque transcribed audio, model-described images,
// sub-agent output (a delegated task runs its own tool calls and returns
// attacker-influenceable text), and recall of prior-session transcripts
// (which may themselves carry previously-injected content).
//
// `shell` is deliberately NOT in this set even though its output can carry
// untrusted bytes: it is the agent's primary work tool and tainting it would
// taint nearly every session, making the provenance gate useless.
var AlwaysExternalTools = map[string]bool{
	"browser":        true,
	"http_batch":     true,
	"transcribe":     true,
	"session_search": true,
	"web_search":     true,
	"vision":         true,
	"delegate_tasks": true,
}

// PathReadingTools are tools that read filesystem content (or structure) into
// the transcript. They taint the episode only when one of their path arguments
// resolves OUTSIDE the workspace (see pathReadEscapes) — reads confined to the
// workspace, or to odek's own ~/.odek state, stay trusted so ordinary coding
// sessions remain recallable.
//
// This must list every tool that surfaces file contents/structure to the
// model. A tool missing here would let an injected agent read a secret into a
// TRUSTED, recallable episode; when adding a new file-reading tool, add it
// here too.
var PathReadingTools = map[string]bool{
	"read_file":    true,
	"search_files": true,
	"multi_grep":   true,
	"batch_read":   true,
	"json_query":   true,
	"head_tail":    true,
	"count_lines":  true,
	"checksum":     true,
	"word_count":   true,
	"sort":         true,
	"tr":           true,
	"diff":         true,
	"file_info":    true,
	"glob":         true,
	"tree":         true,
	"base64":       true,
}

// UntrustedToolNames is the union of the two categories above. It is retained
// as the canonical "these tools can produce untrusted content" set for
// external references and documentation. The actual per-call decision is made
// by ToolCallTaints, which is argument-aware for the path-reading tools.
var UntrustedToolNames = func() map[string]bool {
	m := make(map[string]bool, len(AlwaysExternalTools)+len(PathReadingTools))
	for k := range AlwaysExternalTools {
		m[k] = true
	}
	for k := range PathReadingTools {
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
//   - PathReadingTools taint only when one of their path arguments resolves
//     OUTSIDE the workspace trust zone (workspace dir, the sandbox /workspace
//     mount, or ~/.odek). Symlinks are resolved so e.g. /etc → /private/etc on
//     macOS cannot disguise an escape. A malformed argument string taints
//     conservatively; absent/empty paths default to the workspace (trusted).
//   - Everything else (shell, patch, write_file, …) is trusted.
func ToolCallTaints(name, argsJSON string) bool {
	if strings.Contains(name, "__") {
		return true
	}
	if AlwaysExternalTools[name] {
		return true
	}
	if PathReadingTools[name] {
		return pathReadEscapes(argsJSON)
	}
	return false
}

// pathReadEscapes extracts every filesystem path argument from a path-reading
// tool call and reports whether any of them resolves outside the workspace
// trust zone. The known path-bearing argument shapes across odek's file tools
// are: "path", "path_a"/"path_b" (diff), and a "files":[{"path":…}] array
// (batch_read, head_tail, count_lines, checksum, word_count, sort).
func pathReadEscapes(argsJSON string) bool {
	var a struct {
		Path  string `json:"path"`
		PathA string `json:"path_a"`
		PathB string `json:"path_b"`
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return true // can't determine the paths → assume the worst
	}

	roots := trustedRoots()
	candidates := []string{a.Path, a.PathA, a.PathB}
	for _, f := range a.Files {
		candidates = append(candidates, f.Path)
	}

	sawPath := false
	for _, p := range candidates {
		if strings.TrimSpace(p) == "" {
			continue
		}
		sawPath = true
		if pathOutsideRoots(p, roots) {
			return true
		}
	}
	// No path argument at all → the tool defaulted to the workspace (trusted).
	_ = sawPath
	return false
}

// trustedRoots returns the set of directory prefixes within which a file read
// is considered inside the trust boundary: the current workspace (process cwd
// at session-end), the conventional sandbox mount "/workspace", and odek's own
// ~/.odek state directory. Each is included both as-is and symlink-resolved.
func trustedRoots() []string {
	var roots []string
	add := func(p string) {
		if p == "" {
			return
		}
		c := filepath.Clean(p)
		roots = append(roots, c)
		if r, err := filepath.EvalSymlinks(c); err == nil && r != c {
			roots = append(roots, r)
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		add(cwd)
	}
	add("/workspace") // sandbox mount point (see internal/sandbox)
	if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, ".odek"))
	}
	return roots
}

// pathOutsideRoots reports whether p resolves outside every trusted root.
// The path is checked both as filepath.Abs(p) and symlink-resolved, so a
// symlinked sensitive path (e.g. /etc → /private/etc) cannot evade detection.
func pathOutsideRoots(p string, roots []string) bool {
	abs, err := filepath.Abs(p)
	if err != nil {
		return true // unresolvable → conservative
	}
	abs = filepath.Clean(abs)
	cands := []string{abs}
	if r, err := filepath.EvalSymlinks(abs); err == nil && r != abs {
		cands = append(cands, r)
	}
	for _, c := range cands {
		for _, root := range roots {
			if c == root || strings.HasPrefix(c, root+string(filepath.Separator)) {
				return false // inside a trusted root
			}
		}
	}
	return true
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
