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

// A reply that is nothing but a decline: NONE, N/A, or one short sentence
// saying there is nothing to record. Anchored at both ends so a summary
// that merely begins with "None of the tests…" or "No significant
// regressions were found; …" is not mistaken for one.
var reEpisodeDeclined = regexp.MustCompile(`(?i)^\W*(none|n/?a|nothing (to|worth) \w+|no (durable|notable|significant) [^.!?;]{0,80})[.!]?\W*$`)

// A reply that records an absence rather than a session: a last sentence
// of "there is nothing worth remembering", a short note ending in "nothing
// to persist" or "N/A". The object is a memory verb, so "nothing worth
// optimizing" or "nothing to roll back" inside a real summary is not a
// decline, and the long form is judged on the last sentence only.
var reEpisodeTrivial = regexp.MustCompile(`(?i)(\bthere (is|was) nothing (to|worth) (persist|remember|record|summari[sz]|note|retain|keep|report|save|store)\w*|\bn/a\W*$)`)

// reEpisodeTrivialShort is applied to the last sentence of replies of at
// most episodeTrivialMaxBytes.
var reEpisodeTrivialShort = regexp.MustCompile(`(?i)\bnothing (to|worth) (persist|remember|record|summari[sz]|note|retain|keep|report|save|store)\w*`)

const episodeTrivialMaxBytes = 120

// A reply that opens as a turn in the conversation rather than a record:
// an acknowledgement ("Sure thing —", "Okay so", "Done."), a second-person
// or first-person opener, or a "let me / let's / here's" continuation.
// "Let's Encrypt…", "OK status codes…" and "Thanks to a new cache…" are
// not matched.
var reEpisodeOpensAsTurn = regexp.MustCompile(`(?i)^\W*((picking (this|it|that) (back )?up|i'll|i will|i've|i'm|i am|you're|you are|you were|you asked|you can|you should|you'll|great question|good news|happy to help|glad to help|no problem|sounds good|of course)\b|(sure|okay|ok|got it|thanks|alright|certainly|done|absolutely)\b\s*([,.!—–:-]|thing\b|so\b)|let me (know|check|start|look|see|verify|take|try|pick|re-?run|summari[sz]e)\b|let's (start|begin|check|look|see|pick|continue|move|go|try|get|dig|recap|verify)\b|here('s| is) (what|the|a|my|an)\b)`)

// A first sentence that addresses the user or narrates the model's next
// action. Applied to the first sentence only, with quoted and code spans
// removed, so a third-person summary that quotes the user ("the user said
// 'your tests fail'"), documents a `you` alias or mentions their files later
// on is not rejected.
var reEpisodeAddressesUser = regexp.MustCompile(`(?i)\b(you (can|should|may|might|need|want|asked|said|told|mentioned|wanted|requested)|you'll|you'd|you've|let me know|your (request|question|message|turn|answer)|i'll|i will|i've)\b`)

// A later sentence that opens in the second or first person, offers more
// work or asks the user to reply ("… You can rerun it with make test.",
// "… Want me to add tests?", "… Hope this helps!"): checked over the whole
// opening, spans removed.
var reEpisodeLaterTurn = regexp.MustCompile(`(?i)(^|[.!?]\s+)(you (can|should|may|might|need|want)|you'll|i'll|i will|i've|i can also|let me know|want me to|should i|shall i|would you like|hope (this|that) helps|happy to)\b`)

// Quoted and code spans in a sentence: double quotes (straight or curly),
// backticks, and single quotes that open after whitespace, so apostrophes
// ("user's") are left.
var reEpisodeQuoted = regexp.MustCompile("\"[^\"]*\"|“[^”]*”|`[^`]*`|(^|\\s)'[^']*'")

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
	if reply == "" || reEpisodeDeclined.MatchString(reply) {
		return false
	}
	// Quoted and code spans are what the session worked on, not what the
	// reply says about it ("a 'Nothing to summarize' placeholder was added").
	plain := reEpisodeQuoted.ReplaceAllString(reply, " ")
	closing := lastSentence(plain)
	if reEpisodeTrivial.MatchString(closing) || (len(reply) <= episodeTrivialMaxBytes && reEpisodeTrivialShort.MatchString(closing)) {
		return false
	}
	opening := reply
	if len(opening) > episodeOpeningBytes {
		opening = truncateAtRune(opening, episodeOpeningBytes)
	}
	// Strip leading markdown emphasis/heading so "**Session summary:** …"
	// is judged by its words; fold curly apostrophes so "I’ll" is "I'll".
	opening = strings.TrimLeft(opening, "*#_ \t")
	opening = strings.NewReplacer("’", "'", "‘", "'").Replace(opening)
	if reEpisodeOpensAsTurn.MatchString(opening) {
		return false
	}
	unquoted := reEpisodeQuoted.ReplaceAllString(opening, " ")
	if reEpisodeLaterTurn.MatchString(unquoted) {
		return false
	}
	return !reEpisodeAddressesUser.MatchString(firstSentence(unquoted))
}

// lastSentence returns the text after the last sentence end (". ", "! ",
// "? " or a newline) that is followed by more text, or all of s.
func lastSentence(s string) string {
	s = strings.TrimSpace(s)
	start := 0
	for _, term := range []string{". ", "! ", "? ", "\n"} {
		if i := strings.LastIndex(s, term); i >= 0 && i+len(term) > start {
			start = i + len(term)
		}
	}
	return s[start:]
}

// firstSentence returns s up to its first sentence end (". ", "! ", "? " or
// a newline), or all of s.
func firstSentence(s string) string {
	end := len(s)
	for _, term := range []string{". ", "! ", "? ", "\n"} {
		if i := strings.Index(s, term); i >= 0 && i < end {
			end = i
		}
	}
	return s[:end]
}

// truncateEpisodeSummary bounds a summary to maxBytes, preferring the last
// sentence end in the second half of the allowance so the stored text stays
// whole sentences. Without one it falls back to a rune-safe cut marked "...", still within maxBytes.
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
	return truncateAtRune(s, maxBytes-3) + "..."
}
