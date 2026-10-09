package session

import "strings"

// UntrustedWrapperPrefix opens every untrusted-content boundary odek writes
// around externally-sourced text (tool output, @-refs, attachments, …).
const UntrustedWrapperPrefix = "<untrusted_content_"

// TrimmedUntrustedMarker is the marker the loop writes in place of a trimmed
// tool body that carried untrusted content (format argument: bytes dropped).
// It keeps the history recognisably tainted after the body itself is gone.
const TrimmedUntrustedMarker = "[tool output trimmed: untrusted content, %d bytes dropped to fit context budget]"

const trimmedUntrustedPrefix = "[tool output trimmed: untrusted content,"

// engineDerivedSources are the wrapper source labels the engine itself uses
// for context it derives from the conversation or from operator-owned state:
// the rolling digest, the plan, the memory block, re-wrapped persisted system
// messages, progress summaries, effect evidence, skill and extended-memory
// recall and the return-after-break summary. Episode recall is deliberately
// NOT on the list: an episode summarises another session and is admitted by
// the memory gate's per-tool rule (ToolCallTaintsEpisode), which trusts
// workspace reads and local shell commands — weaker than this taint — so
// recalling one taints the run. That context is wrapped too (a
// summary can paraphrase hostile text), but it is not a new external ingest:
// whatever it derives from was already taint-tracked when it entered the
// history, and the session flag keeps that taint. Tool-side wraps can never
// carry these labels (cmd/odek re-labels them), so they do not taint.
var engineDerivedSources = map[string]bool{
	"compaction":         true, // rolling conversation digest
	"plan":               true, // persisted plan body
	"plan_remaining":     true, // remaining-steps block
	"memory":             true, // memory prompt block
	"persisted_system":   true, // re-wrapped persisted system message
	"progress_summary":   true, // partial-progress / iteration-budget summary
	"completed_effects":  true, // effect evidence ledger
	"skill":              true, // trusted skill bodies (reviewed skills only)
	"extended_memory":    true, // extended-memory recall (wrappers stripped at extraction)
	"return_after_break": true, // resume summary from extended memory
}

// EngineDerivedSource reports whether source is one of the engine's own
// context labels (see engineDerivedSources). Every other label — tool output
// ("tool:<name>", paths, "$ <cmd>", URLs), MCP ("mcp:…"), @-refs, attachments,
// --ctx, session_search, sub-agent results, background notices, Telegram
// media and forwards, and project instructions ("project:AGENTS.md") — is
// external content and taints. Labels starting with PureToolSourcePrefix are
// engine-derived too.
func EngineDerivedSource(source string) bool {
	return engineDerivedSources[source] || strings.HasPrefix(source, PureToolSourcePrefix)
}

// PureToolSourcePrefix labels the loop's wrapper around the output of a
// first-party tool call whose output is derived only from the model's own
// arguments or operator-controlled state (tool.OutputIsPure), e.g.
// "pure_tool:math_eval". The output keeps its untrusted boundary, but the
// label is engine-derived: it records no ingest and does not taint. Like the
// other engine labels, tool-side code cannot mint it (cmd/odek re-labels it).
const PureToolSourcePrefix = "pure_tool:"

// WrapperSources returns the source label of every real untrusted-content
// wrapper in s, in order (engine-derived labels included). Prose naming the
// tag and neutralised tags nested in a wrapper body are not wrappers.
func WrapperSources(s string) []string {
	var out []string
	const attr = ` source="`
	for {
		i := strings.Index(s, UntrustedWrapperPrefix)
		if i < 0 {
			return out
		}
		s = s[i+len(UntrustedWrapperPrefix):]
		n := 0
		for n < len(s) && isLowerHex(s[n]) {
			n++
		}
		if n == 0 || !strings.HasPrefix(s[n:], attr) {
			continue
		}
		rest := s[n+len(attr):]
		end := strings.Index(rest, `">`)
		if end < 0 {
			return append(out, "") // malformed: callers treat it as external
		}
		out = append(out, rest[:end])
		s = rest[end:]
	}
}

// ContentCarriesUntrusted reports whether s holds a wrapper around EXTERNAL
// content (`<untrusted_content_<hex nonce> source="<label>"` with a label that
// is not engine-derived), or the marker left where trimmed untrusted content
// used to be. Prose that merely names the tag — the security pillar in every
// system prompt says "<untrusted_content_...>" — is not a wrapper, and tags
// nested inside a wrapper body are neutralised by the wrappers, so neither
// counts.
func ContentCarriesUntrusted(s string) bool {
	if strings.Contains(s, trimmedUntrustedPrefix) {
		return true
	}
	for _, src := range WrapperSources(s) {
		if !EngineDerivedSource(src) {
			return true
		}
	}
	return false
}

func isLowerHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
}

// MessagesCarryUntrusted reports whether any message content carries
// untrusted content (see ContentCarriesUntrusted).
func MessagesCarryUntrusted(msgs []Message) bool {
	for i := range msgs {
		if ContentCarriesUntrusted(msgs[i].Content) {
			return true
		}
	}
	return false
}
