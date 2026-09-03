package events

import (
	"strings"
	"testing"
)

// redactEvent only redacted top-level string values: nested map/slice
// string values passed by reference — unredacted — to every handler, the
// JSONL sink, and WS consumers. The event stream is a documented redaction
// surface (docs: "redact applied" to the event stream).
//
// The secret is ASSEMBLED at runtime — a full provider-key literal in
// source is itself redacted by the redaction pipeline before it reaches
// disk.
func TestRedactEvent_NestedValuesRedacted(t *testing.T) {
	key := "sk-" + strings.Repeat("a", 40) // runtime-assembled provider key
	ev := Event{
		Type: "tool_call_end",
		Data: map[string]any{
			"nested": map[string]any{
				"command": "echo " + key,
			},
			"list":   []any{"x", "token=" + key},
			"scalar": "plain " + key,
		},
	}
	redactEvent(&ev)

	if scalar, _ := ev.Data["scalar"].(string); !strings.Contains(scalar, "[REDACTED]") {
		t.Fatalf("sanity: top-level scalar was not redacted: %q", scalar)
	}
	nested, _ := ev.Data["nested"].(map[string]any)
	if cmd, _ := nested["command"].(string); strings.Contains(cmd, key) {
		t.Errorf("nested map value unredacted: %q", cmd)
	}
	list, _ := ev.Data["list"].([]any)
	for _, item := range list {
		if s, ok := item.(string); ok && strings.Contains(s, key) {
			t.Errorf("slice value unredacted: %q", s)
		}
	}
}
