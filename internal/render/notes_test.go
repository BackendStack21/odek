package render

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderer_IndependentStreamKinds(t *testing.T) {
	for _, reasoning := range []bool{false, true} {
		for _, content := range []bool{false, true} {
			var out bytes.Buffer
			r := New(&out, false)
			r.SetStreamedKinds(reasoning, content)
			r.Thinking("Thought")
			r.Note("Note")
			r.FinalAnswer("Answer")
			if strings.Contains(out.String(), "Thought") == reasoning {
				t.Errorf("reasoning=%v content=%v output=%q", reasoning, content, out.String())
			}
			for _, text := range []string{"Note", "Answer"} {
				if strings.Contains(out.String(), text) == content {
					t.Errorf("content=%v text=%q output=%q", content, text, out.String())
				}
			}
		}
	}
}

func TestRenderer_NoteEmptyAndDisabled(t *testing.T) {
	var out bytes.Buffer
	r := New(&out, false)
	r.Note("")
	if out.Len() != 0 {
		t.Fatal("empty note rendered")
	}
	var disabled *Renderer
	disabled.SetStreamedKinds(false, false)
	disabled.Note("note")
	New(nil, false).Note("note")
}
