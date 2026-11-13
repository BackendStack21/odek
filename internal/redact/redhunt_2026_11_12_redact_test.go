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

// MYSQL_PWD=VALUE carries the client password at any length: a short
// literal and a value in JSON-escaped quotes are redacted (the value only,
// inside its escaped quotes, so encoded JSON stays well-formed), while
// references, command substitutions, placeholders and an empty value stay
// readable.
func TestRED_MySQLPwdAnyLengthAndEscapedQuotes(t *testing.T) {
	for in, want := range map[string]string{
		"MYSQL_PWD=r00t mysql -uroot db":              "MYSQL_PWD=[REDACTED] mysql -uroot db",
		"MYSQL_PWD=abc12 mysqldump db":                "MYSQL_PWD=[REDACTED] mysqldump db",
		"MYSQL_PWD=x mysql db":                        "MYSQL_PWD=[REDACTED] mysql db",
		"MYSQL_PWD='r00t' mysql db":                   "MYSQL_PWD=[REDACTED] mysql db",
		`MYSQL_PWD="r00t" mysql db`:                   "MYSQL_PWD=[REDACTED] mysql db",
		`{"cmd":"MYSQL_PWD=\"hunter22\" mysql db"}`:   `{"cmd":"MYSQL_PWD=\"[REDACTED]\" mysql db"}`,
		`{"cmd":"MYSQL_PWD=r00t\nmysql db"}`:          `{"cmd":"MYSQL_PWD=[REDACTED]\nmysql db"}`,
		`{"cmd":"echo \"MYSQL_PWD=r00t\" > env"}`:     `{"cmd":"echo \"MYSQL_PWD=[REDACTED]\" > env"}`,
		"MYSQL_PWD=pa\\$\\$w0rd mysql db":             "MYSQL_PWD=[REDACTED] mysql db",
		"env MYSQL_PWD=r00t mysql db; MYSQL_PWD=x ls": "env MYSQL_PWD=[REDACTED] mysql db; MYSQL_PWD=[REDACTED] ls",
	} {
		if out := RedactSecrets(in); out != want {
			t.Errorf("%q => %q, want %q", in, out, want)
		}
	}
	for _, in := range []string{
		`{"cmd":"MYSQL_PWD=\"\" mysql db"}`,
		`{"cmd":"MYSQL_PWD=\"$DB_PASS\" mysql db"}`,
		"MYSQL_PWD= mysql db",
		"MYSQL_PWD='' mysql db",
		"MYSQL_PWD=$(cat /run/secrets/db) mysql db",
		`MYSQL_PWD="$(cat /run/secrets/db)" mysql db`,
		"MYSQL_PWD=<password> mysql db",
		"MYSQL_PWD=YOUR_PASSWORD mysql db",
		"MYSQL_PWD=changeme mysql db",
	} {
		if out := RedactSecrets(in); out != in {
			t.Errorf("over-redaction: %q => %q", in, out)
		}
	}
}

// sshpass parses its own options only up to the wrapped command (the first
// non-option word or "--"): the wrapped command's options, such as an ssh
// port, are not sshpass passwords, and -f/-d/-P values carry no password. An
// sshpass word inside the wrapped command (a jump-host chain) starts another
// option list.
func TestRED_SshpassJudgesOnlyItsOwnOptions(t *testing.T) {
	for in, want := range map[string]string{
		"sshpass -p s3cretPW ssh -p 2222 user@host uptime":     "sshpass -p [REDACTED] ssh -p 2222 user@host uptime",
		"sshpass -ps3cretPW ssh -p2222 user@host":              "sshpass -p[REDACTED] ssh -p2222 user@host",
		"sshpass -vp s3cretPW scp -P 2222 -p f h:":             "sshpass -vp [REDACTED] scp -P 2222 -p f h:",
		"sshpass -P assword: -p s3cretPW ssh -p 22 h":          "sshpass -P assword: -p [REDACTED] ssh -p 22 h",
		"sshpass -p s3cretPW -- ssh -p 22 h":                   "sshpass -p [REDACTED] -- ssh -p 22 h",
		"sshpass -p one1 ssh -p 22 jump sshpass -p two2 ssh t": "sshpass -p [REDACTED] ssh -p 22 jump sshpass -p [REDACTED] ssh t",
		"sshpass -v -- /usr/bin/sshpass -p two2 ssh -p 22 t":   "sshpass -v -- /usr/bin/sshpass -p [REDACTED] ssh -p 22 t",
		`{"cmd":"sshpass -p \"s3cretPW\" ssh -p 2222 h"}`:      `{"cmd":"sshpass -p \"[REDACTED]\" ssh -p 2222 h"}`,
	} {
		if out := RedactSecrets(in); out != want {
			t.Errorf("%q => %q, want %q", in, out, want)
		}
	}
	for _, in := range []string{
		"sshpass -f /tmp/pw ssh -p 2222 user@host",
		"sshpass -f/tmp/pw ssh -p2222 user@host",
		"sshpass -d 3 ssh -p 2222 h",
		"sshpass -e ssh -p 2222 h",
		"sshpass -eSSHPASS_X ssh -p2222 h",
		"sshpass ssh -p 2222 h",
		"sshpass -v -- ssh -p 2222 h",
		"sshpass -P",
	} {
		if out := RedactSecrets(in); out != in {
			t.Errorf("over-redaction: %q => %q", in, out)
		}
	}
}

// A MySQL/MariaDB client name counts wherever it stands as a whole word
// (also a path's last component), so a client run through docker exec,
// kubectl exec, ssh or a wrapper keeps its password redacted; it does not
// count after '@' (a host), as an --option= value, or right after --name,
// --hostname, ssh or a package or release verb. Inside a docker/podman
// run/create command a port-mapping -p value is never a password.
func TestRED_DBClientNameCountsOnlyAsTheClient(t *testing.T) {
	for _, in := range []string{
		"docker run --name mysql -p3306:3306 -d mysql:8",
		"docker run --name=mysql -p3306:3306 mysql",
		`docker run --name="mysql" -p3306:3306 mysql`,
		"docker run -d -v /var/lib/mysql -p127.0.0.1:3306:3306/tcp mysql:8",
		"podman create --network mysql -p13306-13307:3306-3307 mysql:8",
		"sudo docker container run -it --hostname mysql -p3306:3306 img",
		`{"cmd":"docker run --link mysql -p8080:80 app"}`,
		"helm install mysql -pvalues bitnami/mysql",
		"helm upgrade mysql -pvalues bitnami/mysql",
		"ssh root@mysql -p2222 uptime",
		"ssh mysql -p2222 uptime",
	} {
		if out := RedactSecrets(in); out != in {
			t.Errorf("over-redaction: %q => %q", in, out)
		}
	}
	const pw = "S3cretPW"
	for _, in := range []string{
		"docker exec db mysql -p" + pw + " app",
		"docker exec -it db mysql -uroot -p" + pw + " app",
		"kubectl exec -it pod -- mysql -p" + pw,
		"ssh host mysql -p" + pw + " -e 'select 1'",
		"sudo -E mysql -p" + pw,
		"sudo -n mysql -p" + pw,
		"env -i mysql -p" + pw,
		"time -p mysql -p" + pw,
		"sudo -u mysql mysql -p" + pw,
		"MYSQL=mysql -uroot -p" + pw,
		"docker run --rm -it mysql mysql -hdb -p" + pw,
		"docker run --name mysql --rm mysql:8 mysql -p" + pw,
		"docker run --rm mysql:8 mysqldump -p" + pw + " db",
		"/usr/bin/mysql -p" + pw,
		"$(mysql -p" + pw + ")",
		"mysql -p1234:5678 db",
		"helm install mysql bitnami/mysql; mysql -p" + pw,
		`{"cmd":"echo hi\nmysql -uroot -p` + pw + ` db"}`,
		`{"cmd":"echo hi\tmysql -uroot -p` + pw + ` db"}`,
		`{"cmd":"echo hi\ncurl -u admin:` + pw + `99 http://x"}`,
		`{"cmd":"echo hi\nsshpass -p ` + pw + ` ssh h"}`,
	} {
		if out := RedactSecrets(in); strings.Contains(out, pw) || strings.Contains(out, "1234:5678") {
			t.Errorf("leak: %q => %q", in, out)
		}
	}
}

// The client-name rule looks back a bounded distance; a name whose word or
// blank run does not fit counts as the client, so its password is redacted.
func TestRED_DBClientNameLookBackFailsTowardRedaction(t *testing.T) {
	const pw = "S3cretPW"
	if out := RedactSecrets("install /a/mysql -p" + pw); out != "install /a/mysql -p"+pw {
		t.Errorf("excluded name judged: %q", out)
	}
	for _, in := range []string{
		"install /" + strings.Repeat("a", 300) + "/mysql -p" + pw,
		"install" + strings.Repeat(" ", 300) + "mysql -p" + pw,
		"   mysql -p" + pw,
	} {
		if out := RedactSecrets(in); strings.Contains(out, pw) {
			t.Errorf("leak: %q", out)
		}
	}
}

func TestRED_DBClientAndSshpassScanScalesLinearly(t *testing.T) {
	for name, unit := range map[string]string{
		"--name mysql":     "--name mysql ",
		"host@mysql":       "u@mysql ",
		"option value":     "--x=mysql ",
		"path":             "/a/b/mysql ",
		"sshpass file":     "sshpass -f x ",
		"sshpass chain":    "sshpass -v ",
		"docker port":      "docker run mysql -p1:1;",
		"verb":             "install mysql ",
		"json escape":      `\nmysql`,
		"long word before": strings.Repeat("a", 300) + " mysql ",
	} {
		redactR3Linear(t, name, func(n int) string { return strings.Repeat(unit, n/len(unit)) })
	}
}

// A '$'-leading flag or MYSQL_PWD value is a variable reference only in
// shell-variable shape ($NAME in upper case, digits and '_', or ${NAME}) and
// only where the shell expands it: inside single quotes, after a backslash
// or as $'...' it is a literal password, like the DB-URL rule judges it.
func TestRED_SingleQuotedDollarPasswordIsLiteral(t *testing.T) {
	for _, in := range []string{
		"mysql -p'$ecret123' db",
		"MYSQL_PWD='$ecret123' mysql db",
		"mysql -p$ecret123 db",
		"mysql -p\"$ecret123\" db",
		"mysql -p\\$ecret123 db",
		"mysql -p$'$ecret123' db",
		"mysql --password='$ecret123' db",
		"sshpass -p '$ecret123' ssh h",
		"MYSQL_PWD=$ecret123 mysql db",
		"MYSQL_PWD=\\$ecret123 mysql db",
		`{"cmd":"mysql -p'$ecret123' db"}`,
		`{"cmd":"MYSQL_PWD=\"$ecret123\" mysql db"}`,
	} {
		if out := RedactSecrets(in); strings.Contains(out, "ecret123") {
			t.Errorf("leak: %q => %q", in, out)
		}
	}
	for in, want := range map[string]string{
		"mysql -p'$DB_PASS' db":         "mysql -p'[REDACTED]' db",
		"MYSQL_PWD='${DB_PASS}' mysql":  "MYSQL_PWD=[REDACTED] mysql",
		`mysql -u"$user" -p"$password"`: `mysql -u"$user" -p"[REDACTED]"`,
		"sshpass -p '$SSHPASS' ssh h":   "sshpass -p '[REDACTED]' ssh h",
		"mysql -p$'S3CRET' db":          "mysql -p[REDACTED]' db",
		"mysql --password=\\$DB_PW db":  "mysql --password=\\[REDACTED] db",
		`{"cmd":"mysql -p'$DB_PASS'"}`:  `{"cmd":"mysql -p'[REDACTED]'"}`,
		"MYSQL_PWD=$Passw0rd mysql db":  "MYSQL_PWD=[REDACTED] mysql db",
		"mysql -p'$(cat /run/pw)' db":   "mysql -p'[REDACTED]' db",
		"sshpass -p'${X:-hunter2}' ssh": "sshpass -p'[REDACTED]' ssh",
	} {
		if out := RedactSecrets(in); out != want {
			t.Errorf("%q => %q, want %q", in, out, want)
		}
	}
	for _, in := range []string{
		"mysql -p$DB_PASS db",
		`mysql -p"$DB_PASS" db`,
		"mysql -p${db_pass} db",
		`mysql -p"${db_pass}" db`,
		`mysql -p"${DB_PASS}" db`,
		"mysql --password=$DB_PW db",
		"sshpass -p $SSHPASS ssh h",
		`sshpass -p "$SSHPASS" ssh h`,
		"MYSQL_PWD=$MYSQL_ROOT_PASSWORD mysql db",
		`MYSQL_PWD="${DB_PASS}" mysql db`,
		`{"cmd":"mysql -p\"$DB_PASS\" db"}`,
		`{"cmd":"MYSQL_PWD=\"$DB_PASS\" mysql db"}`,
		"mysql -p$(cat /run/secrets/db) db",
		`mysql -p"$(cat /run/secrets/db)" db`,
		"mysql -p${DB_PASS:-} db",
	} {
		if out := RedactSecrets(in); out != in {
			t.Errorf("reference rewritten: %q => %q", in, out)
		}
	}
}

func TestRED_MySQLPwdScalesLinearly(t *testing.T) {
	redactR3Linear(t, "assignments", func(n int) string { return strings.Repeat("MYSQL_PWD=x ", n/12) })
	redactR3Linear(t, "escaped quotes", func(n int) string { return strings.Repeat(`MYSQL_PWD=\"`, n/12) })
	redactR3Linear(t, "escapes", func(n int) string { return "MYSQL_PWD=" + strings.Repeat(`\$`, n/2) })
}
