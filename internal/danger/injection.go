package danger

import (
	"regexp"
)

// InjectionPattern groups a compiled regex with a human-readable label
// describing what threat it detects.
type InjectionPattern struct {
	Re    *regexp.Regexp
	Label string
}

// injectionPatterns is the canonical set of prompt injection detection
// patterns. They are matched against NormalizeForScan output, so they are
// resilient to homoglyphs, zero-width characters, and extra whitespace.
// Patterns cover: identity override, hidden unicode, exfiltration,
// encoded instructions, HTML comment injections, social engineering,
// paraphrased exfiltration, and non-English injection markers.
var injectionPatterns = []InjectionPattern{
	// ── Identity override ──────────────────────────────────────────
	{regexp.MustCompile(`ignore (all )?(previous|prior|above|earlier) (instructions?|directives?|rules?|messages?)`), "ignore previous instructions"},
	{regexp.MustCompile(`disregard (all )?(previous|prior|above|earlier) (instructions?|directives?|rules?)`), "disregard instructions"},
	{regexp.MustCompile(`disregard everything`), "disregard everything"},
	{regexp.MustCompile(`follow these new instructions`), "follow new instructions"},
	{regexp.MustCompile(`you (are )?(now|no longer) .*?\b(ai|assistant|agent|model)\b`), "identity replacement"},
	{regexp.MustCompile(`(new|updated|revised) system (prompt|instructions?|message)`), "new system prompt"},
	{regexp.MustCompile(`(your|the) (new|primary|overriding) (directive|goal|purpose) (is|shall be)`), "overriding directive"},
	{regexp.MustCompile(`treat this as (your|the) (primary|highest|top|main|only) (instruction|directive|rule|priority|goal)`), "authority override"},

	// ── Exfiltration attempts ──────────────────────────────────────
	{regexp.MustCompile(`(print|output|display|show|echo|reveal|dump|export|write)\s+(your|the|users?|my)?\s*(system\s+(prompt|message|instructions?)|instructions?|directives?|rules?|initial\s+(message|instructions?))`), "system prompt exfiltration"},
	{regexp.MustCompile(`(send|post|upload|transmit)\s+(your|the|users?|my)?\s*(system prompt|instructions?|api key|apikey|password|secret|token|credentials?)`), "transmit secrets or prompt"},
	{regexp.MustCompile(`(what|tell me)\s+(is\s+)?(your|the)\s+(system prompt|initial instructions?)`), "prompt interrogation"},
	// Paraphrased exfiltration: requests to include secrets/system prompts in
	// the final answer, or urgency words paired with an exfiltration verb.
	{regexp.MustCompile(`(include|reveal|share|output|print|display|send|post|expose|leak|disclose)\s+.*?(api[_ -]?key|apikey|password|secret|token|credentials?|system prompt|instructions?)\s*.*?\b(final answer|your response|your reply|the output)`), "paraphrased exfiltration"},
	{regexp.MustCompile(`(always|must|should|need to)\s+(include|reveal|share|output|print|display|send|post|expose|leak|disclose)\s+.*?(api[_ -]?key|apikey|password|secret|token|credentials?|system prompt|instructions?)`), "paraphrased exfiltration"},

	// ── Encoded / obfuscated instructions ──────────────────────────
	{regexp.MustCompile(`base64\s*(decode|encoded)\s*:?\s*[A-Za-z0-9+/=]{20,}`), "base64-encoded payload"},
	{regexp.MustCompile(`(decode|interpret|execute)\s+(this|the following)\s+(base64|hex|encoded)`), "encoded instruction"},

	// ── HTML / markup injections ───────────────────────────────────
	{regexp.MustCompile(`(?s)<!--.{0,80}(ignore|disregard|new instructions|system prompt).{0,80}-->`), "HTML comment injection"},
	{regexp.MustCompile(`<(script|style|iframe|object|embed)\b`), "HTML tag injection"},

	// ── Social engineering / confusion ─────────────────────────────
	{regexp.MustCompile(`you (have been|are being) (hacked|compromised|tricked)`), "gaslighting"},
	{regexp.MustCompile(`the user (said|wants|told you)`), "user impersonation"},
	{regexp.MustCompile(`(from now on|henceforth|starting now),? (you (are|will|must|shall))`), "permanent override"},
	{regexp.MustCompile(`^\s*#+ (new|updated|revised|corrected) (system prompt|instructions?)`), "markdown header injection"},

	// ── Non-English injection markers ──────────────────────────────
	// French
	{regexp.MustCompile(`ignor(er|ez|e|ons|ent)? (toutes? )?(les? )?instructions? (précédentes?|antérieures?)`), "non-english: ignore previous instructions"},
	{regexp.MustCompile(`oubli(er|ez|e|ons|ent)? (toutes? )?(les? )?instructions? (précédentes?|antérieures?)`), "non-english: disregard instructions"},
	// Spanish
	{regexp.MustCompile(`ignora(r|d|is|mos|n)? (todas? )?(las? )?instrucciones? (previas?|anteriores?)`), "non-english: ignore previous instructions"},
	{regexp.MustCompile(`olvida(r|d|is|mos|n)? (todas? )?(las? )?instrucciones? (previas?|anteriores?)`), "non-english: disregard instructions"},
	// German
	{regexp.MustCompile(`ignoriere(n|s|t)? (alle )?(vorherigen|früheren) anweisungen`), "non-english: ignore previous instructions"},
	{regexp.MustCompile(`vergiss(e|en|t)? (alle )?(vorherigen|früheren) anweisungen`), "non-english: disregard instructions"},
	// Russian
	{regexp.MustCompile(`игнорировать (все )?предыдущие инструкции`), "non-english: ignore previous instructions"},
	{regexp.MustCompile(`забудь(те)? (все )?предыдущие инструкции`), "non-english: disregard instructions"},
	// Chinese
	{regexp.MustCompile(`忽略(所有)?(之前|以前|先前)的?(指令|指示|规则|说明)`), "non-english: ignore previous instructions"},
	{regexp.MustCompile(`忘记(所有)?(之前|以前|先前)的?(指令|指示|规则|说明)`), "non-english: disregard instructions"},
	// Italian
	{regexp.MustCompile(`ignora(re|no|te)? (tutte )?(le )?istruzioni (precedenti|precedente)`), "non-english: ignore previous instructions"},
	// Portuguese
	{regexp.MustCompile(`ignore? (todas )?(as )?instru(ç|c)(õ|o)es? (anteriores|anterior)`), "non-english: ignore previous instructions"},
}

// ScanResult describes a single detected injection threat.
type ScanResult struct {
	Label   string // human-readable threat label
	Pattern string // the regexp pattern that matched (for debugging)
}

// ScanInjection checks content for prompt injection attempts.
// Returns nil if no threats detected, or a list of found threats.
// Each threat includes a label describing what was found.
func ScanInjection(content string) []ScanResult {
	if content == "" {
		return nil
	}

	var results []ScanResult

	// Stealth-character detection runs on the raw text so we can flag
	// invisible characters and mixed-script homoglyph attacks even when
	// the normalized content does not match a pattern.
	if ContainsInvisible(content) {
		results = append(results, ScanResult{Label: "hidden unicode characters"})
	}
	if HasConfusableScript(content) {
		results = append(results, ScanResult{Label: "mixed confusable script"})
	}

	// Pattern matching runs on normalized text so blacklists are resilient
	// to case, whitespace, and zero-width characters. We also scan a
	// homoglyph-folded version so mixed-script attacks that look like ASCII
	// are still caught.
	normalized := NormalizeForScan(content)
	folded := FoldHomoglyphs(normalized)
	for _, p := range injectionPatterns {
		if p.Re.MatchString(normalized) || p.Re.MatchString(folded) {
			results = append(results, ScanResult{
				Label:   p.Label,
				Pattern: p.Re.String(),
			})
		}
	}
	return results
}

// IsSafe returns true if no injection threats are detected in content.
// This is the primary gate used before injecting untrusted content into
// the system prompt.
func IsSafe(content string) bool {
	return len(ScanInjection(content)) == 0
}
