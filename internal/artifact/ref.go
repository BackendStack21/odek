// Package artifact implements parsing, validation, and model-facing rendering
// of odek.artifact-ref/v1 references carried inside odek.tool-result/v1
// envelopes (see docs/EXTENSIONS.md).
//
// An artifact ref points at a file produced by an extension (MCP) server whose
// full output is too large or too binary for the model context. Refs are
// validated fail-closed against the server's configured artifact roots, and
// artifact content is NEVER read into the model context — the model sees only
// the compact envelope text plus per-artifact metadata lines (id, media type,
// size, short hash prefix, summary). Raw absolute paths are never rendered.
package artifact

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Schema names carried in the "schema" field of structured payloads. These
// are the canonical definitions; internal/mcpclient aliases them so the
// extension contract has a single source of truth.
const (
	// SchemaToolResult names the odek tool-result envelope: a JSON text
	// content item with a compact model-facing "text" plus out-of-band
	// artifact references.
	SchemaToolResult = "odek.tool-result/v1"

	// SchemaArtifactRef names a single artifact reference inside a
	// tool-result envelope.
	SchemaArtifactRef = "odek.artifact-ref/v1"
)

// MaxArtifactsPerEnvelope caps how many artifact refs one tool-result
// envelope may carry. Every validated ref becomes a model-facing metadata
// line appended AFTER the envelope text has passed the server's
// max_result_chars cap, so the count must itself be bounded.
const MaxArtifactsPerEnvelope = 64

// Ref is a single odek.artifact-ref/v1 object. Unknown fields are ignored
// per the contract's additive rule. SizeBytes is a pointer so "absent" is
// distinguishable from an explicit zero (verification is skipped when absent,
// enforced when present).
type Ref struct {
	Schema    string `json:"schema"`
	ID        string `json:"id"`
	URI       string `json:"uri"`
	MediaType string `json:"media_type"`
	SHA256    string `json:"sha256,omitempty"`
	SizeBytes *int64 `json:"size_bytes,omitempty"`
	Summary   string `json:"summary,omitempty"`
}

// Envelope is the odek.tool-result/v1 envelope shape. Unknown fields are
// ignored per the contract's additive rule.
type Envelope struct {
	Schema    string `json:"schema"`
	Text      string `json:"text"`
	Artifacts []Ref  `json:"artifacts,omitempty"`
}

// ParseEnvelope detects and parses an odek.tool-result/v1 envelope carried as
// the text of a tool result.
//
// A result that is not a JSON object, or whose "schema" field does not match
// SchemaToolResult exactly, is a plain text result: ParseEnvelope returns
// (nil, nil) and the caller forwards the text unchanged. A result that claims
// the envelope schema but is structurally malformed (e.g. "artifacts" is not
// an array of objects) fails closed with an error — the schema marker is a
// claim the payload must back up.
func ParseEnvelope(text string) (*Envelope, error) {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, nil
	}
	var probe struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal([]byte(trimmed), &probe); err != nil {
		// Not JSON at all → plain text result, delivered unchanged.
		return nil, nil
	}
	if probe.Schema != SchemaToolResult {
		return nil, nil
	}
	var env Envelope
	if err := json.Unmarshal([]byte(trimmed), &env); err != nil {
		return nil, fmt.Errorf("malformed %s envelope: %w", SchemaToolResult, err)
	}
	// Cap the artifact count (audit 2026-08): each ref costs one metadata
	// line in Render AFTER the envelope text has already passed
	// max_result_chars, so an unbounded count was a context-stuffing /
	// cost-DoS channel for an approved server.
	if len(env.Artifacts) > MaxArtifactsPerEnvelope {
		return nil, fmt.Errorf("%s envelope carries %d artifacts; the cap is %d", SchemaToolResult, len(env.Artifacts), MaxArtifactsPerEnvelope)
	}
	return &env, nil
}

// renderedArtifactPrefix begins every per-artifact metadata line produced by
// Render. CountRendered uses it to recover the artifact count from a rendered
// result string without re-parsing the envelope.
const renderedArtifactPrefix = "- artifact "

// Render produces the model-facing form of a validated envelope: the compact
// text plus one metadata line per artifact (id, media type, size, short hash
// prefix, summary). It NEVER includes the resolved filesystem path or any
// artifact content. Server-controlled strings are flattened to a single line
// each so one artifact cannot forge additional metadata lines.
func Render(env *Envelope) string {
	var b strings.Builder
	b.WriteString(env.Text)
	for i := range env.Artifacts {
		a := &env.Artifacts[i]
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s%q (%s", renderedArtifactPrefix, oneLine(a.ID), oneLine(a.MediaType))
		if a.SizeBytes != nil {
			fmt.Fprintf(&b, ", %d bytes", *a.SizeBytes)
		}
		if a.SHA256 != "" {
			fmt.Fprintf(&b, ", sha256 %s", shortHash(a.SHA256))
		}
		b.WriteString(")")
		if a.Summary != "" {
			b.WriteString(": ")
			b.WriteString(oneLine(a.Summary))
		}
	}
	return b.String()
}

// CountRendered returns the number of artifact metadata lines in a string
// produced by Render — 0 for plain-text results. Used by the runtime event
// stream to report artifact_count without re-parsing the envelope.
func CountRendered(s string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, renderedArtifactPrefix) {
			n++
		}
	}
	return n
}

// oneLine flattens CR/LF so a server-controlled field stays on its own
// metadata line.
func oneLine(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

// shortHash returns the first 12 hex characters of a digest, enough to
// identify the artifact without dumping the full hash into the context.
func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12] + "…"
	}
	return h
}
