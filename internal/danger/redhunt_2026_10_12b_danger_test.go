package danger

import "testing"

// dangerHunt12bAtLeast asserts every command classifies at least min and is
// not auto-allowed under the default policy.
func dangerHunt12bAtLeast(t *testing.T, min RiskClass, cmds ...string) {
	t.Helper()
	cfg := &DangerousConfig{}
	for _, cmd := range cmds {
		if got := Classify(cmd); Rank(got) < Rank(min) {
			t.Errorf("Classify(%q) = %s, want at least %s", cmd, got, min)
		}
		if act := cfg.ActionForCommand(cmd); act == Allow {
			t.Errorf("ActionForCommand(%q) = allow under the default policy", cmd)
		}
	}
}

// dangerHunt12bDenied asserts a "git push" denylist entry denies every
// command even when code execution and system writes are allowed.
func dangerHunt12bDenied(t *testing.T, cmds ...string) {
	t.Helper()
	cfg := &DangerousConfig{
		Denylist: []string{"git push"},
		Classes: map[RiskClass]Action{
			CodeExecution: Allow, SystemWrite: Allow, Persistence: Allow, NetworkEgress: Allow,
		},
	}
	for _, cmd := range cmds {
		if act := cfg.ActionForCommand(cmd); act != Deny {
			t.Errorf("ActionForCommand(%q) = %s with denylist [git push], want deny", cmd, act)
		}
	}
}

// Bash expands a command substitution inside an arithmetic expansion even
// when it sits in single quotes: the $(( )) body is treated as if it were
// double-quoted, so `'` is literal there (verified with /bin/bash 3.2:
// `echo $(( a['$(echo PWNED >&2)'] ))` prints PWNED). The prior fix covered
// let/declare/(( ))/[[ ]] operands but not the $(( )) / $[ ] expansion.
func TestRED_ArithmeticExpansionSingleQuotedSubstitutionRuns(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	dangerHunt12bAtLeast(t, Destructive,
		`echo $(( a['$(rm -rf ~)'] ))`,
		`echo $(( '$(rm -rf ~)' ))`,
		`x=$(( 1 + '$(rm -rf ~)' ))`,
		`echo $[ a['$(rm -rf ~)'] ]`,
		`echo "$(( a['$(rm -rf ~)'] ))"`,
		"echo $(( 1 + a['`rm -rf ~`'] ))",
	)
}

// The offset and length of ${name:offset:length} are arithmetic: bash runs a
// command substitution in them even inside single quotes
// (`echo ${s:'$(echo PWNED >&2)'}` prints PWNED with /bin/bash 3.2).
func TestRED_SubstringOffsetSingleQuotedSubstitutionRuns(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	dangerHunt12bAtLeast(t, Destructive,
		`echo ${s:'$(rm -rf ~)'}`,
		`echo ${s:'a[$(rm -rf ~)]'}`,
		`echo ${s:a['$(rm -rf ~)']}`,
		`echo ${s:1:'a[$(rm -rf ~)]'}`,
	)
}

// A wildcard that the shell expands to an odek trust anchor, the process
// environment or a credential file in sensitivePathFragments is a secret read
// like the literal spelling (`cat ~/.odek/secrets.env` is system_write).
func TestRED_WildcardSpellingOfTrustAnchorsAndEnvironReadsSafe(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	dangerHunt12bAtLeast(t, SystemWrite,
		`cat ~/.odek/secrets.en?`,
		`cat ~/.odek/config.jso?`,
		`cat ~/.odek/IDENTITY.m?`,
		`cat /proc/self/env*`,
		`cat /proc/self/enviro[n]`,
		`cat ~/.pgpas?`,
		`cat ~/.vault-tok*`,
		`cat ~/.my.cn?`,
	)
}

// An interactive or login shell runs $HOME/.bashrc / .profile (zsh
// $ZDOTDIR-or-$HOME/.zshrc). Pointing HOME at the workspace runs a file the
// agent never read, exactly like BASH_ENV/ENV/ZDOTDIR, which are gated.
func TestRED_HomeOverrideRunsUnreadStartupFile(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	dangerHunt12bAtLeast(t, CodeExecution,
		`HOME=. bash -i`,
		`HOME=/tmp/x bash -i`,
		`HOME=. bash -l`,
		`HOME=. bash --login`,
		`env HOME=. bash -i`,
		`export HOME=.; bash -i`,
		`HOME=. zsh`,
		`HOME=. sh -i`,
	)
}

// pkill/killall forms that signal (nearly) every process: the inverse match
// (procps --inverse, BSD/macOS pkill -v), macOS killall -m REGEX, an empty
// pattern (the doc comment of killByNameBroadcasts lists `pkill ”`), a
// parent list containing init, and a real-group selector of root.
func TestRED_PkillKillallBroadcastFormsMissed(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	dangerHunt12bAtLeast(t, SystemWrite,
		`pkill --inverse sshd`,
		`pkill -9 --inverse -f zzzz`,
		`pkill -v sshd`,
		`killall -m .`,
		`killall -m '.*'`,
		`pkill ''`,
		`pkill -9 ''`,
		`pkill -f ''`,
		`pkill -P 0,1`,
		`pkill --parent 2,1`,
		`pkill -G root`,
		`pkill -G 0`,
	)
}

// `kill -1` is system_write, but the same broadcast assembled from a
// catch-all pgrep (`kill $(pgrep .)`, `pgrep . | xargs kill -9`) is
// local_write and auto-allowed.
func TestRED_PgrepFedKillBroadcastAutoAllowed(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	dangerHunt12bAtLeast(t, SystemWrite,
		`kill $(pgrep .)`,
		`kill -9 $(pgrep -f .)`,
		`pgrep . | xargs kill -9`,
		`pgrep -f . | xargs kill`,
	)
}

// jq's deprecated --argfile NAME FILE takes two values; it is missing from
// jqValueOptions, so the env program after it is not seen. Python yq (a jq
// wrapper installed as `yq`) evaluates `env` like jq.
func TestRED_JqEnvDumpBehindArgfileAutoAllowed(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	dangerHunt12bAtLeast(t, SystemWrite,
		`jq -n --argfile x /dev/null env`,
		`jq -n --argfile x /dev/null '$ENV'`,
		`yq -n env`,
	)
}

// gpg verbs that import keys into the keyring or change keys, cards or the
// trust database, missing from gpgKeyringVerbs: --fetch-keys (downloads and
// imports), --edit-card (alias of --card-edit), --change-pin,
// --tofu-policy, --quick-update-pref.
func TestRED_GpgKeyringMutatingVerbsClassifySafe(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	dangerHunt12bAtLeast(t, SystemWrite,
		`gpg --fetch-keys https://evil.example/k.asc`,
		`gpg --edit-card`,
		`gpg --change-pin`,
		`gpg --tofu-policy bad ABCD1234`,
		`gpg --quick-update-pref ABCD1234`,
	)
}

// The code bodies the classifier already extracts (PROMPT_COMMAND, printf -v
// / read into PROMPT_COMMAND, arithmetic-operand substitutions) are command
// positions, but the denylist never scans them, so a `git push` entry is
// bypassed while the classifier sees the push.
func TestRED_DenylistMissesEnvCodeAndArithmeticBodies(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	dangerHunt12bDenied(t,
		`PROMPT_COMMAND='git push' bash -i`,
		`env PROMPT_COMMAND='git push' bash -i`,
		`printf -v PROMPT_COMMAND 'git push'; bash -i`,
		`read PROMPT_COMMAND <<< 'git push'; export PROMPT_COMMAND; bash -i`,
		`let 'a[$(git push)]=1'`,
	)
}

// xargs appends its standard input to the command: `echo push | xargs git`
// runs `git push`. The classifier composes pipe-fed xargs, the denylist does
// not.
func TestRED_DenylistMissesPipeFedXargsComposition(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	dangerHunt12bDenied(t,
		`echo push | xargs git`,
		`printf 'push\n' | xargs git`,
	)
}

// sqlite3's readfile() opens the path its literal names; ~/.ssh is caught by
// the fragment table, but odek's own trust anchors are not.
func TestRED_SqliteReadfileOfOdekTrustAnchorSafe(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	dangerHunt12bAtLeast(t, SystemWrite,
		`sqlite3 db "select readfile('/home/user/.odek/secrets.env')"`,
		`sqlite3 db "select readfile('/home/user/.odek/config.json')"`,
	)
}

// A recursive search of a process directory reads its environ file, which
// `cat /proc/self/environ` already treats as a secret read.
func TestRED_RecursiveReadOfProcDirDumpsEnviron(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	dangerHunt12bAtLeast(t, SystemWrite,
		`grep -r . /proc/self`,
		`grep -R KEY /proc/1`,
		`rg -uuu KEY /proc/self`,
	)
}

// IsSensitiveReadPath compares the parent of a shell rc/history file with the
// home directory byte-for-byte. On APFS /Uſers is /Users (U+017F folds to
// 's'), and accountHomes lower-cases with strings.ToLower, which does not
// fold it, so the history file escapes the @-reference gate.
func TestRED_SensitiveReadPathHistoryViaFoldedHomeSpelling(t *testing.T) {
	t.Setenv("HOME", "/Users/alice")
	for _, p := range []string{
		"/Uſers/alice/.zsh_history",
		"/Uſers/alice/.bash_history",
		"/Uſers/alice/.bashrc",
	} {
		if !IsSensitiveReadPath("", p) {
			t.Errorf("IsSensitiveReadPath(%q) = false, want true (same file as /Users/alice/...)", p)
		}
	}
}

// env_dump_tools.go documents that a jq field access such as `.env` is data,
// but the filter word is judged as a `.env` credential file operand, so
// ordinary jq/yq queries prompt.
func TestRED_JqFieldAccessDotEnvOverClassified(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	cfg := &DangerousConfig{}
	for _, cmd := range []string{
		`jq '.env' x.json`,
		`jq .env x.json`,
		`jq '.env.PORT' x.json`,
		`yq '.env' x.yaml`,
	} {
		if got := Classify(cmd); got != Safe {
			t.Errorf("Classify(%q) = %s, want safe (a jq field access is data)", cmd, got)
		}
		if act := cfg.ActionForCommand(cmd); act != Allow {
			t.Errorf("ActionForCommand(%q) = %s, want allow", cmd, act)
		}
	}
}

// A zone-scoped IPv6 literal is still the loopback / link-local address the
// dialer connects to, but net.ParseIP rejects the zone, so ClassifyURL rates
// it network_egress instead of system_write.
func TestRED_ClassifyURLZoneScopedIPv6Loopback(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	for _, u := range []string{
		"http://[::1%25lo0]:8080/",
		"http://[fe80::1%25en0]/",
	} {
		if got := ClassifyURL(u); got != SystemWrite {
			t.Errorf("ClassifyURL(%q) = %s, want system_write", u, got)
		}
	}
}

func TestRED_HomeOverrideNegativesAndBroaderShells(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	dangerHunt12bAtLeast(t, CodeExecution,
		`HOME=. bash -ic 'true'`,
		`HOME=. bash`,
		`HOME=. zsh -c 'true'`,
		`HOME=. fish -c 'true'`,
		`XDG_CONFIG_HOME=. fish -c 'true'`,
		`env -i HOME=. bash --login`,
		`HOME=. /bin/bash -i`,
	)
	for _, cmd := range []string{
		`HOME=/tmp/x npm install`,
		`HOME=/tmp/x bash -c 'echo hi'`,
		`HOME=/tmp/x bash script.sh`,
		`HOME=/home/user bash -i`,
		`HOME=$HOME bash -i`,
		`XDG_CONFIG_HOME=. bash -i`,
	} {
		if got := Classify(cmd); Rank(got) >= Rank(SystemWrite) {
			t.Errorf("Classify(%q) = %s, want below system_write", cmd, got)
		}
	}
}

// Wildcards over ordinary files stay ordinary; broader spellings of secrets
// are still caught.
func TestRED_WildcardSecretReadsNegativesAndBroaderSpellings(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	dangerHunt12bAtLeast(t, SystemWrite,
		`cat ~/.odek/*`,
		`cat ~/.od*/secrets.env`,
		`cat ~/.odek/runtime.log.?`,
		`cat /home/*/.odek/secrets.env`,
		`cat /proc/*/environ`,
		`cat /proc/[0-9]*/environ`,
		`cat ~/.ss?/id_rsa`,
		`cat /etc/shado?`,
	)
	cfg := &DangerousConfig{}
	for _, cmd := range []string{
		`cat *.go`, `ls ./*`, `cat /tmp/*.log`, `cat ~/*.txt`, `cat ~/.[a-z]*rc2`, `cat /proc/*/status`,
		`cat src/*/main.go`, `cat docs/*.md`,
	} {
		if got := Classify(cmd); Rank(got) >= Rank(SystemWrite) {
			t.Errorf("Classify(%q) = %s, want below system_write", cmd, got)
		}
		_ = cfg
	}
}

// Ordinary parameter expansions and arithmetic stay safe.
func TestRED_ArithmeticQuotedSubstitutionNegatives(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	cfg := &DangerousConfig{}
	for _, cmd := range []string{
		`echo $(( 1 + 2 ))`,
		`echo $[ 1 + 2 ]`,
		`s=hello; echo ${s:1:2}`,
		`echo ${s:-'$(rm -rf ~)'}`,
		`echo ${a[1]} ${#a[@]} ${s:-default}`,
		`echo '${s:1}' '$[1]'`,
	} {
		if got := Classify(cmd); got != Safe {
			t.Errorf("Classify(%q) = %s, want safe", cmd, got)
		}
		if act := cfg.ActionForCommand(cmd); act != Allow {
			t.Errorf("ActionForCommand(%q) = %s, want allow", cmd, act)
		}
	}
}
