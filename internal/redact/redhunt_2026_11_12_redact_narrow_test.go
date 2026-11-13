package redact

import (
	"strings"
	"testing"
)

// narrowPW is the fixture password of the corpora below; it is not a real
// credential.
const narrowPW = "S3cretPw"

// narrowUnchanged are commands that mention a MySQL/MariaDB name without
// running a client in the simple command that holds the -p word: a path, a
// host, a service, a package, an option value, or a client in another simple
// command. None holds a secret and each must come out as written.
var narrowUnchanged = []string{
	// Another simple command, after an unquoted separator.
	"systemctl stop mysql && tar -cpzf /backup/db.tgz /var/lib/mysql",
	"cd /var/lib/mysql && tar -xpf b.tar",
	"docker stop mysql && tar -C /v -cpf vol.tar .",
	"mysqldump db | ssh -Cp2222 backup 'cat > d.sql'",
	"mysqldump db | pv -petr > d.sql",
	"mysqldump db | pv -petr | gzip > d.sql.gz",
	"mysqldump --all-databases > all.sql; tar -cpzf all.tgz all.sql",
	"mysql -e 'select 1' || ps -p1234",
	"mysqladmin ping & nc -lp3306",
	"service mysql stop; rsync -ap /var/lib/mysql/ /mnt/backup/",
	"brew services stop mariadb && cp -Rp /opt/homebrew/var/mysql /tmp/mysql.bak",
	"mariadb-dump shop | ssh -p2222 backup 'cat > shop.sql'",
	"which mysql || apt-get install -y default-mysql-client -oDpkg::Options::=--force-confold",
	"mysql --version; python3 -m http.server -p8080",
	"pgrep mysql | xargs ps -p1",
	`sh -c "systemctl stop mysql && tar -cpzf /backup/db.tgz /var/lib/mysql"`,
	`ssh db1 'service mysql stop; tar -cpf /tmp/d.tar /var/lib/mysql'`,
	// The name is part of a longer word.
	"tar --exclude=mysql -cpf b.tar .",
	"cd mysql-server && patch -sp1 < x.diff",
	"ssh db-mysql-01 -p2222 uptime",
	"ssh mysql-primary.internal -p2222 uptime",
	"systemctl status mysql.service -pMainPID",
	"journalctl -u mysql.service -perr",
	"tar -tf mysql-backup.tar -px",
	"ffmpeg -i mysql-talk.mp4 -preset fast -pix_fmt yuv420p o.mp4",
	"openssl enc -in mysql.sql -pass env:KEY",
	"java -jar app.jar --db=mysql -port 8080",
	"gcc main.c -lmysqlclient -pthread",
	"apt-get install -y mysql-server -purge",
	"apt-get install -y mariadb-server mariadb-client -purge",
	"mariadb-" + strings.Repeat("x", 64) + " -pfoo",
	"dpkg -l mysql-common -pfoo",
	"docker pull mysql:8.4 -platform linux/amd64",
	"docker logs mysql-1 -pfoo",
	"kubectl logs mysql-0 -previous",
	"kubectl get pods -l app=mysql -pfoo",
	"docker run -e DB_HOST=mysql app -pfoo",
	"helm upgrade db bitnami/mysql -pfoo",
	"scp backup-mysql.sql.gz host:/tmp -pv",
	"gzip -dc mysql-2026-01-01.sql.gz -pfoo",
	"python manage.py dbshell --database=mysql -pfoo",
	// The name is a path that is an argument, not the command.
	"go test ./internal/mysql/... -cpuprofile cpu.out",
	"go test ./internal/mysql/... -run TestConn -cpuprofile cpu.out",
	"go test ./internal/mysql/ -parallel 4",
	"go test ./internal/mysql/ -parallel 4 -v",
	"find /var/lib/mysql -type f -print",
	"find /var/lib/mysql -name '*.ibd' -print | wc -l",
	"find /var/lib/mysql -perm 600",
	"find /var/lib/mysql -type f -perm 600",
	"find /var/lib/mysql -path x -prune -o -print0",
	"gcc -I/usr/include/mysql -pipe -pedantic x.c",
	"gcc -I/usr/include/mysql -pipe -pedantic udf.c",
	"cp -rp /var/lib/mysql /backup",
	"rsync -a /etc/mysql backup:/etc -pv",
	"chown -R mysql:mysql /var/lib/mysql -P",
	"ls -la /var/run/mysqld/ -p",
	"du -sh /var/lib/mysql /var/lib/mariadb -px",
	"tar -C /var/lib/mysql -cpf /backup/ib.tar .",
	"cat /etc/mysql/my.cnf /etc/mysql/conf.d/mysql -pn",
	"go build ./cmd/mysql -pkgdir /tmp/pkg",
	"vim /etc/mysql/mariadb.conf.d/50-server.cnf -p",
	"restorecon -R /var/lib/mysql -pv",
	"docker run -d -v /srv/mysql:/var/lib/mysql -p3306:3306 mysql:8",
	"docker cp db:/var/lib/mysql ./mysql-data -pfoo",
	"sudo -E tar -C /var/lib/mysql -cpf x.tar .",
	"sudo -n cp -a /var/lib/mysql /backup -px",
	"sudo --preserve-env rsync /var/lib/mysql /mnt -pv",
	"sudo -u root -g wheel tar -C /srv/mysql -xpf b.tar",
	"timeout 60 tar -C /var/lib/mysql -cpf x.tar .",
	"nice -n 19 tar -C /var/lib/mysql -cpf x.tar .",
	"xargs -n1 ls /var/lib/mysql -p",
	"echo sudo /var/lib/mysql -pfoo",
	"mysqldump db 2>dump.err; tar -cpf d.tar dump.err",
	"(cd /var/lib/mysql && ls) ; tar -cpf x.tar .",
	"(systemctl stop mysql) && tar -cpf x.tar .",
	// A cluster whose flags are not the client's own.
	"mysql db -Zpx",
	"mysqladmin status -dpx",
	// A bare -p prompts, -P is a port, a reference is not a password.
	"mysql -u root -p",
	"mysqldump -u root -p --all-databases",
	"mysql -P 3306 -h db",
	"mysql -p$MYSQL_ROOT_PASSWORD db",
	"mysql -sNp db",
	"mysql -p<password> db",
	"docker run --name mysql -p3306:3306 -d mysql:8",
}

// narrowRedacted are commands in which a client word and the password word
// stand in one simple command; "S3cretPw" must not survive.
var narrowRedacted = []string{
	"mysql -uroot -p" + narrowPW + " db",
	"mysql -sNp" + narrowPW + " -e 'select 1'",
	"mysqldump -Rp" + narrowPW + " db",
	"mysqldump -uroot -Rp" + narrowPW + " db",
	"mysqldump -RFQYadeyp" + narrowPW + " db",
	"mariadb-dump -Rp" + narrowPW + " db",
	"mysqladmin -fp" + narrowPW + " shutdown",
	"mysqlcheck -Ap" + narrowPW,
	"mariadb-check -Ap" + narrowPW,
	"mariadb-import --password=" + narrowPW + " db f.txt",
	"mysqlimport -Lp" + narrowPW + " db f.txt",
	"docker exec db mysql -uroot -p" + narrowPW,
	`docker exec -it mysql bash -c "mysql -uroot -p` + narrowPW + `"`,
	`docker exec mysql sh -c 'mysql -uroot -p` + narrowPW + ` -e "show databases"'`,
	`ssh -t mysql 'mysql -p` + narrowPW + `'`,
	`su mysql -c "mysql -p` + narrowPW + `"`,
	"kubectl exec pod -- mysql -p" + narrowPW,
	"kubectl exec pod -- /usr/bin/mysql -p" + narrowPW,
	"docker run --rm --entrypoint=mysqldump mysql:8 -h db -uroot -p" + narrowPW + " app",
	"docker run --rm --entrypoint mysqldump mysql:8 -h db -uroot -p" + narrowPW + " app",
	"docker run --rm --entrypoint /usr/bin/mysql mysql:8 -h db -uroot -p" + narrowPW,
	"docker run --rm --entrypoint=/usr/bin/mysql mysql:8 -h db -uroot -p" + narrowPW,
	"/usr/bin/mysql -p" + narrowPW,
	"/usr/local/mysql/bin/mysqldump -uroot -p" + narrowPW + " db",
	"./mysql -p" + narrowPW,
	"sudo -E mysql -p" + narrowPW,
	"sudo -u mysql /usr/bin/mysql -p" + narrowPW,
	"sudo /usr/bin/mysql -p" + narrowPW,
	"MYSQL_HOST=db LANG=C /usr/bin/mysql -p" + narrowPW,
	"env -i PATH=/bin /usr/bin/mysql -p" + narrowPW,
	"timeout 30 /usr/bin/mysqldump -p" + narrowPW + " db",
	"nice -n 10 /usr/bin/mysqldump -p" + narrowPW + " db",
	"nohup /usr/bin/mysqldump -p" + narrowPW + " db &",
	"exec /usr/bin/mysql -p" + narrowPW,
	"time -p /usr/bin/mysql -p" + narrowPW,
	"cd /x && /usr/bin/mysql -uroot -p" + narrowPW,
	"echo start; /opt/mariadb/bin/mariadb -p" + narrowPW,
	"$(/usr/bin/mysql -p" + narrowPW + " -e 'select 1')",
	`C:\mysql\bin\mysql.exe -uroot -p` + narrowPW,
	`"C:\Program Files\MySQL\MySQL Server 8.0\bin\mysql.exe" -uroot -p` + narrowPW,
	`{"cmd":"mysql -uroot -p` + narrowPW + ` db"}`,
	`{"cmd":"cd /x && mysql -uroot -p` + narrowPW + ` db"}`,
	`{"cmd":"echo hi\nmysql -uroot -p` + narrowPW + ` db"}`,
	`{"cmd":"echo hi\n/usr/bin/mysql -uroot -p` + narrowPW + ` db"}`,
	`{"cmd":"sh -c \"mysql -uroot -p` + narrowPW + `\""}`,
	`{"command":"mysqldump","args":["-uroot","-p` + narrowPW + `","db"]}`,
	`["mysql","-uroot","-p` + narrowPW + `"]`,
	`subprocess.run(["mysql", "-uroot", "-p` + narrowPW + `"])`,
	`echo 5" ; mysql -u root -p"` + narrowPW + `12" db`,
	`echo 5" ; mysql -e "select 1; select 2" -p` + narrowPW,
	`echo it's; mysql -e "select 1; select 2" -p` + narrowPW,
	`mysql -e "select 1; select 2" -p` + narrowPW,
	`mysql -e 'select 1; select 2' -p` + narrowPW,
	`mysql -e "select 'a;b' | x" -p` + narrowPW,
	`sh -c "cd /x && mysql -uroot -p` + narrowPW + `"`,
	`ssh host "mysql -e 'select 1; select 2' -p` + narrowPW + `"`,
	"mysql --password=" + narrowPW + " db",
	"mariadb-dump --pass=" + narrowPW + " db",
	"mysql -h db \\\n  -u root \\\n  -p" + narrowPW,
	"$MYSQL -uroot -p" + narrowPW,
	`"${MYSQLDUMP}" -uroot -p` + narrowPW + " db",
	"alias m=mysql; mysql -p" + narrowPW,
	"mysql <dump.sql -p" + narrowPW,
	"ssh root@mysql -p" + narrowPW + " uptime",
	"tar -cpf x.tar . && mysql -p" + narrowPW,
	"mysql -uroot 2>&1 -p" + narrowPW,
	"mysqldump db &>dump.log -p" + narrowPW,
	"mysql -uroot >&2 -p" + narrowPW,
	"mysql -h $(vault read -field=host db; true) -p" + narrowPW,
	"mysql -h `get-host; true` -p" + narrowPW,
	"(cd /x && mysql -uroot -p" + narrowPW + ")",
	"mysql -e 'select (1); select 2' -p" + narrowPW + " (",
	strings.Repeat("(", 40) + "x; mysql ; y -p" + narrowPW,
	"if /opt/db/mysql -uroot -p" + narrowPW + "; then echo ok; fi",
	"while ! /opt/db/mysql -p" + narrowPW + " -e 'select 1'; do sleep 1; done",
	"{ /opt/db/mysql -p" + narrowPW + "; }",
	"docker exec db /usr/bin/mysql -uroot -p" + narrowPW,
	"ssh host /usr/local/mysql/bin/mysqldump -uroot -p" + narrowPW + " db",
	"docker exec db /usr/sbin/mysqladmin -p" + narrowPW + " status",
	"sudo --user root /opt/db/mysql -p" + narrowPW,
	"sudo -Eu root /opt/db/mysql -p" + narrowPW,
	"sudo -uroot /opt/db/mysql -p" + narrowPW,
	"xargs -I{} /opt/db/mysql -p" + narrowPW + " -e {}",
	"watch -n 5 /opt/db/mysqladmin -p" + narrowPW + " status",
	"stdbuf -oL /opt/db/mysql -p" + narrowPW,
}

// On a line that mentions a MySQL/MariaDB name, a -p word is the client's
// password only when a client word stands before it in the same simple
// command; other programs' options stay as written.
func TestRED_MysqlPasswordNeedsClientWordInSameCommand(t *testing.T) {
	unchanged, redacted := 0, 0
	for _, in := range narrowUnchanged {
		if out := RedactSecrets(in); out != in {
			t.Errorf("altered\n in =%s\n out=%s", in, out)
			continue
		}
		unchanged++
	}
	for _, in := range narrowRedacted {
		out := RedactSecrets(in)
		if strings.Contains(out, narrowPW) || !strings.Contains(out, "[REDACTED]") {
			t.Errorf("leak\n in =%s\n out=%s", in, out)
			continue
		}
		if again := RedactSecrets(out); again != out {
			t.Errorf("not stable\n once =%s\n twice=%s", out, again)
		}
		redacted++
	}
	t.Logf("unchanged %d of %d, redacted %d of %d", unchanged, len(narrowUnchanged), redacted, len(narrowRedacted))
}

// Outcomes that follow from the rule and are kept as they are: a bare client
// name is a client word whatever its role in the command, so an option of
// that command reads as the client's.
func TestRED_MysqlBareNameInSameCommandIsAClientWord(t *testing.T) {
	for in, want := range map[string]string{
		"journalctl -u mariadb -perr":       "journalctl -u mariadb -p[REDACTED]",
		"helm install mysql -pfoo":          "helm install mysql -p[REDACTED]",
		"Restart mysql then ps -p1234":      "Restart mysql then ps -p[REDACTED]",
		"DB_HOST=mysql ./server -p8080":     "DB_HOST=mysql ./server -p8080",
		"MYSQL=mysql -uroot -p" + narrowPW:  "MYSQL=mysql -uroot -p[REDACTED]",
		"export M=mysqldump -Rp" + narrowPW: "export M=mysqldump -Rp[REDACTED]",
		"ssh $MYSQL_HOST -p2222 uptime":     "ssh $MYSQL_HOST -p[REDACTED] uptime",
		"mysql -p1234 db":                   "mysql -p[REDACTED] db",
		"install /a/mysql -p" + narrowPW:    "install /a/mysql -p" + narrowPW,
		"mysqladmin -ip5 status":            "mysqladmin -ip5 status",
		"sh -c 'tar -cpf a.tar x' ; mysql":  "sh -c 'tar -cpf a.tar x' ; mysql",
		// A client path outside bin and sbin that is another command's
		// argument is not read as a client.
		"docker exec db /opt/db/mysqldump -p" + narrowPW: "docker exec db /opt/db/mysqldump -p" + narrowPW,
		// An unbalanced quote leaves the separators unreadable: none counts.
		"echo don't; systemctl stop mysql; tar -cpf b.tar .": "echo don't; systemctl stop mysql; tar -cp[REDACTED] b.tar .",
	} {
		if out := RedactSecrets(in); out != want {
			t.Errorf("%q\n got  %q\n want %q", in, out, want)
		}
	}
}

// narrowEnvPW is the fixture password of the environment-entry forms.
const narrowEnvPW = "Zq7kT9wLm2xR"

// A MYSQL_PWD entry of a container environment list carries the password
// under the "value" key (or on the next line of a YAML list item): the
// password is replaced and the keys stay.
func TestRED_MysqlPwdEnvEntryRedactsTheValue(t *testing.T) {
	for in, want := range map[string]string{
		`{"name":"MYSQL_PWD","value":"` + narrowEnvPW + `"}`:                     `{"name":"MYSQL_PWD","value":"[REDACTED]"}`,
		`{"name": "MYSQL_PWD", "value": "` + narrowEnvPW + `"}`:                  `{"name": "MYSQL_PWD", "value": "[REDACTED]"}`,
		`[{"name":"MYSQL_PWD","value":"pw"},{"name":"X","value":"1"}]`:           `[{"name":"MYSQL_PWD","value":"[REDACTED]"},{"name":"X","value":"1"}]`,
		`{"body":"{\"name\":\"MYSQL_PWD\",\"value\":\"` + narrowEnvPW + `\"}"}`:  `{"body":"{\"name\":\"MYSQL_PWD\",\"value\":\"[REDACTED]\"}"}`,
		"env:\n  - name: MYSQL_PWD\n    value: " + narrowEnvPW + "\n  - name: X": "env:\n  - name: MYSQL_PWD\n    value: [REDACTED]\n  - name: X",
		"  - name: \"MYSQL_PWD\"\n    value: \"" + narrowEnvPW + "\"":            "  - name: \"MYSQL_PWD\"\n    value: \"[REDACTED]\"",
		"  - name: MYSQL_PWD\r\n    value: 'pw'":                                 "  - name: MYSQL_PWD\r\n    value: '[REDACTED]'",
		"env:\n  - name: SSHPASS\n    value: " + narrowEnvPW:                     "env:\n  - name: SSHPASS\n    value: [REDACTED]",
		`{"y":"- name: MYSQL_PWD\r\n  value: pw"}`:                               `{"y":"- name: MYSQL_PWD\r\n  value: [REDACTED]"}`,
		`{"y":"- name: MYSQL_PWD\n  value: pw"}`:                                 `{"y":"- name: MYSQL_PWD\n  value: [REDACTED]"}`,
		"MYSQL_PWD = f(" + strings.Repeat("a ", 200):                             "MYSQL_PWD = [REDACTED] " + strings.Repeat("a ", 199),
		"MYSQL_PWD = f(a, b) x":                                                  "MYSQL_PWD = [REDACTED] b) x",
		`{"name":"SSHPASS","value":"` + narrowEnvPW + `"}`:                       `{"name":"SSHPASS","value":"[REDACTED]"}`,
		`os.Setenv("MYSQL_PWD", "pw")`:                                           `os.Setenv("MYSQL_PWD", "[REDACTED]")`,
		`setenv("MYSQL_PWD", "pw", 1)`:                                           `setenv("MYSQL_PWD", "[REDACTED]", 1)`,
		`os.environ.setdefault('MYSQL_PWD', 'pw')`:                               `os.environ.setdefault('MYSQL_PWD', '[REDACTED]')`,
		`monkeypatch.setenv(  "MYSQL_PWD", "pw")`:                                `monkeypatch.setenv(  "MYSQL_PWD", "[REDACTED]")`,
		`run(cmd, "MYSQL_PWD", "pw")`:                                            `run(cmd, "MYSQL_PWD", "[REDACTED]")`,
		`{"cmd":"python -c 'import os; os.putenv(\"MYSQL_PWD\", \"pw\")'"}`:      `{"cmd":"python -c 'import os; os.putenv(\"MYSQL_PWD\", \"[REDACTED]\")'"}`,
		"MYSQL_PWD: hunter2":                                                     "MYSQL_PWD: [REDACTED]",
		`MYSQL_PWD = "x"`:                                                        `MYSQL_PWD = "[REDACTED]"`,
		`"MYSQL_PWD": "abc"`:                                                     `"MYSQL_PWD": "[REDACTED]"`,
		"MYSQL_PWD = hunter2":                                                    "MYSQL_PWD = [REDACTED]",
		`MYSQL_PWD: str = "` + narrowEnvPW + `"`:                                 `MYSQL_PWD: [REDACTED] = "[REDACTED]"`,
		"MYSQL_PWD: Abc(123x":                                                    "MYSQL_PWD: [REDACTED]",
		"MYSQL_PWD: !" + narrowEnvPW + "x9$":                                     "MYSQL_PWD: [REDACTED]",
		"MYSQL_PWD = !vault":                                                     "MYSQL_PWD = [REDACTED]",
		"MYSQL_PWD=cfg.Password mysql db":                                        "MYSQL_PWD=[REDACTED] mysql db",
		"MYSQL_PWD=null mysql db":                                                "MYSQL_PWD=[REDACTED] mysql db",
	} {
		got := RedactSecrets(in)
		if got != want {
			t.Errorf("%s\n  got  %s\n  want %s", in, got, want)
		}
		if again := RedactSecrets(got); again != got {
			t.Errorf("not stable\n once =%s\n twice=%s", got, again)
		}
	}
}

// A MYSQL_PWD mention whose value is a reference, a keyword, a code
// expression, a YAML tag or a comment holds no password, and a quoted name
// that is not a call argument has none after its comma.
func TestRED_MysqlPwdCodeAndKeywordValuesStayReadable(t *testing.T) {
	for _, in := range []string{
		"MYSQL_PWD = cfg.Password",
		"MYSQL_PWD = os.getenv('DB_PASS')",
		`MYSQL_PWD = os.environ.get("DB_PASS", "")`,
		`"MYSQL_PWD": process.env.DB_PASS,`,
		`"MYSQL_PWD": null`,
		`"MYSQL_PWD": null,`,
		`{"MYSQL_PWD": null}`,
		"MYSQL_PWD: None",
		"MYSQL_PWD = nil",
		"MYSQL_PWD: true",
		"MYSQL_PWD: false",
		"MYSQL_PWD: str = Field(default=None)",
		"MYSQL_PWD: Optional[str] = None",
		"MYSQL_PWD: !vault |",
		"MYSQL_PWD: !secret db_password",
		"MYSQL_PWD: !!str",
		"MYSQL_PWD: # set in CI",
		"MYSQL_PWD = # set in CI",
		"MYSQL_PWD:",
		"MYSQL_PWD: ${DB_PASSWORD}",
		"MYSQL_PWD = settings.DATABASES['default']['PASSWORD']",
		"MYSQL_PWD = vault.read(\"secret/db\", \"password\")",
		"for k in ('MYSQL_PWD', 'MYSQL_USER'):",
		`for k in ("MYSQL_PWD", "MYSQL_USER"):`,
		`names = ["MYSQL_PWD", "MYSQL_USER"]`,
		`("MYSQL_PWD", "pw")`,
		`return "MYSQL_PWD", "pw"`,
		`{"name":"MYSQL_PWD","valueFrom":{"secretKeyRef":{"name":"db","key":"pw"}}}`,
		`{"name": "MYSQL_PWD", "valueFrom": {"secretKeyRef": {"name": "db", "key": "pw"}}}`,
		"  - name: MYSQL_PWD\n    valueFrom:\n      secretKeyRef:\n        name: db",
		"  - name: MYSQL_PWD\n    value: ${DB_PASSWORD}",
		"  - name: MYSQL_PWD\n\n    value: pw",
		"MYSQL_PWD\nvalue: 5",
		`{"y":"- name: MYSQL_PWD\q  value: pw"}`,
		"- name: MYSQL_PWD\n  values: pw",
		"unset MYSQL_PWD",
		"echo $MYSQL_PWD",
		"bash: sshpass: command not found",
		"sshpass: installed",
		"SSHPASS_FILE=/run/secrets/pw sshpass -f /run/secrets/pw ssh h",
		"SSHPASS: ${SSH_PASSWORD}",
		"SSHPASS = os.getenv('SSH_PASS')",
		"unset SSHPASS",
	} {
		if out := RedactSecrets(in); out != in {
			t.Errorf("altered\n in =%s\n out=%s", in, out)
		}
	}
}

// The SSHPASS environment variable carries the password sshpass -e reads; it
// is judged like MYSQL_PWD, at any length.
func TestRED_SshpassEnvironmentVariable(t *testing.T) {
	for in, want := range map[string]string{
		"SSHPASS=" + narrowEnvPW + " sshpass -e ssh u@h": "SSHPASS=[REDACTED] sshpass -e ssh u@h",
		"export SSHPASS=pw":                       "export SSHPASS=[REDACTED]",
		"export SSHPASS='p w'":                    "export SSHPASS='[REDACTED]'",
		"SSHPASS: pw":                             "SSHPASS: [REDACTED]",
		`"SSHPASS": "pw"`:                         `"SSHPASS": "[REDACTED]"`,
		`os.environ["SSHPASS"] = "pw"`:            `os.environ["SSHPASS"] = "[REDACTED]"`,
		`os.Setenv("SSHPASS", "pw")`:              `os.Setenv("SSHPASS", "[REDACTED]")`,
		`{"cmd":"SSHPASS=pw sshpass -e ssh u@h"}`: `{"cmd":"SSHPASS=[REDACTED] sshpass -e ssh u@h"}`,
		"env SSHPASS=pw sshpass -e scp f h:":      "env SSHPASS=[REDACTED] sshpass -e scp f h:",
	} {
		got := RedactSecrets(in)
		if got != want {
			t.Errorf("%s\n  got  %s\n  want %s", in, got, want)
		}
		if again := RedactSecrets(got); again != got {
			t.Errorf("not stable\n once =%s\n twice=%s", got, again)
		}
	}
}

// The narrowed rules read each line once: a line that holds many client
// words, separators, quotes or environment entries costs time in proportion
// to its length.
func TestRED_NarrowedShellPasswordRulesScaleLinearly(t *testing.T) {
	for name, gen := range map[string]func(n int) string{
		"client words":   func(n int) string { return strings.Repeat("mysql -sNpx ; ", n/14) },
		"quotes":         func(n int) string { return "mysql " + strings.Repeat(`"a;b" 'c' \"d\" `, n/16) + "-pfoo" },
		"lines":          func(n int) string { return strings.Repeat("ls /var/lib/mysql -print\n", n/25) },
		"long line":      func(n int) string { return strings.Repeat("x ", n/2) + "mysql -pfoo" },
		"env entries":    func(n int) string { return strings.Repeat(`{"name":"MYSQL_PWD","value":"pw"},`, n/34) },
		"yaml entries":   func(n int) string { return strings.Repeat("- name: MYSQL_PWD\n  value: pw\n", n/30) },
		"call arguments": func(n int) string { return strings.Repeat("(", n/2) + strings.Repeat(`"MYSQL_PWD", "pw" `, n/36) },
		"annotations":    func(n int) string { return strings.Repeat("MYSQL_PWD: a = b = c\n", n/21) },
		"sshpass names":  func(n int) string { return strings.Repeat("SSHPASS: x.y\n", n/13) },
	} {
		redactR3Linear(t, name, gen)
	}
}

// Outcomes docs/SECURITY.md states for these rules.
func TestRED_DocumentedShellPasswordOutcomes(t *testing.T) {
	deep := func(n int) string {
		return "mysql " + strings.Repeat("(", n) + "x" + strings.Repeat(")", n) + " ; tar -cpf b.tar ."
	}
	for in, want := range map[string]string{
		// In a connection URL the reference shape alone is judged.
		"postgres://app:<hunter2>@db/app": "postgres://app:<hunter2>@db/app",
		"postgres://app:{hunter2}@db/app": "postgres://app:{hunter2}@db/app",
		"postgres://app:%PASS1%@db/app":   "postgres://app:%PASS1%@db/app",
		"postgres://app:$X1@db/app":       "postgres://app:$X1@db/app",
		// Code-shaped literals stay readable in the mapping forms only.
		"MYSQL_PWD: my.pass":                      "MYSQL_PWD: my.pass",
		"MYSQL_PWD = settings.DB['PASSWORD']":     "MYSQL_PWD = settings.DB['PASSWORD']",
		"MYSQL_PWD: undefined":                    "MYSQL_PWD: undefined",
		"MYSQL_PWD: NaN":                          "MYSQL_PWD: NaN",
		"MYSQL_PWD: the variable":                 "MYSQL_PWD: [REDACTED] variable",
		"mysql -h $(a; b) -p" + narrowPW:          "mysql -h $(a; b) -p[REDACTED]",
		"mysqlpump -Rp" + narrowPW:                "mysqlpump -Rp[REDACTED]",
		"mysql -Rp" + narrowPW:                    "mysql -Rp" + narrowPW,
		"timeout -s KILL 5 /opt/db/mysql -pfoo":   "timeout -s KILL 5 /opt/db/mysql -p[REDACTED]",
		"mysql -p3306-3307:3306-3307 db":          "mysql -p[REDACTED] db",
		"docker run --entrypoint mysqldump -pfoo": "docker run --entrypoint mysqldump -p[REDACTED]",
		// Fifteen open parentheses are followed; one more and no separator counts.
		deep(15): deep(15),
		deep(16): "mysql " + strings.Repeat("(", 16) + "x" + strings.Repeat(")", 16) + " ; tar -cp[REDACTED] b.tar .",
	} {
		if out := RedactSecrets(in); out != want {
			t.Errorf("%q\n got  %q\n want %q", in, out, want)
		}
	}
}

// Every combination of a way to reach a client, a client word, arguments that
// hold separators inside quotes or substitutions, and a password option: the
// password never survives, and a second pass changes nothing.
func TestRED_MysqlClientPasswordNeverSurvivesProduct(t *testing.T) {
	type wrap struct{ open, close, quote string }
	wraps := []wrap{
		{"", "", ""}, {"sudo ", "", ""}, {"sudo -u root ", "", ""}, {"docker exec -it db ", "", ""}, {"cd /x && ", "", ""},
		{"echo hi; ", " | head", ""}, {"FOO=1 BAR=2 ", "", ""}, {"(", ")", ""}, {"$(", ")", ""}, {`sh -c "`, `"`, `"`},
		{"bash -lc '", "'", "'"}, {`ssh h "`, `" ; echo done`, `"`}, {"time -p ", "", ""}, {"env -i A=b ", "", ""},
		{"tar -cpf x.tar . && ", " && tar -xpf x.tar", ""}, {`{"cmd":"`, `"}`, `"`}, {"kubectl exec pod -- ", "", ""},
		{"if ! ", "; then exit 1; fi", ""}, {"nohup ", " &", ""}, {"timeout -k 5 30 ", "", ""},
	}
	clients := []string{"mysql", "mysqldump", "mariadb", "mysqladmin", "mariadb-dump", "mysqlsh", "MYSQL", "mysql.exe", "/usr/bin/mysql", "/opt/db/mysqldump", `C:\db\mysql.exe`}
	args := []string{
		"", "-uroot ", "-h db -P 3306 ", `-e "select 1; select 2" `, "-e 'a|b && c' ", "--ssl 2>&1 ", "-h $(cat f; true) ",
		"-h `cat f; true` ", "db >out.txt 2>&1 ", "-u root --protocol=tcp ",
	}
	options := []string{"-p" + narrowPW, "--password=" + narrowPW, "-p'" + narrowPW + "'", `-p"` + narrowPW + `"`, "-fp" + narrowPW, "--pass=" + narrowPW + ";"}
	n := 0
	for _, w := range wraps {
		for _, c := range clients {
			for _, a := range args {
				for _, o := range options {
					if w.quote != "" && (strings.Contains(a, w.quote) || strings.Contains(o, w.quote)) {
						continue // the argument would close the wrapper's quote
					}
					if strings.HasPrefix(w.open, "docker exec") && strings.HasPrefix(c, "/opt/") {
						continue // a path outside bin that is another command's argument
					}
					in := w.open + c + " " + a + o + w.close
					out := RedactSecrets(in)
					if strings.Contains(out, narrowPW) {
						t.Fatalf("leak\n in =%s\n out=%s", in, out)
					}
					if again := RedactSecrets(out); again != out {
						t.Fatalf("not stable\n once =%s\n twice=%s", out, again)
					}
					n++
				}
			}
		}
	}
	t.Logf("%d commands", n)
}

// Every combination of a command that mentions a client name, a separator,
// and another program with a -p option, bare and inside a quoted or
// parenthesised script: nothing is rewritten.
func TestRED_MysqlMentionLeavesOtherCommandsProduct(t *testing.T) {
	firsts := []string{
		"systemctl stop mysql", "cd /var/lib/mysql", "mysqldump db > d.sql", "docker stop mariadb", "ls /etc/mysql",
		"service mysql restart", "mysql -e 'select 1'", "mysqladmin ping", "sudo -u mysql true", "/usr/bin/mysql db < in.sql",
		"MYSQL_HOME=/etc/mysql mysqld_safe", "mariadb-dump --all-databases 2>&1",
	}
	seps := []string{" && ", "; ", " | ", " || ", " & ", ";", "&&", "|"}
	seconds := []string{
		"tar -cpzf b.tgz /var/lib/mysql", "ssh -Cp2222 host", "pv -petr", "find /var/lib/mysql -print", "patch -sp1 -i x.diff",
		"rsync -ap /var/lib/mysql/ /mnt/", "ps -p1234", "nc -lp3306", "cp -rp /var/lib/mysql /backup", "gcc -pthread -pipe x.c",
		"go test ./internal/mysql/... -cpuprofile cpu.out", "sudo -E tar -C /var/lib/mysql -cpf x.tar .",
	}
	wraps := [][2]string{{"", ""}, {`sh -c "`, `"`}, {"bash -c '", "'"}, {"(", ")"}, {`{"cmd":"`, `"}`}, {"$(", ")"}, {"echo `", "`"}}
	n := 0
	for _, f := range firsts {
		for _, sep := range seps {
			for _, sec := range seconds {
				for _, w := range wraps {
					if w[0] != "" && strings.ContainsAny(f, w[1]) {
						continue // the command would close the wrapper's quote
					}
					in := w[0] + f + sep + sec + w[1]
					if out := RedactSecrets(in); out != in {
						t.Fatalf("altered\n in =%s\n out=%s", in, out)
					}
					n++
				}
			}
		}
	}
	t.Logf("%d commands", n)
}
