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

// Completion evidence is state, not optional conversation context. Its
// untrusted body cannot promote resource names or command text to authority.
func (e *Engine) refreshEffectEvidence(ctx context.Context, messages []session.Message) []session.Message {
	if len(e.runMutations) == 0 {
		return messages
	}
	body := redact.RedactSecrets(strings.Join(e.runMutations, "\n"))
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
