package memory

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// maxExtractionTranscriptBytes bounds the transcript handed to the
// session-end summariser. The raw history of a long session is dominated by
// tool output (one observed case: 730 KB, 93% tool results); fed whole, the
// model continued the conversation instead of summarising it. The budget is
// split between the head (the original task) and the tail (the outcome).
const maxExtractionTranscriptBytes = 32 * 1024

// extractionHeadBytes is the share of the budget reserved for the start of
// the session; the rest goes to the most recent messages.
const extractionHeadBytes = 8 * 1024

// maxExtractionMessageBytes caps one user/assistant message inside the
// transcript so a single pasted document cannot consume the whole budget.
const maxExtractionMessageBytes = 4 * 1024

// buildExtractionTranscript turns the session's role-labelled message
// strings ("user: …", "assistant: …", "tool: …") into the text the episode
// summariser reads. Tool results are replaced by a size note — the
// assistant's own narration carries what they meant — system messages are
// dropped, long messages are head-cut, and when the whole still exceeds the
// budget the head and tail are kept around an elision marker. Message
// boundaries are respected and every cut is rune-safe.
func buildExtractionTranscript(messages []string) string {
	lines := make([]string, 0, len(messages))
	for _, raw := range messages {
		role, content, _ := strings.Cut(raw, ":")
		role = strings.TrimSpace(strings.ToLower(role))
		content = strings.TrimSpace(content)
		switch role {
		case "tool":
			lines = append(lines, fmt.Sprintf("tool: [tool result omitted, %d chars]", utf8.RuneCountInString(content)))
			continue
		case "system":
			continue
		case "user", "assistant":
			if len(content) > maxExtractionMessageBytes {
				content = truncateAtRune(content, maxExtractionMessageBytes) + " […]"
			}
			lines = append(lines, role+": "+content)
		default:
			// Unlabelled lines are passed through (legacy callers); bounded
			// like any other message.
			if len(raw) > maxExtractionMessageBytes {
				raw = truncateAtRune(raw, maxExtractionMessageBytes) + " […]"
			}
			lines = append(lines, raw)
		}
	}

	total := 0
	for _, l := range lines {
		total += len(l) + 1
	}
	if total <= maxExtractionTranscriptBytes {
		return strings.Join(lines, "\n") + "\n"
	}

	// Head: whole messages until the head budget is spent.
	head, used := 0, 0
	for head < len(lines) && used+len(lines[head])+1 <= extractionHeadBytes {
		used += len(lines[head]) + 1
		head++
	}
	// Tail: whole messages from the end until the remaining budget is spent
	// (leave room for the marker line).
	tailBudget := maxExtractionTranscriptBytes - used - 80
	tail, tused := len(lines), 0
	for tail > head && tused+len(lines[tail-1])+1 <= tailBudget {
		tused += len(lines[tail-1]) + 1
		tail--
	}
	if tail <= head {
		return strings.Join(lines, "\n") + "\n"
	}
	var b strings.Builder
	for _, l := range lines[:head] {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "[… %d messages elided …]\n", tail-head)
	for _, l := range lines[tail:] {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return b.String()
}

// Replies that decline to summarise. The prompt asks for exactly NONE; older
// models answered in prose.
var reEpisodeDeclined = regexp.MustCompile(`(?i)^\W*(none|n/?a|nothing (to|worth) (persist|remember|summari[sz]|record|note)|no (durable|notable|significant) )`)

// Replies that summarise a session as having nothing in it ("User greeted
// with 'hi' — nothing to persist. N/A"): a record of absence is not a record.
var reEpisodeTrivial = regexp.MustCompile(`(?i)(\bnothing (to|worth) (persist|remember|summari[sz]e|record|note)\b|\bn/a\W*$)`)

// Signals that the text addresses the user or narrates the model's next
// action — a conversation turn, not a record of the session. Checked only in
// the opening of the reply; a genuine summary may quote "you" later on.
var reEpisodeConversational = regexp.MustCompile(`(?i)(^\W*(picking (this|it|that) (back )?up|let me|let's|here's|here is|sure|okay|ok|got it|thanks|i'll|i will|i've|i'm|i am)\b|\b(you|your|you're|you've|you'd)\b|\b(let me know|i'll|i will|i've)\b)`)

// episodeOpeningBytes is how much of a reply the conversational check reads.
const episodeOpeningBytes = 240

// looksLikeEpisode reports whether an extraction reply is a summary worth
// storing. It rejects empty replies, declines (NONE), and replies whose
// opening reads as a turn in the conversation — a reply like "Picking this
// back up — the connection reset hit …" is the model continuing the chat
// over a transcript that ended on its own message, and replaying it into a
// later session would be noise at best.
func looksLikeEpisode(reply string) bool {
	reply = strings.TrimSpace(reply)
	if reply == "" || reEpisodeDeclined.MatchString(reply) || reEpisodeTrivial.MatchString(reply) {
		return false
	}
	opening := reply
	if len(opening) > episodeOpeningBytes {
		opening = truncateAtRune(opening, episodeOpeningBytes)
	}
	// Strip leading markdown emphasis/heading so "**Session summary:** …"
	// is judged by its words.
	opening = strings.TrimLeft(opening, "*#_ \t")
	return !reEpisodeConversational.MatchString(opening)
}

// truncateEpisodeSummary bounds a summary to maxBytes, preferring the last
// sentence end in the second half of the allowance so the stored text stays
// whole sentences. Without one it falls back to a rune-safe cut marked "...".
func truncateEpisodeSummary(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := truncateAtRune(s, maxBytes)
	best := -1
	for _, term := range []string{". ", ".\n", "! ", "!\n", "? ", "?\n"} {
		if i := strings.LastIndex(cut, term); i > best {
			best = i
		}
	}
	if best >= maxBytes/2 {
		return strings.TrimSpace(cut[:best+1])
	}
	return cut + "..."
}
