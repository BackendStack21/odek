package redact

import (
	"strings"
	"testing"
	"time"
)

// The command-line password of the MySQL/MariaDB clients (-pPASSWORD,
// attached, also shell-quoted) and of sshpass
// (-p PASSWORD / -pPASSWORD) is a credential in exactly the way curl's
// -u USER:PASSWORD is. The curl fix scoped command-line credential parsing to
// curl, wget and HTTPie/xh, so these passwords reach session files (tool-call
// arguments are redacted per string value with this same function), audit and
// runtime logs verbatim — both when the agent writes such a command and when
// a tool prints one (ps output, shell history, CI logs). Port flags of other
// tools (docker -p 8080:80, ssh -p 2222, mysql -P 3306) and a bare
// prompting -p must stay readable.
func TestRED_MySQLAndSshpassCommandLinePasswordsLeak(t *testing.T) {
	const pw = "S3cr3tPassw0rd"
	var leaked []string
	for _, in := range []string{
		`mysql -u root -p` + pw + ` mydb`,
		`mysql -h db -uroot -p` + pw + ` -e "select 1"`,
		`mysqldump -uroot -p` + pw + ` --all-databases > dump.sql`,
		`mysqladmin -u root -p'` + pw + `!' status`,
		`mariadb -u root -p` + pw,
		`sshpass -p '` + pw + `' ssh deploy@host`,
		`sshpass -p` + pw + ` scp f deploy@host:`,
		`{"command":"mysql -u root -p` + pw + ` -e \"show databases\""}`,
	} {
		if out := RedactSecrets(in); strings.Contains(out, pw) {
			leaked = append(leaked, out)
		}
	}
	for _, keep := range []string{
		`mysql -u root -p mydb`,
		`mysql -u root -P 3306 -h db mydb`,
		`docker run -p 8080:80 nginx`,
		`ssh -p 2222 user@host`,
	} {
		if out := RedactSecrets(keep); out != keep {
			t.Fatalf("over-redaction of %q: %q", keep, out)
		}
	}
	if len(leaked) > 0 {
		t.Fatalf("command-line password not redacted (%d of 8):\n  %s", len(leaked), strings.Join(leaked, "\n  "))
	}
}

// A connection URL whose password is a variable or template reference holds
// no secret: the credential-name rule skips ${VAR} / $VAR references whole
// (referenceValue), but the database-URL rule has no such judge and replaces
// the entire URL — host, port, database and query — with [REDACTED]. Every
// docker-compose file, .env template and connection-string builder in a
// workspace then reads back corrupted through read_file / shell output, and a
// patch or write_file round-trip writes the literal [REDACTED] into the file.
func TestRED_DBURLWithReferencePasswordRedactedWhole(t *testing.T) {
	// Control: a literal password in the same position is still a secret.
	if out := RedactSecrets(`DATABASE_URL=postgres://app:Tr0ub4dor3xyz@db:5432/app`); strings.Contains(out, "Tr0ub4dor3xyz") {
		t.Fatalf("control changed: literal DB password not redacted: %q", out)
	}
	var corrupted []string
	for _, in := range []string{
		`DATABASE_URL: postgres://app:${POSTGRES_PASSWORD}@postgres:5432/app?sslmode=disable`,
		`DATABASE_URL=postgres://user:$DB_PASSWORD@db:5432/app`,
		`MONGO_URL=mongodb://${MONGO_USER}:${MONGO_PASSWORD}@mongo:27017/app`,
		`REDIS_URL=redis://:${REDIS_PASSWORD}@redis:6379/0`,
		`dsn = f"postgresql://{user}:{password}@{host}:{port}/{db}"`,
	} {
		if out := RedactSecrets(in); out != in {
			corrupted = append(corrupted, in+"  ->  "+out)
		}
	}
	if len(corrupted) > 0 {
		t.Fatalf("connection URL with a reference password was rewritten (%d of 5):\n  %s", len(corrupted), strings.Join(corrupted, "\n  "))
	}
}

// Leak corpus: every spelling of a database-client or sshpass password flag.
func TestRED_DBClientPasswordFlagLeakCorpus(t *testing.T) {
	const pw = "S3cr3tPassw0rd"
	for _, in := range []string{
		`mysql --password=` + pw + ` -u root`,
		`mysql --user=root --password='` + pw + `' db`,
		`mysql --pass=` + pw,
		`/usr/bin/mysql -u root -p"` + pw + `" db`,
		`MYSQL mysql -uroot -p` + pw,
		`mariadb-dump -u root -p` + pw + ` db`,
		`mariadb-admin -p` + pw + ` status`,
		`mysqlimport -p` + pw + ` db f.txt`,
		`mysqlshow -p` + pw,
		`mysqlcheck -p` + pw + ` --all-databases`,
		`sshpass -p"` + pw + `" ssh h`,
		`sshpass -p ` + pw + ` rsync -a f h:`,
		`echo hi; mysql -u root -p` + pw + ` | head`,
		`PGPASSWORD=` + pw + ` psql -h db`,
		`mysql -ps3cr3t`,
	} {
		out := RedactSecrets(in)
		if strings.Contains(out, pw) || strings.Contains(out, "s3cr3t") {
			t.Errorf("leak: %q -> %q", in, out)
		}
	}
	if out := RedactSecrets(`mysql -u root -p` + pw + ` mydb`); out != `mysql -u root -p[REDACTED] mydb` {
		t.Errorf("only the password may be replaced: %q", out)
	}
}

// Over-redaction corpus: -p is a common flag in other tools and a bare -p
// prompts.
func TestRED_DBClientPasswordFlagKeepsOtherFlags(t *testing.T) {
	for _, keep := range []string{
		`mkdir -p /srv/app/data`,
		`ssh -p 2222 user@host`,
		`docker run -p 80:80 nginx`,
		`mysql -p`,
		`mysql -u root -p mydb`,
		`mysql -P 3306 -h db`,
		`mysql --port=3306 --host=db`,
		`mysqldump -u root -p --all-databases`,
		`cp -p a b`,
		`ps -p 123`,
		`nc -p 8080 host`,
		`tar -p -xf a.tar`,
		`sshpass -P assword ssh h`,
		`sshpass -e ssh h`,
		`sshpass -p`,
		`mysql -p$MYSQL_PWD db`,
		`mysql --password=$DB_PW`,
		`mysqlish -pS3cr3tPassw0rd`,
		`scp -P 22 f h:`,
		`mysql --password db`,
		`mysql --password= db`,
		`mysql --pa=Longvalue99 db`,
		`mysql --protocol=tcp db`,
		`sshpass -v ssh h`,
	} {
		if out := RedactSecrets(keep); out != keep {
			t.Errorf("over-redaction of %q: %q", keep, out)
		}
	}
}

func TestRED_DBClientPasswordFlagScalesLinearly(t *testing.T) {
	for name, gen := range map[string]func(n int) string{
		"mysql repeated":   func(n int) string { return strings.Repeat("mysql -p", n/8) },
		"mysql open quote": func(n int) string { return strings.Repeat("mysql -p'", n/9) },
		"sshpass repeated": func(n int) string { return strings.Repeat("sshpass -p ", n/11) },
		"mariadb dash":     func(n int) string { return strings.Repeat("mariadb-", n/8) },
	} {
		base := 16 << 10
		small, large := finalRedactTime(gen(base), 2), finalRedactTime(gen(4*base), 1)
		if small < 2*time.Millisecond {
			small = 2 * time.Millisecond
		}
		if large > 12*small {
			t.Errorf("%s: super-linear: %v then %v", name, small, large)
		}
	}
}

// A pure reference password is left readable; a literal password is still
// redacted, also when it only contains reference characters mixed with
// literal ones.
func TestRED_DBURLPasswordReferenceCorpus(t *testing.T) {
	for _, in := range []string{
		`postgres://app:{password}@db:5432/app`,
		`postgres://app:<password>@db:5432/app`,
		`mysql://app:%(password)s@db/app`,
		`postgres://app:${DB_PASSWORD}@db/app?sslmode=disable`,
		`amqp://guest:{{ .Values.password }}@mq:5672/`,
	} {
		if out := RedactSecrets(in); out != in {
			t.Errorf("reference password rewritten: %q -> %q", in, out)
		}
	}
	for _, in := range []string{
		`postgres://app:p$ssw0rdX@db:5432/app`,
		`postgres://app:$ecret123@db:5432/app`,
		`postgres://app:${X}abcdef123@db:5432/app`,
		`postgres://app:${X:-hunter2xyz}@db:5432/app`,
		`postgres://app:ab{cd}ef12@db:5432/app`,
		`postgres://app:{secret}tail99@db:5432/app`,
		`mysql://app:Tr0ub4dor3xyz@db/app`,
	} {
		out := RedactSecrets(in)
		if strings.Contains(out, "@db") || strings.Contains(out, "db:5432") {
			t.Errorf("literal password not redacted: %q -> %q", in, out)
		}
	}
}

func TestRED_DBURLJudgeFailsClosedOnMalformedInput(t *testing.T) {
	for _, in := range []string{"no-scheme", "postgres://nocolon@host"} {
		if !dbURLHoldsSecret(in) {
			t.Errorf("%q treated as reference", in)
		}
	}
}

// Every user:password pair of a connection-URL span is judged: a reference
// password first in a comma- or semicolon-joined list never hides a literal
// one after it, while a list of references alone stays readable.
func TestRED_DBURLListJudgesEveryPassword(t *testing.T) {
	const lit = "Tr0ub4dor9xQ"
	for _, in := range []string{
		"postgres://u:${DB_PASS}@h1/db,postgres://u:" + lit + "@h2/db",
		"postgres://u:" + lit + "@h1/db,postgres://u:${DB_PASS}@h2/db",
		"postgres://u:${DB_PASS}@h1/db;postgres://u:" + lit + "@h2/db",
		"postgres://u:" + lit + "@h1/db;postgres://u:${DB_PASS}@h2/db",
		"mongodb://u:${P}@h1,u:" + lit + "@h2/db",
		"mongodb://u:" + lit + "@h1,u:${P}@h2/db",
		"redis://:${P}@h1:6379;redis://:" + lit + "@h2:6379",
		"postgres://u:${A}@h1:5432/db,postgres://u:${B}@h2:5432/db,postgres://u:" + lit + "@h3/db",
	} {
		if out := RedactSecrets(in); strings.Contains(out, lit) {
			t.Errorf("literal password leaked: %q => %q", in, out)
		}
	}
	for _, in := range []string{
		"postgres://u:${A}@h1/db,postgres://u:${B}@h2/db",
		"postgres://u:${A}@h1:5432/db;postgres://u:${B}@h2:5432/db",
		"mongodb://u:${A}@h1,u:${B}@h2/db",
	} {
		if out := RedactSecrets(in); out != in {
			t.Errorf("reference-only list rewritten: %q => %q", in, out)
		}
	}
}

func TestRED_DBURLListScalesLinearly(t *testing.T) {
	gen := func(n int) string { return strings.Repeat("postgres://u:${A}@h1/db,", n/25) + "x" }
	base := 16 << 10
	small, large := finalRedactTime(gen(base), 2), finalRedactTime(gen(4*base), 1)
	if small < 2*time.Millisecond {
		small = 2 * time.Millisecond
	}
	if large > 12*small {
		t.Errorf("super-linear: %v then %v", small, large)
	}
}
