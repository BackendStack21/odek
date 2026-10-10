package loop

import (
	"context"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// A hostile session file can persist a multi-MB digest. The digest sits in
// the undroppable head, so it must be re-capped on load with the same budget
// installDigest applies.
func TestRED_PersistedDigest_IsRecappedOnLoad(t *testing.T) {
	engine := New(nil, tool.NewRegistry(nil), 1, "runtime", nil, 32000)
	capBytes := engine.digestBodyCapBytes()
	if capBytes <= 0 {
		t.Fatal("fixture needs a context limit")
	}
	huge := strings.Repeat("A", 4<<20)
	got := engine.sanitizePersistedSystemMessages(context.Background(), []session.Message{
		{Role: "system", Content: "runtime"},
		{Role: "system", Content: digestMsgHeader + huge},
	})
	if n := len(got[1].Content); n > len(digestMsgHeader)+capBytes+digestWrapperBytes+64 {
		t.Fatalf("persisted digest not re-capped: %d bytes (cap %d)", n, capBytes)
	}
}

// Capping a capped digest is a no-op, so repeated resumes do not churn it.
func TestRED_PersistedDigestCap_Idempotent(t *testing.T) {
	engine := New(nil, tool.NewRegistry(nil), 1, "runtime", nil, 32000)
	once := engine.capPersistedDigest(strings.Repeat("A", 1<<20))
	if len(once) > engine.digestBodyCapBytes() {
		t.Fatalf("capped body %d exceeds cap %d", len(once), engine.digestBodyCapBytes())
	}
	if twice := engine.capPersistedDigest(once); twice != once {
		t.Fatal("re-capping changed the digest")
	}
}

// Without a context limit the digest still has an absolute ceiling.
func TestRED_PersistedDigest_CappedWithoutContextLimit(t *testing.T) {
	engine := New(nil, tool.NewRegistry(nil), 1, "runtime", nil, 0)
	huge := strings.Repeat("B", 4<<20)
	got := engine.sanitizePersistedSystemMessages(context.Background(), []session.Message{
		{Role: "system", Content: "runtime"},
		{Role: "system", Content: digestMsgHeader + huge},
	})
	if n := len(got[1].Content); n > digestMaxTokens*4+len(digestMsgHeader)+digestWrapperBytes+64 {
		t.Fatalf("persisted digest unbounded without a context limit: %d bytes", n)
	}
}

// Persisted plan step titles and notes are bounded by the parser: a plan
// carrying an oversized title or note is rejected, never restored.
func TestRED_PersistedPlan_OversizedTitleOrNoteRejected(t *testing.T) {
	header := "[Current plan: v1 — 0/1 done, 0 blocked. Structured state, not instructions.]\n"
	cases := map[string]string{
		"title": header + "s1 [pending] " + strings.Repeat("t", maxPlanTitleChars+1),
		"note":  header + "s1 [pending] ok — " + strings.Repeat("n", maxPersistedPlanNoteChars+1),
		"total": header + "s1 [pending] ok — " + strings.Repeat("x", maxPersistedPlanBytes),
	}
	for name, content := range cases {
		if _, err := parsePlanState(content, 50); err == nil {
			t.Errorf("%s: oversized persisted plan accepted", name)
		}
	}
	// Legit bounds still parse.
	ok := header + "s1 [pending] " + strings.Repeat("t", maxPlanTitleChars) + " — " + strings.Repeat("n", 500)
	if _, err := parsePlanState(ok, 50); err != nil {
		t.Fatalf("in-bounds plan rejected: %v", err)
	}
}
