package memory

import (
	"strings"

	"github.com/BackendStack21/odek/internal/llm"
)

// EpisodeProvenance carries the trust signals of the session that
// produced an episode. The default zero value means trusted.
//
// An untrusted episode is one whose originating session ingested
// content from outside the agent's trust boundary (fetched pages, files
// outside the working directory, MCP tool output, audio transcription).
// Such episodes are stored on disk for audit but are NEVER auto-
// replayed into future sessions — they must be explicitly promoted
// (UserApproved=true) by the user first. This stops a one-shot prompt
// injection from becoming a persistent backdoor.
type EpisodeProvenance struct {
	Untrusted    bool     `json:"untrusted,omitempty"`
	Sources      []string `json:"sources,omitempty"`
	UserApproved bool     `json:"user_approved,omitempty"`
}

// UntrustedToolNames is the canonical set of tools whose results come from
// outside the agent's trust boundary. Any tool whose output reaches the
// model wrapped in <untrusted_content> belongs here.
//
// This is the single source of truth — skills/selfimprove.go imports it
// to derive skill provenance from the same definition.
var UntrustedToolNames = map[string]bool{
	"browser":      true,
	"http_batch":   true,
	"transcribe":   true,
	"read_file":    true, // conservative: covers /etc/, $HOME/.ssh, etc.
	"search_files": true,
	"multi_grep":   true,
}

// DeriveProvenance walks a session's structured messages and returns
// the provenance an episode derived from those messages should carry.
// A message qualifies as untrusted if it contains a tool call whose
// name is in untrustedToolNames OR follows the MCP adapter naming
// convention (server__tool).
func DeriveProvenance(messages []llm.Message) EpisodeProvenance {
	prov := EpisodeProvenance{}
	seen := make(map[string]bool)
	for _, m := range messages {
		for _, tc := range m.ToolCalls {
			name := tc.Function.Name
			tainted := UntrustedToolNames[name] || strings.Contains(name, "__")
			if !tainted {
				continue
			}
			prov.Untrusted = true
			if !seen[name] {
				seen[name] = true
				prov.Sources = append(prov.Sources, name)
			}
		}
	}
	return prov
}
