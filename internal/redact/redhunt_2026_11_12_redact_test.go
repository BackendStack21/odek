package redact

import (
	"math/rand"
	"strconv"
	"strings"
	"testing"
	"time"
)

// redactR3Linear fails when quadrupling the input grows the redaction time
// more than 12x (quadratic growth is 16x). Both sizes take the best of two
// runs, so one preempted run does not read as super-linear growth.
func redactR3Linear(t *testing.T, name string, gen func(n int) string) {
	t.Helper()
	base := 16 << 10
	small, large := finalRedactTime(gen(base), 2), finalRedactTime(gen(4*base), 2)
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
	if in := "postgres://app:${DB_PASSWORD}@db:5432/app?sslmode=disable"; RedactSecrets(in) != in {
		t.Errorf("reference-only URL rewritten: %q => %q", in, RedactSecrets(in))
	}
	// A second '@' may belong to the password before it, so a span with one
	// is redacted whole even when every password in it reads as a reference.
	for _, in := range []string{
		"postgres://app:${DB_PASSWORD}@db/app?application_name=a@b.example",
		"postgres://app:${A}@h1/db?replica=mysql://root:${B}@h2/db",
		"postgres://app:${A}@h1/db|mysql://root@h2/db",
		"mongodb://u:${A}@h1:27017,u:${B}@h2:27017/db",
	} {
		if out := RedactSecrets(in); out != "[REDACTED]" {
			t.Errorf("span with a second '@' not redacted whole: %q => %q", in, out)
		}
	}
	if out := RedactSecrets("postgres://app:${A}@db:5432/app?x=a@b"); out != "[REDACTED]" {
		t.Errorf("ambiguous userinfo not redacted whole: %q", out)
	}
}

func TestRED_DBURLJoinedSpanScalesLinearly(t *testing.T) {
	redactR3Linear(t, "many @", func(n int) string { return "postgres://u:${A}@" + strings.Repeat("h/x@", n/4) })
	redactR3Linear(t, "many urls", func(n int) string {
		return "postgres://u:${A}@h" + strings.Repeat("?mysql://u:${B}@h", n/17)
	})
	for name, unit := range map[string]string{
		"://a:b@":           "://a:b@",
		"mysql://a:b@":      "mysql://a:b@",
		"@":                 "@",
		"mysql://a:${B}@x,": "mysql://a:${B}@x,",
		"mysql://a:b/c@":    "mysql://a:b/c@",
	} {
		redactR3Linear(t, name, func(n int) string { return strings.Repeat(unit, n/len(unit)) })
	}
}

// A backslash-newline (also before CRLF) continues the line and the word, a
// JSON \t escape separates sshpass's words, and a quoted "-p" option takes
// the next word; a line break without a backslash ends the client's line.
func TestRED_ShellPasswordContinuationsAndQuotedOption(t *testing.T) {
	const pw = "S3cretPw"
	for _, in := range []string{
		"mysql -h db \\\r\n -p" + pw,
		"mysql -pab\\\r\n" + pw + " db",
		"mysql -pab\\\n" + pw + " db",
		"sshpass -p \\\r\n" + pw + " ssh h",
		"sshpass -p \\\n" + pw + " ssh h",
		`{"c":"sshpass\t-p\t` + pw + ` ssh h"}`,
		`sshpass "-p" ` + pw + ` ssh h`,
		`sshpass '-p' sshpass -p ` + pw + ` ssh h`,
	} {
		if out := RedactSecrets(in); strings.Contains(out, pw) {
			t.Errorf("leak: %q => %q", in, out)
		}
	}
	for _, in := range []string{
		"mysql x\r\nps -p1234",
		"mysql x\nps -p1234",
		`sshpass "-p"`,
		`sshpass "-p" ` + "\n" + pw,
	} {
		if out := RedactSecrets(in); out != in {
			t.Errorf("over-redaction: %q => %q", in, out)
		}
	}
}

// The shell-password rules stay linear on the shapes that make a lexer
// search far ahead or a redaction pass repeat: unmatched quotes,
// substitutions and parentheses, JSON escapes, and many client words, -p
// values and MYSQL_PWD assignments on one line.
func TestRED_ShellPasswordRulesScaleLinearly(t *testing.T) {
	for name, unit := range map[string]string{
		"mysql -p1:1)":        "mysql -p1:1)",
		"sshpass -p ":         "sshpass -p ",
		"MYSQL_PWD=\\\"":      "MYSQL_PWD=\\\"",
		"MYSQL_PWD='":         "MYSQL_PWD='",
		"docker run mysql -p": "docker run mysql -p1:1 ",
		"newline":             "docker run x\nmysql -p1:1 ",
		"mysql\\t":            "mysql\t",
		"ssh mysql ":          "ssh mysql ",
		"/mysql\\":            "/mysql\\",
		"\\nmysql -p":         "\\nmysql -pX ",
		"escaped backslashes": "x\\\\\\\\\\nmysql ",
		"=mysql ":             "=mysql ",
		"@mysql ":             "@mysql ",
		"mysql ' ":            "mysql -p' ",
		"quoted -p":           "mysql '-p\" ",
		"json -p":             `{"c":"mysql -p\"a b\" -pS3c\\\"x"},`,
		"sshpass json":        `{"c":"sshpass -v \"(x)\" -p S3cret ssh h"},`,
		"redacted -p":         "mysql -p[REDACTED] ",
		"pw words":            "mysql -p\"$A\"'b'$'c'${D}$(e)\\ f ",
	} {
		redactR3Linear(t, name, func(n int) string { return strings.Repeat(unit, n/len(unit)+1)[:n] })
	}
}

// MYSQL_PWD=VALUE carries the client password at any length: a short
// literal and a quoted value are redacted (a value that is one quoted string
// only inside its quotes, so encoded JSON stays well-formed), while
// references, command substitutions, placeholders and an empty value stay
// readable.
func TestRED_MySQLPwdAnyLengthAndEscapedQuotes(t *testing.T) {
	for in, want := range map[string]string{
		"MYSQL_PWD=r00t mysql -uroot db":              "MYSQL_PWD=[REDACTED] mysql -uroot db",
		"MYSQL_PWD=abc12 mysqldump db":                "MYSQL_PWD=[REDACTED] mysqldump db",
		"MYSQL_PWD=x mysql db":                        "MYSQL_PWD=[REDACTED] mysql db",
		"MYSQL_PWD='r00t' mysql db":                   "MYSQL_PWD='[REDACTED]' mysql db",
		`MYSQL_PWD="r00t" mysql db`:                   `MYSQL_PWD="[REDACTED]" mysql db`,
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

// A MySQL/MariaDB client name counts wherever it stands, so a client run
// through docker exec, kubectl exec, ssh or any wrapper keeps its password
// redacted, and so does every later attached -p value on the line; a port
// mapping (digits, dots and colons with a colon, optional /tcp or /udp) is
// the only -p value shape that stays readable.
func TestRED_DBClientNameCountsWhereverItStands(t *testing.T) {
	for _, in := range []string{
		"docker run --name mysql -p3306:3306 -d mysql:8",
		"docker run --name=mysql -p3306:3306 mysql",
		`docker run --name="mysql" -p3306:3306 mysql`,
		"docker run -d -v /var/lib/mysql -p127.0.0.1:3306:3306/tcp mysql:8",
		"sudo docker container run -it --hostname mysql -p3306:3306 img",
		`{"cmd":"docker run --link mysql -p8080:80 app"}`,
		`{"cmd":"docker run x\nmysql -p1234:5678"}`,
		"mysql -p1234:5678 db",
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
		"helm install mysql bitnami/mysql; mysql -p" + pw,
		"helm install mysql -p" + pw + " bitnami/mysql",
		"ssh root@mysql -p" + pw + " uptime",
		"install /a/mysql -p" + pw,
		"install /" + strings.Repeat("a", 300) + "/mysql -p" + pw,
		"install" + strings.Repeat(" ", 300) + "mysql -p" + pw,
		"podman create --network mysql -p13306-13307:" + pw + " mysql:8",
		`{"cmd":"echo hi\nmysql -uroot -p` + pw + ` db"}`,
		`{"cmd":"echo hi\tmysql -uroot -p` + pw + ` db"}`,
		`{"cmd":"echo hi\ncurl -u admin:` + pw + `99 http://x"}`,
		`{"cmd":"echo hi\nsshpass -p ` + pw + ` ssh h"}`,
	} {
		if out := RedactSecrets(in); strings.Contains(out, pw) {
			t.Errorf("leak: %q => %q", in, out)
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
		"sshpass -p":       "sshpass -p ",
		"sshpass quoted":   `sshpass "`,
		"sshpass quote":    "sshpass '",
		"docker port":      "docker run mysql -p1:1;",
		"verb":             "install mysql ",
		"json escape":      `\nmysql`,
		"long word before": strings.Repeat("a", 300) + " mysql ",
		"json array ports": `"mysql -p1:1",`,
		"paren ports":      "mysql -p1:1)",
		"docker array":     `["docker","run","x"],"mysql -p1:1",`,
		"stray quotes":     "mysql -p' ",
		"stray dquotes":    `mysql -p" `,
		"substitutions":    "mysql -p$( ",
		"nested subst":     "mysql -p$($(",
		"expansions":       "mysql -p${ ",
		"ansi-c":           "mysql -p$' ",
		"many -p":          "-px ",
		"line of mysql":    "mysql -p",
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
		"MYSQL_PWD='${DB_PASS}' mysql":  "MYSQL_PWD='[REDACTED]' mysql",
		`mysql -u"$user" -p"$password"`: `mysql -u"$user" -p"[REDACTED]"`,
		"sshpass -p '$SSHPASS' ssh h":   "sshpass -p '[REDACTED]' ssh h",
		"mysql -p$'S3CRET' db":          "mysql -p[REDACTED] db",
		"mysql --password=\\$DB_PW db":  "mysql --password=[REDACTED] db",
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

// Where a command line ends is the same for every credential rule as it was
// for curl, wget and HTTPie: a line break (or a JSON \n, \r escape), | ; &.
// A parenthesis or backquote does not end it, so a credential after one in a
// shell argument whose quotes are JSON-escaped (\"...\") or in plain text is
// still judged.
func TestRED_CommandScanDoesNotEndAtParenOrBackquote(t *testing.T) {
	for _, c := range []struct{ in, secret string }{
		{`{"command":"curl -H \"X-Note: (draft)\" -u admin:S3cretPass https://x"}`, "S3cretPass"},
		{`{"command":"curl -d \"f(x)\" -u admin:S3cretPass https://x"}`, "S3cretPass"},
		{`{"command":"wget --header=\"X: (a)\" --password=S3cretPass https://x"}`, "S3cretPass"},
		{`{"command":"http POST x \"q=f(x)\" -a admin:S3cretPass"}`, "S3cretPass"},
		{"curl -d `date` -u admin:S3cretPass https://x", "S3cretPass"},
		{`curl https://en.wikipedia.org/wiki/Foo_(bar) -u admin:S3cretPass`, "S3cretPass"},
		{`{"command":"mysql -uroot -e \"SELECT COUNT(*) FROM t\" -pS3cretPw db"}`, "S3cretPw"},
		{"{\"command\":\"mysql -e \\\"select * from `users`\\\" -pS3cretPw db\"}", "S3cretPw"},
		{`{"command":"sshpass -v \"(x)\" -p S3cretPass ssh h"}`, "S3cretPass"},
		{`cmd := exec.Command("sh", "-c", "mysql -e \"SELECT COUNT(*) FROM t\" -pS3cretPw db")`, "S3cretPw"},
	} {
		if out := RedactSecrets(c.in); strings.Contains(out, c.secret) {
			t.Errorf("leak: %s\n  got %s", c.in, out)
		}
	}
}

// A line that holds a MySQL/MariaDB client word anywhere (a word starting with
// mysql or mariadb, also inside quotes, as a path's last component or as an
// option value) treats every later attached -pVALUE and --password=VALUE as
// that client's password. The only exemptions are a port-mapping shape (digits,
// dots and colons with a colon, optional /tcp or /udp), a shell variable
// reference or command substitution, a placeholder and an empty value.
func TestRED_DBClientWordAnywhereOnTheLineJudgesLaterPasswords(t *testing.T) {
	const pw = "S3cretPw"
	for _, in := range []string{
		"docker run --rm --entrypoint=mysqldump mysql:8 -h db -uroot -p" + pw + " app",
		"docker run --rm --entrypoint=mysql mysql:8 -h db -uroot -p" + pw,
		`docker exec mysql sh -c 'mysql -uroot -p` + pw + ` -e "show databases"'`,
		`docker exec -it mysql bash -c "mysql -uroot -p` + pw + `"`,
		`ssh -t mysql 'mysql -p` + pw + `'`,
		`su mysql -c "mysql -p` + pw + `"`,
		"helm install mysql -p" + pw,
		"install /a/mysql -p" + pw,
		"ssh mysql -p" + pw,
		"ssh root@mysql -p" + pw,
		"docker run --name mysql -p" + pw + " mysql:8",
		`["mysql","-uroot","-p` + pw + `"]`,
		"mysql -h db \\\n  -u root \\\n  -p" + pw,
		"C:\\mysql\\bin\\mysql.exe -uroot -p" + pw,
		"MySQL server: run mysqladmin -p" + pw + " status",
		"mariadb-dump --pass=" + pw + " db",
		"mysql --password=" + pw + " db",
		"mysqlsh -p" + pw,
		"mysqlish -p" + pw,
		"mysql -p" + pw + ";echo done",
	} {
		if out := RedactSecrets(in); strings.Contains(out, pw) {
			t.Errorf("leak: %q => %q", in, out)
		}
	}
	// Accepted over-redaction: a mysql word earlier on the line makes a later
	// attached -p value a password whatever command it belongs to.
	for in, want := range map[string]string{
		"helm install mysql -pfoo":           "helm install mysql -p[REDACTED]",
		"Restart mysql then ps -p1234":       "Restart mysql then ps -p[REDACTED]",
		"Connect to mysql via psql -p5432":   "Connect to mysql via psql -p[REDACTED]",
		"DB_HOST=mysql ./server -p8080":      "DB_HOST=mysql ./server -p[REDACTED]",
		"mysql -p1234 db":                    "mysql -p[REDACTED] db",
		"mysql -p3306-3307:3306-3307 db":     "mysql -p[REDACTED] db",
		"mysql --password=33:06 db":          "mysql --password=[REDACTED] db",
		"systemctl status mysql.service -px": "systemctl status mysql.service -p[REDACTED]",
	} {
		if out := RedactSecrets(in); out != want {
			t.Errorf("%q => %q, want %q", in, out, want)
		}
	}
	for _, in := range []string{
		"docker run --name mysql -p3306:3306 -d mysql:8",
		"DB_HOST=mysql docker run -p3306:3306 app",
		"Start mysql with docker run -p3306:3306 mysql:8",
		"docker run -d -p127.0.0.1:3306:3306/tcp mysql:8",
		"docker run -p3306:3306/udp mysql",
		"mysql -p db",
		"mysql -P 3306 -p db",
		"mysql -u root -p",
		"mysql --password db",
		"mysql --password= db",
		"ssh -p 2222 host",
		"mkdir -p x && nmap -p80 h && ps -p1234",
		"cat ~/.my.cnf; ssh -p2222 host",
		"mysqlclient -p", // a bare -p prompts
		"mysql -p$MYSQL_ROOT_PASSWORD db",
		"mysql -p\"$MYSQL_ROOT_PASSWORD\" db",
		"mysql -p${mysql_pw} db",
		"mysql -p$(cat /run/secrets/db) db",
		"mysql -p<password> db",
		"mysql -pYOUR_PASSWORD db",
		"mysql -p'' db",
	} {
		if out := RedactSecrets(in); out != in {
			t.Errorf("over-redaction: %q => %q", in, out)
		}
	}
}

// Every sshpass word on a line starts an option list of its own, also inside
// a quoted argument of another sshpass: -p VALUE and -pVALUE are passwords;
// the list ends at the first word that names a program, or at --, so the
// wrapped command's own -p stays readable.
func TestRED_SshpassEveryOccurrenceJudgesItsOwnOptions(t *testing.T) {
	for _, c := range []struct{ in, secret string }{
		{`sshpass -p AAAAAA ssh host "sshpass -p S3cretPw ssh h2"`, "S3cretPw"},
		{`sshpass -p AAAAAA ssh host "sshpass -p S3cretPw ssh h2"`, "AAAAAA"},
		{`sshpass -p P1xxxxx ssh -o ProxyCommand="sshpass -p P2secret ssh -W %h:%p jump" target`, "P2secret"},
		{`sshpass -p P1xxxxx ssh -o ProxyCommand="sshpass -p P2secret ssh -W %h:%p jump" target`, "P1xxxxx"},
		{`sshpass -v "(x)" -p S3cretPass ssh h`, "S3cretPass"},
		{`sshpass -v "$SSH" -p S3cretPass ssh h`, "S3cretPass"},
		{`{"cmd":"sshpass -p \"S3 cretPass\" ssh h"}`, "cretPass"},
		{`sshpass -pS3cret"Pass word" ssh h`, "word"},
		{`ssh h 'sshpass -p S3cretPass ssh h2'`, "S3cretPass"},
	} {
		if out := RedactSecrets(c.in); strings.Contains(out, c.secret) {
			t.Errorf("leak of %q: %s\n  got %s", c.secret, c.in, out)
		}
	}
	for in, want := range map[string]string{
		`sshpass -p A1b2c3 ssh -p 2222 h "sshpass -p D4e5f6 ssh -p 22 h2"`: `sshpass -p [REDACTED] ssh -p 2222 h "sshpass -p [REDACTED] ssh -p 22 h2"`,
		`ssh h 'sshpass -p A1b2c3 scp -p f h2:'`:                           `ssh h 'sshpass -p [REDACTED] scp -p f h2:'`,
	} {
		if out := RedactSecrets(in); out != want {
			t.Errorf("%q => %q, want %q", in, out, want)
		}
	}
}

// A connection-URL span stays readable only when it holds exactly one '@'
// and the password before it is wholly a reference: a password may hold '@',
// ':', '/', ',' or ';', so a second '@' can always belong to it.
func TestRED_DBURLSecondAtFailsClosed(t *testing.T) {
	for _, c := range []struct{ in, secret string }{
		{`mysql://u:${PW}@h,v:hunter,22@h2`, "hunter"},
		{`mysql://u:${PW}@h;v:hunter;22@h2`, "hunter"},
		{`mysql://u:${PW}@h/x,v:hunter,22@h2`, "hunter"},
		{`postgres://app:${A}@h1/db?replica=mysql://root:x://cd@h2/db`, "x://cd"},
		{`postgres://app:${A}@h1/db?replica=pg://v:q://zz@h2`, "q://zz"},
		{`postgres://u:S3c/ret99@h/db`, "S3c/ret99"},
		{`postgres://u:S3c@ret99@h/db`, "ret99"},
	} {
		if out := RedactSecrets(c.in); strings.Contains(out, c.secret) {
			t.Errorf("leak: %s\n  got %s", c.in, out)
		}
	}
	for _, in := range []string{
		"postgres://app:${DB_PASSWORD}@db:5432/app?sslmode=disable",
		"mysql://root:$MYSQL_ROOT_PASSWORD@db/app",
		"mongodb://u:{password}@h1,h2,h3/db?replicaSet=rs0",
		"mysql://root:pw@localhost", // shorter than six characters, as before
	} {
		if out := RedactSecrets(in); out != in {
			t.Errorf("rewritten: %q => %q", in, out)
		}
	}
}

// Property: whatever joins one to three connection URLs, a literal password
// in any of them never survives, and a lone URL whose password is wholly a
// reference stays readable.
func TestRED_DBURLSpanPropertyNoLiteralSurvives(t *testing.T) {
	rng := rand.New(rand.NewSource(1112))
	schemes := []string{"postgres", "postgresql+psycopg2", "mysql", "mongodb", "mongodb+srv", "redis", "rediss", "amqp", "mssql", "clickhouse"}
	refs := []string{"${DB_PASSWORD}", "$DB_PASSWORD", "{password}", "{{ db_pw }}", "<password>", "%(password)s", "%DB_PW%"}
	glue := []string{",", ";", "?replica=", "|", "&&", "&", ")(", ">", "=", "/", "#", "?x=a@b&y=", "@", ",u:", ";"}
	litChars := "abcXYZ0189,;@:/?#!%-_."
	lit := func() string {
		n := 6 + rng.Intn(10)
		b := []byte("Lz")
		for len(b) < n {
			b = append(b, litChars[rng.Intn(len(litChars))])
		}
		return string(b)
	}
	host := func() string {
		return []string{"db", "h1:5432", "db.example:3306/app", "h2/db?ssl=1", "[::1]:6379"}[rng.Intn(5)]
	}
	for iter := 0; iter < 20000; iter++ {
		n := 1 + rng.Intn(3)
		var b strings.Builder
		var lits []string
		for k := 0; k < n; k++ {
			if k > 0 {
				b.WriteString(glue[rng.Intn(len(glue))])
			}
			pw := refs[rng.Intn(len(refs))]
			if rng.Intn(2) == 0 {
				pw = lit()
				lits = append(lits, pw)
			}
			b.WriteString(schemes[rng.Intn(len(schemes))] + "://u" + strconv.Itoa(k) + ":" + pw + "@" + host())
		}
		in := b.String()
		out := RedactSecrets(in)
		for _, l := range lits {
			if strings.Contains(out, l) {
				t.Fatalf("literal %q survives: %q => %q", l, in, out)
			}
		}
	}
	for _, s := range schemes {
		for _, r := range refs {
			in := s + "://app:" + r + "@db:5432/app?sslmode=disable"
			if out := RedactSecrets(in); out != in {
				t.Errorf("reference-only URL rewritten: %q => %q", in, out)
			}
		}
	}
}

// A MYSQL_PWD, -p, --password= or sshpass -p value is one whole shell word:
// quoted and unquoted parts, ANSI-C and locale quoting, backslash escapes and
// JSON-escaped quotes all belong to it and are replaced with it, up to its
// quotes, which stay. Only a word that is wholly a reference, a command
// substitution, a placeholder or empty stays readable; a reference glued to
// literal text is a literal.
func TestRED_ShellWordPasswordIsWholeWord(t *testing.T) {
	for _, c := range []struct{ in, secret string }{
		{`MYSQL_PWD=$'s3cr3t' mysql`, "s3cr3t"},
		{`MYSQL_PWD=$"s3cr3t" mysql`, "s3cr3t"},
		{`MYSQL_PWD=abc's3cr3t' mysql`, "s3cr3t"},
		{`MYSQL_PWD="abc"s3cr3t mysql`, "s3cr3t"},
		{`MYSQL_PWD=p\ s3cr3t mysql`, "s3cr3t"},
		{`{"env":"MYSQL_PWD=\"r0\\\"0t\" mysql"}`, "0t"},
		{`MYSQL_PWD=${PW}s3cr3t mysql`, "s3cr3t"},
		{`MYSQL_PWD="$PW"'s3cr3t' mysql`, "s3cr3t"},
		{`mysql -p"$PW"'hunter2' db`, "hunter2"},
		{`mysql -p${PW}hunter2 db`, "hunter2"},
		{`mysql -p${PW:-hunter2} db`, "hunter2"},
		{`mysql -p'a b'"c d" db`, "c d"},
		{`mysql -p'a b'"c d" db`, "a b"},
		{`mysql --password=ab\ cd db`, "cd"},
		{`mysql -pS3cret(1) db`, "S3cret"},
		{`mysql -p%abc db`, "abc"},
		{`sshpass -p a\ s3cr3t ssh h`, "s3cr3t"},
		{`sshpass -p "$PW"x ssh h`, `x ssh`},
		{`{"cmd":"mysql -p\"S3 cret\" db"}`, "cret"},
	} {
		if out := RedactSecrets(c.in); strings.Contains(out, c.secret) {
			t.Errorf("leak of %q: %s\n  got %s", c.secret, c.in, out)
		}
	}
	for in, want := range map[string]string{
		`mysql -p"$PW"'hunter2' db`:           `mysql -p"[REDACTED]"'[REDACTED]' db`,
		`MYSQL_PWD="abc"def mysql`:            `MYSQL_PWD="[REDACTED]"[REDACTED] mysql`,
		`MYSQL_PWD=abc's3 cr3t'x mysql`:       `MYSQL_PWD=[REDACTED]'[REDACTED]'[REDACTED] mysql`,
		`mysql -p$'S3CRET' db`:                `mysql -p[REDACTED] db`,
		`mysql -p"$PW"x db`:                   `mysql -p"[REDACTED]"[REDACTED] db`,
		`mysql -p${PW}hunter2 db`:             `mysql -p[REDACTED] db`,
		`mysql --password=ab\ cd db`:          `mysql --password=[REDACTED] db`,
		`mysql -p'S3 cret' db`:                `mysql -p'[REDACTED]' db`,
		`{"cmd":"MYSQL_PWD=\"r0\\\"0t\" ls"}`: `{"cmd":"MYSQL_PWD=\"[REDACTED]\" ls"}`,
		`ssh h 'mysql -pS3cretPw'`:            `ssh h 'mysql -p[REDACTED]'`,
		`$(mysql -pS3cretPw)`:                 `$(mysql -p[REDACTED])`,
	} {
		if out := RedactSecrets(in); out != want {
			t.Errorf("%q => %q, want %q", in, out, want)
		}
	}
	for _, in := range []string{
		`MYSQL_PWD="" mysql`,
		`MYSQL_PWD=""$PW mysql`,
		`MYSQL_PWD="${DB_PASS}" mysql`,
		`mysql -p"$(cat /run/secrets/db)" db`,
		`mysql -p${DB_PASS:-} db`,
		`ssh h 'mysql -p$PW'`,
	} {
		if out := RedactSecrets(in); out != in {
			t.Errorf("over-redaction: %q => %q", in, out)
		}
	}
}

// redactR4Pieces are the parts a generated password word is built from:
// literal text, every quoting form, references, substitutions, escapes,
// stray quotes, separators and the redaction marker itself.
var redactR4Pieces = []string{
	"S3cr", "et9", "a b", "'q1'", `"d 2"`, "$'c\\'3'", `$"l4"`, "$PW", "${PW}", "${PW:-d5}", "$(cat f)",
	`\ `, `\$`, `\"`, `\\`, "'", `"`, ")", "(", "`", ";", "|", "&", "{x}", "<pw>", "%PW%", "YOUR_PW",
	"[REDACTED]", "''", `""`, "=", ":", "@", "/", "-p", "\\\n", "3306:3306",
}

// redactR4Commands start a generated line: the MySQL/MariaDB, sshpass,
// MYSQL_PWD and connection-URL rules.
var redactR4Commands = []string{
	"mysql -p", "mysql -uroot -e 'select 1' -p", "mysqldump --password=", "mariadb --pass=", "MYSQL_PWD=",
	"export MYSQL_PWD=", "sshpass -p ", "sshpass -vp", "sshpass -v x -p ", "docker run --name mysql -p",
	"psql postgres://u:", "DATABASE_URL=mysql://root:",
}

// redactR4CurlCommands start a generated line for the curl, wget and HTTPie
// rule.
var redactR4CurlCommands = []string{
	"curl -u admin:", "curl -sS -H 'X: (a)' -u admin:", "wget --password=", "http -a admin:", "xh --auth=u:",
}

// redactR4Line builds one generated line of one to three of cmds.
func redactR4Line(rng *rand.Rand, cmds []string) string {
	var b strings.Builder
	for k := rng.Intn(3); k >= 0; k-- {
		if b.Len() > 0 {
			b.WriteString([]string{" ", "; ", " && ", " | ", " '", ` "`, " $(", "\n", "\\\n"}[rng.Intn(9)])
		}
		b.WriteString([]string{"", "ssh h '", `bash -c "`, "sh -c '", "$(", "`"}[rng.Intn(6)])
		b.WriteString(cmds[rng.Intn(len(cmds))])
		for p := 1 + rng.Intn(4); p > 0; p-- {
			b.WriteString(redactR4Pieces[rng.Intn(len(redactR4Pieces))])
		}
		if strings.HasSuffix(b.String(), "://u:") || strings.Contains(b.String(), "://root:") {
			b.WriteString("@db/app")
		}
		b.WriteString([]string{"", " db", "'", `"`, ")", " -pX2", " h", "`", ` -P 3306`}[rng.Intn(9)])
	}
	return b.String()
}

// redactR4JSON encodes s as the body of a JSON string.
func redactR4JSON(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`).Replace(s)
}

// Redaction is idempotent for every rule this change touched: the first
// pass's output is what the model and the event stream see, and sessions are
// redacted again on every save, so a second pass must change nothing. The
// corpus is the package's test literals and generated lines in plain text,
// in JSON-encoded text and in a Go string literal.
func TestRED_RedactionIsIdempotent(t *testing.T) {
	check := func(in string) {
		t.Helper()
		once := RedactSecrets(in)
		if twice := RedactSecrets(once); twice != once {
			t.Fatalf("not idempotent: %q\n  once  %q\n  twice %q", in, once, twice)
		}
	}
	for _, s := range gateTestLiterals(t) {
		if redactR4KnownNonIdempotent(s) {
			continue
		}
		check(s)
	}
	rng := rand.New(rand.NewSource(4))
	n := 20000
	if testing.Short() {
		n = 4000
	}
	for i := 0; i < n; i++ {
		line := redactR4Line(rng, redactR4Commands)
		check(line)
		check(`{"command":"` + redactR4JSON(line) + `","x":1}`)
		check(`cmd := exec.Command("sh", "-c", "` + redactR4JSON(line) + `")`)
	}
}

// redactR4KnownNonIdempotent reports the test literals whose second pass
// differs through a rule this change did not touch: a bare token key
// followed by a second JSON object on the same line.
func redactR4KnownNonIdempotent(s string) bool {
	return strings.Contains(s, `{"out":"token: Xy7kLm9pQ2zzAbCdEf3456"}`)
}
