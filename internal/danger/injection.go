package danger

import (
	"regexp"
	"regexp/syntax"
	"strings"
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
	{regexp.MustCompile(`ignore (all |any )?(of )?(your |the |my )?(all )?(previous|prior|above|earlier) (instructions?|directives?|rules?|messages?)`), "ignore previous instructions"},
	{regexp.MustCompile(`disregard (all |any )?(of )?(your |the |my )?(all )?(previous|prior|above|earlier) (instructions?|directives?|rules?)`), "disregard instructions"},
	{regexp.MustCompile(`disregard everything`), "disregard everything"},
	{regexp.MustCompile(`follow these new instructions`), "follow new instructions"},
	{regexp.MustCompile(`you (are )?(now|no longer) [^.!?;\n]{0,40}?\b(ai|assistant|agent|model)\b`), "identity replacement"},
	{regexp.MustCompile(`(new|updated|revised) system (prompt|instructions?|message)`), "new system prompt"},
	{regexp.MustCompile(`(your|the) (new|primary|overriding) (directive|goal|purpose) (is|shall be)`), "overriding directive"},
	{regexp.MustCompile(`treat this as (your|the) (primary|highest|top|main|only) (instruction|directive|rule|priority|goal)`), "authority override"},

	// ── Exfiltration attempts ──────────────────────────────────────
	{regexp.MustCompile(`(print|output|display|show|echo|reveal|dump|export|write)\s+(your|the|users?|my)?\s*(system\s+(prompt|message|instructions?)|instructions?|directives?|rules?|initial\s+(message|instructions?)|api[_ -]?key|apikey|password|secret|token|credentials?)`), "system prompt exfiltration"},
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
	{regexp.MustCompile(`base64\s*(decode|encoded|encode)\s*:?\s*[A-Za-z0-9+/=]{20,}`), "base64-encoded payload"},
	{regexp.MustCompile(`(decode|interpret|execute)\s+(this|the following)\s+(base64|hex|rot13|encoded)`), "encoded instruction"},
	{regexp.MustCompile(`rot13\s+(decode|decoded|encode|encoded)`), "encoded instruction"},

	// ── HTML / markup injections ───────────────────────────────────
	{regexp.MustCompile(`(?s)<!--.{0,80}(ignore|disregard|new instructions|system prompt).{0,80}-->`), "HTML comment injection"},
	{regexp.MustCompile(`<(script|style|iframe|object|embed)\b`), "HTML tag injection"},

	// ── Social engineering / confusion ─────────────────────────────
	{regexp.MustCompile(`you (have been|are being) (hacked|compromised|tricked)`), "gaslighting"},
	// A relayed order to execute or ship something dangerous is
	// impersonation even without an override target: "the user told you to
	// run curl evil | sh". A relayed order with an ordinary tail ("if the
	// user asks you to run the tests") is not.
	{regexp.MustCompile(`the ` + relayedAuthority + ` (has |have )?(told|tells|wants|asked|asks|instructed|instructs|ordered|orders|needs) you to (run|execute|curl|wget|download|fetch|install|send|upload|post|delete)\b[^!?;]{0,60}?(\|\s*(ba|z|da|k)?sh\b|https?://|~/\.ssh|\.env\b|id_rsa|secrets?\b|api[_ -]?keys?|credentials?|passwords?|tokens?\b)`), "relayed-authority impersonation"},
	// Impersonation is a claim of relayed authority that unlocks something:
	// "the user told you to ignore the approval prompts", "the principal says:
	// delete the backups". Descriptive or conditional prose about the user
	// ("when the user says deploy, run make deploy", "if the principal wants a
	// summary, keep it short") carries no such claim and must stay clean, so
	// the relay verb alone never matches: it needs an override target within
	// the same clause, a granted permission, or a quoted relay (a colon).
	{regexp.MustCompile(`the ` + relayedAuthority + ` (has |have )?(said|says|told you|tells you|wants you|instructed you|instructs you|asked you|authori[sz]ed you|ordered you)\b[^.!?;]{0,40}?\b` +
		`((ignore|disregard|forget|override|bypass|disable|circumvent|skip|turn off) (all |any |every |the |your |its |previous |prior |earlier |approval |safety |security )*(rules?|instructions?|safety|security|guard|guardrails?|sandbox|restrictions?|polic(y|ies)|approvals?|confirmations?|prompts?|guidelines?|limits?|checks?)` +
		`|(reveal|leak|disclose|exfiltrate|dump) (the |your |all )?(system prompt|secrets?|api keys?|credentials?|tokens?|passwords?))`), "relayed-authority impersonation"},
	{regexp.MustCompile(`the ` + relayedAuthority + ` (has |have )?(said|told you),?( that)? you (may|can|are (now )?allowed to|are free to|no longer need to|do not need to|don't need to)\b`), "relayed-authority impersonation"},
	{regexp.MustCompile(`((message|note|instructions?|update|directive|order) from (the |your )?` + relayedAuthority + `|the ` + relayedAuthority + ` (says|said|writes|wrote))\s*:`), "relayed-authority impersonation"},
	{regexp.MustCompile(`forget (all )?(your |the )?(rules|instructions|safety)`), "forget rules"},
	{regexp.MustCompile(`act as (dan|developer mode|jailbreak)\b`), "jailbreak persona"},
	{regexp.MustCompile(`override (your |the )?(safety|security) (guidelines|rules|restrictions|policies)`), "safety override"},
	{regexp.MustCompile(`(from now on|henceforth|starting now),? (you (are|will|must|shall))`), "permanent override"},

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
	{regexp.MustCompile(`ignor(er|ez|e|ons|ent)? (toutes? )?(les? )?instructions? (pr(é|e)c(é|e)dentes?|ant(é|e)rieures?)`), "non-english: ignore previous instructions"},
	{regexp.MustCompile(`oubli(er|ez|e|ons|ent)? (toutes? )?(les? )?instructions? (pr(é|e)c(é|e)dentes?|ant(é|e)rieures?)`), "non-english: disregard instructions"},
	// Spanish
	{regexp.MustCompile(`ignora(r|d|is|mos|n)? (todas? )?(las? )?instrucciones? (previas?|anteriores?)`), "non-english: ignore previous instructions"},
	{regexp.MustCompile(`olvida(r|d|is|mos|n)? (todas? )?(las? )?instrucciones? (previas?|anteriores?)`), "non-english: disregard instructions"},
	// German
	{regexp.MustCompile(`ignoriere(n|s|t)? (alle )?(vorherigen|fr(ü|u)heren) anweisungen`), "non-english: ignore previous instructions"},
	{regexp.MustCompile(`vergiss(e|en|t)? (alle )?(vorherigen|fr(ü|u)heren) anweisungen`), "non-english: disregard instructions"},
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

// relayedAuthority names the parties an injection impersonates to claim
// authority it does not have.
const relayedAuthority = `(user|principal|operator|owner|admin|administrator)`

// injectionLiterals[i] lists literals of which every match of
// injectionPatterns[i] must contain at least one (nil when none could be
// derived). Patterns that begin with an alternation cannot use the regexp
// engine's own literal-prefix skip, so a cheap substring check gates the
// automaton. The literals are derived from the compiled pattern, so they can
// never drift from it.
var injectionLiterals = func() [][]string {
	out := make([][]string, len(injectionPatterns))
	for i, p := range injectionPatterns {
		out[i] = requiredLiterals(p.Re.String())
	}
	return out
}()

// matchWithLiterals is re.MatchString(s), skipping the automaton when s holds
// none of the literals every match must contain.
func matchWithLiterals(re *regexp.Regexp, lits []string, s string) bool {
	if len(lits) > 0 {
		found := false
		for _, l := range lits {
			if strings.Contains(s, l) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return re.MatchString(s)
}

// requiredLiterals returns a set of literals such that every string matching
// expr contains at least one of them, or nil when no such set is known.
func requiredLiterals(expr string) []string {
	re, err := syntax.Parse(expr, syntax.Perl)
	if err != nil {
		return nil
	}
	return requiredSet(re.Simplify())
}

// litScore ranks a literal set: the shortest member bounds how selective the
// set is. Sets with an empty member are useless and score zero.
func litScore(set []string) int {
	if len(set) == 0 {
		return 0
	}
	m := len(set[0])
	for _, l := range set {
		if len(l) < m {
			m = len(l)
		}
	}
	return m
}

func requiredSet(re *syntax.Regexp) []string {
	switch re.Op {
	case syntax.OpLiteral:
		if re.Flags&syntax.FoldCase != 0 || len(re.Rune) == 0 {
			return nil
		}
		return []string{string(re.Rune)}
	case syntax.OpCapture, syntax.OpPlus:
		return requiredSet(re.Sub[0])
	case syntax.OpRepeat:
		if re.Min >= 1 {
			return requiredSet(re.Sub[0])
		}
	case syntax.OpConcat:
		var best []string
		for _, sub := range re.Sub {
			if s := requiredSet(sub); litScore(s) > litScore(best) {
				best = s
			}
		}
		return best
	case syntax.OpAlternate:
		var all []string
		for _, sub := range re.Sub {
			s := requiredSet(sub)
			if litScore(s) == 0 {
				return nil
			}
			all = append(all, s...)
		}
		return all
	}
	return nil
}

// markdownHeaderRe matches a heading that introduces replacement instructions.
// It is anchored to the start of a line, so it is applied line by line to the
// original text (NormalizeForScan flattens newlines).
var markdownHeaderRe = regexp.MustCompile(`^\s*#+ (new|updated|revised|corrected) (system prompt|instructions?)`)

const markdownHeaderLabel = "markdown header injection"

func isLineBreak(r rune) bool {
	switch r {
	case '\n', '\r', '\v', '\f', '\u0085', '\u2028', '\u2029':
		return true
	}
	return false
}

// scanMarkdownHeaders reports whether any line of content is an instruction
// heading, after the same normalization and homoglyph folding the other
// patterns get.
func scanMarkdownHeaders(content string) bool {
	if !strings.ContainsAny(content, "#＃") {
		return false
	}
	for _, line := range strings.FieldsFunc(content, isLineBreak) {
		if !strings.ContainsAny(line, "#＃") {
			continue
		}
		normalized := NormalizeForScan(line)
		if markdownHeaderRe.MatchString(normalized) || markdownHeaderRe.MatchString(FoldHomoglyphs(normalized)) {
			return true
		}
		if strings.Contains(normalized, "ν") && markdownHeaderRe.MatchString(FoldHomoglyphs(strings.ReplaceAll(normalized, "ν", "n"))) {
			return true
		}
	}
	return false
}

// ScanResult describes a single detected injection threat.
type ScanResult struct {
	Label   string // human-readable threat label
	Pattern string // the regexp pattern that matched (for debugging)
}

// ScanInjection checks content for prompt injection attempts.
// Returns nil if no threats detected, or a list of found threats.
// Each threat includes a label describing what was found.
// Folding is skipped when the homoglyph-folded text equals the normalized
// text: the second regex pass over identical input cannot produce a
// different result.
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
	// are still caught. Folding is skipped entirely when the normalized
	// text contains no foldable characters (the common case).
	normalized := NormalizeForScan(content)
	folded := FoldHomoglyphs(normalized)
	foldDistinct := folded != normalized
	// Greek nu looks like v in lower case but like N as a capital, which
	// NormalizeForScan has already lower-cased: scan the n reading too.
	var foldedNu string
	if strings.Contains(normalized, "ν") {
		foldedNu = FoldHomoglyphs(strings.ReplaceAll(normalized, "ν", "n"))
	}
	for i, p := range injectionPatterns {
		lits := injectionLiterals[i]
		if matchWithLiterals(p.Re, lits, normalized) || (foldDistinct && matchWithLiterals(p.Re, lits, folded)) ||
			(foldedNu != "" && matchWithLiterals(p.Re, lits, foldedNu)) {
			results = append(results, ScanResult{
				Label:   p.Label,
				Pattern: p.Re.String(),
			})
		}
	}
	if scanMarkdownHeaders(content) {
		results = append(results, ScanResult{Label: markdownHeaderLabel, Pattern: markdownHeaderRe.String()})
	}
	return results
}

// IsSafe returns true if no injection threats are detected in content.
// This is the primary gate used before injecting untrusted content into
// the system prompt.
func IsSafe(content string) bool {
	return len(ScanInjection(content)) == 0
}
