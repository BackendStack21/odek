package redact

import (
	"strings"
	"testing"
)

// assignPW is an obviously fake fixture password.
const assignPW = "S3cretPw9x"

// A NAME=value word whose value names a client program is a client word:
// unit files, supervisor programs, container health checks and shell
// variables carry a command line behind the '='.
func TestRED_MysqlPasswordBehindAssignedClientCommand(t *testing.T) {
	for _, in := range []string{
		// The value is a path, in any directory, and an option follows.
		"ExecStart=/usr/bin/mysqldump -uroot -p" + assignPW + " db",
		`ExecStartPre=/usr/bin/mysql -uroot -p` + assignPW + ` -e "select 1"`,
		"ExecStart=-/usr/bin/mysqldump -uroot -p" + assignPW + " db",
		"ExecStart=@/usr/bin/mysqldump -uroot -p" + assignPW + " db",
		"ExecStart=+/usr/bin/mysqldump -uroot -p" + assignPW + " db",
		"ExecStart=!/usr/bin/mysqldump -uroot -p" + assignPW + " db",
		"ExecStart=-mysqldump -uroot -p" + assignPW + " db",
		"command=/usr/bin/mysqldump -uroot -p" + assignPW + " db",
		"CMD=/usr/bin/mysql -uroot -p" + assignPW,
		"DUMP=/usr/bin/mysqldump -uroot -p" + assignPW,
		"DUMP=/opt/db/tools/mysqldump -uroot -p" + assignPW,
		`DUMP=C:\tools\mysqldump.exe -uroot -p` + assignPW,
		"DUMP=/usr/bin/mariadb-dump --password=" + assignPW + " db",
		// A subcommand or a database name stands between the client and its
		// password option.
		"ExecStart=/usr/bin/mysqladmin ping -p" + assignPW,
		"ExecStart=/usr/bin/mysqladmin ping -uroot -p" + assignPW,
		"ExecStart=-/usr/bin/mysqladmin ping -uroot -p" + assignPW,
		"healthcheck.test=mysqladmin ping -p" + assignPW,
		"--health-cmd=mysqladmin ping -uroot -p" + assignPW,
		"docker run --health-cmd=mysqladmin ping -uroot -p" + assignPW + " mysql:8",
		"docker run --health-cmd=/usr/bin/mysqladmin ping -p" + assignPW + " mysql:8",
		"docker run --health-cmd /opt/db/mysqladmin ping -p" + assignPW + " img",
		"CHECK=mysqladmin status -p" + assignPW,
		"CHECK=mysqladmin status --password=" + assignPW,
		"DUMP=mysqldump shop orders -p" + assignPW,
		"export DUMP=mysqldump shop -p" + assignPW,
	} {
		out := RedactSecrets(in)
		if strings.Contains(out, assignPW) {
			t.Errorf("password readable\n in =%s\n out=%s", in, out)
		}
		if again := RedactSecrets(out); again != out {
			t.Errorf("not stable\n once =%s\n twice=%s", out, again)
		}
	}
}

// An assignment that only names an engine, a host or a directory, with no
// client command behind it, is left alone.
func TestRED_MysqlAssignedNameWithoutClientCommandUnchanged(t *testing.T) {
	for _, in := range []string{
		"DB_HOST=mysql ./server -p8080",
		"DB_HOST=mysql /usr/local/bin/server -p8080",
		"DB_ENGINE=mysql",
		"DB_ENGINE=mysql DB_PORT=3306 server -p8080",
		"ENGINE=mysql docker run -p3306:3306 app",
		"ENGINE=mysql; server -p8080",
		"ENGINE=mysql && server -p8080",
		"ENGINE=mysql sh -c 'server -p8080'",
		"MYSQL_HOME=/etc/mysql server -p8080",
		"DATADIR=/var/lib/mysql server -p8080",
		"docker run -e DB_HOST=mysql app -pfoo",
		"helm install db chart --set engine=mysql -pfoo",
		"tar --exclude=mysql -cpf b.tar .",
		"java -jar app.jar --db=mysql -port 8080",
		"python manage.py dbshell --database=mysql -pfoo",
		"docker run --health-interval=5s app -pfoo",
	} {
		if out := RedactSecrets(in); out != in {
			t.Errorf("changed\n in =%s\n out=%s", in, out)
		}
	}
}

// A bare client name behind NAME= governs a later password option of its
// simple command whatever stands in between, so an engine name before another
// program's -p is redacted on purpose.
func TestRED_MysqlAssignedBareNameGovernsLaterPasswordOption(t *testing.T) {
	for in, want := range map[string]string{
		"DB_HOST=mysql server -p8080":    "DB_HOST=mysql server -p[REDACTED]",
		"DB_HOST=mysql server -v -p8080": "DB_HOST=mysql server -v -p[REDACTED]",
		"DB_HOST=mysql server -v":        "DB_HOST=mysql server -v",
		// A client path outside bin and sbin behind an argument is not read
		// as a client.
		"DUMP=/opt/db/mysqldump shop -p" + assignPW: "DUMP=/opt/db/mysqldump shop -p" + assignPW,
	} {
		if out := RedactSecrets(in); out != want {
			t.Errorf("in  =%s\n out =%s\n want=%s", in, out, want)
		}
	}
}
