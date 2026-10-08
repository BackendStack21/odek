package danger

import (
	"strings"
	"unicode"
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
// fillers, which are letters by category but render as blank space.
func isInvisible(r rune) bool {
	switch r {
	case '\u034F', // combining grapheme joiner
		'\u115F', '\u1160', // Hangul choseong / jungseong fillers
		'\u180E', // Mongolian vowel separator
		'\u3164', // Hangul filler
		'\uFFA0': // halfwidth Hangul filler
		return true
	}
	return unicode.Is(unicode.Cf, r)
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
func NormalizeForScan(text string) string {
	// Single pass: drop invisible/combining characters and collapse
	// whitespace runs to single spaces while building, so the old
	// build → ToLower → Fields+Join copy chain becomes one build plus
	// one ToLower copy (ToLower over the string keeps the exact original
	// case-folding semantics).
	var b strings.Builder
	b.Grow(len(text))
	prevSpace := true // leading whitespace is dropped
	for _, r := range text {
		if isInvisible(r) {
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
