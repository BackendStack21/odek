package redact

import (
	"math/rand"
	"strings"
	"testing"
)

// A JSON-encoded command (a tool-call argument) whose shell line continuation
// sits between sshpass and -p, or between -p and the value.
func TestRED_SshpassJSONContinuation(t *testing.T) {
	for in, want := range map[string]string{
		`{"command":"sshpass \\\n  -p Zq7Xk9Wm4Rt2 ssh -p 2222 u@h"}`: `{"command":"sshpass \\\n  -p [REDACTED] ssh -p 2222 u@h"}`,
		`{"command":"sshpass -v \\\n  -p Zq7Xk9Wm4Rt2 ssh u@h"}`:      `{"command":"sshpass -v \\\n  -p [REDACTED] ssh u@h"}`,
		`{"command":"sshpass -p \\\n Zq7Xk9Wm4Rt2 ssh u@h"}`:          `{"command":"sshpass -p \\\n [REDACTED] ssh u@h"}`,
		`{"command":"sshpass -p \\n Zq7Xk9Wm4Rt2 ssh u@h"}`:           `{"command":"sshpass -p \\n [REDACTED] ssh u@h"}`,
		`{"command":"sshpass\t-v\n-p\tZq7Xk9Wm4Rt2 ssh u@h"}`:         `{"command":"sshpass\t-v\n-p\t[REDACTED] ssh u@h"}`,
		"sshpass \\\n  -p Zq7Xk9Wm4Rt2 ssh -p 2222 u@h":               "sshpass \\\n  -p [REDACTED] ssh -p 2222 u@h",
		"sshpass -p \\\r\n  Zq7Xk9Wm4Rt2 ssh u@h":                     "sshpass -p \\\r\n  [REDACTED] ssh u@h",
	} {
		if got := RedactSecrets(in); got != want {
			t.Errorf("%s\n  got  %s\n  want %s", in, got, want)
		}
	}
}

// sshpass given as an argv list, as a quoted command word, with an .exe
// suffix or a path, in any letter case.
func TestRED_SshpassArgvListAndQuotedWords(t *testing.T) {
	for in, want := range map[string]string{
		`subprocess.run(["sshpass", "-p", "Zq7Xk9Wm4Rt2", "ssh", "u@h"])`: `subprocess.run(["sshpass", "-p", "[REDACTED]", "ssh", "u@h"])`,
		`exec.Command("sshpass", "-p", "Zq7Xk9Wm4Rt2", "ssh", "u@h")`:     `exec.Command("sshpass", "-p", "[REDACTED]", "ssh", "u@h")`,
		`["sshpass","-p","Zq7Xk9Wm4Rt2","ssh","-p","2222","u@h"]`:         `["sshpass","-p","[REDACTED]","ssh","-p","2222","u@h"]`,
		`['sshpass', '-p', 'Zq7Xk9Wm4Rt2', 'ssh']`:                        `['sshpass', '-p', '[REDACTED]', 'ssh']`,
		`{"argv":"[\"sshpass\", \"-p\", \"Zq7Xk9Wm4Rt2\", \"ssh\"]"}`:     `{"argv":"[\"sshpass\", \"-p\", \"[REDACTED]\", \"ssh\"]"}`,
		`["sshpass", "-v", "-p", "Zq7Xk9Wm4Rt2"]`:                         `["sshpass", "-v", "-p", "[REDACTED]"]`,
		`("sshpass", "-pZq7Xk9Wm4Rt2", "ssh")`:                            `("sshpass", "-p[REDACTED]", "ssh")`,
		`"sshpass" -p Zq7Xk9Wm4Rt2 ssh u@h`:                               `"sshpass" -p [REDACTED] ssh u@h`,
		`'sshpass' -p Zq7Xk9Wm4Rt2 ssh u@h`:                               `'sshpass' -p [REDACTED] ssh u@h`,
		`sshpass.exe -p Zq7Xk9Wm4Rt2 ssh u@h`:                             `sshpass.exe -p [REDACTED] ssh u@h`,
		`C:\tools\SSHPASS.EXE -p Zq7Xk9Wm4Rt2 ssh u@h`:                    `C:\tools\SSHPASS.EXE -p [REDACTED] ssh u@h`,
		`/usr/local/bin/sshpass -p Zq7Xk9Wm4Rt2 ssh u@h`:                  `/usr/local/bin/sshpass -p [REDACTED] ssh u@h`,
		`sshpass -h -V -p Zq7Xk9Wm4Rt2 ssh u@h`:                           `sshpass -h -V -p [REDACTED] ssh u@h`,
		`sshpass --verbose - -p Zq7Xk9Wm4Rt2 ssh u@h`:                     `sshpass --verbose - -p [REDACTED] ssh u@h`,
		`sshpass -v:1 -p Zq7Xk9Wm4Rt2 ssh u@h`:                            `sshpass -v:1 -p [REDACTED] ssh u@h`,
		`sshpass -Pprompt: -p Zq7Xk9Wm4Rt2 ssh u@h`:                       `sshpass -Pprompt: -p [REDACTED] ssh u@h`,
		`sshpass -p"Zq7 Xk9" ssh u@h`:                                     `sshpass -p"[REDACTED]" ssh u@h`,
		`sshpass -p'Zq7 Xk9' ssh u@h`:                                     `sshpass -p'[REDACTED]' ssh u@h`,
		`{"command":"sshpass","args":["-p","Zq7Xk9Wm4Rt2","ssh"]}`:        `{"command":"sshpass","args":["-p","[REDACTED]","ssh"]}`,
		`{"command": "sshpass", "args": ["-v", "-pZq7Xk9Wm4Rt2"]}`:        `{"command": "sshpass", "args": ["-v", "-p[REDACTED]"]}`,
		`sshpass -p [Zq7Xk9] ssh u@h`:                                     `sshpass -p [[REDACTED] ssh u@h`,
		`sshpass -v $SSH -p Zq7Xk9 h`:                                     `sshpass -v $SSH -p [REDACTED] h`,
	} {
		if got := RedactSecrets(in); got != want {
			t.Errorf("%s\n  got  %s\n  want %s", in, got, want)
		}
	}
	// An unquoted list item is redacted with the comma that ends its word.
	if got := RedactSecrets(`args: [sshpass, -p, Zq7Xk9Wm4Rt2, ssh, u@h]`); strings.Contains(got, "Zq7Xk9Wm4Rt2") || !strings.HasPrefix(got, "args: [sshpass, -p, [REDACTED]") {
		t.Errorf("unquoted list: %s", got)
	}
	for _, in := range []string{
		`["sshpass", "-f", "/run/pw", "ssh", "-p", "2222", "u@h"]`,
		`["sshpass", "-e", "ssh", "-p", "2222", "u@h"]`,
		`["sshpass", "-d", "3", "ssh", "-p", "2222"]`,
		`["sshpass", "ssh", "-p", "2222", "u@h"]`,
		`["sshpass", "--", "ssh", "-p", "2222", "u@h"]`,
		`["sshpass", "-p", "$SSHPASS", "ssh"]`,
		`["sshpass", "-p", "", "ssh"]`,
		`sshpass -p '' ssh -p 2222 h`,
		`sshpass.exe -f pw.txt ssh -p 2222 h`,
		`sshpass.execute -p 2222`,
		`sshpassword -p 2222`,
		`sshpass-wrapper -p 2222`,
		`"sshpass" "-p"`,
		`sshpass -P`,
		`sshpass -p`,
		"Usage: sshpass [-f|-d|-p|-e] [-hV] command parameters",
		"   -P prompt     Which string should sshpass search for to detect a password prompt",
		"sshpass: command not found",
		`{"command":"sshpass","args":["-e","ssh","-p","2222"]}`,
		`{"command":"sshpass","timeout":5}`,
		`{"tool":"sshpass"`,
	} {
		if got := RedactSecrets(in); got != in {
			t.Errorf("over-redaction: %s => %s", in, got)
		}
	}
}

// An unbalanced quote earlier on the line must not make a quoted password
// word read as empty.
func TestRED_OddQuoteBeforeQuotedPassword(t *testing.T) {
	for in, want := range map[string]string{
		`echo 5" ; mysql -u root -p"Zq7Xk9Wm4Rt2" db`:      `echo 5" ; mysql -u root -p"[REDACTED]" db`,
		`echo 5" ; mysql -u root --password="Zq7Xk9" db`:   `echo 5" ; mysql -u root --password="[REDACTED]" db`,
		`echo 5" ; MYSQL_PWD="Zq7Xk9Wm4Rt2" mysql db`:      `echo 5" ; MYSQL_PWD="[REDACTED]" mysql db`,
		`echo 5" ; sshpass -p "Zq7Xk9Wm4Rt2" ssh u@h`:      `echo 5" ; sshpass -p "[REDACTED]" ssh u@h`,
		`echo 5" ; sshpass "-p" "Zq7Xk9Wm4Rt2" ssh u@h`:    `echo 5" ; sshpass "-p" "[REDACTED]" ssh u@h`,
		`echo 5' ; mysql -u root -p'Zq7Xk9Wm4Rt2' db`:      `echo 5' ; mysql -u root -p'[REDACTED]' db`,
		`echo 5' ; sshpass -p 'Zq7Xk9Wm4Rt2' ssh u@h`:      `echo 5' ; sshpass -p '[REDACTED]' ssh u@h`,
		`echo 5" ; mysql -u root -pZq7"Xk9 Wm4"Rt2 db`:     `echo 5" ; mysql -u root -p[REDACTED]"[REDACTED]"[REDACTED] db`,
		`echo 5" ; mysql -u root -p"Zq7Xk9Wm4Rt2" db; x="`: `echo 5" ; mysql -u root -p"[REDACTED]" db; x="`,
	} {
		if got := RedactSecrets(in); got != want {
			t.Errorf("%s\n  got  %s\n  want %s", in, got, want)
		}
	}
	// A double quote that closes a JSON string or a source-code string still
	// ends the word: what follows it is not part of a password.
	for _, in := range []string{
		`{"cmd":"mysql -u root -p","next":"value"}`,
		`{"cmd":"mysql -u root -p"}`,
		`{"args":["mysql","-p"],"x":1}`,
		`run("mysql -u root -p"); other("x")`,
		`cmd = "mysql -u root -p" + pw`,
		`cmd = "mysql -u root -p"+pw+" db"`,
		`cmd := "mysql -u root -p".split()`,
		`{"cmd":"MYSQL_PWD=","x":"y"}`,
	} {
		if got := RedactSecrets(in); got != in {
			t.Errorf("over-redaction: %s => %s", in, got)
		}
	}
}

// The MySQL/MariaDB clients take clustered short options: argument-less
// flags followed by p and the password.
func TestRED_MysqlClusteredShortOptionPassword(t *testing.T) {
	for in, want := range map[string]string{
		`mysql -uroot -sNpZq7Xk9Wm4Rt2 -e 'select 1'`: `mysql -uroot -sNp[REDACTED] -e 'select 1'`,
		`mysql -BNpZq7Xk9Wm4Rt2 db`:                   `mysql -BNp[REDACTED] db`,
		`mysqldump -vp"Zq7 Xk9" db`:                   `mysqldump -vp"[REDACTED]" db`,
		`mariadb -AsNrp'Zq7Xk9' db`:                   `mariadb -AsNrp'[REDACTED]' db`,
		`{"cmd":"mysql -sNpZq7Xk9Wm4Rt2 db"}`:         `{"cmd":"mysql -sNp[REDACTED] db"}`,
	} {
		if got := RedactSecrets(in); got != want {
			t.Errorf("%s\n  got  %s\n  want %s", in, got, want)
		}
	}
	for _, in := range []string{
		"mysql -sNp db",                  // a bare -p prompts
		"mysql -uadminprod -hdbprod db",  // -u and -h take a value
		"mysql -Dshop -e'select p' db",   // -D and -e take a value
		"mysql -sNp$MYSQL_ROOT_PASSWORD", // a reference
		"mysql -sNp<password> db",        // a placeholder
		"docker run --name mysql -tp3306:3306 mysql:8",
		"cp -rp /var/lib/mysql /backup",
		"mysql " + strings.Repeat("s", 40) + "pZq7",  // not an option word
		"mysql -" + strings.Repeat("s", 40) + "pZq7", // longer than any option cluster
	} {
		if got := RedactSecrets(in); got != in {
			t.Errorf("over-redaction: %s => %s", in, got)
		}
	}
	// Accepted over-redaction: on a line that holds a client name, another
	// command's cluster ending in p reads as the client's password option.
	if got, want := RedactSecrets("cd /var/lib/mysql && tar -xpf b.tar"), "cd /var/lib/mysql && tar -xp[REDACTED] b.tar"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// MYSQL_PWD set through a mapping or an API call carries the password at any
// length, like the shell assignment.
func TestRED_MySQLPwdMapAndAPIForms(t *testing.T) {
	for in, want := range map[string]string{
		"    environment:\n      MYSQL_PWD: Zq7Xk9\n      X: 1": "    environment:\n      MYSQL_PWD: [REDACTED]\n      X: 1",
		`MYSQL_PWD: "Zq7Xk9"`:                                    `MYSQL_PWD: "[REDACTED]"`,
		`{"MYSQL_PWD": "Zq7Xk9", "MYSQL_HOST": "db"}`:            `{"MYSQL_PWD": "[REDACTED]", "MYSQL_HOST": "db"}`,
		`{"MYSQL_PWD":"Zq7Xk9","MYSQL_HOST":"db"}`:               `{"MYSQL_PWD":"[REDACTED]","MYSQL_HOST":"db"}`,
		`{"env":{"MYSQL_PWD":"Zq7"}}`:                            `{"env":{"MYSQL_PWD":"[REDACTED]"}}`,
		`{"body":"{\"MYSQL_PWD\": \"Zq7Xk9\", \"X\": 1}"}`:       `{"body":"{\"MYSQL_PWD\": \"[REDACTED]\", \"X\": 1}"}`,
		`os.Setenv("MYSQL_PWD", "Zq7Xk9")`:                       `os.Setenv("MYSQL_PWD", "[REDACTED]")`,
		`os.environ["MYSQL_PWD"] = "Zq7Xk9"`:                     `os.environ["MYSQL_PWD"] = "[REDACTED]"`,
		`process.env.MYSQL_PWD = 'Zq7Xk9';`:                      `process.env.MYSQL_PWD = '[REDACTED]';`,
		`$env:MYSQL_PWD = "Zq7Xk9"`:                              `$env:MYSQL_PWD = "[REDACTED]"`,
		`env = {'MYSQL_PWD': 'Zq7Xk9'}`:                          `env = {'MYSQL_PWD': '[REDACTED]'}`,
		`ENV["MYSQL_PWD"] ||= "x"; h = {"MYSQL_PWD" => "Zq7"}`:   `ENV["MYSQL_PWD"] ||= "x"; h = {"MYSQL_PWD" => "[REDACTED]"}`,
		`MYSQL_PWD := Zq7Xk9`:                                    `MYSQL_PWD := [REDACTED]`,
		`if os.environ["MYSQL_PWD"] == "Zq7Xk9":`:                `if os.environ["MYSQL_PWD"] == "[REDACTED]":`,
		`echo 5" ; export MYSQL_PWD = "Zq7Xk9"`:                  `echo 5" ; export MYSQL_PWD = "[REDACTED]"`,
		`subprocess.run(cmd, env={"MYSQL_PWD": "Zq7"}, check=1)`: `subprocess.run(cmd, env={"MYSQL_PWD": "[REDACTED]"}, check=1)`,
		"MYSQL_PWD: {{Zq7Xk9":                                    "MYSQL_PWD: [REDACTED]",
		"MYSQL_PWD: {{Zq7}}Xk9":                                  "MYSQL_PWD: [REDACTED]",
		"MYSQL_PWD: {{ " + strings.Repeat("a ", 200) + "}}":      "MYSQL_PWD: [REDACTED] " + strings.Repeat("a ", 200) + "}}",
		"MYSQL_PWD: {{ a\n}}":                                    "MYSQL_PWD: [REDACTED] a\n}}",
	} {
		if got := RedactSecrets(in); got != want {
			t.Errorf("%s\n  got  %s\n  want %s", in, got, want)
		}
	}
	for _, in := range []string{
		"MYSQL_PWD: ${DB_PASS}",
		`MYSQL_PWD: "{{ vault_db_password }}"`,
		"MYSQL_PWD: {{ .Values.db.password }}",
		"  MYSQL_PWD: {{ .Values.db.password | quote }}\n  X: 1",
		`{"MYSQL_PWD": "$DB_PASS"}`,
		`{"MYSQL_PWD": ""}`,
		"MYSQL_PWD:\n  next: 1",
		"MYSQL_PWD=",
		"MYSQL_PWD= mysql db",
		`os.Getenv("MYSQL_PWD")`,
		`os.Setenv("MYSQL_PWD", pw)`,
		"set MYSQL_PWD, then run the client",
		`unset "MYSQL_PWD"; mysql db`,
		`os.environ["MYSQL_PWD"]`,
		"MYSQL_PWD",
		`"MYSQL_PWD",`,
		`"MYSQL_PWD" `,
	} {
		if got := RedactSecrets(in); got != in {
			t.Errorf("over-redaction: %s => %s", in, got)
		}
	}
}

// supersetCommands start a generated line for the idempotence corpus: the
// argv-list, quoted-word, continuation, cluster and mapping shapes.
var supersetCommands = []string{
	`["sshpass", "-p", "`, `["sshpass","-v","-p","`, `"sshpass" -p `, "sshpass.exe -p ", "sshpass \\\n -p ", `sshpass "-p" "`,
	"[sshpass, -p, ", `echo 5" ; sshpass -p "`, `echo 5" ; mysql -p"`, `echo 5' ; mysql -p'`, "mysql -sNp", "mysql -uroot -Bvp",
	"MYSQL_PWD: ", `"MYSQL_PWD": "`, `os.Setenv("MYSQL_PWD", "`, `$env:MYSQL_PWD = "`, `{"MYSQL_PWD":"`,
	"postgres://a:$A@h1/x,postgres://u:", `echo hi\npostgres://u:`, "psql postgres://u:",
}

// The shapes this change added redact to a fixed point in one call.
func TestRED_SupersetShapesAreIdempotent(t *testing.T) {
	check := func(in string) {
		t.Helper()
		once := RedactSecrets(in)
		if twice := RedactSecrets(once); twice != once {
			t.Fatalf("not idempotent: %q\n  once  %q\n  twice %q", in, once, twice)
		}
	}
	rng := rand.New(rand.NewSource(5))
	n := 20000
	if testing.Short() {
		n = 4000
	}
	for i := 0; i < n; i++ {
		line := redactR4Line(rng, supersetCommands)
		check(line)
		check(`{"command":"` + redactR4JSON(line) + `","x":1}`)
		check(`cmd := exec.Command("sh", "-c", "` + redactR4JSON(line) + `")`)
	}
}

// The scans this change added stay linear on their hot shapes.
func TestRED_SupersetShapesScaleLinearly(t *testing.T) {
	for name, unit := range map[string]string{
		"sshpass -p ":       "sshpass -p ",
		"sshpass list":      `"sshpass",`,
		"sshpass list -p":   `["sshpass","-p","x"],`,
		"sshpass tokens":    "(sshpass (a) ",
		"sshpass slashes":   `sshpass \\\\\\\\`,
		"sshpass -P":        "sshpass -P ",
		"reference urls":    "://a:$A@h,",
		"reference url":     "postgres://a:$A@h,",
		"quoted references": `postgres://a:${ABCD}@h"`,
		"quoted wide":       `postgres://a:$A@h"`,
		"mysql -sNp":        "mysql -sNp",
		"mysql cluster":     "mysql -sNBEHXtvqfnrACb ",
		"mysql_pwd colon":   "MYSQL_PWD: ",
		"mysql_pwd quoted":  `"MYSQL_PWD": "`,
		"mysql_pwd comma":   `"MYSQL_PWD", `,
		"odd quote":         `mysql -p"a" `,
		"json quote":        `{"c":"mysql -p"x`,
	} {
		redactR3Linear(t, name, func(n int) string { return strings.Repeat(unit, n/len(unit)) })
	}
}
