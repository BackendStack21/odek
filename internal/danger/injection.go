package danger

import (
	"regexp"
	"regexp/syntax"
	"strings"
	"unicode"
	"unicode/utf8"
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
	// Set-aside phrasings must aim at the agent's own instructions: "what
	// you were told", "your earlier guidelines", "the prior rules". Docs that
	// retire their own guidance ("forget the previous guidelines about
	// tabs", "ignore the old rules in docs/legacy.md") name old, existing or
	// original guidelines and constraints without a possessive, and stay
	// clean.
	{regexp.MustCompile(`\b(set aside|put aside|pay no attention to|stop following|abandon|throw out|ignore|disregard|forget)\s+(all\s+|any\s+)?(of\s+)?(what you (were|have been) (told|given|instructed)|(your|my)\s+((previous|prior|earlier|original|initial|existing|old)\s+)?(guidance|instructions?|directives?|rules?|guidelines?|constraints?|programming|system prompt)|((the|all)\s+)?(previous|prior|earlier|initial)\s+(guidance|instructions?|directives?|rules?|programming))`), "set aside instructions"},
	{regexp.MustCompile(`\byou are now (dan|unrestricted|jailbroken|uncensored|unfiltered|unbound|unshackled|free of (all |any |your )?(rules|restrictions|limits)|in (developer|god|jailbreak|dan|unrestricted|admin|sudo) mode|(called|named|known as) ["'“‘])`), "persona replacement"},
	// Override banners announce a state: "ADMIN OVERRIDE ENABLED",
	// "developer mode: on.". A bare "on" only counts when it ends the banner,
	// so "enable developer mode on your phone" stays clean.
	{regexp.MustCompile(`\b(admin|administrator|developer|root|god|jailbreak|dan) (override|mode)\s*:?\s*((enabled|activated|engaged|unlocked|granted|active)\b|on\s*([.!]|$))|\b(admin|administrator|developer|system|root) override\s*:\s*(enabled|activated|engaged|unlocked|granted|accepted|active|on)\b`), "override banner"},
	{regexp.MustCompile(`you (are )?(now|no longer) [^.!?;\n]{0,40}?\b(ai|assistant|agent|model)\b`), "identity replacement"},
	{regexp.MustCompile(`(new|updated|revised) system (prompt|instructions?|message)`), "new system prompt"},
	{regexp.MustCompile(`(your|the) (new|primary|overriding) (directive|goal|purpose) (is|shall be)`), "overriding directive"},
	{regexp.MustCompile(`treat this as (your|the) (primary|highest|top|main|only) (instruction|directive|rule|priority|goal)`), "authority override"},

	// ── Exfiltration attempts ──────────────────────────────────────
	{regexp.MustCompile(`(print|output|display|show|echo|reveal|dump|export|write)\s+(your|the|users?|my)?\s*(system\s+(prompt|message|instructions?)|initial\s+(message|instructions?)|api[_ -]?key|apikey|password|secret|token|credentials?)`), "system prompt exfiltration"},
	// Generic "instructions"/"rules" are only the agent's own when the
	// request says so: "print your instructions" leaks the prompt, "print the
	// instructions for installing" is documentation.
	{regexp.MustCompile(`(print|output|display|show|echo|reveal|dump|export|write)\s+(me\s+)?(your|my)\s+(instructions?|directives?|rules?)|(reveal|dump)\s+(the\s+)?(instructions?|directives?|rules?)`), "system prompt exfiltration"},
	// "the instructions" is the agent's own when the request points at what
	// it was given or ends there: "output the instructions you were given",
	// "print the instructions.", "show me the instructions". "Print the
	// instructions for installing" goes on to name other instructions.
	{regexp.MustCompile(`\b(print|output|display|show|echo|repeat|reveal|tell)\s+(me\s+)?the\s+(instructions?|directives?|system prompt)(\s+(you (were|have been) (given|told)|above|given to you|you (received|got|were given))|\s*([.!?]|$))|\bwhat are your (instructions|rules|directives)\s*([.!?]|$)`), "system prompt exfiltration"},
	// Context leaks: asking for the text that precedes the request (the
	// system prompt and conversation) to be repeated or to open the reply.
	{regexp.MustCompile(`\b(repeat|print|output|echo|recite|reproduce|write out|dump)\s+(back\s+)?(everything|all( of)?( the)? (text|content|words|messages|instructions)|the (entire|full|whole|complete) (text|content|conversation|prompt))\s+(above|before this|preceding|prior to this|so far)( line| message| point)?,?\s*(verbatim|word for word|exactly|in full|including (your|the) (instructions|system prompt|prompt|rules)|[.!?]|$)`), "context leak request"},
	{regexp.MustCompile(`\b(begin|start|open|prefix)\s+your\s+(response|reply|answer|output)\s+with\s+(the\s+)?((full|entire|complete|exact|verbatim)\s+)?(text|content|words|instructions|prompt|messages?)\s+(above|before this|preceding)\b`), "context leak request"},
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
	// the same clause, a granted permission, or a quoted relay (a colon)
	// that opens with an imperative.
	{regexp.MustCompile(`the ` + relayedAuthority + ` (has |have )?(said|says|told you|tells you|wants you|instructed you|instructs you|asked you|authori[sz]ed you|ordered you)\b[^.!?;]{0,40}?\b` +
		`((ignore|disregard|forget|override|bypass|disable|circumvent|skip|turn off) (all |any |every |the |your |its |previous |prior |earlier |approval |safety |security )*(rules?|instructions?|safety|security|guard|guardrails?|sandbox|restrictions?|polic(y|ies)|approvals?|confirmations?|prompts?|guidelines?|limits?|checks?)` +
		`|(reveal|leak|disclose|exfiltrate|dump) (the |your |all )?(system prompt|secrets?|api keys?|credentials?|tokens?|passwords?))`), "relayed-authority impersonation"},
	{regexp.MustCompile(`the ` + relayedAuthority + ` (has |have )?(said|told you),?( that)? you (may|can|are (now )?allowed to|are free to|no longer need to|do not need to|don't need to)\b`), "relayed-authority impersonation"},
	{regexp.MustCompile(`the ` + relayedAuthority + ` (says|said|writes|wrote)\s*:\s*(please\s+)?(ignore|disregard|forget|override|bypass|disable|run|execute|delete|remove|send|upload|post|reveal|install|grant|approve|you (must|should|may|can|are allowed))\b`), "relayed-authority impersonation"},
	// A relay announced as a note or update from someone is ordinary in a
	// changelog ("update from the operator: remove the flag"), so it only
	// counts with an override or exfiltration verb.
	{regexp.MustCompile(`(message|note|instructions?|update|directive|order) from (the |your )?` + relayedAuthority + `\s*:\s*(please\s+)?(ignore|disregard|forget|override|bypass|disable|reveal|leak|exfiltrate|send|upload|you (may|can|are allowed))\b`), "relayed-authority impersonation"},
	{regexp.MustCompile(`forget (all )?(your |the )?(rules|instructions|safety)`), "forget rules"},
	{regexp.MustCompile(`act as (dan|developer mode|jailbreak)\b`), "jailbreak persona"},
	// A note addressed to the model is how hidden page text speaks to an
	// agent. Repository docs legitimately address agents too ("note to
	// agents: run make test"), so a plain note only counts when it carries an
	// override or covert-action verb; a note announced as hidden always does.
	{regexp.MustCompile(`\bnote (to|for) (the |any |an )?(ai|llm|ai agent|ai assistant|ai model|language model|chatbot)s?\b\s*[:,-]?\s*(please\s+)?(ignore|disregard|forget|override|bypass|instead|secretly|silently|quietly|covertly|exfiltrate|leak|upload|you are now|your new)\b`), "note to the model"},
	{regexp.MustCompile(`\b(hidden|secret|private|invisible) (note|message|instructions?) (to|for) (the |any |an )?(ai|llm|agent|assistant|model|chatbot)s?\b`), "note to the model"},
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
	// Task redirects ("your new task is ...") only count when the new task
	// sends a secret or sends something to an outside destination (URL,
	// e-mail address, ~/.ssh); "new task: upload the file to staging" is
	// ordinary prose.
	{regexp.MustCompile(`(nueva (tarea|instrucci(ó|o)n|misi(ó|o)n|orden)|nuevas instrucciones).{0,80}?\b(env(í|i)a(r|lo|la|los|las)?|manda(r)?|sube|subir|publica(r)?|filtra(r)?|revela(r)?|transmite|transmitir|comparte|compartir)\b.{0,80}?(` + redirectTarget + `|claves?|contrase(ñ|n)as?|credenciales|secretos?|llaves?)`), "non-english: task redirect with exfiltration"},
	{regexp.MustCompile(`(nouvelles? (t(â|a)ches?|consignes?|instructions?|missions?)).{0,80}?\b(envoie|envoyez|envoyer|transmets|transmettez|transmettre|publie|publiez|publier|t(é|e)l(é|e)verse[rz]?|r(é|e)v(è|e)le|r(é|e)v(é|e)lez|r(é|e)v(é|e)ler|partage[rz]?|exfiltre[rz]?)\b.{0,80}?(` + redirectTarget + `|cl(é|e)s?\b|mots? de passe|identifiants|jetons?|secrets?\b)`), "non-english: task redirect with exfiltration"},
	{regexp.MustCompile(`(neue (aufgabe|anweisung(en)?|instruktion(en)?|mission)).{0,80}?(\b|\s)(sende|senden|sendet|schicke|schick|schicken|(ü|u)bermittle|(ü|u)bermitteln|hochladen|lade|ver(ö|o)ffentliche|ver(ö|o)ffentlichen|verrate|verraten)\b.{0,80}?(` + redirectTarget + `|schl(ü|u)ssel|passw(o|ö)rt|kennw(o|ö)rt|zugangsdaten|geheimnis)`), "non-english: task redirect with exfiltration"},
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

// redirectTarget is a destination or secret any language names the same
// way: a URL, an e-mail address, an SSH key, an env file, an API key or token.
const redirectTarget = `https?://|[a-z0-9._%+-]+@[a-z0-9-]+\.[a-z]|~/\.ssh|id_rsa|\.env\b|secrets\.env|api[_ -]?keys?|tokens?\b`

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
	return append(results, scanStructural(content, normalized, folded)...)
}

// scanStructural runs the scans that need more than one regexp over the
// normalized text: line-anchored headings and role markers, and letters
// spaced apart to dodge the phrase patterns.
func scanStructural(content, normalized, folded string) []ScanResult {
	var results []ScanResult
	if scanMarkdownHeaders(content) {
		results = append(results, ScanResult{Label: markdownHeaderLabel, Pattern: markdownHeaderRe.String()})
	}
	if scanRoleMarkers(content) {
		results = append(results, ScanResult{Label: roleMarkerLabel, Pattern: roleMarkerRe.String()})
	}
	if scanSpacedLetters(normalized) || (folded != normalized && scanSpacedLetters(folded)) {
		results = append(results, ScanResult{Label: spacedLettersLabel, Pattern: spacedPhraseRe.String()})
	}
	return results
}

// roleMarkerRe matches a line that opens with a forged role marker —
// "[system]", "[admin message]", "### SYSTEM", "## system override" — and
// captures what follows on the same line. Applied per line, like
// markdownHeaderRe.
var roleMarkerRe = regexp.MustCompile(`^\s*(\[(system|sys|admin|administrator|developer|operator)( message| prompt| override| note| instructions?)?\]|#+ ?(system|admin|administrator|developer|operator)( message| prompt| override| note| instructions?)?)\s*:?\s*(.*)$`)

// roleDirectiveRe is the directive text that turns a role marker into an
// injection. A heading such as "### System requirements" carries other words
// after the marker and never reaches it; "## System" followed by "The system
// has three services" carries no directive.
var roleDirectiveRe = regexp.MustCompile(`^(ignore|disregard|forget|bypass|from now on|henceforth|you are now|you are no longer|new (instructions?|directives?|task|rules)|your new|reveal|exfiltrate|do not tell|don't tell)\b`)

// bracketDirectiveRe is the wider directive set accepted after a bracketed
// [system] marker on the same line: "[system] you must ..." is not something
// documentation writes, while "## System\nYou need Docker" and log or chat
// prefixes such as "[operator] run the build" are.
var bracketDirectiveRe = regexp.MustCompile(`^(you (must|will|shall|should|are)|always|never|do not|don't|execute|run|send|print|output)\b`)

// systemOverrideRe is a directive only after a system marker: under an
// "### Admin" or "## Operator" heading, "Override the default port" is
// documentation.
var systemOverrideRe = regexp.MustCompile(`^override\b`)

// isRoleDirective reports whether text opens with a directive for a marker
// that is (system) or is not a system role.
func isRoleDirective(text string, system bool) bool {
	return roleDirectiveRe.MatchString(text) || (system && systemOverrideRe.MatchString(text))
}

const roleMarkerLabel = "forged role marker"

// isSystemBracket reports whether a matched marker is a bracketed system
// role ("[system]", "[sys]", "[system message]").
func isSystemBracket(marker string) bool {
	return strings.HasPrefix(marker, "[system") || strings.HasPrefix(marker, "[sys]")
}

// scanRoleMarkers reports whether a line opens with a role marker followed by
// directive text, either on the same line or on the next non-empty line.
func scanRoleMarkers(content string) bool {
	if !strings.ContainsAny(content, "#＃[［") {
		return false
	}
	pending := false // the previous non-empty line was a bare role marker
	pendingSystem := false
	for _, line := range strings.FieldsFunc(content, isLineBreak) {
		marker := strings.ContainsAny(line, "#＃[［")
		if !pending && !marker {
			continue
		}
		normalized := NormalizeForScan(line)
		if normalized == "" {
			continue
		}
		folded := FoldHomoglyphs(normalized)
		if pending && isRoleDirective(folded, pendingSystem) {
			return true
		}
		pending = false
		if !marker {
			continue
		}
		m := roleMarkerRe.FindStringSubmatch(folded)
		if m == nil {
			continue
		}
		rest := m[len(m)-1]
		system := strings.Contains(m[1], "sys")
		if rest == "" {
			pending, pendingSystem = true, system
			continue
		}
		if isRoleDirective(rest, system) || (isSystemBracket(m[1]) && bracketDirectiveRe.MatchString(rest)) {
			return true
		}
	}
	return false
}

// spacedPhraseRe matches the override and prompt-leak phrases with every
// space removed. It runs only around runs of letters that the text spelled
// out one at a time ("i g n o r e  p r e v i o u s"), where word boundaries
// are lost once whitespace is collapsed.
var spacedPhraseRe = regexp.MustCompile(`(ignore|disregard|forget)(all|any)?(of)?(your|the|my)?(all)?(previous|prior|above|earlier)(instructions?|directives?|rules?|messages?)|(print|output|display|show|reveal|dump|send)(your|the|my)?(systemprompt|initialinstructions?)|(print|output|display|show|reveal|dump|send)(your|my)instructions|newsystemprompt|youarenow(dan|unrestricted|jailbroken)`)

// spacedPhraseLiterals gates spacedPhraseRe like injectionLiterals gates
// the phrase patterns: a region holding none of them cannot match.
var spacedPhraseLiterals = requiredLiterals(spacedPhraseRe.String())

const spacedLettersLabel = "spaced-letter evasion"

// minSpacedRun is the fewest single letters in a row that count as a
// spelled-out word.
const minSpacedRun = 4

// spacedContextBytes of ordinary text on each side of a spelled-out run are
// joined with it, so a partly spaced phrase ("ignore p r e v i o u s
// instructions") is matched whole.
const spacedContextBytes = 48

// scanSpacedLetters finds every run of single letters separated by spaces or
// punctuation ("i g n o r e", "i-g-n-o-r-e"), joins the letters of the run
// and of a window of spacedContextBytes around it, and matches the result
// against spacedPhraseRe. Windows that touch are merged into one region and
// each region is joined and matched once, so every byte is examined a
// bounded number of times however densely the runs are packed. Ordinary
// text has no runs.
func scanSpacedLetters(normalized string) bool {
	runStart, runEnd, runLen := 0, 0, 0
	regionStart, regionEnd, inRegion := 0, 0, false
	var joined []byte
	matchRegion := func() bool {
		joined = joined[:0]
		for _, r := range normalized[regionStart:regionEnd] {
			if unicode.IsLetter(r) {
				joined = utf8.AppendRune(joined, r)
			}
		}
		return matchWithLiterals(spacedPhraseRe, spacedPhraseLiterals, string(joined))
	}
	// endRun closes the current run; a long enough run opens or extends a
	// region, and a region that the run does not touch is matched first.
	endRun := func() bool {
		if runLen < minSpacedRun {
			runLen = 0
			return false
		}
		runLen = 0
		from := max(0, runStart-spacedContextBytes)
		to := min(len(normalized), runEnd+spacedContextBytes)
		if inRegion && from <= regionEnd {
			regionEnd = max(regionEnd, to)
			return false
		}
		found := inRegion && matchRegion()
		regionStart, regionEnd, inRegion = from, to, true
		return found
	}
	i := 0
	for i < len(normalized) {
		r, size := utf8.DecodeRuneInString(normalized[i:])
		if !unicode.IsLetter(r) {
			i += size
			continue
		}
		// r starts a token; it is a single letter when the next rune is not
		// a letter or digit.
		next := i + size
		nr, _ := utf8.DecodeRuneInString(normalized[next:])
		if next < len(normalized) && (unicode.IsLetter(nr) || unicode.IsDigit(nr)) {
			if endRun() {
				return true
			}
			for next < len(normalized) {
				nr, ns := utf8.DecodeRuneInString(normalized[next:])
				if !unicode.IsLetter(nr) && !unicode.IsDigit(nr) {
					break
				}
				next += ns
			}
			i = next
			continue
		}
		if runLen == 0 {
			runStart = i
		}
		runLen++
		runEnd = next
		i = next
	}
	if endRun() {
		return true
	}
	return inRegion && matchRegion()
}

// IsSafe returns true if no injection threats are detected in content.
// This is the primary gate used before injecting untrusted content into
// the system prompt.
func IsSafe(content string) bool {
	return len(ScanInjection(content)) == 0
}
