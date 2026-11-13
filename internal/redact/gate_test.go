package redact

import (
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// allRedactionPatterns lists every regexp the redaction rules run, gated or
// not.
func allRedactionPatterns() []*regexp.Regexp {
	out := append([]*regexp.Regexp{}, patterns...)
	for _, g := range guardedPatterns {
		out = append(out, g.re)
	}
	for _, g := range valuePatterns {
		out = append(out, g.re)
	}
	return append(out, dbURLBasePattern, dbURLWidePattern, credentialNameRe)
}

// gatedRedactionPatterns lists the patterns that have a gate, in a stable
// order.
func gatedRedactionPatterns() []*regexp.Regexp {
	var out []*regexp.Regexp
	for _, re := range allRedactionPatterns() {
		if _, ok := patternGates[re]; ok {
			out = append(out, re)
		}
	}
	return out
}

// patternContaining returns the one redaction pattern whose source contains
// part.
func patternContaining(t *testing.T, part string) *regexp.Regexp {
	t.Helper()
	var found *regexp.Regexp
	for _, re := range allRedactionPatterns() {
		if strings.Contains(re.String(), part) {
			if found != nil && found != re {
				t.Fatalf("%q names more than one pattern", part)
			}
			found = re
		}
	}
	if found == nil {
		t.Fatalf("no pattern contains %q", part)
	}
	return found
}

// ungatedByChoice names (by a part of their source) the patterns left without
// a gate on purpose although they have no literal prefix, because no sound
// necessary condition was found for them. Such a pattern runs on every text:
// it is only slow, never wrong.
var ungatedByChoice = []string{}

func isUngatedByChoice(re *regexp.Regexp) bool {
	for _, part := range ungatedByChoice {
		if strings.Contains(re.String(), part) {
			return true
		}
	}
	return false
}

// Every gate belongs to a pattern the rules run, and every pattern is gated,
// begins with a literal prefix the regexp engine already skips to, or is
// listed in ungatedByChoice.
func TestEveryPatternIsGatedOrHasLiteralPrefix(t *testing.T) {
	all := map[*regexp.Regexp]bool{}
	for _, re := range allRedactionPatterns() {
		all[re] = true
		_, isGated := patternGates[re]
		prefix, _ := re.LiteralPrefix()
		if !isGated && prefix == "" && !isUngatedByChoice(re) {
			t.Errorf("pattern %q has neither a gate nor a literal prefix: add a gate, or list it in ungatedByChoice", re)
		}
		if isGated && prefix != "" {
			t.Logf("gated pattern %q also has literal prefix %q", re, prefix)
		}
	}
	for re := range patternGates {
		if !all[re] {
			t.Errorf("gate registered for %q, which no rule runs", re)
		}
	}
}

const gateTestValue = "Xy7kLm9pQ2zzAbCdEf3456"

// gateExamples holds, for each gated pattern (named by a part of its source),
// a minimal match of every alternative, with letter-case variants for the
// case-insensitive parts, including the runes Unicode folds to k (U+212A) and
// s (U+017F).
var gateExamples = []struct {
	pattern  string
	examples []string
}{
	{`\bsk-`, []string{"sk-" + strings.Repeat("a", 32)}},
	{`(?i)Authorization`, []string{
		"Authorization: Bearer abcdefghijklmnopqrst",
		"authorization=Token abcdefghijklmnopqrst",
		`"AUTHORIZATION": "ApiKey abcdefghijklmnopqrst`,
		"AuThOrIzAtIoN: Bot abcdefghijklmnopqrst",
		"Authorization: OAuth abcdefghijklmnopqrst",
		"Authorization: Negotiate abcdefghijklmnopqrst",
		"Authorization: Digest abcdefghijklmnopqrst",
		"Authorization: bearer abcdefghijklmnopqrst",
	}},
	{`(?:sk|rk)_live_`, []string{"sk_live_" + strings.Repeat("a", 20), "rk_live_" + strings.Repeat("A", 20)}},
	{`\b[0-9]{5,}:`, []string{"12345:" + strings.Repeat("a", 30), "123456789:AAHfakeTokenValueExample0123456789abcdef"}},
	{`[rs]k_(live|test)_`, []string{
		"sk_live_" + strings.Repeat("a", 24), "rk_live_" + strings.Repeat("a", 24),
		"sk_test_" + strings.Repeat("a", 24), "rk_test_" + strings.Repeat("a", 24),
	}},
	{`\b1//0`, []string{"1//0" + strings.Repeat("a", 30)}},
	{`\b[MNO]`, []string{
		"M" + strings.Repeat("a", 22) + "." + strings.Repeat("b", 5) + "." + strings.Repeat("c", 27),
		"N" + strings.Repeat("a", 27) + "." + strings.Repeat("b", 7) + "." + strings.Repeat("c", 40),
		"O" + strings.Repeat("_", 22) + "." + strings.Repeat("-", 6) + "." + strings.Repeat("9", 30),
	}},
	{`*:[^@\s/]{6,}@[`, []string{
		"postgres://u:secret1@db", "postgresql+psycopg2://u:secret1@db", "mysql://u:${DB_PW}@db", "mongodb+srv://u:secret1@db",
		"rediss://:secret1@db", "amqps://u:secret1@db", "mssql://u:secret1@db", "CLICKHOUSE://u:secret1@db",
		"poſtgres://u:secret1@db", "clicKhouse://u:secret1@db",
	}},
	{`[$%{<][^@\s/]*`, []string{
		"postgres://u:$A@db", `\npostgres://u:secret1@db`, `\tmysql://u:{p}@db`,
		"postgres://u:secret1@db", "postgresql://u:secret1@db", "postgresql+psycopg2://u:secret1@db",
		"mysql://u:secret1@db", "mongodb://u:secret1@db", "mongodb+srv://u:secret1@db",
		"redis://:secret1@db", "rediss://u:secret1@db", "amqp://u:secret1@db", "amqps://u:secret1@db",
		"mssql://u:secret1@db", "clickhouse://u:secret1@db",
		"mysql://u:a/b@db", "mysql://u:a@b@db", "redis://:p@ss/w@db",
		"POSTGRES://u:secret1@db", "MySQL://u:secret1@db", "ClickHouse://u:secret1@db", "REDIS+tls://u:secret1@db",
		"po\u017ftgres://u:secret1@db", "my\u017fql://u:secret1@db", "redi\u017f://u:secret1@db",
		"m\u017f\u017fql://u:secret1@db", "clic\u212ahouse://u:secret1@db", "clickhou\u017fe://u:secret1@db",
	}},
	{`(?:\b(?:token|secret|passphrase|Token`, []string{
		"token=" + gateTestValue, "secret: " + gateTestValue, "passphrase => " + gateTestValue,
		"Token=" + gateTestValue, "Secret=" + gateTestValue, "Passphrase=" + gateTestValue,
		"TOKEN=" + gateTestValue, "SECRET=" + gateTestValue, "PASSPHRASE=" + gateTestValue,
		"x_token=" + gateTestValue, "X_TOKEN=" + gateTestValue, "x_Secret=" + gateTestValue,
		"db_passphrase=" + gateTestValue, "db_pwd=" + gateTestValue, "DB_PWD=" + gateTestValue,
		"db_pass=" + gateTestValue, "DB_PASS=" + gateTestValue, "db_PaSs=" + gateTestValue,
		"githubToken=" + gateTestValue, "apiSecret=" + gateTestValue, "myPassphrase=" + gateTestValue, "dbPassword=" + gateTestValue,
		`token\": ` + gateTestValue, "token = '" + gateTestValue + "'", "token\n\t= " + gateTestValue,
		"Secret`=>" + gateTestValue, `x_pwd\'` + "\r\n:" + gateTestValue, "\fpassphrase\f:\f" + gateTestValue,
		"x_to\u212aen=" + gateTestValue, "x_\u017fecret=" + gateTestValue, "x_pa\u017f\u017f=" + gateTestValue,
		"x_pa\u017f\u017fphrase=" + gateTestValue,
	}},
	{`\b(?:pwd|Pwd|PWD)`, []string{"pwd=" + gateTestValue, "Pwd: " + gateTestValue, "PWD => " + gateTestValue, `pwd\"` + "\n:" + gateTestValue}},
	{`\b(?:pass|Pass)`, []string{"pass=" + gateTestValue, "Pass: " + gateTestValue, "pass'\t=>'" + gateTestValue + "'"}},
	{`(?i)--?(?:password`, []string{
		"--password " + gateTestValue, "-passwd " + gateTestValue, "--passphrase " + gateTestValue,
		"--pwd " + gateTestValue, "--pass " + gateTestValue, "--token " + gateTestValue, "--secret " + gateTestValue,
		"--api-key " + gateTestValue, "--api_key " + gateTestValue, "--apikey " + gateTestValue,
		"--secret-key " + gateTestValue, "--private-key " + gateTestValue, "--access_key " + gateTestValue,
		"--client-secret " + gateTestValue,
		"--PASSWORD " + gateTestValue, "--Token " + gateTestValue, "--API-KEY " + gateTestValue, "--Client_Secret " + gateTestValue,
		"--to\u212aen " + gateTestValue, "--\u017fecret " + gateTestValue, "--pa\u017f\u017f " + gateTestValue,
		"--api\u212aey " + gateTestValue, "--acce\u017f\u017f-key " + gateTestValue, "--private-\u212aey " + gateTestValue,
	}},
	{`(?i)\b(?:password|passwd|passphrase)[ \t]+`, []string{
		"password " + gateTestValue, "passwd\t" + gateTestValue, "passphrase " + gateTestValue,
		"PASSWORD " + gateTestValue, "Passwd " + gateTestValue, "PassPhrase " + gateTestValue,
		"pa\u017fsword " + gateTestValue, "pas\u017fwd " + gateTestValue, "pa\u017f\u017fphrase " + gateTestValue,
	}},
	{`(?i)<(?:password`, []string{
		"<password>" + gateTestValue + "</password>", "<passwd>" + gateTestValue + "</passwd>",
		"<passphrase>" + gateTestValue + "</x>", "<pwd>" + gateTestValue + "</x>", "<secret>" + gateTestValue + "</x>",
		"<token>" + gateTestValue + "</x>", "<api_key>" + gateTestValue + "</x>", "<api-key>" + gateTestValue + "</x>",
		"<apikey>" + gateTestValue + "</x>", "<access_token>" + gateTestValue + "</x>", "<accesstoken>" + gateTestValue + "</x>",
		"<client_secret>" + gateTestValue + "</x>", "<secret_key>" + gateTestValue + "</x>",
		"<PASSWORD>" + gateTestValue + "</PASSWORD>", `<Token attr="x">` + gateTestValue + "</Token>",
		"<to\u212aen>" + gateTestValue + "</x>", "<\u017fecret>" + gateTestValue + "</x>", "<pa\u017fsword>" + gateTestValue + "</x>",
		"<api\u212aey>" + gateTestValue + "</x>", "<acce\u017fs_token>" + gateTestValue + "</x>",
	}},
	{`hooks\.slack\.com/`, []string{
		"hooks.slack.com/services/T0/B0/" + strings.Repeat("a1", 10),
		"hooks.slack.com/workflows/T0/" + strings.Repeat("a1", 10),
		"hooks.slack.com/triggers/T0/B0/C0/" + strings.Repeat("a1", 10),
	}},
	{`discord(?:app)?`, []string{
		"discord.com/api/webhooks/123/" + strings.Repeat("a1", 20),
		"discordapp.com/api/v10/webhooks/123/" + strings.Repeat("a1", 20),
	}},
	{`passw(?:or)?d|credential`, []string{
		"token=x", "secret=x", "password=x", "passwd=x", "credential=x",
		"api_key=x", "api-key=x", "apikey=x", "access_key=x", "accesskey=x", "access-key=x",
		"private_key=x", "privatekey=x", "private-key=x", "MY_TOKEN: x", `"Api_Key" => x`,
		"to\u212aen=x", "\u017fecret=x", "pa\u017f\u017fword=x", "credential\u017f=x",
		"api\u212aey=x", "acce\u017f\u017fkey=x", "private\u212aey=x",
	}},
}

// gateFiller is secret-free text a match is embedded in.
var gateFiller = redhunt1109Prose(300)

// Each example matches its pattern and the pattern's gate lets it through,
// alone and inside other text.
func TestGateExamplesPassTheirGates(t *testing.T) {
	covered := map[*regexp.Regexp]bool{}
	for _, tc := range gateExamples {
		re := patternContaining(t, tc.pattern)
		if _, ok := patternGates[re]; !ok {
			t.Errorf("%q is not gated", tc.pattern)
			continue
		}
		covered[re] = true
		for _, ex := range tc.examples {
			if !re.MatchString(ex) {
				t.Errorf("%q: example %q does not match the pattern", tc.pattern, ex)
				continue
			}
			for _, in := range []string{ex, gateFiller + ex + gateFiller, strings.ToUpper(gateFiller) + "\n" + ex} {
				if !newScan(in, false).may(re) {
					t.Errorf("%q: gate rejects a match: %q", tc.pattern, in)
				}
			}
		}
	}
	for _, re := range gatedRedactionPatterns() {
		if !covered[re] {
			t.Errorf("gated pattern %q has no examples", re)
		}
	}
}

// sampleMatch appends to b a random string built from the syntax tree re.
// Empty-width assertions are ignored, so the caller keeps only the strings
// the pattern actually matches. Case-insensitive literals take a random member
// of each rune's case-folding orbit (k also as U+212A, s also as U+017F).
func sampleMatch(r *rand.Rand, re *syntax.Regexp, b *strings.Builder) {
	repeat := func(lo, hi int) {
		n := lo
		if hi < 0 {
			hi = lo + 4
		}
		if hi > lo+8 {
			hi = lo + 8
		}
		n += r.Intn(hi - lo + 1)
		for i := 0; i < n; i++ {
			sampleMatch(r, re.Sub[0], b)
		}
	}
	switch re.Op {
	case syntax.OpLiteral:
		for _, c := range re.Rune {
			if re.Flags&syntax.FoldCase != 0 {
				orbit := []rune{c}
				for f := unicode.SimpleFold(c); f != c; f = unicode.SimpleFold(f) {
					orbit = append(orbit, f)
				}
				c = orbit[r.Intn(len(orbit))]
			}
			b.WriteRune(c)
		}
	case syntax.OpCharClass:
		k := r.Intn(len(re.Rune) / 2)
		lo, hi := re.Rune[2*k], re.Rune[2*k+1]
		if lo < utf8.RuneSelf && hi >= utf8.RuneSelf && r.Intn(4) != 0 {
			hi = utf8.RuneSelf - 1
		}
		c := lo + rune(r.Int63n(int64(hi-lo)+1))
		if !utf8.ValidRune(c) {
			c = lo
		}
		b.WriteRune(c)
	case syntax.OpAnyCharNotNL, syntax.OpAnyChar:
		b.WriteByte(byte(' ' + r.Intn(95)))
	case syntax.OpCapture:
		sampleMatch(r, re.Sub[0], b)
	case syntax.OpStar:
		repeat(0, 3)
	case syntax.OpPlus:
		repeat(1, 3)
	case syntax.OpQuest:
		repeat(0, 1)
	case syntax.OpRepeat:
		repeat(re.Min, re.Max)
	case syntax.OpConcat:
		for _, sub := range re.Sub {
			sampleMatch(r, sub, b)
		}
	case syntax.OpAlternate:
		sampleMatch(r, re.Sub[r.Intn(len(re.Sub))], b)
	}
}

// Random strings generated from each gated pattern, over every alternative and
// letter case, pass the gate whenever the pattern matches them.
func TestGatesAcceptRandomMatches(t *testing.T) {
	samples := 3000
	if raceEnabled {
		samples = 600
	}
	r := rand.New(rand.NewSource(1109))
	for _, re := range gatedRedactionPatterns() {
		tree, err := syntax.Parse(re.String(), syntax.Perl)
		if err != nil {
			t.Fatal(err)
		}
		tree = tree.Simplify()
		matched := 0
		for i := 0; i < samples; i++ {
			var b strings.Builder
			if r.Intn(2) == 0 {
				b.WriteString(" ")
			}
			sampleMatch(r, tree, &b)
			s := b.String()
			if !re.MatchString(s) {
				continue
			}
			matched++
			if !newScan(s, false).may(re) {
				t.Fatalf("gate of %q rejects a match: %q", re, s)
			}
		}
		if matched < samples/4 {
			t.Errorf("%q: only %d of %d samples matched; the generator does not exercise it", re, matched, samples)
		}
	}
}

// The shape and separator checks equal the regular expressions they stand
// for.
func TestGateShapesMatchTheirExpressions(t *testing.T) {
	digitColon := regexp.MustCompile(`[0-9]{5}:[A-Za-z0-9_-]{30}`)
	dotted := regexp.MustCompile(`[A-Za-z0-9_-]{23}\.[A-Za-z0-9_-]{5,7}\.[A-Za-z0-9_-]{27}`)
	keySep := regexp.MustCompile("(?i)(?:token|secret|passphrase|pwd|pass|password)\\\\?[\"'`]?\\s*[=:]")
	keys := keyed("token", "secret", "passphrase", "pwd", "pass", "password").keys
	r := rand.New(rand.NewSource(7))
	alphabet := "0123456789:=.aZ_-\\\"'`\n\t\f\r \u00e9"
	pieces := []string{"tOkEn", "secret", "PASS", "pwd", "passphrase", "Password", "\u017fecret", "to\u212aen"}
	rounds := 20000
	if raceEnabled {
		rounds = 4000
	}
	for i := 0; i < rounds; i++ {
		var b strings.Builder
		n := r.Intn(60)
		for j := 0; j < n; j++ {
			switch r.Intn(9) {
			case 0:
				b.WriteString(strings.Repeat("7", 4+r.Intn(3)))
			case 1:
				b.WriteString(strings.Repeat("x", 20+r.Intn(15)))
			case 2:
				b.WriteString(strings.Repeat("y", 4+r.Intn(5)) + ".")
			case 3:
				b.WriteString(pieces[r.Intn(len(pieces))])
			default:
				b.WriteByte(alphabet[r.Intn(len(alphabet))])
			}
		}
		s := b.String()
		if got, want := digitColonShape(s), digitColon.MatchString(s); got != want {
			t.Fatalf("digitColonShape(%q) = %v, want %v", s, got, want)
		}
		if got, want := dottedTokenShape(s), dotted.MatchString(s); got != want {
			t.Fatalf("dottedTokenShape(%q) = %v, want %v", s, got, want)
		}
		got, want := newScan(s, false).keyBeforeSeparator(keys), keySep.MatchString(s)
		if strings.ContainsAny(s, "\u212a\u017f") {
			if want && !got {
				t.Fatalf("keyBeforeSeparator(%q) rejects a match", s)
			}
		} else if got != want {
			t.Fatalf("keyBeforeSeparator(%q) = %v, want %v", s, got, want)
		}
	}
}

// containsAnchored finds a literal exactly when strings.Contains does, from
// every anchor byte; lowerASCII lower-cases A-Z and nothing else.
func TestAnchoredSearchAndLowering(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	alphabet := "aAbkK_-:/\u212a\u017f"
	gen := func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteByte(alphabet[r.Intn(len(alphabet))])
		}
		return b.String()
	}
	for i := 0; i < 20000; i++ {
		text, lit := gen(r.Intn(40)), gen(1+r.Intn(5))
		if r.Intn(3) == 0 && len(text) > 0 {
			a := r.Intn(len(text))
			lit = text[a : a+r.Intn(len(text)-a)+1]
		}
		for a := 0; a < len(lit); a++ {
			if got, want := containsAnchored(text, lit, a), strings.Contains(text, lit); got != want {
				t.Fatalf("containsAnchored(%q, %q, %d) = %v, want %v", text, lit, a, got, want)
			}
			from := r.Intn(len(text) + 1)
			want := strings.Index(text[from:], lit)
			if want >= 0 {
				want += from
			}
			if got := indexAnchored(text, lit, a, from); got != want {
				t.Fatalf("indexAnchored(%q, %q, %d, %d) = %d, want %d", text, lit, a, from, got, want)
			}
		}
		lower := lowerASCII(text)
		if len(lower) != len(text) {
			t.Fatalf("lowerASCII changed the length of %q", text)
		}
		for k := 0; k < len(text); k++ {
			want := text[k]
			if want >= 'A' && want <= 'Z' {
				want += 'a' - 'A'
			}
			if lower[k] != want {
				t.Fatalf("lowerASCII(%q) = %q", text, lower)
			}
		}
	}
	for lit, want := range map[string]int{"authorization": 7, "token": 2, "k_live_": 0, "@": 0, "<pass": 1, "seeA": 3, "e-e": 1} {
		if a := rarestByte(lit); a != want {
			t.Fatalf("rarestByte(%q) = %d, want %d", lit, a, want)
		}
	}
}

// The command-word search over the lower-cased copy finds what indexFoldASCII
// finds, from every offset.
func TestCommandWordSearchMatchesIndexFold(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	parts := []string{"curl", "CURL", "Curl", "wget", "WGET", "http", "HTTPS", "xh", "XH", "cur", "c", "u", " ", ";", "\u212a", "\u017f", "\xff", "\u00e9"}
	for i := 0; i < 5000; i++ {
		var b strings.Builder
		for n := r.Intn(30); n > 0; n-- {
			b.WriteString(parts[r.Intn(len(parts))])
		}
		text := b.String()
		gatedScan, reference := newScan(text, false), newScan(text, true)
		for _, tool := range commandTools {
			for pos := 0; pos <= len(text); pos++ {
				if got, want := gatedScan.indexFoldFrom(tool.name, pos), reference.indexFoldFrom(tool.name, pos); got != want {
					t.Fatalf("indexFoldFrom(%q, %q, %d) = %d, want %d", text, tool.name, pos, got, want)
				}
			}
		}
	}
}

// gateTestLiterals collects every string literal in the package's test files.
func gateTestLiterals(tb testing.TB) []string {
	tb.Helper()
	files, _ := filepath.Glob("*_test.go")
	sort.Strings(files)
	fset := token.NewFileSet()
	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			tb.Fatal(err)
		}
		af, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			tb.Fatal(err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			if bl, ok := n.(*ast.BasicLit); ok && bl.Kind == token.STRING {
				if s, err := strconv.Unquote(bl.Value); err == nil && !seen[s] {
					seen[s] = true
					out = append(out, s)
				}
			}
			return true
		})
	}
	return out
}

// foldRuneVariants respells k and s with the non-ASCII runes Unicode case
// folding pairs them with.
var foldRuneVariants = []*strings.Replacer{
	strings.NewReplacer("k", "\u212a", "K", "\u212a"),
	strings.NewReplacer("s", "\u017f", "S", "\u017f"),
	strings.NewReplacer("k", "\u212a", "K", "\u212a", "s", "\u017f", "S", "\u017f"),
}

// gateCorpus is every test literal of the package, alone, joined to its
// neighbour, embedded in secret-free filler, in upper and lower case, and with
// k and s respelled as the runes that fold to them.
func gateCorpus(tb testing.TB) []string {
	lits := gateTestLiterals(tb)
	out := make([]string, 0, 8*len(lits))
	for i, s := range lits {
		out = append(out, s, gateFiller+s+gateFiller, strings.ToUpper(s), strings.ToLower(s))
		if i+1 < len(lits) {
			out = append(out, s+lits[i+1])
		}
		for _, v := range foldRuneVariants {
			if f := v.Replace(s); f != s {
				out = append(out, f)
			}
		}
	}
	return out
}

// checkGatedMatchesUngated fails when the gates change any result for s, or
// when a gated pattern matches s and its gate rejects s.
func checkGatedMatchesUngated(t *testing.T, s string) {
	t.Helper()
	if got, want := redactSecrets(s, false), redactSecrets(s, true); got != want {
		t.Fatalf("RedactSecrets differs with gates for %q:\n gated   %q\n ungated %q", s, got, want)
	}
	if got, want := hasSecrets(s, false), hasSecrets(s, true); got != want {
		t.Fatalf("HasSecrets differs with gates for %q: gated %v, ungated %v", s, got, want)
	}
	if got, want := countSecrets(s, false), countSecrets(s, true); got != want {
		t.Fatalf("CountSecrets differs with gates for %q: gated %d, ungated %d", s, got, want)
	}
	scan := newScan(s, false)
	for _, re := range gatedRedactionPatterns() {
		if re.MatchString(s) && !scan.may(re) {
			t.Fatalf("gate of %q rejects a match in %q", re, s)
		}
	}
}

// The gates never change a result: for every string literal in the package's
// tests (every known leak and over-redaction case) and its variants, the gated
// and ungated paths agree. The corpus is checked in parallel shards to stay
// inside the CI test timeout under -race. A lean run checks every fifth
// input, which takes each literal's variants in turn; the plain literals all
// run as the seeds of FuzzRedactGatedMatchesUngated.
func TestGatedRedactionMatchesUngated(t *testing.T) {
	t.Parallel()
	corpus := gateCorpus(t)
	const shards = 4
	step := shards
	if leanRun() {
		step = 5 * shards
	}
	t.Logf("differential corpus: %d of %d inputs", (len(corpus)+step/shards-1)/(step/shards), len(corpus))
	for shard := 0; shard < shards; shard++ {
		t.Run(strconv.Itoa(shard), func(t *testing.T) {
			t.Parallel()
			for i := shard; i < len(corpus); i += step {
				checkGatedMatchesUngated(t, corpus[i])
			}
		})
	}
}

// FuzzRedactGatedMatchesUngated: whatever the input, the gates change no
// result and never reject a match of their pattern.
// Under plain go test it runs the package's test literals; with -fuzz it
// starts from the whole differential corpus.
func FuzzRedactGatedMatchesUngated(f *testing.F) {
	seeds := gateTestLiterals(f)
	if fl := flag.Lookup("test.fuzz"); fl != nil && fl.Value.String() != "" {
		seeds = gateCorpus(f)
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 4096 {
			t.Skip()
		}
		checkGatedMatchesUngated(t, s)
	})
}
