package telegram

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// ─── Reserved characters in Telegram MarkdownV2 ───
// _ * [ ] ( ) ~ ` > # + - = | { } . !
// All must be escaped with \ outside of code spans/blocks.

// FormatResponse converts odek markdown output to Telegram MarkdownV2.
// It splits the result into chunks of at most 4096 bytes at paragraph boundaries.
func FormatResponse(text string) ([]string, error) {
	lines := strings.Split(text, "\n")
	var resultLines []string

	inCodeBlock := false
	inTable := false
	var tableLines []string

	flushTable := func() {
		if !inTable || len(tableLines) == 0 {
			return
		}
		resultLines = append(resultLines, "```")
		resultLines = append(resultLines, tableLines...)
		resultLines = append(resultLines, "```")
		tableLines = nil
		inTable = false
	}

	for _, rawLine := range lines {
		line := rawLine

		// ── Track code blocks ──
		if strings.HasPrefix(line, "```") {
			flushTable()
			inCodeBlock = !inCodeBlock
			resultLines = append(resultLines, line)
			continue
		}

		if inCodeBlock {
			resultLines = append(resultLines, line)
			continue
		}

		// ── MEDIA prefix lines: leave as-is ──
		if strings.HasPrefix(line, "MEDIA:") {
			flushTable()
			resultLines = append(resultLines, line)
			continue
		}

		// ── Separator lines: ─── or ──...── → --- ──
		if isSeparator(line) {
			flushTable()
			resultLines = append(resultLines, "---")
			continue
		}

		// ── Pipe tables ──
		if containsTableRow(line) {
			if !inTable {
				inTable = true
				tableLines = nil
			}
			tableLines = append(tableLines, line)
			continue
		}

		// End of table region
		flushTable()

		// ── ## headers → *bold text* ──
		if strings.HasPrefix(line, "## ") {
			content := strings.TrimPrefix(line, "## ")
			content = convertItalicAndEscape(content)
			resultLines = append(resultLines, "*"+content+"*")
			continue
		}

		// ── Normal line: convert italic, escape reserved chars ──
		processed := convertItalicAndEscape(line)
		resultLines = append(resultLines, processed)
	}

	// Flush any remaining table
	flushTable()

	fullText := strings.Join(resultLines, "\n")
	chunks := splitChunks(fullText, 4096)
	return chunks, nil
}

// EscapeMarkdown escapes all reserved MarkdownV2 characters.
// Characters inside code spans (`...`) and code blocks (```...```) are NOT escaped.
func EscapeMarkdown(text string) string {
	var result strings.Builder
	i := 0
	for i < len(text) {
		// Code blocks: ```...```
		if i <= len(text)-3 && text[i:i+3] == "```" {
			result.WriteString("```")
			i += 3
			end := strings.Index(text[i:], "```")
			if end >= 0 {
				result.WriteString(text[i : i+end])
				result.WriteString("```")
				i += end + 3
			}
			continue
		}

		// Inline code: `...`
		if text[i] == '`' {
			result.WriteByte('`')
			i++
			for i < len(text) && text[i] != '`' {
				result.WriteByte(text[i])
				i++
			}
			if i < len(text) {
				result.WriteByte('`')
				i++
			}
			continue
		}

		// Escape reserved characters
		if isReserved(rune(text[i])) {
			result.WriteByte('\\')
		}
		result.WriteByte(text[i])
		i++
	}
	return result.String()
}

// ─── Helpers ───

// isReserved returns true if r is a reserved MarkdownV2 character.
func isReserved(r rune) bool {
	switch r {
	case '_', '*', '[', ']', '(', ')', '~', '`', '>', '#',
		'+', '-', '=', '|', '{', '}', '.', '!':
		return true
	}
	return false
}

// isSeparator reports whether a line is a horizontal rule made of ─ characters.
func isSeparator(line string) bool {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) < 3 {
		return false
	}
	for _, r := range trimmed {
		if r != '─' {
			return false
		}
	}
	return true
}

// containsTableRow reports whether a line looks like a pipe table row.
func containsTableRow(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.Contains(trimmed, "|")
}

// isInsideCode reports whether the byte position pos in text falls inside
// a `code span` or a ```code block```.
func isInsideCode(text string, pos int) bool {
	inCodeSpan := false
	inCodeBlock := false
	i := 0
	for i < len(text) && i <= pos {
		if i <= len(text)-3 && text[i:i+3] == "```" {
			inCodeBlock = !inCodeBlock
			i += 3
			continue
		}
		if !inCodeBlock && text[i] == '`' {
			inCodeSpan = !inCodeSpan
			i++
			continue
		}
		if i == pos {
			return inCodeSpan || inCodeBlock
		}
		i++
	}
	return false
}

// convertItalicAndEscape processes a single line of normal text.
//   - Converts *italic* to _italic_ (but leaves **bold** unchanged)
//   - Escapes all reserved MarkdownV2 characters outside code spans/blocks
func convertItalicAndEscape(line string) string {
	var result strings.Builder
	inCodeSpan := false
	i := 0

	for i < len(line) {
		// ── Handle code spans ──
		if line[i] == '`' {
			// Triple backtick code block (on one line)
			if i <= len(line)-3 && line[i:i+3] == "```" {
				result.WriteString("```")
				i += 3
				end := strings.Index(line[i:], "```")
				if end >= 0 {
					result.WriteString(line[i : i+end])
					result.WriteString("```")
					i += end + 3
				}
				continue
			}
			// Single backtick
			inCodeSpan = !inCodeSpan
			result.WriteByte('`')
			i++
			continue
		}

		if inCodeSpan {
			result.WriteByte(line[i])
			i++
			continue
		}

		// ── **bold** → keep as-is ──
		if i <= len(line)-2 && line[i:i+2] == "**" {
			result.WriteString("**")
			i += 2
			continue
		}

		// ── *italic* → _italic_ ──
		if line[i] == '*' {
			result.WriteByte('_')
			i++
			continue
		}

		// ── Escape reserved chars ──
		if isReserved(rune(line[i])) {
			result.WriteByte('\\')
		}
		result.WriteByte(line[i])
		i++
	}

	return result.String()
}

// splitChunks splits text into chunks no larger than maxBytes bytes.
// It splits at paragraph boundaries (\n\n) when possible, but never
// splits inside fenced code blocks (```), which would break MarkdownV2
// formatting and cause Telegram parse errors.
// If a single paragraph exceeds maxBytes, it is split at the last space
// before the limit.
func splitChunks(text string, maxBytes int) []string {
	if maxBytes <= 0 {
		maxBytes = 4096
	}

	// Walk through the text to find \n\n paragraph boundaries,
	// but skip \n\n that falls inside fenced code blocks (```).
	// \n\n inside a code block is kept as part of the block.
	var paragraphs []string
	start := 0
	inCodeBlock := false

	for i := 0; i < len(text); {
		// Toggle code block state on ``` at the start of a line.
		if i <= len(text)-3 && text[i:i+3] == "```" {
			if i == 0 || text[i-1] == '\n' {
				inCodeBlock = !inCodeBlock
				i += 3
				continue
			}
		}

		// Look for \n\n paragraph boundary — only split when outside
		// a code block.
		if i <= len(text)-2 && text[i:i+2] == "\n\n" && !inCodeBlock {
			seg := text[start:i]
			if len(seg) > 0 {
				paragraphs = append(paragraphs, seg)
			}
			i += 2 // skip the \n\n
			start = i
			continue
		}
		i++
	}

	// Flush the remaining segment.
	if start < len(text) {
		seg := text[start:]
		if len(seg) > 0 {
			paragraphs = append(paragraphs, seg)
		}
	}

	if len(paragraphs) == 0 {
		return nil
	}

	// Now chunk paragraphs. Each paragraph (including code blocks) is
	// an atomic unit that won't be split at \n\n boundaries.
	var chunks []string
	var current strings.Builder

	for _, p := range paragraphs {
		// +2 for the "\n\n" separator we'll add
		needed := current.Len() + len(p)
		if current.Len() > 0 {
			needed += 2
		}

		if needed > maxBytes && current.Len() > 0 {
			// Flush current chunk
			chunks = append(chunks, current.String())
			current.Reset()
		}

		// If this single paragraph exceeds maxBytes, split it
		if len(p) > maxBytes {
			// Flush any accumulated content first
			if current.Len() > 0 {
				chunks = append(chunks, current.String())
				current.Reset()
			}
			// Split the paragraph at spaces
			remaining := p
			for len(remaining) > 0 {
				if len(remaining) <= maxBytes {
					current.WriteString(remaining)
					break
				}
				// Find last space within maxBytes
				splitAt := strings.LastIndex(remaining[:maxBytes], " ")
				if splitAt <= 0 {
					splitAt = maxBytes
				}
				// Never split mid-rune: spaceless text (CJK, URLs, base64)
				// cut at a raw byte boundary ships invalid UTF-8, which the
				// Telegram API coerces to U+FFFD — the user receives corrupted
				// text. Back off to the nearest rune start.
				for splitAt > 0 && !utf8.RuneStart(remaining[splitAt]) {
					splitAt--
				}
				chunks = append(chunks, remaining[:splitAt])
				remaining = remaining[splitAt:]
				// Trim leading spaces from remaining
				remaining = strings.TrimLeftFunc(remaining, unicode.IsSpace)
			}
			continue
		}

		// Append paragraph to current chunk
		if current.Len() > 0 {
			current.WriteString("\n\n")
		}
		current.WriteString(p)
	}

	// Flush remaining
	if current.Len() > 0 {
		chunks = append(chunks, current.String())
	}

	return chunks
}
