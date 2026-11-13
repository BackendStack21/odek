package redact

import (
	"strings"
	"testing"
	"time"
)

// redactR3Linear fails when quadrupling the input grows the redaction time
// more than 12x (quadratic growth is 16x).
func redactR3Linear(t *testing.T, name string, gen func(n int) string) {
	t.Helper()
	base := 16 << 10
	small, large := finalRedactTime(gen(base), 2), finalRedactTime(gen(4*base), 1)
	if small < 2*time.Millisecond {
		small = 2 * time.Millisecond
	}
	if large > 12*small {
		t.Errorf("%s: super-linear: %v then %v", name, small, large)
	}
}

// A connection-URL span runs to the next blank or quote, so it can join
// several URLs with any glue. A reference password in the first URL must not
// leave a literal password of a later user:password pair readable, whatever
// separates them; a single URL whose only password is a reference stays
// readable.
func TestRED_DBURLJoinedSpanJudgesEveryPairWhateverTheGlue(t *testing.T) {
	const lit = "S3cretPass99"
	for _, in := range []string{
		"postgres://app:${DB_PASSWORD}@db/app?replica=mysql://root:" + lit + "@h2/db",
		"postgres://app:${DB_PASSWORD}@db/app|mysql://root:" + lit + "@h2/db",
		"A=postgres://app:${DB_PASSWORD}@db/app&&B=mysql://root:" + lit + "@h2/db",
		"postgres://app:$DB_PASSWORD@db/app&redis://:" + lit + "@h2:6379",
		"postgres://app:${DB_PASSWORD}@db/app)(mysql://root:" + lit + "@h2/db",
		"postgres://app:${DB_PASSWORD}@db/app>mysql://root:" + lit + "@h2/db",
		"postgres://app:${DB_PASSWORD}@db/app=root:" + lit + "@h2/db",
		"postgres://app:${DB_PASSWORD}@db/app|root:" + lit + "@h2/db",
		// An '@' right after a host with nothing ending the authority may
		// still belong to the password before it.
		"postgres://app:${DB_PASSWORD}@" + lit + "@h2/db",
		"postgres://app:${DB_PASSWORD}@h1:" + lit + "@h2/db",
		// A second colon in a later userinfo is part of its password.
		"mongodb://u:${A}@h1,u:" + lit + ":${B}@h2/db",
	} {
		if out := RedactSecrets(in); strings.Contains(out, lit) {
			t.Errorf("leak: %q => %q", in, out)
		}
	}
	for _, in := range []string{
		"postgres://app:${DB_PASSWORD}@db:5432/app?sslmode=disable",
		"postgres://app:${DB_PASSWORD}@db/app?application_name=a@b.example",
		"postgres://app:${A}@h1/db?replica=mysql://root:${B}@h2/db",
		"postgres://app:${A}@h1/db|mysql://root@h2/db",
		"mongodb://u:${A}@h1:27017,u:${B}@h2:27017/db",
	} {
		if out := RedactSecrets(in); out != in {
			t.Errorf("reference-only span rewritten: %q => %q", in, out)
		}
	}
	// A userinfo that cannot be isolated is redacted whole, as a literal
	// password always was.
	if out := RedactSecrets("postgres://app:${A}@db:5432/app?x=a@b"); out != "[REDACTED]" {
		t.Errorf("ambiguous userinfo not redacted whole: %q", out)
	}
}

func TestRED_DBURLJoinedSpanScalesLinearly(t *testing.T) {
	redactR3Linear(t, "many @", func(n int) string { return "postgres://u:${A}@" + strings.Repeat("h/x@", n/4) })
	redactR3Linear(t, "many urls", func(n int) string {
		return "postgres://u:${A}@h" + strings.Repeat("?mysql://u:${B}@h", n/17)
	})
}
