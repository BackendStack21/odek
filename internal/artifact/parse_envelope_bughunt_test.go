package artifact

import (
	"encoding/json"
	"strings"
	"testing"
)

// An MCP server that prepends a UTF-8 BOM (or other tooling that does)
// produces envelope text whose first byte is not '{' — ParseEnvelope used to
// return (nil, nil) ("plain text"), and the mcpclient then delivered the raw
// envelope JSON with file:// refs to the model without any artifact-root
// validation. A BOM-prefixed envelope must parse like any other.
func TestRED_ParseEnvelopeToleratesBOM(t *testing.T) {
	env := &Envelope{Schema: SchemaToolResult, Text: "hello"}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	bomText := "\uFEFF" + string(raw)
	got, err := ParseEnvelope(bomText)
	if err != nil {
		t.Fatalf("BOM-prefixed envelope must parse, got error: %v", err)
	}
	if got == nil {
		t.Fatal("BOM-prefixed envelope was treated as plain text (nil, nil) — fail-open for envelope JSON with file:// refs")
	}
	if got.Text != "hello" {
		t.Fatalf("unexpected text: %q", got.Text)
	}
}

// Junk-prefixed text is still plain text, but text that merely has the
// schema marker inside must not be misparsed either way.
func TestRED_ParseEnvelopeJunkPrefixStaysPlain(t *testing.T) {
	env := &Envelope{Schema: SchemaToolResult, Text: "hi"}
	raw, _ := json.Marshal(env)
	got, err := ParseEnvelope("note: " + string(raw))
	if err != nil {
		t.Fatalf("plain text must not error: %v", err)
	}
	if got != nil {
		t.Fatalf("junk-prefixed text must stay plain text, got envelope %+v", got)
	}
	if !strings.Contains("ok", "ok") {
		t.Fatal("unreachable")
	}
}
