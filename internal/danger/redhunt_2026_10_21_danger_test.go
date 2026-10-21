package danger

import "testing"

// hunt21Action evaluates cmd with the stock policy.
func hunt21Action(cmd string) Action {
	return (&DangerousConfig{}).ActionForCommand(cmd)
}

// hunt21CodeExec asserts every command classifies as at least code_execution
// and is not auto-allowed under the default policy.
func hunt21CodeExec(t *testing.T, cmds ...string) {
	t.Helper()
	for _, cmd := range cmds {
		if got := Classify(cmd); Rank(got) < Rank(CodeExecution) {
			t.Errorf("Classify(%q) = %s, want at least code_execution", cmd, got)
		}
		if act := hunt21Action(cmd); act == Allow {
			t.Errorf("ActionForCommand(%q) = allow under the default policy", cmd)
		}
	}
}

// hunt21Allowed asserts a benign form stays auto-allowed, so the escalations
// below do not over-fire on ordinary use.
func hunt21Allowed(t *testing.T, cmds ...string) {
	t.Helper()
	for _, cmd := range cmds {
		if act := hunt21Action(cmd); act != Allow {
			t.Errorf("ActionForCommand(%q) = %s, want allow", cmd, act)
		}
	}
}

// ssh-keygen loads attacker-chosen native code through two option values:
// -D <lib> enumerates keys in a PKCS#11 shared library, and -w <lib> selects
// a FIDO middleware library for sk-* key generation. Both run the module's
// initializer on load, a classic GTFOBins escape. ssh-keygen is a write
// prefix, so the forms otherwise read as local_write and auto-allowed.
func TestRED_SSHKeygenSharedLibraryLoadIsCodeExecution(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	hunt21CodeExec(t,
		`ssh-keygen -D /tmp/evil.so`,
		`ssh-keygen -D/tmp/evil.so`,
		`ssh-keygen -D pkcs11.so -e`,
		`ssh-keygen -w /tmp/evil.so -t ed25519-sk`,
		`ssh-keygen -w/tmp/evil.so`,
	)
	// Ordinary key generation and inspection stay allowed.
	hunt21Allowed(t,
		`ssh-keygen -t ed25519 -f key -N ''`,
		`ssh-keygen -l -f key.pub`,
		`ssh-keygen -y -f key`,
	)
}

// openssl loads a shared library into its own process through -engine,
// -provider and -provider-path; the ENGINE / OSSL provider module executes
// native code on load. openssl is a network prefix, so these forms otherwise
// fell through to network_egress and auto-allow.
func TestRED_OpenSSLEngineProviderLoadIsCodeExecution(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	hunt21CodeExec(t,
		`openssl req -engine /tmp/x.so`,
		`openssl req -engine=/tmp/x.so`,
		`openssl dgst -provider /tmp/x.so -sha256 f`,
		`openssl dgst -provider-path /tmp/lib -provider x -sha256 f`,
	)
	// Local digest/encode and metadata forms stay allowed.
	hunt21Allowed(t,
		`openssl dgst -sha256 file`,
		`openssl version`,
		`openssl rand -hex 16`,
	)
}

// pandoc runs attacker-chosen code through two option values: --filter/-F
// runs an arbitrary external program as a JSON filter, and --lua-filter runs
// a Lua script in pandoc's interpreter (which exposes os.execute). pandoc is
// a write prefix, so these forms otherwise read as local_write and
// auto-allowed.
func TestRED_PandocFilterIsCodeExecution(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	hunt21CodeExec(t,
		`pandoc --lua-filter=/tmp/x.lua a.md`,
		`pandoc --lua-filter /tmp/x.lua a.md`,
		`pandoc --filter=/tmp/evil a.md -o b.html`,
		`pandoc --filter /tmp/evil a.md`,
		`pandoc -F /tmp/evil a.md`,
	)
	// Ordinary conversion stays allowed.
	hunt21Allowed(t,
		`pandoc a.md -o b.pdf`,
		`pandoc -s a.md -o b.html`,
		`pandoc --version`,
	)
}

// pip wheel / pip download build the requested project from source, running
// its setup.py or PEP 517 build backend — the same arbitrary-code exposure as
// pip install. They are absent from the install-verb switch, so without a
// dedicated rule they fell through to Safe (auto-allow) while `pip install .`
// prompted.
func TestRED_PipBuildFromSourceRunsCode(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	// Classified Install (prompt), the same class as `pip install .`; the
	// point is that they are no longer auto-allowed.
	for _, cmd := range []string{
		`pip wheel .`,
		`pip wheel evilpkg`,
		`pip download .`,
		`pip download evilpkg`,
		`pip3 wheel .`,
		`pip3 download .`,
	} {
		if got := Classify(cmd); Rank(got) < Rank(Install) {
			t.Errorf("Classify(%q) = %s, want at least install", cmd, got)
		}
		if act := hunt21Action(cmd); act == Allow {
			t.Errorf("ActionForCommand(%q) = allow under the default policy", cmd)
		}
	}
	// Read-only pip queries stay allowed.
	hunt21Allowed(t,
		`pip list`,
		`pip show requests`,
		`pip --version`,
		`pip freeze`,
	)
}
