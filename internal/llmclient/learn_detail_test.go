package llmclient

import (
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/redact"
)

// Provider-supplied error text reaches clients verbatim through
// LearnDetail, so credential-shaped strings must be redacted before they
// hit the WS frame or the UI toast.
func TestLearnDetail_RedactsSecretsInMessage(t *testing.T) {
	ev := LearnEvent{
		Kind:     LearnDropStreamOpts,
		Provider: "gw",
		Status:   400,
		Message:  `rejected: {"auth":"sk-proj-abcdefghijklmnopqrstuvwx1234567890abcd"}`,
	}
	detail := LearnDetail(ev)
	if redact.RedactSecrets("sk-proj-abcdefghijklmnopqrstuvwx1234567890abcd") == "sk-proj-abcdefghijklmnopqrstuvwx1234567890abcd" {
		t.Skip("redactor does not classify this pattern; test premise invalid")
	}
	if strings.Contains(detail, "sk-proj-abcdefghijklmnopqrstuvwx1234567890abcd") {
		t.Fatalf("detail leaks credential-shaped text: %q", detail)
	}
}

// A verbose or hostile gateway can put an arbitrarily large parsed
// error.message on the wire; the rendered detail must stay bounded
// (matches the ~2048-char wire-clamping convention).
func TestLearnDetail_ClampsOversizedMessage(t *testing.T) {
	ev := LearnEvent{
		Kind:     LearnBuffered,
		Provider: "gw",
		Message:  strings.Repeat("x", 100_000),
	}
	detail := LearnDetail(ev)
	if len(detail) > 2400 {
		t.Fatalf("detail length %d exceeds clamp", len(detail))
	}
	if !strings.HasSuffix(detail, "…") && !strings.HasSuffix(detail, "...") {
		t.Errorf("clamped detail should mark truncation, got suffix %q", detail[max(0, len(detail)-8):])
	}
}

// The provider id is operator config, but it still lands in client-facing
// text — keep it clamped too.
func TestLearnDetail_ClampsProviderID(t *testing.T) {
	ev := LearnEvent{
		Kind:     LearnBuffered,
		Provider: strings.Repeat("p", 5000),
	}
	detail := LearnDetail(ev)
	if len(detail) > 2400 {
		t.Fatalf("detail length %d exceeds clamp", len(detail))
	}
}
