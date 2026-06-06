package memory

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// maxBufferSummaryRunes bounds the length of a single buffer turn-summary.
// Raised from the old ~100-byte cut so that, after filler-stripping, one or two
// substantive sentences survive. Total injected ≈ BufferLines × this; at the
// default 20 lines that is ~4 KB worst case — negligible against the prompt.
const maxBufferSummaryRunes = 200

// codePlaceholder is substituted when a turn is nothing but a code block, so the
// buffer line is never blank.
const codePlaceholder = "[code]"

var (
	// Fenced code blocks: ```lang\n ... ``` (dot-all, non-greedy).
	reCodeFence = regexp.MustCompile("(?s)```.*?```")
	// Inline code: `text` -> text (keep inner content).
	reInlineCode = regexp.MustCompile("`([^`]*)`")
	// Markdown images ![alt](url) -> alt (run before links).
	reMdImage = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	// Markdown links [text](url) -> text.
	reMdLink = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	// Line-leading markers: heading #, blockquote >, bullets -/*/+, numbered N.
	reLineMarker = regexp.MustCompile(`(?m)^[ \t]*(?:#{1,6}|>+|[-*+]|\d+\.)[ \t]+`)
	// Bold/italic emphasis pairs. Single * / _ are left alone to avoid harming
	// snake_case identifiers and lone math/glob asterisks.
	reBold = regexp.MustCompile(`(\*\*|__)([^*_]+)(\*\*|__)`)
	// Leading acknowledgement clause (pure filler), up to its sentence end.
	// Note: "let me ..." / "i'll start ..." are actions, deliberately NOT here.
	reFiller = regexp.MustCompile(`(?i)^(?:sure|okay|ok|got it|certainly|of course|alright|no problem|sounds good|absolutely|great|happy to help|glad to help|i'?ll help|i can help)\b[^.!?]*[.!?]\s+`)
)

// summarizeForBuffer produces a deterministic, no-LLM, rune-safe single-line
// excerpt of raw turn text for the tier-2 buffer. It strips code/markdown noise,
// collapses whitespace, drops a leading filler clause when substance remains,
// and cuts at a sentence/word boundary within maxBufferSummaryRunes.
//
// It is invoked only on WRITE (MemoryManager.AppendBuffer). It must never run on
// already-stored buffer lines (RestoreBuffer bypasses it) — re-running it on a
// formatted "HH:MM role msg" line would corrupt the persisted summary.
func summarizeForBuffer(text string) string {
	// 1. Remove fenced code blocks; remember whether we saw one.
	hadCode := reCodeFence.MatchString(text)
	text = reCodeFence.ReplaceAllString(text, " ")

	// 2. Inline code -> inner text, then drop any residual backticks (e.g. from
	//    an unclosed fence that step 1 could not match).
	text = reInlineCode.ReplaceAllString(text, "$1")
	text = strings.ReplaceAll(text, "`", "")

	// 3. Images/links -> their visible text.
	text = reMdImage.ReplaceAllString(text, "$1")
	text = reMdLink.ReplaceAllString(text, "$1")

	// 4. Line-leading markers and bold/italic emphasis.
	text = reLineMarker.ReplaceAllString(text, "")
	text = reBold.ReplaceAllString(text, "$2")

	// 5. Collapse every whitespace run (incl. newlines/tabs) to a single space.
	text = strings.Join(strings.Fields(text), " ")

	if text == "" {
		if hadCode {
			return codePlaceholder
		}
		return ""
	}

	// 6. Drop a single leading filler clause, but only if substance remains.
	if loc := reFiller.FindStringIndex(text); loc != nil {
		if rest := strings.TrimSpace(text[loc[1]:]); rest != "" {
			text = rest
		}
	}

	// 7. Excerpt to the rune cap at a sentence/word boundary.
	return excerptRunes(text, maxBufferSummaryRunes)
}

// excerptRunes returns s unchanged if it fits within max runes; otherwise it
// cuts at the last sentence terminator (.!?) before the cap, else the last
// space, else hard-cuts at the cap — always on a rune boundary — and appends an
// ellipsis. Never splits a multibyte rune.
//
// A sentence cut is only preferred when it lands at least halfway through the
// window. Otherwise a single early terminator (an abbreviation like "e.g.", a
// version "v1.2", or a domain "node.js") would collapse the summary to a few
// runes; in that case we fall back to the word boundary near the cap instead.
func excerptRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	window := runes[:max]

	cut := -1
	for i, r := range window {
		if r == '.' || r == '!' || r == '?' {
			cut = i + 1 // include the terminator
		}
	}
	if cut < max/2 {
		// No (or only an early) sentence end; fall back to the last word
		// boundary so we keep as much content as possible.
		cut = -1
		for i := len(window) - 1; i >= 0; i-- {
			if window[i] == ' ' {
				cut = i
				break
			}
		}
	}
	if cut <= 0 {
		// Single very long token: hard-cut at the cap.
		cut = max
	}

	return strings.TrimSpace(string(window[:cut])) + "…"
}
