// Extension contract constants for the odek generic extension surface.
//
// This file is purely additive: it introduces the versioned contract under
// which third-party MCP servers can interoperate with odek, without changing
// any existing client behavior. The full normative specification lives in
// docs/EXTENSIONS.md.
//
// Compatibility rule: all schemas below are additive. Producers may add new
// fields in future contract versions; consumers MUST ignore fields they do
// not recognize rather than failing.
package mcpclient

import "github.com/BackendStack21/odek/internal/artifact"

// ExtensionContractVersion identifies the version of the odek extension
// contract implemented by this package. Extension servers and odek itself
// use this string to negotiate/document which schema set they speak.
const ExtensionContractVersion = "odek-extension/v1"

// Schema names carried in the "schema" field of structured payloads so
// consumers can detect and version-check them. The tool-result envelope and
// artifact-ref schema names are aliased from internal/artifact (the canonical
// definition and parser/validator live there) so the contract has a single
// source of truth.
const (
	// SchemaToolResult names the odek tool-result envelope. An MCP tool may
	// return this envelope (as a JSON text content item) to attach
	// out-of-band artifact references to an otherwise compact text result.
	SchemaToolResult = artifact.SchemaToolResult

	// SchemaArtifactRef names a single artifact reference inside a
	// tool-result envelope. An artifact ref points at a file produced by the
	// tool (e.g. a full log-analysis report) that is intentionally NOT
	// inlined into the model context.
	SchemaArtifactRef = artifact.SchemaArtifactRef

	// SchemaEvent names one structured runtime event, emitted as a single
	// JSON object per line on an event stream (JSONL).
	SchemaEvent = "odek.event/v1"
)
