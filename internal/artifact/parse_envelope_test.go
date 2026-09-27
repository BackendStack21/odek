package artifact

import (
	"encoding/json"
	"strings"
	"testing"
)

// A UTF-8 BOM before the envelope JSON must not turn the envelope into
// "plain text": envelopes parse regardless of a leading BOM.
func TestParseEnvelopeToleratesBOM(t *testing.T) {
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
		t.Fatal("BOM-prefixed envelope was treated as plain text (nil, nil) instead of parsing")
	}
	if got.Text != "hello" {
		t.Fatalf("unexpected text: %q", got.Text)
	}
}

// Junk-prefixed text is still plain text, but text that merely has the
// schema marker inside must not be misparsed either way.
func TestParseEnvelopeJunkPrefixStaysPlain(t *testing.T) {
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
