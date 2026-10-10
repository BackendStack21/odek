package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/BackendStack21/odek/internal/danger"
)

// Episode taint is the memory gate's trust rule: it decides whether an episode
// summarising a session may be auto-recalled into later sessions. It is
// deliberately weaker than the delegation taint (UntrustedIngested): reads
// inside the workspace and local shell commands stay trusted so ordinary
// coding sessions remain recallable. It lives here, beside the store, so every
// save can record it (Session.EpisodeUntrusted) before trimming or compaction
// drops the tool calls it is derived from. memory.DeriveSessionProvenance is
// its consumer.

// EpisodeExternalTools are tools whose RESULT content originates outside the
// agent's trust boundary regardless of their arguments: network fetches,
// search-engine results, opaque transcribed audio, model-described images,
// sub-agent output (a delegated task runs its own tool calls and returns
// attacker-influenceable text), recall of prior-session transcripts
// (which may themselves carry previously-injected content), and parent-side
// reads of sub-agent result artifacts (the model supplies an id, never a
// path; bytes still originated in a child run).
//
// `shell` is deliberately NOT in this set: it is the agent's primary work
// tool and tainting every call would taint nearly every session, making the
// provenance gate useless. Shell commands are classified per call instead
// (see EpisodeShellTools).
// Retired names remain here only to classify persisted historical transcripts.
var EpisodeExternalTools = map[string]bool{
	"browser":        true,
	"http_request":   true,
	"http_batch":     true,
	"transcribe":     true,
	"session_search": true,
	"web_search":     true,
	"vision":         true,
	"delegate_tasks": true,
	"artifact_read":  true,
}

// EpisodeShellTools run a shell command given in their "command" argument.
// They taint the episode only when the danger classifier gives the command a
// network effect (network_egress or network_upload) or cannot classify it
// (unknown): `curl https://evil.example | cat` pulls remote text into the
// transcript, while local builds, tests and file inspection stay trusted.
var EpisodeShellTools = map[string]bool{
	"shell":    true,
	"bg_start": true,
}

// EpisodePathTools are tools that read filesystem content (or structure) into
// the transcript. They taint the episode only when one of their path arguments
// resolves OUTSIDE the workspace (see pathReadEscapes) — reads confined to the
// workspace, or to odek's own ~/.odek state, stay trusted so ordinary coding
// sessions remain recallable.
//
// This must list every tool that surfaces file contents/structure to the
// model. A tool missing here would let an injected agent read a secret into a
// TRUSTED, recallable episode; when adding a new file-reading tool, add it
// here too. Retired names classify historical transcripts; they do not
// register tools or enable execution.
var EpisodePathTools = map[string]bool{
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

// ToolCallTaintsEpisode reports whether a single recorded tool call crossed
// the agent's trust boundary for episode (memory) provenance.
//
//   - MCP adapter calls (name contains "__") always taint — third-party servers
//     return arbitrary text.
//   - EpisodeExternalTools always taint, regardless of arguments.
//   - EpisodePathTools taint only when one of their path arguments resolves
//     OUTSIDE the workspace trust zone (workspace dir, the sandbox /workspace
//     mount, or ~/.odek). Symlinks are resolved so e.g. /etc → /private/etc on
//     macOS cannot disguise an escape. A malformed argument string taints
//     conservatively; absent/empty paths default to the workspace (trusted).
//   - EpisodeShellTools taint when the command has a network or unknown
//     effect (see shellCommandTaints); local commands stay trusted.
//   - Everything else (patch, write_file, …) is trusted.
func ToolCallTaintsEpisode(name, argsJSON string) bool {
	if strings.Contains(name, "__") {
		return true
	}
	if EpisodeExternalTools[name] {
		return true
	}
	if EpisodePathTools[name] {
		return pathReadEscapes(argsJSON)
	}
	if EpisodeShellTools[name] {
		return shellCommandTaints(argsJSON)
	}
	return false
}

// EpisodeTaintSources walks messages and returns, in first-seen order, the
// sources that make an episode summarising them untrusted: a tool call that
// crossed the trust boundary per ToolCallTaintsEpisode, or wrapped external
// content in a non-tool message that no tool call accounts for (see
// externalNonToolIngest). Nil means the messages alone are trusted. Each
// message is judged on its own, so the union over successive slices equals
// the result over their concatenation.
func EpisodeTaintSources(messages []Message) []string {
	var sources []string
	seen := make(map[string]bool)
	mark := func(source string) {
		if !seen[source] {
			seen[source] = true
			sources = append(sources, source)
		}
	}
	for i := range messages {
		m := &messages[i]
		for _, tc := range m.ToolCalls {
			if ToolCallTaintsEpisode(tc.Function.Name, tc.Function.Arguments) {
				mark(tc.Function.Name)
			}
		}
		if m.Role == "tool" {
			continue // tool output is judged by its call above
		}
		for _, src := range WrapperSources(m.Content) {
			if externalNonToolIngest(src) {
				mark(src)
			}
		}
	}
	return sources
}

// messagesTaintEpisode reports whether any message makes the episode
// untrusted. It stops at the first tainting message.
func messagesTaintEpisode(messages []Message) bool {
	for i := range messages {
		m := &messages[i]
		for _, tc := range m.ToolCalls {
			if ToolCallTaintsEpisode(tc.Function.Name, tc.Function.Arguments) {
				return true
			}
		}
		if m.Role == "tool" {
			continue
		}
		for _, src := range WrapperSources(m.Content) {
			if externalNonToolIngest(src) {
				return true
			}
		}
	}
	return false
}

// externalNonToolIngest reports whether a wrapper label in a non-tool message
// is external content that reached the session without a tool call the
// per-tool rule could judge: attachments, @-refs, --ctx files, Telegram
// forwards/voice/captions/media, and anything re-labelled "external:".
// Excluded: engine-derived context (EngineDerivedSource), recalled episodes
// (already gated when they were stored), tool output re-wrapped by the engine
// ("tool:<name>", judged by its call), workspace project instructions
// ("project:…", inside the workspace trust zone like a workspace read) and
// background-job notices ("bg", judged by the bg_start call that started the
// job).
func externalNonToolIngest(src string) bool {
	switch {
	case EngineDerivedSource(src), src == "episode", src == "bg",
		strings.HasPrefix(src, "tool:"), strings.HasPrefix(src, "project:"):
		return false
	}
	return true
}

// shellCommandTaints reports whether a recorded shell command may have pulled
// content from outside the trust boundary: any network_egress,
// network_upload or unknown effect in danger.Analyze. Arguments that do not
// parse taint conservatively; an absent or empty command ran nothing.
func shellCommandTaints(argsJSON string) bool {
	if strings.TrimSpace(argsJSON) == "" {
		return false
	}
	var a struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return true
	}
	if strings.TrimSpace(a.Command) == "" {
		return false
	}
	for _, effect := range danger.Analyze(a.Command).Effects {
		switch effect {
		case danger.NetworkEgress, danger.NetworkUpload, danger.Unknown:
			return true
		}
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
	// An absent path argument means the tool defaulted to the workspace.
	for _, p := range candidates {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if pathOutsideRoots(p, roots) {
			return true
		}
	}
	return false
}

// trustedRoots returns the set of directory prefixes within which a file read
// is considered inside the trust boundary: the current workspace (process cwd
// when the call is judged), the conventional sandbox mount "/workspace", and
// odek's own ~/.odek state directory. Each is included both as-is and
// symlink-resolved.
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
