package loop

import (
	"context"
	"github.com/BackendStack21/odek/internal/redact"
	"github.com/BackendStack21/odek/internal/session"
	"strings"
)

const effectEvidencePrefix = "[Completed effects: these actions already ran; verify their current state before repeating them.]\n"

func isEffectEvidence(m session.Message) bool {
	return m.Role == "system" && strings.HasPrefix(m.Content, effectEvidencePrefix)
}

// effectBodyCache memoizes the redacted ledger text. The ledger is append-only
// within a run, so its length and newest entry identify the input; the
// redaction registry generation identifies the policy, so a secret registered
// after the body was built forces a fresh redaction.
type effectBodyCache struct {
	n    int
	last string
	gen  uint64
	body string
}

func (e *Engine) effectEvidenceBody() string {
	n := len(e.runMutations)
	last := e.runMutations[n-1]
	gen := redact.Generation()
	if c := e.effectBody; c.n == n && c.last == last && c.gen == gen && c.body != "" {
		return c.body
	}
	body := redact.RedactSecrets(strings.Join(e.runMutations, "\n"))
	e.effectBody = effectBodyCache{n: n, last: last, gen: gen, body: body}
	return body
}

// Completion evidence is state, not optional conversation context. Its
// untrusted body cannot promote resource names or command text to authority.
func (e *Engine) refreshEffectEvidence(ctx context.Context, messages []session.Message) []session.Message {
	if len(e.runMutations) == 0 {
		return messages
	}
	body := e.effectEvidenceBody()
	for i, m := range messages {
		if isEffectEvidence(m) {
			if strings.Contains(m.Content, body) {
				return messages
			}
			messages[i].Content = effectEvidencePrefix + e.protectDerivedContext(ctx, "completed_effects", body)
			return messages
		}
	}
	return append(messages, session.Message{Role: "system", Content: effectEvidencePrefix + e.protectDerivedContext(ctx, "completed_effects", body)})
}
