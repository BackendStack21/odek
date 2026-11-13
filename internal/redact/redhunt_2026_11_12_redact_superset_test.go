package redact

import (
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"testing"
)

// releaseDBURLPattern is the connection-URL pattern of the release this
// change is based on, kept here as a test-only oracle together with its
// replacement (the whole match becomes the marker).
var releaseDBURLPattern = regexp.MustCompile(`(?i)\b(?:postgres(?:ql)?|mysql|mongodb(?:\+srv)?|rediss?|amqps?|mssql|clickhouse)(?:\+[a-z0-9_]+)?://[^:\s/]*:[^@\s/]{6,}@[^\s'\x60"]+`)

// releaseDBURLRedact is what the release's connection-URL rule made of text.
func releaseDBURLRedact(text string) string {
	return releaseDBURLPattern.ReplaceAllString(text, "[REDACTED]")
}

// The pattern the rule runs first is the release's, byte for byte.
func TestRED_DBURLBasePatternIsTheReleasePattern(t *testing.T) {
	if got, want := dbURLBasePattern.String(), releaseDBURLPattern.String(); got != want {
		t.Fatalf("base pattern differs from the release pattern:\n  got  %s\n  want %s", got, want)
	}
}

// A reference-only connection URL followed, with no blank or quote between,
// by a second URL whose literal password holds a quote or a backquote: the
// release hides the second password, so this build must too.
func TestRED_DBURLReferenceThenQuotedPasswordIsRedacted(t *testing.T) {
	for _, in := range []string{
		`postgres://a:$A@h1/x,postgres://u:Zq7Xk9'Wm4Rt2@h2/y`,
		`postgres://a:${A}@h1/x,postgres://u:Zq7Xk9"Wm4Rt2@h2/y`,
		"postgres://a:$A@h1/x,mysql://u:Zq7Xk9`Wm4Rt2@h2/y",
		`postgres://a:$A@h1;postgres://u:Zq7Xk9'Wm4Rt2@h2/y`,
		`postgres://a:$A@h1/x|postgres://u:Zq7Xk9'Wm4Rt2@h2/y`,
		`postgres://a:$A@h1/x#postgres://u:Zq7Xk9'Wm4Rt2@h2/y`,
		`REPLICAS=postgres://a:$A@h1/x,postgres://u:'Zq7Xk9Wm4Rt2@h2/y`,
		`postgres://a:$A@h1/x,postgres://u:Zq7Xk9Wm4Rt2'@h2/y`,
		`DATABASE_URL=postgres://app:$PGPASSWORD@primary/app,postgres://ro:Zq7Xk9"Wm4Rt2@replica/app`,
		`postgres://a:<password>@h1/x,postgres://u:Zq7Xk9'Wm4Rt2@h2/y`,
		`postgres://a:%DB_PASS%@h1/x,postgres://u:Zq7Xk9'Wm4Rt2@h2/y`,
		`postgres://a:{{pw}}@h1/x,postgres://u:Zq7Xk9'Wm4Rt2@h2/y`,
	} {
		if got := RedactSecrets(in); strings.Contains(got, "Zq7Xk9") {
			t.Errorf("password prefix readable:\n  in  %s\n  out %s", in, got)
		}
	}
	// A reference-only URL whose span is cut by a quote that a later '@'
	// follows before any blank is not a clean single URL.
	for _, in := range []string{
		`postgres://a:${DB_PASS}@h1,u:Zq7Xk9'Wm4Rt2@h2/y`,
		`postgres://a:$DB_PASS@h1,u:Zq7Xk9"Wm4Rt2@h2/y`,
	} {
		if got := RedactSecrets(in); strings.Contains(got, "Zq7Xk9") {
			t.Errorf("password prefix readable:\n  in  %s\n  out %s", in, got)
		}
	}
}

// A connection URL right after a JSON \n, \r or \t escape starts a word of
// the decoded text.
func TestRED_DBURLAfterJSONBlankEscape(t *testing.T) {
	for _, in := range []string{
		`{"cmd":"echo hi\npostgres://user:Zq7Xk9Wm4Rt2@db/app"}`,
		`{"cmd":"echo hi\tmysql://user:Zq7Xk9Wm4Rt2@db/app"}`,
		`{"cmd":"echo hi\r\nredis://:Zq7Xk9Wm4Rt2@db:6379"}`,
	} {
		if got := RedactSecrets(in); strings.Contains(got, "Zq7Xk9Wm4Rt2") {
			t.Errorf("password readable:\n  in  %s\n  out %s", in, got)
		}
	}
	for _, in := range []string{
		`{"cmd":"echo hi\npostgres://user:${DB_PASS}@db/app"}`,
		`{"cmd":"echo hi\npostgres://db/app"}`,
	} {
		if got := RedactSecrets(in); got != in {
			t.Errorf("over-redaction: %s => %s", in, got)
		}
	}
}

// supersetSchemes, supersetRefs, supersetGlue and supersetSpecials are what
// the generated connection-URL spans are built from.
var (
	supersetSchemes  = []string{"postgres", "postgresql", "postgresql+psycopg2", "mysql", "mongodb+srv", "redis", "rediss", "amqp", "mssql", "clickhouse", "MySQL"}
	supersetRefs     = []string{"${DB_PASSWORD}", "$DB_PASSWORD", "$A", "${A}", "{password}", "{{ db_pw }}", "{{pw}}", "<password>", "%(password)s", "%DB_PASS%"}
	supersetGlue     = []string{"", ",", ";", "|", "#", "?x=", "&&", " ", "'", `"`, "`"}
	supersetSpecials = []string{"'", `"`, "`", ",", ";", "@", ":", "/", "?", "#", "$", "%", "{", "<"}
)

// supersetContainers wrap a span the way it reaches the redactor: bare, in a
// JSON string, in single or double quotes, and as an env line.
var supersetContainers = []func(string) string{
	func(s string) string { return s },
	func(s string) string { return `{"url":"` + redactR4JSON(s) + `"}` },
	func(s string) string { return "'" + s + "'" },
	func(s string) string { return `"` + s + `"` },
	func(s string) string { return "DATABASE_URL=" + s },
}

// supersetCheck fails when a planted piece the release rule removed is still
// readable after RedactSecrets.
func supersetCheck(t *testing.T, in string, pieces []string) {
	t.Helper()
	rel := releaseDBURLRedact(in)
	if rel == in {
		return
	}
	out := RedactSecrets(in)
	for _, p := range pieces {
		if !strings.Contains(rel, p) && strings.Contains(out, p) {
			t.Fatalf("piece %q hidden by the release rule is readable:\n  in      %s\n  release %s\n  out     %s", p, in, rel, out)
		}
	}
}

// supersetPassword returns a password for URL k of span id and the unique
// literal pieces planted in it: a reference shape (no pieces), a plain
// literal, or a literal with one special character at its start, middle or
// end.
func supersetPassword(id, k, shape, special, where int) (string, []string) {
	a, b := fmt.Sprintf("Lq%dx%dAAz", id, k), fmt.Sprintf("Lq%dx%dBBz", id, k)
	switch {
	case shape < len(supersetRefs):
		return supersetRefs[shape], nil
	case shape == len(supersetRefs):
		return a, []string{a}
	}
	sp := supersetSpecials[special]
	switch where {
	case 0:
		return sp + a, []string{a}
	case 1:
		return a + sp + b, []string{a, b}
	}
	return a + sp, []string{a}
}

// Superset of the release: every literal password piece the release's
// connection-URL rule removed is removed by this build too, over spans of
// one to four URLs joined by any glue, with reference and literal passwords
// (special characters at the start, middle and end), in every container. The
// one exception is covered by TestRED_DBURLCleanReferenceURLStaysReadable.
func TestRED_DBURLSupersetOfRelease(t *testing.T) {
	cases, differing := 0, 0
	run := func(span string, pieces []string) {
		for _, wrap := range supersetContainers {
			in := wrap(span)
			cases++
			if releaseDBURLRedact(in) != in {
				differing++
			}
			supersetCheck(t, in, pieces)
		}
	}
	hosts := []string{"db", "h1:5432", "db.example:3306/app", "h2/db?ssl=1", "[::1]:6379"}
	shapes := len(supersetRefs) + 2
	// Every pair: first password × glue × second password.
	id := 0
	for s1 := 0; s1 < shapes; s1++ {
		for _, g := range supersetGlue {
			for s2 := 0; s2 < shapes; s2++ {
				specials, wheres := 1, 1
				if s2 == shapes-1 {
					specials, wheres = len(supersetSpecials), 3
				}
				for sp := 0; sp < specials; sp++ {
					for w := 0; w < wheres; w++ {
						id++
						p1, l1 := supersetPassword(id, 0, s1, sp, w)
						p2, l2 := supersetPassword(id, 1, s2, sp, w)
						span := "postgres://a:" + p1 + "@h1/x" + g + supersetSchemes[id%len(supersetSchemes)] + "://u:" + p2 + "@h2/y"
						run(span, append(l1, l2...))
					}
				}
			}
		}
	}
	// Random spans of one to four URLs.
	rng := rand.New(rand.NewSource(1112))
	n := 60000
	if testing.Short() {
		n = 12000
	}
	for iter := 0; iter < n; iter++ {
		id++
		var b strings.Builder
		var pieces []string
		for k, urls := 0, 1+rng.Intn(4); k < urls; k++ {
			if k > 0 {
				b.WriteString(supersetGlue[rng.Intn(len(supersetGlue))])
			}
			pw, ps := supersetPassword(id, k, rng.Intn(shapes+4), rng.Intn(len(supersetSpecials)), rng.Intn(3))
			pieces = append(pieces, ps...)
			b.WriteString(supersetSchemes[rng.Intn(len(supersetSchemes))] + "://u" + fmt.Sprint(k) + ":" + pw + "@" + hosts[rng.Intn(len(hosts))])
		}
		run(b.String(), pieces)
	}
	t.Logf("corpus: %d inputs, %d changed by the release rule, 0 violations", cases, differing)
	if differing < cases/4 {
		t.Errorf("the release rule changed only %d of %d inputs: the corpus does not exercise it", differing, cases)
	}
}

// The one exception to the release rule: a single URL (one "://", one '@')
// whose password is wholly a reference stays readable in every container.
func TestRED_DBURLCleanReferenceURLStaysReadable(t *testing.T) {
	for _, s := range supersetSchemes {
		for _, r := range supersetRefs {
			for _, tail := range []string{"db", "db:5432/app?sslmode=disable", "h1,h2,h3/db?replicaSet=rs0"} {
				for _, wrap := range supersetContainers {
					in := wrap(s + "://app:" + r + "@" + tail)
					if out := RedactSecrets(in); out != in {
						t.Errorf("reference-only URL rewritten: %q => %q", in, out)
					}
				}
			}
		}
	}
	// A release match that is not one clean URL is redacted as the release
	// did, whatever its passwords read as.
	for _, in := range []string{
		"postgres://app:${DB_PASSWORD}@h1/db,postgres://ro@h2/db",
		"postgres://app:${DB_PASSWORD}@h1/db?next=https://x.example/",
		"postgres://app:${DB_PASSWORD}@h1/db?mail=a@b.example",
		"postgres://app:${DB_PASSWORD}x@h1/db",
		"postgres://app:x${DB_PASSWORD}@h1/db",
		"postgres://app:${DB_PASSWORD:-hunter2}@h1/db",
		"postgres://app:$Passw0rd@h1/db",
	} {
		if out := RedactSecrets(in); out != releaseDBURLRedact(in) || out == in {
			t.Errorf("%q => %q, want the release result %q", in, out, releaseDBURLRedact(in))
		}
	}
}
