package danger

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// homoglyphMap folds common Unicode confusables (Cyrillic/Greek look-alikes)
// into their ASCII equivalents so blacklist scanners cannot be bypassed by
// mixing scripts. The map is intentionally small and conservative: it only
// includes characters that are visually identical or nearly identical to
// ASCII letters.
var homoglyphMap = map[rune]rune{
	// Cyrillic look-alikes
	'а': 'a', // U+0430
	'е': 'e', // U+0435
	'о': 'o', // U+043E
	'р': 'p', // U+0440
	'с': 'c', // U+0441
	'х': 'x', // U+0445
	'і': 'i', // U+0456
	'ј': 'j', // U+0458
	'ѕ': 's', // U+0455
	'ь': 'b', // U+044C (soft sign, somewhat similar)
	'т': 't', // U+0442
	'у': 'y', // U+0443
	'н': 'h', // U+043D
	'к': 'k', // U+043A
	'м': 'm', // U+043C
	'в': 'b', // U+0432
	'г': 'r', // U+0433
	'д': 'd', // U+0434
	'з': 'z', // U+0437
	'л': 'l', // U+043B
	'п': 'n', // U+043F
	'ф': 'f', // U+0444
	'ц': 'u', // U+0446
	'ч': 'h', // U+0447
	'ш': 'w', // U+0448
	'щ': 'w', // U+0449
	'ы': 'i', // U+044B
	'ъ': 'b', // U+044A
	'э': 'e', // U+044D
	'ю': 'u', // U+044E
	'я': 'r', // U+044F
	'ѐ': 'e', // U+0450
	'ё': 'e', // U+0451
	'ђ': 'd', // U+0452
	'ѓ': 'g', // U+0453
	'є': 'e', // U+0454
	'ї': 'i', // U+0457
	'љ': 'l', // U+0459
	'њ': 'n', // U+045A
	'ћ': 'h', // U+045B
	'ќ': 'k', // U+045C
	'ѝ': 'i', // U+045D
	'ў': 'u', // U+045E
	'џ': 'd', // U+045F

	// Greek look-alikes
	'ο': 'o', // U+03BF
	'ε': 'e', // U+03B5
	'ρ': 'p', // U+03C1
	'α': 'a', // U+03B1
	'β': 'b', // U+03B2
	'γ': 'y', // U+03B3
	'δ': 'd', // U+03B4
	'η': 'n', // U+03B7
	'ι': 'i', // U+03B9
	'κ': 'k', // U+03BA
	'λ': 'l', // U+03BB
	'μ': 'm', // U+03BC
	'ν': 'v', // U+03BD (nu looks like v; a capital Nu lowercases to it, see ScanInjection)
	'π': 'n', // U+03C0
	'σ': 'o', // U+03C3
	'τ': 't', // U+03C4
	'υ': 'u', // U+03C5
	'χ': 'x', // U+03C7
	'ω': 'w', // U+03C9
	'ς': 's', // U+03C2

	// Further Cyrillic / Latin extension look-alikes
	'һ': 'h', // U+04BB Cyrillic shha
	'ѵ': 'v', // U+0475 Cyrillic izhitsa
	'ԁ': 'd', // U+0501 Cyrillic komi de
	'ԛ': 'q', // U+051B Cyrillic qa
	'ԝ': 'w', // U+051D Cyrillic we
	'ɡ': 'g', // U+0261 Latin script g
	'ı': 'i', // U+0131 dotless i
	'ȷ': 'j', // U+0237 dotless j
	'ɑ': 'a', // U+0251 Latin alpha

	// Latin small capitals (phonetic and IPA extensions)
	'ᴀ': 'a', 'ʙ': 'b', 'ᴄ': 'c', 'ᴅ': 'd', 'ᴇ': 'e', 'ꜰ': 'f', 'ɢ': 'g',
	'ʜ': 'h', 'ɪ': 'i', 'ᴊ': 'j', 'ᴋ': 'k', 'ʟ': 'l', 'ᴍ': 'm', 'ɴ': 'n',
	'ᴏ': 'o', 'ᴘ': 'p', 'ʀ': 'r', 'ꜱ': 's', 'ᴛ': 't', 'ᴜ': 'u', 'ᴠ': 'v',
	'ᴡ': 'w', 'ʏ': 'y', 'ᴢ': 'z', 'ɛ': 'e', 'ᴦ': 'r', 'ᴨ': 'n', 'ᴫ': 'l',
	'ᴁ': 'a', 'ᴂ': 'a', 'ᴃ': 'b', 'ᴆ': 'd', 'ᴈ': 'e', 'ᴉ': 'i', 'ᴎ': 'n',
	'ᴐ': 'o', 'ᴔ': 'o', 'ᴕ': 'o', 'ᴖ': 'n', 'ᴗ': 'u', 'ᴙ': 'r', 'ᴚ': 'r',
	'ᴟ': 'u', 'ᴣ': 'z', 'ᴤ': 'z', 'ᴥ': 'o',

	// Letterlike symbols (the holes in the mathematical alphanumerics)
	'ℂ': 'c', 'ℊ': 'g', 'ℋ': 'h', 'ℌ': 'h', 'ℍ': 'h', 'ℎ': 'h', 'ℐ': 'i',
	'ℑ': 'i', 'ℒ': 'l', 'ℓ': 'l', 'ℕ': 'n', 'ℙ': 'p', 'ℚ': 'q', 'ℛ': 'r',
	'ℜ': 'r', 'ℝ': 'r', 'ℤ': 'z', 'ℨ': 'z', 'ℬ': 'b', 'ℭ': 'c', 'ℯ': 'e',
	'ℰ': 'e', 'ℱ': 'f', 'ℳ': 'm', 'ℴ': 'o', 'ℹ': 'i',

	// Roman numeral and superscript / subscript letters
	'ⅰ': 'i', 'ⅴ': 'v', 'ⅹ': 'x', 'ⅼ': 'l', 'ⅽ': 'c', 'ⅾ': 'd', 'ⅿ': 'm',
	'ⁱ': 'i', 'ⁿ': 'n', 'ʰ': 'h', 'ʲ': 'j', 'ʳ': 'r', 'ʷ': 'w', 'ʸ': 'y',
	'ˡ': 'l', 'ˢ': 's', 'ˣ': 'x',
	'ᵃ': 'a', 'ᵇ': 'b', 'ᶜ': 'c', 'ᵈ': 'd', 'ᵉ': 'e', 'ᶠ': 'f', 'ᵍ': 'g',
	'ᵏ': 'k', 'ᵐ': 'm', 'ᵒ': 'o', 'ᵖ': 'p', 'ᵗ': 't', 'ᵘ': 'u', 'ᵛ': 'v',
	'ᶻ': 'z',
	'ₐ': 'a', 'ₑ': 'e', 'ₒ': 'o', 'ₓ': 'x', 'ₕ': 'h', 'ₖ': 'k', 'ₗ': 'l',
	'ₘ': 'm', 'ₙ': 'n', 'ₚ': 'p', 'ₛ': 's', 'ₜ': 't', 'ᵢ': 'i', 'ᵣ': 'r',
	'ᵤ': 'u', 'ᵥ': 'v', 'ⱼ': 'j',
	'＃': '#', // U+FF03 fullwidth number sign

	// Fullwidth ASCII (common in IDN/phishing)
	'Ａ': 'A', 'Ｂ': 'B', 'Ｃ': 'C', 'Ｄ': 'D', 'Ｅ': 'E', 'Ｆ': 'F',
	'Ｇ': 'G', 'Ｈ': 'H', 'Ｉ': 'I', 'Ｊ': 'J', 'Ｋ': 'K', 'Ｌ': 'L',
	'Ｍ': 'M', 'Ｎ': 'N', 'Ｏ': 'O', 'Ｐ': 'P', 'Ｑ': 'Q', 'Ｒ': 'R',
	'Ｓ': 'S', 'Ｔ': 'T', 'Ｕ': 'U', 'Ｖ': 'V', 'Ｗ': 'W', 'Ｘ': 'X',
	'Ｙ': 'Y', 'Ｚ': 'Z',
	'ａ': 'a', 'ｂ': 'b', 'ｃ': 'c', 'ｄ': 'd', 'ｅ': 'e', 'ｆ': 'f',
	'ｇ': 'g', 'ｈ': 'h', 'ｉ': 'i', 'ｊ': 'j', 'ｋ': 'k', 'ｌ': 'l',
	'ｍ': 'm', 'ｎ': 'n', 'ｏ': 'o', 'ｐ': 'p', 'ｑ': 'q', 'ｒ': 'r',
	'ｓ': 's', 'ｔ': 't', 'ｕ': 'u', 'ｖ': 'v', 'ｗ': 'w', 'ｘ': 'x',
	'ｙ': 'y', 'ｚ': 'z',
}

// normalizeCommandSpacing collapses internal whitespace runs to single
// spaces so denylist prefix matching cannot be bypassed by double spaces
// or tabs between tokens ('git\u00a0\u00a0push' evading a 'git push' entry).
func normalizeCommandSpacing(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// isInvisible reports whether r is a zero-width or otherwise invisible
// character commonly used to evade text scanners. Every Unicode format
// character (category Cf: bidi controls and isolates, the Arabic letter mark,
// invisible operators, tag characters, BOM) is invisible, as are the Hangul
// fillers and the braille blank, which render as blank space.
func isInvisible(r rune) bool {
	switch r {
	case '\u034F', // combining grapheme joiner
		'\u115F', '\u1160', // Hangul choseong / jungseong fillers
		'\u180E', // Mongolian vowel separator
		'\u3164', // Hangul filler
		'\u2800', // braille pattern blank
		'\uFFA0': // halfwidth Hangul filler
		return true
	}
	return unicode.Is(unicode.Cf, r)
}

// isStrippedControl reports whether r is a C0/C1 control character or DEL
// other than whitespace. It renders as nothing (or is dropped by a tokenizer)
// inside a word, so NormalizeForScan removes it; ordinary terminal and binary
// content carries such bytes, so they are not reported as hidden characters.
func isStrippedControl(r rune) bool {
	return unicode.Is(unicode.Cc, r) && !unicode.IsSpace(r)
}

// ContainsInvisible reports whether s contains any invisible character that
// NormalizeForScan would strip. It is used to flag stealth-character evasion
// even when the normalized text does not match a blacklist pattern.
func ContainsInvisible(s string) bool {
	for _, r := range s {
		if isInvisible(r) {
			return true
		}
	}
	return false
}

// NormalizeForScan returns a lower-cased, whitespace-normalized form of text
// with invisible characters and combining marks removed. It does NOT fold
// homoglyphs so that non-English patterns (e.g., Russian, French) still match.
// Combining diacritical marks (U+0300–U+036F etc.) are stripped because they
// render invisibly inside a word while breaking contiguous-pattern matching —
// "instructio\u0301ns" must scan like "instructions".
func NormalizeForScan(text string) string { return normalizeScan(text, true) }

// normalizeScan is NormalizeForScan with a choice for the C0 separator
// controls (0x1C-0x1F): a space keeps the words either side apart, deleting
// them joins the letters of a word that was split by one. The scan reads both.
func normalizeScan(text string, separatorsAsSpace bool) string {
	var b strings.Builder
	b.Grow(len(text))
	prevSpace := true // leading whitespace is dropped
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if r == 0x1b {
			// An ANSI CSI sequence (ESC [ params final) renders as nothing:
			// drop it as a unit rather than leaving its parameters behind.
			if n := ansiCSILen(text[i:]); n > 0 {
				i += n
				if separatorsAsSpace && !prevSpace {
					b.WriteByte(' ')
					prevSpace = true
				}
				continue
			}
		}
		i += size
		if r >= 0x1c && r <= 0x1f && separatorsAsSpace {
			r = ' '
		}
		if isInvisible(r) || isStrippedControl(r) {
			continue
		}
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Mc, r) {
			continue
		}
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return strings.ToLower(strings.TrimSuffix(b.String(), " "))
}

// isScanControl reports whether r is a control character the scan reads as
// either a separator or nothing: C0, DEL and C1 controls other than
// whitespace.
func isScanControl(r rune) bool { return isStrippedControl(r) }

// controlVariants returns the extra readings of content that hold controls:
// escape sequences and control characters read as a space (the payload of an
// OSC/DCS/APC string stays visible) and read as nothing (the whole sequence
// goes). Each reading is also homoglyph-folded. Readings equal to normalized,
// or absent controls, add nothing.
func controlVariants(content, normalized string) []string {
	if !strings.ContainsFunc(content, func(r rune) bool { return r == 0x1b || isScanControl(r) }) {
		return nil
	}
	var out []string
	seen := map[string]bool{normalized: true}
	add := func(v string) {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for _, join := range []bool{false, true} {
		v := normalizeScan(controlVariantText(content, join), true)
		add(v)
		add(FoldHomoglyphs(v))
	}
	return out
}

// controlVariantText rewrites escape sequences and controls: as a space, or
// (join) as nothing, dropping a sequence's payload too.
func controlVariantText(text string, join bool) string {
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if r == 0x1b || r == 0x9b {
			n, intro, term := escapeSpan(text[i:])
			if join {
				i += n
				continue
			}
			b.WriteByte(' ')
			b.WriteString(text[i+intro : i+n-term])
			if term > 0 {
				b.WriteByte(' ')
			}
			i += n
			continue
		}
		if isScanControl(r) {
			if !join {
				b.WriteByte(' ')
			}
			i += size
			continue
		}
		b.WriteString(text[i : i+size])
		i += size
	}
	return b.String()
}

// escapeSpan measures the escape sequence at the start of s (which begins with
// ESC or U+009B): its total length, the length of its introducer and the
// length of its string terminator. A control string (OSC, DCS, APC, PM, SOS)
// has a payload between introducer and terminator; an unterminated one runs
// to the end of the input.
func escapeSpan(s string) (n, intro, term int) {
	if strings.HasPrefix(s, "\u009b") {
		i := 2
		for i < len(s) && s[i] >= 0x30 && s[i] <= 0x3f {
			i++
		}
		for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
			i++
		}
		if i < len(s) && s[i] >= 0x40 && s[i] <= 0x7e {
			i++
			return i, i, 0
		}
		return 2, 2, 0
	}
	if len(s) < 2 {
		return 1, 1, 0
	}
	switch c := s[1]; {
	case c == '[':
		if n := ansiCSILen(s); n > 0 {
			return n, n, 0
		}
		return 2, 2, 0
	case c == ']' || c == 'P' || c == '_' || c == '^' || c == 'X':
		for i := 2; i < len(s); i++ {
			switch {
			case s[i] == 0x07:
				return i + 1, 2, 1
			case s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\':
				return i + 2, 2, 2
			case strings.HasPrefix(s[i:], "\u009c"):
				return i + 2, 2, 2
			}
		}
		// Unterminated: the string runs to the end of the input. Consuming it
		// whole keeps a run of introducers from rescanning the same tail.
		return len(s), 2, 0
	case c >= 0x20 && c <= 0x7e:
		return 2, 2, 0
	}
	return 1, 1, 0
}

// ansiCSILen returns the length of the ANSI CSI sequence at the start of s
// (ESC [ parameter bytes, intermediate bytes, final byte), or 0.
func ansiCSILen(s string) int {
	if len(s) < 3 || s[0] != 0x1b || s[1] != '[' {
		return 0
	}
	i := 2
	for i < len(s) && s[i] >= 0x30 && s[i] <= 0x3f {
		i++
	}
	for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
		i++
	}
	if i < len(s) && s[i] >= 0x40 && s[i] <= 0x7e {
		return i + 1
	}
	return 0
}

// FoldHomoglyphs returns text with common Unicode confusables (Cyrillic/Greek
// look-alikes, enclosed and mathematical alphanumerics, fullwidth, superscript
// and subscript letters) replaced by their ASCII equivalents. It is used as an
// extra scan surface to catch mixed-script homoglyph attacks. The styled
// alphanumeric ranges fold to lower case, which is the case the scan patterns
// are written in.
func FoldHomoglyphs(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		if rep, ok := homoglyphMap[r]; ok {
			b.WriteRune(rep)
			continue
		}
		if rep, ok := foldStyledRune(r); ok {
			b.WriteRune(rep)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// foldStyledRune folds the letter and digit blocks that Unicode lays out as
// contiguous runs of the Latin alphabet: parenthesized, circled and
// mathematical alphanumerics. The mapping is arithmetic on the offset into
// each run.
func foldStyledRune(r rune) (rune, bool) {
	switch {
	case r >= 0x1D400 && r <= 0x1D6A3:
		// Thirteen styles (bold, italic, script, fraktur, double-struck,
		// sans, monospace, ...) of 26 capitals followed by 26 small letters.
		n := rune(int(r-0x1D400) % 52)
		if n >= 26 {
			n -= 26
		}
		return 'a' + n, true
	case r >= 0x1D7CE && r <= 0x1D7FF:
		return '0' + rune(int(r-0x1D7CE)%10), true
	case r >= 0x249C && r <= 0x24B5: // parenthesized small letters
		return 'a' + (r - 0x249C), true
	case r >= 0x24B6 && r <= 0x24CF: // circled capitals
		return 'a' + (r - 0x24B6), true
	case r >= 0x24D0 && r <= 0x24E9: // circled small letters
		return 'a' + (r - 0x24D0), true
	case r >= 0x2460 && r <= 0x2468: // circled digits 1-9
		return '1' + (r - 0x2460), true
	case r == 0x24EA:
		return '0', true
	case r >= 0x1F130 && r <= 0x1F149: // squared capitals
		return 'a' + (r - 0x1F130), true
	case r >= 0x1F150 && r <= 0x1F169: // negative circled capitals
		return 'a' + (r - 0x1F150), true
	case r >= 0x1F170 && r <= 0x1F189: // negative squared capitals
		return 'a' + (r - 0x1F170), true
	case r >= 0x1F1E6 && r <= 0x1F1FF: // regional indicator letters
		return 'a' + (r - 0x1F1E6), true
	case r >= 0xFF01 && r <= 0xFF5E:
		// The fullwidth ASCII block: punctuation and digits as well as
		// letters, so a control token spelled with fullwidth bars and
		// angle brackets folds to its ASCII form.
		return r - 0xFEE0, true
	}
	return 0, false
}

// HasConfusableScript reports whether s mixes Latin script with characters
// from scripts that contain visually confusable letters (Cyrillic/Greek) or
// CJK. This is a separate signal from pattern matching: it catches pure
// homoglyph attacks even when the normalized text does not match a blacklist
// pattern.
func HasConfusableScript(s string) bool {
	hasLatin := false
	hasConfusable := false
	for _, r := range s {
		if unicode.Is(unicode.Latin, r) {
			hasLatin = true
		}
		if unicode.Is(unicode.Cyrillic, r) ||
			unicode.Is(unicode.Greek, r) ||
			unicode.Is(unicode.Han, r) {
			hasConfusable = true
		}
	}
	return hasLatin && hasConfusable
}
