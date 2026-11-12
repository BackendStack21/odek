package loop

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/BackendStack21/odek/internal/redact"
	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// The rendered ledger keeps the newest entries within the cap and counts the
// rest; the ledger itself keeps every entry, so reconciliation and the
// completion nudge still see all of them.
func TestEffectEvidence_NewestKeptOlderCounted(t *testing.T) {
	e := New(nil, tool.NewRegistry(nil), 10, "sys", nil, 8192)
	for i := 0; i < 200; i++ {
		e.runMutations = append(e.runMutations, fmt.Sprintf("shell: touch build/artifact_%03d.bin && echo %s", i, strings.Repeat("x", 100)))
	}
	body := e.effectEvidenceBody()
	if len(body) > e.effectEvidenceCapBytes() {
		t.Fatalf("body is %d bytes, over the %d-byte cap", len(body), e.effectEvidenceCapBytes())
	}
	if !strings.Contains(body, "artifact_199") {
		t.Fatalf("newest entry missing from the body: %q", body)
	}
	if strings.Contains(body, "artifact_000") {
		t.Fatalf("oldest entry kept although the cap was exceeded")
	}
	if !strings.HasPrefix(body, "[") || !strings.Contains(body, "earlier completed effect(s) omitted]") {
		t.Fatalf("missing count line for the omitted entries: %q", body[:min(len(body), 120)])
	}
	var omitted int
	fmt.Sscanf(body, "[%d earlier", &omitted)
	kept := strings.Count(body, "shell: touch")
	if omitted+kept != 200 {
		t.Fatalf("omitted %d + kept %d != 200 ledger entries", omitted, kept)
	}
	if len(e.runMutations) != 200 {
		t.Fatalf("rendering changed the ledger: %d entries", len(e.runMutations))
	}
	// Reconciliation reads the ledger, not the rendering.
	if got := e.reconcileFinalReply("No changes were made."); !strings.Contains(got, "and 195 more") {
		t.Fatalf("reconciliation lost ledger entries: %q", got)
	}
}

// A long entry keeps its head and tail and names what was left out, cut on
// rune boundaries.
func TestEffectEvidence_LongEntryElided(t *testing.T) {
	cmd := "cat > pkg/file.go <<'EOF'\n" + strings.Repeat("é", 2000) + "\nEOF tail-marker"
	got := elideEffectEntry(cmd)
	if len(got) > effectEntryMaxBytes+32 {
		t.Fatalf("elided entry is %d bytes", len(got))
	}
	if !strings.HasPrefix(got, "cat > pkg/file.go") || !strings.HasSuffix(got, "EOF tail-marker") {
		t.Fatalf("head or tail lost: %q", got)
	}
	if !strings.Contains(got, "bytes elided]") || !utf8.ValidString(got) {
		t.Fatalf("elision marker missing or invalid UTF-8: %q", got)
	}
	if short := "shell: rm -f a.txt"; elideEffectEntry(short) != short {
		t.Fatal("a short entry must stay as it is")
	}
}

// Each entry is redacted before it is cut: a secret straddling the cut must
// not leave a recognisable fragment behind.
func TestEffectEvidence_RedactedBeforeElision(t *testing.T) {
	redact.ResetSecrets()
	t.Cleanup(redact.ResetSecrets)
	secret := "registered-ledger-secret-" + strings.Repeat("Q", 300)
	redact.RegisterSecret(secret)
	// Place the secret across the head/tail boundary of the elision.
	cmd := "shell: curl -H 'X-Key: " + secret + "' " + strings.Repeat("a", 400)
	got := renderEffectLedger([]string{cmd}, effectNoBudgetBytes)
	if strings.Contains(got, "QQQQQQQQQQ") {
		t.Fatalf("part of a registered secret survived: %q", got)
	}
}

// Without a context limit the body is still bounded.
func TestEffectEvidence_BoundedWithoutContextLimit(t *testing.T) {
	e := New(nil, tool.NewRegistry(nil), 10, "sys", nil, 0)
	for i := 0; i < 2000; i++ {
		e.runMutations = append(e.runMutations, fmt.Sprintf("write_file /work/very/long/path/name/file_%05d.go", i))
	}
	msgs := e.refreshEffectEvidence(context.Background(), []session.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "go"}})
	ev := msgs[len(msgs)-1]
	if !isEffectEvidence(ev) {
		t.Fatal("evidence message not installed")
	}
	if len(ev.Content) > effectNoBudgetBytes+len(effectEvidencePrefix)+digestWrapperBytes {
		t.Fatalf("evidence is %d bytes without a context limit", len(ev.Content))
	}
	if !strings.Contains(ev.Content, "file_01999.go") {
		t.Fatal("newest entry missing")
	}
}

// A changed context window re-renders the body under the new cap.
func TestEffectEvidence_CapFollowsContextWindow(t *testing.T) {
	e := New(nil, tool.NewRegistry(nil), 10, "sys", nil, 0)
	for i := 0; i < 2000; i++ {
		e.runMutations = append(e.runMutations, fmt.Sprintf("write_file /work/f_%05d.go", i))
	}
	wide := e.effectEvidenceBody()
	e.maxContext = 8192
	narrow := e.effectEvidenceBody()
	if len(narrow) >= len(wide) || len(narrow) > e.effectEvidenceCapBytes() {
		t.Fatalf("body did not follow the smaller window: %d -> %d bytes (cap %d)", len(wide), len(narrow), e.effectEvidenceCapBytes())
	}
}
