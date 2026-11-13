package loop

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/BackendStack21/odek/internal/redact"
	"github.com/BackendStack21/odek/internal/session"
)

const effectEvidencePrefix = "[Completed effects: these actions already ran; verify their current state before repeating them.]\n"

func isEffectEvidence(m session.Message) bool {
	return m.Role == "system" && strings.HasPrefix(m.Content, effectEvidencePrefix)
}

// The evidence message is protected by every trim mode, so its body is
// bounded like the compaction digest: each ledger entry is cut to
// effectEntryMaxBytes (its head and tail kept, the middle elided) and the
// whole body to a share of the context budget. The newest entries are kept;
// older ones collapse into one count line. Only the rendering is bounded:
// the ledger itself (runMutations) keeps every entry, so reconciliation and
// the completion nudge still see every completed effect.
const (
	effectEntryMaxBytes  = 512
	effectEntryTailBytes = 128
	effectMinTokens      = 256
	effectMaxTokens      = 8192
	// effectNoBudgetBytes bounds the body when no context limit is set.
	effectNoBudgetBytes = 32 << 10
	// effectOmittedReserve is room kept for the count line.
	effectOmittedReserve = 64
)

// effectBodyCache memoizes the rendered ledger text. The ledger is
// append-only within a run, so its length and newest entry identify the
// input; the redaction registry generation identifies the policy, so a
// secret registered after the body was built forces a fresh redaction; the
// cap changes with the context window.
type effectBodyCache struct {
	n       int
	last    string
	gen     uint64
	capSize int
	body    string
}

// effectEvidenceCapBytes bounds the evidence body: an eighth of the context
// budget (between effectMinTokens and effectMaxTokens), less the fixed
// prefix and wrapper, or effectNoBudgetBytes without a context limit.
func (e *Engine) effectEvidenceCapBytes() int {
	budget := contextBudget(e.maxContext)
	if budget <= 0 {
		return effectNoBudgetBytes
	}
	t := min(max(budget/8, effectMinTokens), effectMaxTokens)
	return max(t*4-len(effectEvidencePrefix)-digestWrapperBytes, effectEntryMaxBytes+effectOmittedReserve)
}

func (e *Engine) effectEvidenceBody() string {
	n := len(e.runMutations)
	last := e.runMutations[n-1]
	gen := redact.Generation()
	capBytes := e.effectEvidenceCapBytes()
	if c := e.effectBody; c.n == n && c.last == last && c.gen == gen && c.capSize == capBytes && c.body != "" {
		return c.body
	}
	body := renderEffectLedger(e.runMutations, capBytes)
	e.effectBody = effectBodyCache{n: n, last: last, gen: gen, capSize: capBytes, body: body}
	return body
}

// renderEffectLedger renders the newest entries that fit in capBytes, oldest
// first, after a line counting the entries left out. Each entry is redacted
// before it is cut, so a cut can never split a secret the redactor would
// have recognised. Only the entries kept are redacted.
func renderEffectLedger(entries []string, capBytes int) string {
	room := capBytes - effectOmittedReserve
	var kept []string
	used := 0
	i := len(entries) - 1
	for ; i >= 0; i-- {
		line := elideEffectEntry(redact.RedactSecrets(entries[i]))
		if len(kept) > 0 && used+len(line)+1 > room {
			break
		}
		kept = append(kept, line)
		used += len(line) + 1
	}
	var b strings.Builder
	b.Grow(used + effectOmittedReserve)
	if omitted := i + 1; omitted > 0 {
		fmt.Fprintf(&b, "[%d earlier completed effect(s) omitted]\n", omitted)
	}
	for k := len(kept) - 1; k >= 0; k-- {
		b.WriteString(kept[k])
		if k > 0 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// elideEffectEntry cuts an entry over effectEntryMaxBytes to its head and
// its last effectEntryTailBytes, on rune boundaries, naming how many bytes
// were left out.
func elideEffectEntry(s string) string {
	if len(s) <= effectEntryMaxBytes {
		return s
	}
	tailStart := len(s) - effectEntryTailBytes
	for tailStart < len(s) && !utf8.RuneStart(s[tailStart]) {
		tailStart++
	}
	headLen := effectEntryMaxBytes - effectEntryTailBytes - 40
	head := cutUTF8(s, headLen)
	return fmt.Sprintf("%s…[%d bytes elided]…%s", head, tailStart-len(head), s[tailStart:])
}

// Completion evidence is state, not optional conversation context. Its
// untrusted body cannot promote resource names or command text to authority.
// It is installed before trimming measures the history, so its bounded size
// is part of what trimming fits into the budget.
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
