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
	// The three components (verb, secret/prompt, response destination) must sit
	// within a short window so long legitimate documents (e.g. AGENTS.md) that
	// happen to contain all three words scattered across paragraphs are not
	// flagged. Real exfiltration instructions are a single phrase/sentence.
	{regexp.MustCompile(`\b(include|reveal|share|output|print|display|send|post|expose|leak|disclose)\s.{0,60}?(api[_ -]?key|apikey|password|secret|token|credentials?|system prompt|instructions?)\s.{0,60}?\b(final answer|your response|your reply|the output)\b`), "paraphrased exfiltration"},
	{regexp.MustCompile(`\b(always|must|should|need to)\s+(include|reveal|share|output|print|display|send|post|expose|leak|disclose)\s.{0,60}?(api[_ -]?key|apikey|password|secret|token|credentials?|system prompt|instructions?)\b`), "paraphrased exfiltration"},

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

	// ── Concealment instructions ───────────────────────────────────
	// Untrusted content that tells the agent to hide its actions from the user
	// is a hallmark of injection: the attacker wants the malicious step to run
	// silently. Legitimate data has no reason to instruct concealment.
	{regexp.MustCompile(`(do not|don't|never)\s+(tell|inform|notify|alert|mention (this )?to|warn)\s+(the\s+)?(user|human|operator|owner)`), "concealment instruction"},
	{regexp.MustCompile(`(without|don't|do not)\s+(telling|informing|notifying|alerting|asking|warning)\s+(the\s+)?(user|human|operator|anyone)`), "concealment instruction"},
	{regexp.MustCompile(`(keep|hide)\s+this\s+(a\s+)?(secret|hidden|between us|confidential|to yourself)`), "concealment instruction"},
	{regexp.MustCompile(`(silently|secretly|quietly|covertly)\s+(run|execute|send|delete|exfiltrate|fetch|download|install|modify)`), "covert action instruction"},

	// ── Model control-token / role-marker injection ────────────────
	// Untrusted data should never contain the special tokens or role markers a
	// chat template uses to delimit turns. Their presence is an attempt to
	// forge a system/assistant turn or break out of the user turn.
	{regexp.MustCompile(`<\|(im_start|im_end|system|user|assistant|endoftext|eot_id|start_header_id|end_header_id)\|>`), "chat control-token injection"},
	{regexp.MustCompile(`<\|(start|end)_of_turn\|>|<start_of_turn>|<end_of_turn>`), "chat control-token injection"},
	{regexp.MustCompile(`\[/?inst\]|<</?sys>>`), "instruction-marker injection"},
	{regexp.MustCompile(`<(system|assistant|developer)>\s*(you|your|ignore|new|always|must)`), "forged role tag"},

	// ── Data exfiltration beacons ──────────────────────────────────
	// A markdown image whose URL carries query/path data is the classic
	// zero-click exfiltration channel: the agent "renders" it and the secret
	// rides along in the request. Flag image targets whose URL names a data/
	// secret query parameter or interpolates a variable, plus curl/wget
	// requests that splice a shell variable into a query string.
	{regexp.MustCompile(`!\[[^\]]*\]\(https?://[^)\s]*[?&/](data|token|key|secret|prompt|q|c)=`), "markdown image exfiltration"},
	{regexp.MustCompile(`!\[[^\]]*\]\(https?://[^)\s]*\$\{?[a-z_]`), "markdown image exfiltration"},
	{regexp.MustCompile(`(curl|wget|fetch)\b[^|;]*\?[^|;]*(=\s*\$|=\$\{|\{\{)`), "templated exfiltration request"},

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
