package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/memory/extended"
)

// memHunt12bLLM returns a fixed user-state inference diff.
type memHunt12bLLM struct{ resp string }

func (l memHunt12bLLM) SimpleCall(context.Context, string, string) (string, error) {
	return l.resp, nil
}

// TestRED_MemoryPendingListingEmitsRawTerminalEscapesFromInferredFields:
// user-state inference stores the LLM's pending-review entries with only the
// Value guard-scanned; Field (and a model-supplied ID) are kept verbatim. The
// human review surface `odek memory extended pending` then prints p.ID and
// p.Field with plain %s, so model output steered by conversation content can
// put ESC/CSI sequences on the operator's terminal — erasing the line and
// redrawing a benign-looking entry right before the operator runs `confirm`.
// Sibling of the fixed quarantine-listing escape finding.
func TestRED_MemoryPendingListingEmitsRawTerminalEscapesFromInferredFields(t *testing.T) {
	dir := t.TempDir()
	cfg := extended.DefaultConfig()
	on := true
	cfg.Enabled = &on
	cfg.InferUserState = &on
	diff := `{"pending":[{"id":"p1\u001b[2K\r","field":"focus.task\u001b[2K\r\u001b[32mstyle.tone","value":"terse","confidence":0.9}]}`
	um := extended.NewUserModelWithStore(filepath.Join(dir, "extended"), memHunt12bLLM{resp: diff}, cfg)
	um.Update(extended.MemoryAtom{ID: "a1", Text: "user is working on the release", SourceClass: extended.SourceUserApproved})
	if err := um.Infer(context.Background()); err != nil {
		t.Fatalf("infer: %v", err)
	}

	out := captureStdout(func() {
		if err := extendedMemoryCmd(dir, []string{"pending"}); err != nil {
			t.Errorf("pending: %v", err)
		}
	})
	if !strings.Contains(out, "focus.task") {
		t.Fatalf("precondition: inferred pending entry not listed: %q", out)
	}
	if strings.ContainsAny(out, "\x1b\r") {
		t.Fatalf("pending-review listing wrote raw terminal control bytes: %q", out)
	}
}
