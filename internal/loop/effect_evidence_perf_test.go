package loop

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/redact"
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

// A secret registered after the body was memoized must be redacted on the
// next refresh even though the ledger itself did not change.
func TestRED_Loop_EffectEvidenceRescannedAfterSecretRegistered(t *testing.T) {
	redact.ResetSecrets()
	t.Cleanup(redact.ResetSecrets)
	secret := "late-registered-secret-value-0123456789"
	e := &Engine{runMutations: []string{"shell: curl -H 'X-Token: " + secret + "'"}}
	if body := e.effectEvidenceBody(); !strings.Contains(body, secret) {
		t.Fatalf("precondition: unregistered value should be visible, got %q", body)
	}
	redact.RegisterSecret(secret)
	if body := e.effectEvidenceBody(); strings.Contains(body, secret) {
		t.Fatalf("memo served a body redacted under the old registry: %q", body)
	}
}
