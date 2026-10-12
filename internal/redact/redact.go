// Package redact provides secret detection and redaction for odek output.
//
// RedactSecrets scans text for API keys, tokens, credentials, private keys,
// and other secrets, replacing matched content with [REDACTED]. This prevents
// secrets from leaking into session files, memory episodes, and Telegram
// messages.
//
// Design:
//   - No external dependencies — pure Go regex
//   - Compiled once at init time — zero allocation on hot path
//   - Ordered by specificity — specific patterns (OpenAI, GitHub, AWS) before
//     generic patterns to avoid false positives
//   - False-positive resistant — minimum length thresholds, entropy checks
//
// The patterns are deliberately conservative. Generic patterns require
// contextual prefixes (key=, token=, secret=, password=) to reduce false
// positives on code snippets like UUIDs or base64-encoded data.
package redact

import (
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

// ── Patterns ───────────────────────────────────────────────────────────

// Each pattern is a regex that matches a specific secret format.
// Patterns are ordered from most specific to least specific.
// The first matching pattern wins — subsequent patterns are skipped for
// that portion of text.
var patterns = []*regexp.Regexp{
	// Provider-specific AI API keys (ordered before the generic sk- pattern).
	// Groq: gsk_<alphanumeric>
	regexp.MustCompile(`gsk_[a-zA-Z0-9]{32,}`),
	// xAI: xai-<alphanumeric+hyphens/underscores>
	regexp.MustCompile(`xai-[a-zA-Z0-9_-]{32,}`),
	// HuggingFace: hf_<alphanumeric>
	regexp.MustCompile(`hf_[a-zA-Z0-9]{32,}`),
	// OpenAI / generic AI provider keys: sk-<alphanumeric+hyphens+underscores>
	// sk-proj- variant for project-scoped keys. The underscore is required
	// because Anthropic keys (sk-ant-...) and some provider keys contain it.
	// \b anchors the key start: without it "sk-" inside ordinary words
	// ("task-notes-...", "risk-assessment-...") matched and surrounding
	// prose was corrupted to [REDACTED] in sessions and logs.
	regexp.MustCompile(`\bsk-[a-zA-Z0-9_-]{32,}`),

	// GitHub personal access tokens (classic + fine-grained) and the
	// other ghp_/gho_/ghu_/ghs_/ghr_ families (OAuth, user-to-server,
	// server-to-server, refresh).
	regexp.MustCompile(`ghp_[a-zA-Z0-9]{36,}`),
	regexp.MustCompile(`gh[ours]_[a-zA-Z0-9]{36,}`),
	regexp.MustCompile(`github_pat_[a-zA-Z0-9]{22,}`),

	// npm, GitLab personal, and PyPI API tokens.
	regexp.MustCompile(`npm_[a-zA-Z0-9]{36,}`),
	regexp.MustCompile(`glpat-[a-zA-Z0-9_-]{20,}`),
	regexp.MustCompile(`pypi-[a-zA-Z0-9_-]{20,}`),

	// AWS access keys: AKIA + 16 uppercase (also ASIA for temp credentials)
	regexp.MustCompile(`A[SK]IA[0-9A-Z]{16}`),

	// Private keys (RSA, EC, OpenSSH, DSA, ED25519, PKCS#8)
	// The body is matched lazily up to the first END marker so legacy
	// Proc-Type/DEK-Info headers (which contain dashes) stay inside the match.
	// PKCS#8 format (default openssl genpkey output) — with optional
	// ENCRYPTED prefix and optional algorithm label.
	regexp.MustCompile(`-----BEGIN (RSA |EC |OPENSSH |DSA |ED25519 |ENCRYPTED )?PRIVATE KEY-----[\s\S]*?-----END (RSA |EC |OPENSSH |DSA |ED25519 |ENCRYPTED )?PRIVATE KEY-----`),
	// PGP armored private keys (gpg --export-secret-keys output) — the
	// PEM alternation above deliberately does not cover them.
	regexp.MustCompile(`-----BEGIN PGP PRIVATE KEY BLOCK-----[\s\S]*?-----END PGP PRIVATE KEY BLOCK-----`),

	// JWT tokens (three base64url segments separated by dots)
	// Minimum ~40 chars to avoid matching short dotted strings
	regexp.MustCompile(`eyJ[a-zA-Z0-9_-]{20,}\.[a-zA-Z0-9_-]{20,}\.[a-zA-Z0-9_-]{20,}`),

	// Bearer / token-scheme credentials in Authorization headers: the
	// header-line form (Authorization: Bearer …, Authorization: token …) and the JSON/JS header-object form
	// ("Authorization": "Bearer …"), where a closing quote precedes the
	// colon and an opening quote precedes the scheme.
	regexp.MustCompile(`(?i)Authorization["'\x60]?\s*[:=]\s*["'\x60]?(?:Bearer|Token|ApiKey|Bot|OAuth|Negotiate|Digest)\s+([a-zA-Z0-9+/=._~-]{20,})`),

	// Slack bot tokens: xoxb-, xoxp-
	regexp.MustCompile(`xox[abpose]-[0-9]{10,}-[0-9]{10,}-[a-zA-Z0-9]{24,}`),
	// Slack app tokens: xapp-1-<app id>-<install id>-<secret>
	regexp.MustCompile(`xapp-1-[A-Za-z0-9]+-[0-9]{8,}-[0-9a-zA-Z]{24,}`),

	// Azure storage connection-string account keys (2026-09 posture review:
	// AccountKey= is a structural prefix — no KEY/TOKEN name segment for the
	// contextual matcher to catch).
	regexp.MustCompile(`AccountKey=[A-Za-z0-9+/=]{40,}`),
	// Stripe webhook signing secrets and live API keys (test-mode tk_/
	// whsec_ look-alikes in fixtures stay short; live bodies are long).
	regexp.MustCompile(`whsec_[A-Za-z0-9]{24,}`),
	regexp.MustCompile(`(?:sk|rk)_live_[A-Za-z0-9]{20,}`),

	// Telegram bot tokens: <numeric bot id>:<35-char base64url secret>.
	// e.g. 123456789:AAHfakeTokenValueExample0123456789abcdef
	regexp.MustCompile(`\b[0-9]{5,}:[A-Za-z0-9_-]{30,}\b`),

	// Stripe keys: sk_live_, sk_test_, pk_live_, pk_test_
	regexp.MustCompile(`[rs]k_(live|test)_[a-zA-Z0-9]{24,}`),

	// Google API keys: AIza + 35+ alphanumeric/hyphen/underscore
	regexp.MustCompile(`AIza[0-9A-Za-z_-]{35,}`),

	// Twilio keys: SK + 32 hex
	regexp.MustCompile(`SK[0-9a-fA-F]{32}`),

	// HashiCorp Vault tokens — service (hvs.) and batch (hvb.).
	regexp.MustCompile(`hv[sb]\.[A-Za-z0-9_-]{30,}`),

	// Google OAuth 2.0 access tokens (ya29. prefix) and refresh tokens
	// (1//0 prefix). 1// alone is too generic — refresh tokens always
	// begin with the literal "1//0".
	regexp.MustCompile(`ya29\.[A-Za-z0-9_.-]{20,}`),
	regexp.MustCompile(`\b1//0[A-Za-z0-9_-]{30,}`),

	// SendGrid API keys — SG.<22-char id>.<43-char secret>.
	regexp.MustCompile(`SG\.[A-Za-z0-9_-]{15,}\.[A-Za-z0-9_-]{30,}`),

	// Discord bot tokens — three base64url segments. Discord user IDs
	// are 17–19 digit decimal numbers, which encode in base64 to strings
	// starting with M, N, or O. Anchoring on that prefix + the strict
	// segment-length structure avoids collisions with generic dotted
	// base64 strings; real JWTs are already matched by the eyJ pattern
	// above.
	regexp.MustCompile(`\b[MNO][A-Za-z0-9_-]{22,27}\.[A-Za-z0-9_-]{5,7}\.[A-Za-z0-9_-]{27,40}\b`),

	// Database connection URLs with embedded credentials. We require a
	// scheme that genuinely carries DB creds (so this doesn't catch HTTP
	// basic auth URLs that often appear legitimately in code). The
	// password segment must be at least 6 chars to avoid matching common
	// placeholders like `:x@`.
	regexp.MustCompile(`(?i)\b(?:postgres(?:ql)?|mysql|mongodb(?:\+srv)?|redis|amqps?|mssql|clickhouse)://[^:\s/]+:[^@\s/]{6,}@[^\s'\x60"]+`),
}

// Credential-named assignments (api_key, access_token, password,
// DB_PASSWORD, SECRET_KEY_BASE, …) are matched by credentialMatches below,
// not by a regex alone: the value is parsed as a literal token and rejected
// when it is a code expression (see credentialValueOK), so source code and
// minified JSON survive redaction intact.

// guardedPattern is a context pattern whose value must also look like a
// secret. The bare key names (token, secret, pass, …) are everywhere in
// ordinary output (`--- PASS: TestX`, `token = strings.TrimSpace(h)`,
// `secret: kubernetes-secret-name`), so a match counts only when the value
// (capture group 1, quotes and CDATA wrappers removed) passes ok.
type guardedPattern struct {
	re *regexp.Regexp
	ok func(value string) bool
}

const (
	gSep = `["'\x60]?\s*(?:=>|[:=])\s*`
	gVal = `("[^"\n]{20,}"|'[^'\n]{20,}'|\x60[^\x60\n]{20,}\x60|[^\s]{20,})`
)

var guardedPatterns = []guardedPattern{
	// Bare and camelCase credential key names with :, = or => separators.
	// Lower-case, Capitalised and UPPER spellings of token/secret, but
	// only lower-case/Capitalised "pass" (UPPER PASS is test-runner output)
	// unless it ends an env-style name (DB_PASS).
	{regexp.MustCompile(`(?:\b(?:token|secret|passphrase|Token|Secret|Passphrase|TOKEN|SECRET|PASSPHRASE)|_(?i:token|secret|passphrase|pwd|pass)|\b[a-z][a-z0-9]*(?:Token|Secret|Passphrase|Password))` + gSep + gVal), looksCredentialShape},
	// Bare pwd/PWD keys (ODBC pwd=..., PWD=...): the shell's PWD variable
	// holds a working directory, so path-shaped values are not credentials.
	{regexp.MustCompile(`\b(?:pwd|Pwd|PWD)` + gSep + gVal), looksCredentialShapeNotPath},
	// Bare pass keys: test runners print `pass: TestX_Delete2`.
	{regexp.MustCompile(`\b(?:pass|Pass)` + gSep + gVal), looksCredentialShapeNotTestName},
	// CLI flags with a whitespace-separated value (--password VALUE,
	// --secret-key VALUE, --private-key VALUE).
	{regexp.MustCompile(`(?i)--?(?:password|passwd|passphrase|pwd|pass|token|secret|api[_-]?key|secret[_-]?key|private[_-]?key|access[_-]?key|client[_-]?secret)[ \t]+("[^"\n]{20,}"|'[^'\n]{20,}'|[^\s"']{20,})`), plausibleFlagValue},
	// .netrc lines: password VALUE.
	{regexp.MustCompile(`(?i)\b(?:password|passwd|passphrase)[ \t]+("[^"\n]{20,}"|'[^'\n]{20,}'|[^\s"']{20,})`), looksSecret},
	// XML elements: <password>VALUE</password>, optional attributes and
	// CDATA (Maven settings.xml, .NET config).
	{regexp.MustCompile(`(?i)<(?:password|passwd|passphrase|pwd|secret|token|api[_-]?key|access[_-]?token|client[_-]?secret|secret[_-]?key)(?:\s[^<>]*)?>\s*((?:<!\[CDATA\[)?[^<\n]{20,}?(?:\]\]>)?)\s*</[A-Za-z_-]+>`), looksSecret},
}

// plausibleValue rejects values that are plainly code expressions.
func plausibleValue(v string) bool {
	v = strings.Trim(strings.TrimSuffix(strings.TrimPrefix(v, "<![CDATA["), "]]>"), "\"'`")
	return len(v) >= 20 && !strings.ContainsAny(v, "(){}")
}

// plausibleFlagValue additionally rejects shell variable references, which
// name a credential rather than carry one.
func plausibleFlagValue(v string) bool {
	t := unwrapValue(v)
	return plausibleValue(v) && !strings.HasPrefix(t, "$") && !strings.HasPrefix(t, "%")
}

// looksSecret additionally requires a digit: random credentials have one
// with overwhelming probability, while identifiers, import paths, resource
// names and test names rarely do.
func looksSecret(v string) bool {
	return plausibleValue(v) && strings.ContainsAny(v, "0123456789") && !strings.Contains(v, "://")
}

// looksCredentialShape is the value test for the bare key names (token,
// secret, pass, pwd). Those words head ordinary code and configuration, so
// the value must be a single literal that is shaped like a generated
// credential: variable and environment references, member-access chains,
// paths, ARNs, timestamps and lowercase resource names are never secrets.
// Keyed forms with their own dedicated patterns do not go through here.
func looksCredentialShape(v string) bool {
	if !looksSecret(v) {
		return false
	}
	t := strings.TrimRight(unwrapValue(v), ";,")
	if i := strings.IndexByte(t, '&'); i > 0 { // URL query: stop at the next parameter
		t = t[:i]
	}
	if len(t) < 16 || codeExpression(t) {
		return false
	}
	for _, p := range []string{"$", "%", "process.env", "os.environ", "os.Getenv", "ENV[", "env."} {
		if strings.HasPrefix(t, p) {
			return false
		}
	}
	if strings.Contains(t, "::") || strings.Contains(strings.ToLower(t), "arn:") {
		return false
	}
	hasUpper, hasLower, hasDigit, special, hex, sep, punct := false, false, false, false, true, false, false
	for i := 0; i < len(t); i++ {
		c := t[i]
		switch {
		case c >= 'A' && c <= 'Z':
			hasUpper = true
			hex = hex && c <= 'F'
		case c >= 'a' && c <= 'z':
			hasLower = true
			hex = hex && c <= 'f'
		case c >= '0' && c <= '9':
			hasDigit = true
		case c == '+' || c == '/' || c == '=':
			special = true
			hex = false
		case c == '-' || c == '_' || c == '~':
			hex = false
			sep = true
		case strings.IndexByte(passwordPunct, c) >= 0:
			// Generated passwords routinely carry these.
			hex = false
			punct = true
		default:
			return false // '.', ':', quotes, brackets: not a credential alphabet
		}
	}
	// A value with a '/' is a path unless every segment is credential-like:
	// a word-only segment (Users/Alice/keysfolder) marks a path.
	if strings.Contains(t, "/") {
		for _, seg := range strings.Split(t, "/") {
			if len(seg) >= 3 && !strings.ContainsAny(seg, "0123456789+=-_~") {
				return false
			}
		}
	}
	switch {
	case hasUpper && hasLower && hasDigit:
		return true
	case punct && hasDigit && (hasUpper || hasLower):
		return true
	case !special && !sep && len(t) >= 20 && hasDigit && (hasUpper || hasLower):
		// One unseparated alphanumeric run (lowercase+digits or UPPER+digits).
		// Dash- or underscore-joined groups such as UUIDs, resource names and
		// identifiers are deliberately not matched.
		return true
	case hex && len(t) >= 32 && hasDigit:
		return true
	case special && hasDigit && (hasUpper || hasLower) && !strings.Contains(t, "//"):
		return strings.ContainsAny(t, "+=") || (hasUpper && hasLower)
	}
	return false
}

func looksCredentialShapeNotPath(v string) bool {
	return looksSecretNotPath(v) && looksCredentialShape(v)
}

func looksCredentialShapeNotTestName(v string) bool {
	return looksSecretNotTestName(v) && looksCredentialShape(v)
}

func unwrapValue(v string) string {
	return strings.Trim(strings.TrimSuffix(strings.TrimPrefix(v, "<![CDATA["), "]]>"), "\"'`")
}

// looksSecretNotPath additionally rejects filesystem paths.
func looksSecretNotPath(v string) bool {
	t := unwrapValue(v)
	if strings.HasPrefix(t, "/") || strings.HasPrefix(t, "~") || strings.HasPrefix(t, "./") || strings.HasPrefix(t, "../") {
		return false
	}
	return looksSecret(v)
}

// looksSecretNotTestName additionally rejects Go test identifiers.
func looksSecretNotTestName(v string) bool {
	return looksSecret(v) && !goTestName.MatchString(unwrapValue(v))
}

var goTestName = regexp.MustCompile(`^(?:Test|Benchmark|Fuzz|Example)[A-Za-z0-9]*(?:_[A-Za-z0-9]+)*$`)

// guardedMatches returns the [start,end) offsets of every guarded match.
func guardedMatches(text string) [][2]int {
	var out [][2]int
	for _, g := range guardedPatterns {
		for _, m := range g.re.FindAllStringSubmatchIndex(text, -1) {
			if m[2] >= 0 && g.ok(text[m[2]:m[3]]) {
				out = append(out, [2]int{m[0], m[1]})
			}
		}
	}
	return append(out, credentialMatches(text)...)
}

// redactGuarded replaces every guarded match, merging overlaps.
func redactGuarded(text string) string {
	ms := guardedMatches(text)
	if len(ms) == 0 {
		return text
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i][0] < ms[j][0] })
	var b strings.Builder
	last := 0
	for _, m := range ms {
		if m[0] < last {
			if m[1] > last {
				last = m[1]
			}
			continue
		}
		b.WriteString(text[last:m[0]])
		b.WriteString("[REDACTED]")
		last = m[1]
	}
	b.WriteString(text[last:])
	return b.String()
}

// basicAuthPattern matches an HTTP Basic credential in a header line
// (`Authorization: Basic <b64>`, also Proxy-Authorization) or a JSON/JS
// header object (`"Authorization": "Basic <b64>"`). The word "Basic" alone
// is common prose ("Authorization: Basic authentication…"), so a match only
// counts when its token decodes to a printable user:password pair; see
// basicCredential.
// The token is capped at basicTokenMax bytes so the regex never consumes an
// unbounded run; a longer run is handled in basicAuthMatches.
var basicAuthPattern = regexp.MustCompile(`(?i)(authorization["'\x60]?\s*[:=]\s*["'\x60]?basic\s+)([A-Za-z0-9+/_-]{4,1000}={0,2})`)

// dockerAuthPattern matches the "auth" field of a Docker config.json
// (base64 of user:password); like a Basic header it only counts when the
// token decodes to a printable user:password pair.
var dockerAuthPattern = regexp.MustCompile(`(?i)(["']auth["']\s*:\s*["'])([A-Za-z0-9+/_-]{4,1000}={0,2})`)

// basicTokenMax is the token cap in basicAuthPattern.
const basicTokenMax = 1000

// basicCredential reports whether tok is base64 (standard or URL alphabet,
// padded or not) of printable text containing a ':' — the user:password
// shape of an HTTP Basic credential.
func basicCredential(tok string) bool {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		b, err := enc.DecodeString(tok)
		if err != nil {
			continue
		}
		// Decodes of ordinary words are almost never valid control-free
		// UTF-8 containing ':'; real credentials always are.
		if !strings.Contains(string(b), ":") || !utf8.Valid(b) {
			return false
		}
		for _, c := range b {
			if c < 0x20 || c == 0x7f {
				return false
			}
		}
		return true
	}
	return false
}

// basicAuthMatches returns the [start,end) offsets of every Basic credential
// token in text (the token only, not the header name).
// basicAuthCandidate is the cheap prefilter for the Basic-auth pass: the
// regex only runs on text that contains both "authorization" and "basic"
// (any case), so prose or data merely containing "basic" costs one scan.
func basicAuthCandidate(text string) bool {
	i := indexFoldASCII(text, "authorization")
	return i >= 0 && indexFoldASCII(text[i:], "basic") >= 0
}

// dockerAuthCandidate is the prefilter for the Docker config pass.
func dockerAuthCandidate(text string) bool {
	return indexFoldASCII(text, "auth") >= 0
}

func basicAuthMatches(text string) [][2]int {
	var out [][2]int
	if basicAuthCandidate(text) {
		out = appendCredentialMatches(out, text, basicAuthPattern)
	}
	if dockerAuthCandidate(text) {
		out = appendCredentialMatches(out, text, dockerAuthPattern)
	}
	if len(out) > 1 {
		sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	}
	return out
}

// appendCredentialMatches appends the token offsets of each match of pat
// (group 1: the key prefix, group 2: the base64 token) that decodes to a
// user:password pair.
func appendCredentialMatches(out [][2]int, text string, pat *regexp.Regexp) [][2]int {
	// Locate whole matches with the fast unanchored matcher, then take the
	// token submatch on each short match only: submatch extraction over the
	// full text falls back to a much slower engine on large inputs.
	for _, m := range pat.FindAllStringIndex(text, -1) {
		sm := pat.FindStringSubmatchIndex(text[m[0]:m[1]])
		if sm == nil {
			continue
		}
		start, end := m[0]+sm[4], m[0]+sm[5]
		if end-start >= basicTokenMax && end < len(text) && isBasicTokenByte(text[end]) {
			// The token runs past the cap: no Basic credential is this
			// long, but it sits where one belongs, so redact the whole run
			// without decoding it.
			for end < len(text) && isBasicTokenByte(text[end]) {
				end++
			}
			out = append(out, [2]int{start, end})
			continue
		}
		if basicCredential(text[start:end]) {
			out = append(out, [2]int{start, end})
		}
	}
	return out
}

// isBasicTokenByte reports whether c can continue a base64 Basic token.
func isBasicTokenByte(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
		c == '+' || c == '/' || c == '_' || c == '-' || c == '='
}

// indexFoldASCII returns the index of the first occurrence of the
// lower-case ASCII word in text in any letter case, or -1, without copying
// text. It jumps between candidate first bytes with IndexByte.
func indexFoldASCII(text, word string) int {
	lo, up := word[0], word[0]-('a'-'A')
	for i := 0; i+len(word) <= len(text); {
		rest := text[i : len(text)-len(word)+1]
		j := strings.IndexByte(rest, lo)
		if k := strings.IndexByte(rest, up); k >= 0 && (j < 0 || k < j) {
			j = k
		}
		if j < 0 {
			return -1
		}
		i += j
		if strings.EqualFold(text[i:i+len(word)], word) {
			return i
		}
		i++
	}
	return -1
}

// redactBasicAuth replaces each Basic credential token with [REDACTED],
// keeping the header name so the redacted text still reads as a header.
func redactBasicAuth(text string) string {
	ms := basicAuthMatches(text)
	if len(ms) == 0 {
		return text
	}
	var b strings.Builder
	last := 0
	for _, m := range ms {
		b.WriteString(text[last:m[0]])
		b.WriteString("[REDACTED]")
		last = m[1]
	}
	b.WriteString(text[last:])
	return b.String()
}

// ── Public API ─────────────────────────────────────────────────────────

// RedactSecrets scans text for known secret patterns and replaces matched
// content with "[REDACTED]". Returns the sanitized text.
//
// Two layers run: first the known-value layer (exact secret values registered
// via RegisterSecret, plus their common encodings), then the format-pattern
// layer below. The known-value layer is the reliable one for odek's own
// secrets — it catches them even when printed in a format the patterns miss
// (a bare echo of a non-standard token, base64/hex encodings, etc.).
//
// The function is safe to call on empty strings and strings without secrets
// (returns the original string unchanged in the common case).
func RedactSecrets(text string) string {
	if text == "" {
		return text
	}

	result := text
	if r := currentReplacer(); r != nil {
		result = r.Replace(result)
	}
	for _, p := range patterns {
		// ReplaceAllString copies its input even when nothing matches, so
		// only patterns that match pay for a rewrite.
		if p.FindStringIndex(result) == nil {
			continue
		}
		result = p.ReplaceAllString(result, "[REDACTED]")
	}
	return redactBasicAuth(redactGuarded(result))
}

// HasSecrets returns true if the text contains any recognized secret pattern
// or any registered known secret value.
// Useful for quick pre-checks without allocating the full redacted string.
func HasSecrets(text string) bool {
	if text == "" {
		return false
	}
	for _, f := range currentForms() {
		if strings.Contains(text, f) {
			return true
		}
	}
	for _, p := range patterns {
		if p.MatchString(text) {
			return true
		}
	}
	return len(guardedMatches(text)) > 0 || len(basicAuthMatches(text)) > 0
}

// IsSafe returns true if the text contains no recognized secrets.
// Convenience inverse of HasSecrets.
func IsSafe(text string) bool {
	return !HasSecrets(text)
}

// ── Helpers ────────────────────────────────────────────────────────────

// CountSecrets returns the number of secret patterns found in the text.
// Useful for logging and metrics.
func CountSecrets(text string) int {
	if text == "" {
		return 0
	}
	count := 0
	for _, f := range currentForms() {
		count += strings.Count(text, f)
	}
	for _, p := range patterns {
		matches := p.FindAllString(text, -1)
		count += len(matches)
	}
	count += len(guardedMatches(text)) + len(basicAuthMatches(text))
	return count
}

// ── Known-value redaction ──────────────────────────────────────────────
//
// Pattern matching only catches secrets whose *format* we recognise. odek
// also holds its own secrets: the LLM API key (needed to talk to the model),
// the Telegram bot token, and anything injected via .env / secrets.env.
// Because we know those exact values, we can redact them — and their common
// encodings — from tool output regardless of how the agent prints them.
//
// This closes two gaps that pure pattern matching cannot:
//   - a bare echo of a secret whose format we don't recognise
//     (e.g. `echo $TELEGRAM_BOT_TOKEN`)
//   - a trivially transformed secret (`echo $API_KEY | base64`, `| xxd`)
//
// It does NOT defend against arbitrary transformations the agent could apply
// (gzip, openssl enc, char substitution) or against side-channel exfiltration
// that never returns text to the tool surface — those are the job of the
// network-egress controls, not redaction.

// minSecretLen is the shortest raw value we will register. Short values risk
// over-redacting ordinary text, and real keys/tokens are far longer.
const minSecretLen = 8

var (
	secretGeneration uint64
	secretsMu        sync.RWMutex
	secretSet        = map[string]struct{}{} // every literal form to redact
	secretReplacer   *strings.Replacer
	secretFormsList  []string
)

// osEnviron is os.Environ, swapped in tests.
var osEnviron = os.Environ

// RegisterSecret records a known secret value so that it — and its common
// encodings (base64 std/url, hex, percent-encoding, reversed) — are redacted
// from all tool output. Values shorter than minSecretLen are ignored to
// avoid over-redaction. Safe to call repeatedly and concurrently; callers
// should register before any tool output is produced (i.e. at startup).
func RegisterSecret(value string) {
	value = strings.TrimSpace(value)
	if len(value) < minSecretLen {
		return
	}
	forms := encodeForms(value)
	secretsMu.Lock()
	defer secretsMu.Unlock()
	changed := false
	for _, f := range forms {
		if len(f) < minSecretLen {
			continue
		}
		if _, ok := secretSet[f]; !ok {
			secretSet[f] = struct{}{}
			changed = true
		}
	}
	if changed {
		rebuildReplacerLocked()
	}
}

// RegisterSecretsFromEnv scans the process environment for variables whose
// names look sensitive and registers their values. This automatically covers
// secrets injected via .env (docker env_file) or ~/.odek/secrets.env without
// the caller having to enumerate them.
func RegisterSecretsFromEnv() {
	for _, kv := range osEnviron() {
		name, val, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if sensitiveName(name) {
			RegisterSecret(val)
		}
	}
}

// ResetSecrets clears the known-value registry. Intended for tests.
func ResetSecrets() {
	secretsMu.Lock()
	defer secretsMu.Unlock()
	secretSet = map[string]struct{}{}
	secretReplacer = nil
	secretFormsList = nil
	secretGeneration++
}

// Generation identifies the current known-value registry. It changes whenever
// a secret is registered or the registry is reset, so a caller that memoizes
// "already redacted" verdicts can tie them to the registry that produced them
// and rescan after a late registration.
func Generation() uint64 {
	secretsMu.RLock()
	defer secretsMu.RUnlock()
	return secretGeneration
}

// sensitiveName reports whether an env var name has a segment that marks it
// as secret-bearing. Matching whole `_`/`-` separated segments avoids
// substring false positives like GIT_AUTHOR (AUTH) or compass (PASS).
func sensitiveName(name string) bool {
	for _, seg := range strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == '-' }) {
		switch strings.ToUpper(seg) {
		case "KEY", "APIKEY", "TOKEN", "SECRET", "PASSWORD", "PASSWD", "PASS",
			"CREDENTIAL", "CREDENTIALS", "PRIVATEKEY", "ACCESSKEY", "SECRETKEY":
			return true
		}
	}
	// Fused names with no separator at all (MYAPITOKEN, OPENAIAPIKEY) carry
	// the secret word at the end. Suffix matching only — prefix matching
	// false-positives on legitimate names like TOKENBUCKET. Short words are
	// excluded so accidental endings (MONKEY → KEY) don't match.
	fused := strings.ToUpper(strings.Map(func(r rune) rune {
		if r == '_' || r == '-' {
			return -1
		}
		return r
	}, name))
	for _, word := range []string{"APIKEY", "TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL", "CREDENTIALS", "PRIVATEKEY", "ACCESSKEY", "SECRETKEY"} {
		// >= so a bare PASSWORD/TOKEN/SECRET env name is sensitive too;
		// over-matching only over-redacts, which is the safe direction.
		if len(fused) >= len(word) && strings.HasSuffix(fused, word) {
			return true
		}
	}
	return false
}

// encodeForms returns the raw value plus the encodings an agent is most
// likely to pipe a secret through. url.QueryEscape and the base64 variants
// frequently coincide for alphanumeric keys; duplicates are collapsed by the
// registry's set.
func encodeForms(v string) []string {
	b := []byte(v)
	return []string{
		v,
		base64.StdEncoding.EncodeToString(b),
		base64.RawStdEncoding.EncodeToString(b),
		base64.URLEncoding.EncodeToString(b),
		base64.RawURLEncoding.EncodeToString(b),
		hex.EncodeToString(b),
		strings.ToUpper(hex.EncodeToString(b)),
		url.QueryEscape(v),
		reverseString(v),
	}
}

func reverseString(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

// rebuildReplacerLocked recomputes the replacer and form list from secretSet.
// strings.Replacer takes, at each position, the first argument that matches
// rather than the longest, so forms are ordered longest first (ties broken
// lexically for a deterministic build): a registered secret that is a prefix
// of another can then never win and leave the longer secret's tail in clear.
// Caller must hold secretsMu for writing.
func rebuildReplacerLocked() {
	secretGeneration++
	forms := make([]string, 0, len(secretSet))
	for f := range secretSet {
		forms = append(forms, f)
	}
	sort.Slice(forms, func(i, j int) bool {
		if len(forms[i]) != len(forms[j]) {
			return len(forms[i]) > len(forms[j])
		}
		return forms[i] < forms[j]
	})
	pairs := make([]string, 0, len(forms)*2)
	for _, f := range forms {
		pairs = append(pairs, f, "[REDACTED]")
	}
	secretFormsList = forms
	if len(pairs) == 0 {
		secretReplacer = nil
		return
	}
	secretReplacer = strings.NewReplacer(pairs...)
}

func currentReplacer() *strings.Replacer {
	secretsMu.RLock()
	defer secretsMu.RUnlock()
	return secretReplacer
}

func currentForms() []string {
	secretsMu.RLock()
	defer secretsMu.RUnlock()
	return secretFormsList
}

// RedactWithCount returns both the redacted text and a count of redacted
// secrets, so callers can log how many were caught without a second pass.
func RedactWithCount(text string) (string, int) {
	if text == "" {
		return text, 0
	}
	count := CountSecrets(text)
	result := RedactSecrets(text)
	return result, count
}

// ── Chunk helpers ──────────────────────────────────────────────────────

// RedactChunk redacts a single chunk of text and returns it along with
// a boolean indicating whether any secrets were found.
// Designed for streaming/chunked output where callers want to know
// per-chunk whether redaction occurred.
func RedactChunk(chunk string) (string, bool) {
	if chunk == "" {
		return chunk, false
	}
	had := HasSecrets(chunk)
	if !had {
		return chunk, false
	}
	return RedactSecrets(chunk), true
}

// ── Sanitize for safe comparison ───────────────────────────────────────

// SanitizeForLog returns a version of the text safe for logging.
// Unlike RedactSecrets which replaces matched substrings, this returns
// a descriptive summary when secrets are found. Useful for log messages
// where you want to know secrets WERE present without any risk of
// partial leakage.
func SanitizeForLog(text string) string {
	if text == "" {
		return text
	}
	if HasSecrets(text) {
		return strings.Repeat("[REDACTED] ", CountSecrets(text))
	}
	return text
}
