package agent

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestContextWindowNotice(t *testing.T) {
	if got := contextWindowNotice("m", 32768); got != `odek: model "m" context window: 32768 tokens` {
		t.Fatalf("known window: %q", got)
	}
	got := contextWindowNotice("Qwen3.5-2B-4bit", 0)
	for _, want := range []string{`"Qwen3.5-2B-4bit"`, "unknown", "proactive trimming is off",
		"only trimmed after the provider rejects it", "set llm.context_window"} {
		if !strings.Contains(got, want) {
			t.Fatalf("unknown window notice %q lacks %q", got, want)
		}
	}
}

// Every agent a process builds (serve sessions, chats, sub-agents) reports the
// window; the line prints once per model and window.
func TestNoteContextWindowPrintsOnce(t *testing.T) {
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) })

	model := "once-" + t.Name()
	noteContextWindow(model, 0)
	noteContextWindow(model, 0)
	noteContextWindow(model, 4096)
	if n := strings.Count(buf.String(), model); n != 2 {
		t.Fatalf("printed %d notices, want 2 (one per window):\n%s", n, buf.String())
	}
}
