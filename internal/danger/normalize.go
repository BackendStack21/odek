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
	'ν': 'n', // U+03BD
	'π': 'n', // U+03C0
	'σ': 'o', // U+03C3
	'τ': 't', // U+03C4
	'υ': 'u', // U+03C5
	'χ': 'x', // U+03C7
	'ω': 'w', // U+03C9
	'ς': 's', // U+03C2

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

// isInvisible reports whether r is a zero-width or otherwise invisible
// character commonly used to evade text scanners.
func isInvisible(r rune) bool {
	switch r {
	case '\u00AD', // soft hyphen
		'\u034F', // combining grapheme joiner
		'\u180E', // Mongolian vowel separator
		'\u200B', // zero-width space
		'\u200C', // zero-width non-joiner
		'\u200D', // zero-width joiner
		'\u200E', // left-to-right mark
		'\u200F', // right-to-left mark
		'\u202A', // left-to-right embedding
		'\u202B', // right-to-left embedding
		'\u202C', // pop directional formatting
		'\u202D', // left-to-right override
		'\u202E', // right-to-left override
		'\u2060', // word joiner
		'\u2061', // function application
		'\u2062', // invisible times
		'\u2063', // invisible separator
		'\u2064', // invisible plus
		'\uFEFF': // byte order mark
		return true
	}
	return false
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
// with invisible characters removed. It does NOT fold homoglyphs so that
// non-English patterns (e.g., Russian, French) still match.
func NormalizeForScan(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		if isInvisible(r) {
			continue
		}
		b.WriteRune(r)
	}
	normalized := strings.ToLower(b.String())
	// Collapse all whitespace to a single space so patterns do not have to
	// account for arbitrary runs of spaces, tabs, or newlines.
	return strings.Join(strings.Fields(normalized), " ")
}

// FoldHomoglyphs returns text with common Unicode confusables (Cyrillic/Greek
// look-alikes) replaced by their ASCII equivalents. It is used as an extra
// scan surface to catch mixed-script homoglyph attacks.
func FoldHomoglyphs(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		if rep, ok := homoglyphMap[r]; ok {
			b.WriteRune(rep)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
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
