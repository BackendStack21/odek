package danger

import (
	"testing"
)

// `hash -p FILE NAME` binds NAME in the shell's command table, and every
// builtin that looks its operand up as a command reads that table: `exec
// NAME`, `command exec NAME`, `exec command NAME`, `builtin command NAME`
// all run FILE in bash. The binding was only resolved for a bare NAME or
// directly behind `command`, so the same binding used behind exec or a
// chain of lookup builtins classified as the bound name (safe).
//
// Correct behaviour: every position where the shell itself looks a word up
// as a command resolves the binding; external wrappers (env, nohup, nice,
// xargs), which search PATH on their own, do not.
func TestRED_HashBindingResolvedBehindLookupBuiltinChains(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	cfg := &DangerousConfig{}
	for _, cmd := range []string{
		"hash -p ./p ls; exec ls",
		"hash -p ./p ls; builtin command ls",
		"hash -p ./p ls; command exec ls",
		"hash -p ./p ls; exec command ls",
		"hash -p ./p ls; exec -c ls",
		"hash -p ./p ls; exec -l ls",
		"hash -p ./p ls; exec -cl -- ls",
		"hash -p ./p ls; exec -a name ls",
		"hash -p ./p ls; builtin exec ls",
		"hash -p ./p ls; builtin builtin exec -c ls",
		"hash -p ./p ls; command -- exec -- ls",
		"hash -p ./p ls; command -p exec ls",
		"hash -p ./p ls; X=1 exec ls",
		"hash -p ./p ls; time command exec ls",
		"hash -p ./p ls; ! exec ls",
		"hash -p ./p ls; exec builtin command ls -la",
		"hash ls=./p; exec ls",
		"f() { hash -p ./p ls; }; f; exec command ls",
		"hash -p \"$(command -v rm)\" ls; exec ls -rf ~",
	} {
		if got, action := Classify(cmd), cfg.ActionForCommand(cmd); Rank(got) < Rank(CodeExecution) || action == Allow {
			t.Errorf("%q: class=%s action=%s; the shell looks the operand up in its command table and runs the bound file", cmd, got, action)
		}
	}
}

// External wrappers search PATH themselves, a lookup (`command -v`) runs
// nothing, and `builtin NAME` only runs builtins: none of them reads the
// command table, so their verdicts are unchanged by a binding.
func TestHashBindingExternalWrappersAndLookupsUnchanged(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	for _, tc := range []struct{ cmd, plain string }{
		{"hash -p ./p ls; nohup ls", "nohup ls"},
		{"hash -p ./p ls; env ls", "env ls"},
		{"hash -p ./p ls; nice ls", "nice ls"},
		{"hash -p ./p ls; xargs ls", "xargs ls"},
		{"hash -p ./p ls; command -v ls", "command -v ls"},
		{"hash -p ./p ls; command -pV ls", "command -pV ls"},
		{"hash -p ./p ls; builtin ls", "builtin ls"},
		{"hash -p ./p ls; exec echo ls", "exec echo ls"},
		{"hash -p ./p ls; exec command echo ls", "exec command echo ls"},
		{"exec ls", "ls"},
		{"exec command ls", "ls"},
	} {
		if got, want := Classify(tc.cmd), Classify(tc.plain); got != want {
			t.Errorf("%q classified %s, want %s (the verdict of %q)", tc.cmd, got, want, tc.plain)
		}
	}
}
