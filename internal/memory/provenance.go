package memory

import (
	"github.com/BackendStack21/odek/internal/session"
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

// The per-tool trust rule lives in the session package so every save can
// record it before trimming drops the tool calls (session.EpisodeUntrusted).
// These names are kept for callers and documentation that refer to them here.
var (
	// AlwaysExternalTools taint regardless of arguments
	// (session.EpisodeExternalTools).
	AlwaysExternalTools = session.EpisodeExternalTools
	// ShellCommandTools taint when the command has a network or unknown
	// effect (session.EpisodeShellTools).
	ShellCommandTools = session.EpisodeShellTools
	// PathReadingTools taint when a path argument resolves outside the
	// workspace (session.EpisodePathTools).
	PathReadingTools = session.EpisodePathTools
)

// UntrustedToolNames is the union of AlwaysExternalTools and
// PathReadingTools. It is retained as the canonical "these tools can produce
// untrusted content" set for external references and documentation. The
// actual per-call decision is made by ToolCallTaints, which is argument-aware
// for the path-reading and shell tools.
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
// agent's trust boundary for episode provenance. See
// session.ToolCallTaintsEpisode for the rule.
func ToolCallTaints(name, argsJSON string) bool {
	return session.ToolCallTaintsEpisode(name, argsJSON)
}

// DeriveSessionProvenance returns the provenance an episode summarising sess
// must carry. It is the only provenance derivation for episode writers: it
// combines the session's current history with its sticky EpisodeUntrusted
// flag, which records taint from tool calls and ingests that trimming,
// write-time size trimming or compaction have since removed from the history.
// A nil session has unknown provenance and is untrusted.
func DeriveSessionProvenance(sess *session.Session) EpisodeProvenance {
	if sess == nil {
		return EpisodeProvenance{Untrusted: true, Sources: []string{"unknown_provenance"}}
	}
	prov := deriveMessagesProvenance(sess.Messages)
	if sess.EpisodeUntrusted && !prov.Untrusted {
		prov.Untrusted = true
		prov.Sources = append(prov.Sources, "trimmed_history")
	}
	return prov
}

// deriveMessagesProvenance walks structured messages and returns the
// provenance their content alone implies (session.EpisodeTaintSources). It
// misses taint whose evidence has left the history, so episode writers use
// DeriveSessionProvenance.
func deriveMessagesProvenance(messages []session.Message) EpisodeProvenance {
	sources := session.EpisodeTaintSources(messages)
	if len(sources) == 0 {
		return EpisodeProvenance{}
	}
	return EpisodeProvenance{Untrusted: true, Sources: sources}
}
