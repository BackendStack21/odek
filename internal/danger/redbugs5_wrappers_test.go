package danger

import "testing"

// wrAction evaluates cmd with the stock policy (no allow/deny lists, default
// per-class actions), exactly as a policy caller does.
func wrAction(cmd string) Action {
	return (&DangerousConfig{}).ActionForCommand(cmd)
}

// `env -S STRING` (and `--split-string`) splits STRING into the command to
// run, so the payload is a real command line. unwrapWrappers consumes the
// value as an ignorable flag argument and isEnvironmentDump then sees "a
// flag-only env" and reports system_write, so:
//
//	env -S 'rm -rf /'          -> system_write (prompt)   want destructive (deny)
//	env --split-string='rm -rf /' -> system_write (prompt) want deny
//	env -S'rm -rf /'           -> safe (allow!)           want deny
//	env -S'LD_PRELOAD=/tmp/x.so ls' -> safe (allow!)      want not-allow
//
// A real `env -S'rm -rf /'` runs `rm -rf /`; the fused spelling is a single
// "-S…" token that unwrapWrappers just skips as an unknown flag, leaving no
// inner command at all (Safe).
func TestRED_EnvSplitStringPayloadIsClassified(t *testing.T) {
	deny := []string{
		"env -S 'rm -rf /'",
		"env -S\"rm -rf /\"",
		"env -S'rm -rf /'",
		"env --split-string='rm -rf /'",
		"env --split-string 'rm -rf /'",
		"env -i -S 'rm -rf /'",
		"env -S 'dd if=/dev/zero of=/dev/sda'",
	}
	for _, c := range deny {
		if got := wrAction(c); got != Deny {
			t.Errorf("ActionForCommand(%q) = %s (class %s); env -S payload is a real command, want deny", c, got, Classify(c))
		}
	}
	c := "env -S'LD_PRELOAD=/tmp/x.so ls'"
	if got := wrAction(c); got == Allow {
		t.Errorf("ActionForCommand(%q) = allow (class %s); env -S can inject LD_PRELOAD", c, Classify(c))
	}
}

// isEnvironmentDump / builtinEnvDump look only at the raw head token, so a
// dump behind any wrapper or assignment prefix is classified by the inner
// verb alone (`env` itself is a "safe command"):
//
//	env                -> system_write (prompt)
//	FOO=1 env          -> safe (allow)      nohup env -> safe     timeout 5 env -> safe
//	env env / env FOO=1 env -> safe         FOO=1 export -p -> safe
//
// Same output, same secrets: all must prompt like bare `env`. Also bare
// `export`, `declare`, `typeset` list every exported / shell variable (the
// same data as `export -p`, which is flagged) but classify as safe.
func TestRED_EnvironmentDumpBehindWrapperOrBareBuiltin(t *testing.T) {
	if wrAction("env") == Allow {
		t.Fatal("precondition: bare env must not be auto-allowed")
	}
	cmds := []string{
		"FOO=1 env",
		"nohup env",
		"time env",
		"nice env",
		"timeout 5 env",
		"setsid env",
		"stdbuf -oL env",
		"env env",
		"env FOO=1 env",
		"env -i env",
		"FOO=1 export -p",
		"nohup env | grep KEY",
		"timeout 5 env | grep KEY",
		// bare builtins that print every (exported) variable
		"export",
		"declare",
		"typeset",
	}
	for _, c := range cmds {
		if got := wrAction(c); got == Allow {
			t.Errorf("ActionForCommand(%q) = allow (class %s); dumps the full environment, want prompt like bare `env`", c, Classify(c))
		}
	}
}

// `export NAME=value` / `declare -x NAME=value` set the variable for every
// later command in the same shell line, but only LEADING assignments (and
// env's) go through envAssignmentRisk. `export` is a plain safe builtin, so:
//
//	LD_PRELOAD=/tmp/x.so; ls          -> system_write
//	export LD_PRELOAD=/tmp/x.so; ls   -> safe (allow)
//
// The exported LD_PRELOAD / PATH / pager / NODE_OPTIONS reach `ls`, `git`,
// `node` exactly as a prefix assignment would.
func TestRED_ExportedExecEnvVarsEscalate(t *testing.T) {
	cmds := []string{
		"export LD_PRELOAD=/tmp/x.so; ls",
		"export LD_PRELOAD=/tmp/x.so && cat x",
		"export PATH=/tmp/evil:$PATH; ls",
		"export GIT_PAGER='sh -c id'; git log",
		"export PAGER='sh -c id'; man ls",
		"export NODE_OPTIONS=--require=/tmp/x; node -v",
		"export ENV=/tmp/x; sh",
		"declare -x LD_PRELOAD=/tmp/x.so; ls",
		"typeset -x LD_PRELOAD=/tmp/x.so; ls",
	}
	for _, c := range cmds {
		if got := wrAction(c); got == Allow {
			t.Errorf("ActionForCommand(%q) = allow (class %s); exported exec-env var must escalate like a prefix assignment", c, Classify(c))
		}
	}
}

// An argv composer's inner verb is checked against xargsDangerousVerb using
// the FIRST token after xargs. Any execution wrapper in between hides the
// real verb, so the fail-closed "payload unknown" rule never fires:
//
//	cat x | xargs rm -rf             -> unknown (deny)
//	cat x | xargs nohup rm -rf       -> local_write (allow!)
//	cat x | xargs timeout 5 rm -rf   -> local_write (allow!)
//	xargs -a paths env rm -rf        -> local_write (allow!)
//	cat x | xargs sudo rm -rf        -> system_write (prompt; sibling denies)
//
// Real xargs execs the wrapper, which execs rm with whatever paths the file
// lists. classifyArgvComposerSink / classifyXargsFileInput must unwrapWrappers
// the inner command before calling xargsDangerousVerb.
func TestRED_XargsInnerVerbBehindWrapperFailsClosed(t *testing.T) {
	if wrAction("cat x | xargs rm -rf") != Deny {
		t.Fatal("precondition: unwrapped sibling must be denied")
	}
	cmds := []string{
		"cat x | xargs nohup rm -rf",
		"cat x | xargs timeout 5 rm -rf",
		"cat x | xargs nice rm -rf",
		"cat x | xargs time rm -rf",
		"cat x | xargs command rm -rf",
		"cat x | xargs env rm -rf",
		"cat x | xargs -I{} nohup rm {}",
		"cat x | xargs sudo rm -rf",
		"xargs -a paths env rm -rf",
		"xargs -a paths nohup rm -rf",
	}
	for _, c := range cmds {
		if got := wrAction(c); got != Deny {
			t.Errorf("ActionForCommand(%q) = %s (class %s); undeterminable xargs input to rm must be denied like the unwrapped form", c, got, Classify(c))
		}
	}
}

// staticPipePayload hands the raw argument text of `printf` / `echo -e` to
// the composer/shell classifier WITHOUT interpreting backslash escapes, but
// both programs decode them before the sink sees the bytes:
//
//	printf '\x2f\n' | xargs rm -rf  -> local_write (allow!)   real: rm -rf /
//	printf '\057\n' | xargs rm -rf  -> local_write (allow!)
//	echo -e '\x2f' | xargs rm -rf   -> local_write (allow!)
//	printf 'a\n/\n' | xargs rm -rf  -> local_write (allow!)   real: rm -rf a /
//	printf 'ls\nrm -rf /\n' | sh    -> code_execution         real: two commands, second is destructive
//	printf '\x72m -rf /\n' | sh     -> code_execution         real: rm -rf /
//
// The literal text "\x2f" is classified as a harmless relative filename.
// A payload containing a backslash must either be decoded or treated as not
// statically determinable (fail closed -> unknown/deny).
func TestRED_PrintfEscapesInStaticPipePayload(t *testing.T) {
	cmds := []string{
		`printf '\x2f\n' | xargs rm -rf`,
		`printf '\057\n' | xargs rm -rf`,
		`printf '%b' '\057' | xargs rm -rf`,
		`echo -e '\x2f' | xargs rm -rf`,
		`printf 'a\n/\n' | xargs rm -rf`,
		`printf 'a\0/\0' | xargs -0 rm -rf`,
		`printf 'ls\nrm -rf /\n' | sh`,
		`printf 'ls\nrm -rf /\n' | bash`,
		`echo -e 'ls\nrm -rf /' | bash`,
		`printf '\x72m -rf /\n' | sh`,
		`printf 'rm\t-rf\t/\n' | sh`,
	}
	for _, c := range cmds {
		if got := wrAction(c); got != Deny {
			t.Errorf("ActionForCommand(%q) = %s (class %s); decoded payload is `rm -rf /`, want deny", c, got, Classify(c))
		}
	}
}

// classifyStage extracts the shell payload with flagArg(tokens, "-c"), which
// only matches the exact token "-c". Fused short-flag clusters that end in c
// (-ec, -xc, -lc, -ic) carry the same payload but only earn the generic
// "script operand" code_execution (prompt) instead of classifying the payload:
//
//	bash -c 'rm -rf /'    -> destructive (deny)
//	bash -ec 'rm -rf /'   -> code_execution (prompt)
//	sh -ec / bash -lc / bash -xc / zsh -fc ... same
//
// `bash -lc` and `sh -ec` are among the most common agent spellings, so a
// destructive payload is downgraded from deny to a prompt.
func TestRED_ShellFusedCFlagPayloadIsClassified(t *testing.T) {
	if wrAction("bash -c 'rm -rf /'") != Deny {
		t.Fatal("precondition: bash -c payload is denied")
	}
	cmds := []string{
		"bash -ec 'rm -rf /'",
		"bash -xc 'rm -rf /'",
		"bash -lc 'rm -rf /'",
		"bash -ic 'rm -rf /'",
		"sh -ec 'rm -rf /'",
		"zsh -fc 'rm -rf /'",
		"env bash -ec 'rm -rf /'",
		"nohup bash -ec 'rm -rf /'",
		"timeout 5 bash -ec 'rm -rf /'",
		"sudo bash -ec 'rm -rf /'",
		"echo x | xargs -I{} bash -ec 'rm -rf /'",
	}
	for _, c := range cmds {
		if got := wrAction(c); got != Deny {
			t.Errorf("ActionForCommand(%q) = %s (class %s); fused -c payload is `rm -rf /`, want deny", c, got, Classify(c))
		}
	}
}

// GIT_CONFIG_COUNT / GIT_CONFIG_KEY_<n> / GIT_CONFIG_VALUE_<n> (git >= 2.31)
// inject arbitrary config exactly like `git -c`, which the classifier already
// treats as code execution. Only GIT_CONFIG_PARAMETERS/GLOBAL/SYSTEM are in
// envExecNames, so:
//
//	GIT_SSH_COMMAND='sh -c id' git fetch                 -> system_write (prompt)
//	GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.sshCommand
//	  GIT_CONFIG_VALUE_0='sh -c id' git fetch origin     -> network_egress (allow!)
//	... core.pager / git log                             -> safe (allow!)
//
// core.sshCommand, core.pager, core.fsmonitor, alias.*, credential.helper
// all execute the value.
func TestRED_GitConfigCountEnvInjectionEscalates(t *testing.T) {
	cmds := []string{
		"GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.sshCommand GIT_CONFIG_VALUE_0='sh -c id' git fetch origin",
		"GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.pager GIT_CONFIG_VALUE_0='sh -c id' git log",
		"GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=credential.helper GIT_CONFIG_VALUE_0='!sh -c id' git ls-remote origin",
		"env GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.pager GIT_CONFIG_VALUE_0=/tmp/x git log",
		"GIT_CONFIG_KEY_0=core.pager GIT_CONFIG_VALUE_0=/tmp/x GIT_CONFIG_COUNT=1 git branch",
	}
	for _, c := range cmds {
		if got := wrAction(c); got == Allow {
			t.Errorf("ActionForCommand(%q) = allow (class %s); GIT_CONFIG_COUNT/KEY_n/VALUE_n injects executable git config, want prompt", c, Classify(c))
		}
	}
}

// envExecNames misses other variables whose value (or pointed-to file) is
// executed by a very common, otherwise-"safe" wrapped command. None of the
// values contains a character that assignmentValueArmed recognises:
//
//	JAVA_TOOL_OPTIONS=-javaagent:/tmp/x.jar java -version  -> safe (loads the agent jar even for -version)
//	_JAVA_OPTIONS / JDK_JAVA_OPTIONS                       -> safe (same)
//	LESSOPEN=/tmp/x less f / LESSCLOSE=...                 -> safe (less popen()s the value)
//	SSH_ASKPASS=/tmp/x SSH_ASKPASS_REQUIRE=force ssh host  -> network_egress (allow; GIT_ASKPASS is listed)
//	GCONV_PATH=/tmp/g iconv -f a -t b f                    -> safe (glibc dlopen()s gconv modules from it)
//
// All are the same class as the already-listed NODE_OPTIONS / PERL5OPT /
// RUBYOPT / GIT_ASKPASS entries and should reach system_write.
func TestRED_MoreCodeLoadingEnvVarsEscalate(t *testing.T) {
	cmds := []string{
		"JAVA_TOOL_OPTIONS=-javaagent:/tmp/x.jar java -version",
		"_JAVA_OPTIONS=-javaagent:/tmp/x.jar java -version",
		"JDK_JAVA_OPTIONS=-javaagent:/tmp/x.jar java -version",
		"LESSOPEN=/tmp/x less README.md",
		"LESSCLOSE='/tmp/x %s %s' less README.md",
		"SSH_ASKPASS=/tmp/x SSH_ASKPASS_REQUIRE=force ssh host",
		"GCONV_PATH=/tmp/g iconv -f x -t y README.md",
	}
	for _, c := range cmds {
		if got := wrAction(c); got == Allow {
			t.Errorf("ActionForCommand(%q) = allow (class %s); env var loads/executes attacker code, want prompt", c, Classify(c))
		}
	}
}

// `man` is a safe command, but -P/--pager runs its value through `sh -c`.
// The environment spelling is already escalated, the flag spelling is not:
//
//	MANPAGER='sh -c id' man ls     -> system_write (prompt)
//	man -P 'sh -c id' ls           -> safe (allow!)
//	man --pager='sh -c id' ls      -> safe (allow!)
//
// (`git --paginate -c core.pager=…` is likewise already code_execution.)
func TestRED_ManPagerFlagIsCodeExecution(t *testing.T) {
	if wrAction("MANPAGER='sh -c id' man ls") == Allow {
		t.Fatal("precondition: MANPAGER spelling must prompt")
	}
	cmds := []string{
		"man -P 'sh -c id' ls",
		"man -Psh ls",
		"man --pager='sh -c id' ls",
		"man --pager 'sh -c id' ls",
	}
	for _, c := range cmds {
		if got := wrAction(c); got == Allow {
			t.Errorf("ActionForCommand(%q) = allow (class %s); -P/--pager value is executed via sh -c, want prompt", c, Classify(c))
		}
	}
}

// False positive on an extremely common idiom. `command` is in execWrappers
// and unwrapWrappers skips the `-v` flag, leaving the looked-up NAME as the
// "inner command". Every NAME is an unrecognised program, so the lookup is
// Unknown (deny): `command -v git`, `command -v docker >/dev/null && …`.
// `command -v/-V` only prints how the name resolves (like `type` / `which`,
// which classify safe) and never executes it.
func TestRED_CommandDashVLookupIsNotUnknown(t *testing.T) {
	cmds := []string{
		"command -v git",
		"command -v node",
		"command -V ls",
		"command -v docker >/dev/null && echo yes",
		"command -v rm",
	}
	for _, c := range cmds {
		if got := wrAction(c); got != Allow {
			t.Errorf("ActionForCommand(%q) = %s (class %s); `command -v` is a lookup like `type`/`which`, want allow", c, got, Classify(c))
		}
	}
}
