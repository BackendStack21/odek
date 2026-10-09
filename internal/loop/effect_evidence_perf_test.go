package loop

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

// With an unchanged ledger the refresh must not re-join and re-redact it.
func TestRED_Loop_EffectEvidenceMemoized(t *testing.T) {
	e := &Engine{}
	for i := 0; i < 400; i++ {
		e.runMutations = append(e.runMutations, fmt.Sprintf("write_file /work/dir/file-%d.go", i))
	}
	msgs := []session.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "task"}}
	msgs = e.refreshEffectEvidence(context.Background(), msgs)
	if len(msgs) != 3 || !isEffectEvidence(msgs[2]) {
		t.Fatalf("evidence message not appended: %d messages", len(msgs))
	}
	allocs := testing.AllocsPerRun(20, func() {
		msgs = e.refreshEffectEvidence(context.Background(), msgs)
	})
	if allocs > 5 {
		t.Fatalf("unchanged ledger refresh made %.0f allocations; want it memoized", allocs)
	}
	if len(msgs) != 3 {
		t.Fatalf("refresh duplicated the evidence message: %d", len(msgs))
	}
}

// Appending to the ledger, or starting a new run, must change the body.
func TestEffectEvidenceBodyTracksLedger(t *testing.T) {
	e := &Engine{runMutations: []string{"shell: echo a"}}
	first := e.effectEvidenceBody()
	e.runMutations = append(e.runMutations, "shell: echo b")
	second := e.effectEvidenceBody()
	if first == second || !strings.Contains(second, "echo b") {
		t.Fatalf("body did not follow the ledger: %q -> %q", first, second)
	}
	e.runMutations = []string{"shell: echo c", "shell: echo d"}
	e.effectBody = effectBodyCache{}
	if got := e.effectEvidenceBody(); strings.Contains(got, "echo a") || !strings.Contains(got, "echo d") {
		t.Fatalf("stale body after reset: %q", got)
	}
}
