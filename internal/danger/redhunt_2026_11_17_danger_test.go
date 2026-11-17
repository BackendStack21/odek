package danger

import (
	"slices"
	"testing"
)

// hunt1117NotAllowed asserts every command classifies at least min and is
// not auto-allowed under the default policy.
func hunt1117NotAllowed(t *testing.T, min RiskClass, cmds ...string) {
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

// hunt1117Allowed asserts a benign form stays auto-allowed, so the escalations
// do not over-fire on ordinary use.
func hunt1117Allowed(t *testing.T, cmds ...string) {
	t.Helper()
	cfg := &DangerousConfig{}
	for _, cmd := range cmds {
		if act := cfg.ActionForCommand(cmd); act != Allow {
			t.Errorf("ActionForCommand(%q) = %s (%s), want allow", cmd, act, Classify(cmd))
		}
	}
}

// pandoc runs Lua through more spellings than --lua-filter: the short -L
// (alone, fused or at the end of a cluster), any unambiguous abbreviation of a
// long option, a defaults file (whose filters list runs filters), a custom
// reader or writer named by -f/-r/-t/-w (a .lua file), and the `pandoc lua`
// interpreter mode. --pdf-engine-opt hands words to the PDF engine
// (`-shell-escape` lets a LaTeX document run shell commands), and a
// --pdf-engine value only known at run time cannot be vouched for. Each of
// these ran a marker script under pandoc 3.1.3, yet classified local_write
// and auto-allowed.
func TestRED_PandocLuaAndProgramCarriers(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	hunt1117NotAllowed(t, CodeExecution,
		`pandoc -L x.lua a.md`,
		`pandoc -Lx.lua a.md`,
		`pandoc -sLx.lua a.md`,
		`pandoc --lua-f x.lua a.md`,
		`pandoc --filt x a.md`,
		`pandoc -d defaults.yaml a.md`,
		`pandoc --defaults=d.yaml a.md`,
		`pandoc --defaults d.yaml a.md`,
		`pandoc -t w.lua a.md`,
		`pandoc -w w.lua a.md`,
		`pandoc --to=/tmp/writer.lua a.md`,
		`pandoc --write w.lua a.md`,
		`pandoc -f r.lua a.md`,
		`pandoc -r /tmp/reader.lua a.md`,
		`pandoc --from=r.lua+smart a.md`,
		`pandoc lua -e 'os.execute("id")'`,
		`pandoc lua script.lua`,
		`pandoc a.md --pdf-engine-opt=-shell-escape -o b.pdf`,
		`pandoc a.md --pdf-engine-opt -shell-escape -o b.pdf`,
		`pandoc a.md --pdf-engine=$X -o b.pdf`,
		`pandoc a.md --pdf-engine $(cat engine) -o b.pdf`,
	)
	hunt1117Allowed(t,
		`pandoc -f markdown -t html a.md`,
		`pandoc a.md -t gfm -o b.md`,
		`pandoc -s --toc a.md -o a.html`,
		`pandoc -r commonmark+smart a.md`,
		`pandoc -w docx a.md -o b.docx`,
		`pandoc --data-dir=dd a.md -o b.html`,
		`pandoc a.md --pdf-engine xelatex -o b.pdf`,
		`pandoc --list-output-formats`,
	)
}

// pandoc writes the media directory named by --extract-media and the log file
// named by --log. The separate `--extract-media /etc` spelling escalated
// through the operand scan, but the `=` spelling and --log did not, so a log
// or extracted media aimed at a shell rc file or /etc auto-allowed.
func TestRED_PandocWriteOptionTargets(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	hunt1117NotAllowed(t, SystemWrite,
		`pandoc --extract-media=/etc a.docx`,
		`pandoc --log=/home/user/.bashrc a.md`,
		`pandoc --log /etc/cron.d/x a.md`,
	)
	hunt1117Allowed(t,
		`pandoc --extract-media=media a.docx -o a.md`,
		`pandoc --log=pandoc.log a.md -o a.html`,
	)
}

// The openssl engine command loads a shared library itself: `openssl engine
// /path/lib.so` loads the path as the engine id, and -pre/-post control
// commands such as SO_PATH:… LOAD load the dynamic engine from any path. A
// bare engine id (rdrand, dynamic without control commands) and the listing
// forms stay allowed.
func TestRED_OpenSSLEngineCommandLoadsLibrary(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	hunt1117NotAllowed(t, CodeExecution,
		`openssl engine /tmp/e.so`,
		`openssl engine ./e.so`,
		`openssl engine -t -pre SO_PATH:/tmp/e.so -pre LOAD dynamic`,
		`openssl engine dynamic -pre SO_PATH:e.so -pre LOAD`,
		`openssl engine -vvv -post X dynamic`,
		`openssl engine --pre SO_PATH:e.so --pre LOAD dynamic`,
		`openssl engine --t ./e.so`,
	)
	hunt1117Allowed(t,
		`openssl engine`,
		`openssl engine -t -c`,
		`openssl engine rdrand`,
	)
}

// -engine and -provider escalate a path-like value; a value only known at run
// time can be one, so it fails closed. OPENSSL_CONF names a configuration
// whose openssl_conf section activates any provider module, and
// OPENSSL_MODULES / OPENSSL_ENGINES move the directory a bare module name is
// loaded from: each loaded a module from an arbitrary path under OpenSSL 3.0,
// yet `OPENSSL_CONF=… openssl version` auto-allowed.
func TestRED_OpenSSLModuleRuntimeValueAndEnv(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	hunt1117NotAllowed(t, CodeExecution,
		`openssl dgst -engine $E -sha256 f`,
		`openssl dgst -provider "$P" -sha256 f`,
		`openssl dgst -provider ${P} -sha256 f`,
		`openssl dgst -provider $(cat p) -sha256 f`,
		`openssl dgst --engine /tmp/e.so -sha256 f`,
		`openssl dgst --provider=./x.so -sha256 f`,
		`openssl req --config x.cnf -new`,
		`openssl list --provider-path . --provider x`,
	)
	hunt1117NotAllowed(t, SystemWrite,
		`OPENSSL_CONF=/tmp/e.cnf openssl version`,
		`OPENSSL_MODULES=/tmp openssl list -providers -provider x`,
		`OPENSSL_ENGINES=/tmp openssl engine x`,
		`export OPENSSL_CONF=/tmp/e.cnf; openssl dgst -sha256 f`,
		`env OPENSSL_CONF=/tmp/e.cnf openssl dgst -sha256 f`,
	)
	hunt1117Allowed(t,
		`openssl dgst -provider legacy -sha256 f`,
		`openssl dgst --provider legacy -sha256 f`,
		`openssl version`,
	)
}

// ssh-keygen -D and -w take their library path as the rest of a short
// cluster too (`-vD lib.so`, `-qD./lib.so`), which the gate missed. A value
// consumed by an earlier option (`-N -D`, where -D is the passphrase) is not
// a library.
func TestRED_SSHKeygenClusteredLibraryOptions(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	hunt1117NotAllowed(t, CodeExecution,
		`ssh-keygen -vD /tmp/evil.so`,
		`ssh-keygen -qD./evil.so`,
		`ssh-keygen -vw/tmp/e.so -t ed25519-sk`,
		`ssh-keygen -D /tmp/evil.so`,
	)
	hunt1117Allowed(t,
		`ssh-keygen -lf k.pub`,
		`ssh-keygen -t ed25519 -f key -N ''`,
		`ssh-keygen -yf key`,
	)
}

// ssh -I names the PKCS#11 provider library the client loads, the short form
// of `-o PKCS11Provider=`, which was already code_execution. ssh-add -s and -S
// ask the running agent to load a PKCS#11 or security-key provider library.
func TestRED_SSHProviderLibraryShortOptions(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	hunt1117NotAllowed(t, CodeExecution,
		`ssh -I /tmp/evil.so host`,
		`ssh -I./e.so host`,
		`ssh -vI /tmp/e.so host`,
		`ssh host -I /tmp/e.so`,
		`ssh-add -s /tmp/evil.so`,
		`ssh-add -vs /tmp/evil.so`,
		`ssh-add -S /tmp/e.so`,
	)
	hunt1117NotAllowed(t, SystemWrite,
		`SSH_SK_PROVIDER=/tmp/e.so ssh-keygen -t ed25519-sk`,
		`SSH_SK_PROVIDER=/tmp/e.so ssh-add -K`,
		`export SSH_SK_PROVIDER=/tmp/e.so; ssh host`,
	)
	// Words of the remote command are not ssh options: grep's -I on the
	// remote side loads nothing locally.
	hunt1117Allowed(t,
		`ssh-add -l`,
		`ssh-add -L`,
		`ssh -V`,
		`ssh host grep -I foo .`,
		`ssh host ls -I '*.o'`,
	)
}

// Interpreter library search paths decide which module a program written in
// that language imports first: PYTHONPATH (sitecustomize), PYTHONUSERBASE
// (.pth files), PYTHONHOME, PERL5LIB/PERLLIB and RUBYLIB shadow a module the
// program loads at start. A marker module ran under `pip --version`,
// `shasum --version` and `gem --version`, all auto-allowed queries.
func TestRED_InterpreterLibraryPathEnvLoadsCode(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	hunt1117NotAllowed(t, SystemWrite,
		`PYTHONPATH=/tmp/x pip --version`,
		`PYTHONUSERBASE=/tmp/u pip --version`,
		`PYTHONHOME=/tmp/h pip list`,
		`PERL5LIB=/tmp/x shasum --version`,
		`PERLLIB=/tmp/x shasum f`,
		`RUBYLIB=/tmp/x gem --version`,
		`export PYTHONPATH=/tmp/x; pip list`,
	)
	hunt1117Allowed(t,
		`PYTHONDONTWRITEBYTECODE=1 pip list`,
		`PYTHONUNBUFFERED=1 python3 --version`,
	)
	// git and the programs it starts do not read these, so they neither
	// unsettle git's repository context nor add a code_execution effect of
	// their own: the statement prompts once, as system_write.
	for _, cmd := range []string{`PYTHONPATH=src git log --oneline -1`, `export PYTHONPATH=$PWD`} {
		if effects := Analyze(cmd).Effects; slices.Contains(effects, CodeExecution) {
			t.Errorf("Analyze(%q).Effects = %v, want no code_execution from a library path", cmd, effects)
		}
	}
}

// curl reads .curlrc from $CURL_HOME, $XDG_CONFIG_HOME or $HOME, and wget
// reads $WGETRC or $HOME/.wgetrc. Those files set any option (output file,
// upload, config), which is why `curl -K FILE` is code execution; pointing the
// variable at another directory did the same and auto-allowed.
func TestRED_TransferClientRCFileEnv(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	hunt1117NotAllowed(t, SystemWrite,
		`CURL_HOME=/tmp/c curl https://example.com`,
		`XDG_CONFIG_HOME=/tmp/x curl https://example.com`,
		`HOME=/tmp/h curl https://example.com`,
		`WGETRC=/tmp/w wget https://example.com`,
		`HOME=/tmp/h wget https://example.com`,
		`export CURL_HOME=/tmp/c; curl https://example.com`,
		`HOME=/tmp/h; curl https://example.com`,
		`env CURL_HOME=/tmp/c curl https://example.com`,
		`WGET2RC=/tmp/w wget https://example.com`,
		// wget2, installed as wget on some systems, reads
		// $XDG_CONFIG_HOME/wget/wget2rc.
		`XDG_CONFIG_HOME=/tmp/x wget https://example.com`,
	)
	// The real home is the default, and a variable only matters to the
	// client that reads it.
	hunt1117Allowed(t,
		`HOME=/home/user curl https://example.com`,
		`CURL_HOME=$HOME curl https://example.com`,
		`CURL_HOME=/home/user curl https://example.com`,
		`curl -s https://example.com`,
		`CURL_HOME=/tmp/c wget https://example.com`,
		`WGETRC=/tmp/w curl https://example.com`,
		`CURL_HOME=/tmp/c ls`,
	)
}

// pandoc, ffmpeg/ffprobe, ImageMagick and xmllint fetch a URL operand (and
// pandoc a URL option value such as --template=URL), and ffmpeg sends its
// output to a URL operand (`-method POST http://…`, udp://…). Without the
// NetworkEgress / NetworkUpload effect a policy denying egress or uploads let
// them through: each request reached a local test server.
func TestRED_URLFetchingToolsCarryNetworkEffects(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	egress := []string{
		`pandoc https://example.com/a.md`,
		`pandoc --template=https://example.com/t.html a.md`,
		`ffprobe http://10.0.0.1/x`,
		`ffmpeg -i http://169.254.169.254/ o.mp4`,
		`ffmpeg -i hls+https://example.com/a.m3u8 o.mp4`,
		`convert https://example.com/a.png b.png`,
		`magick https://example.com/a.png b.png`,
		`identify http://10.0.0.1/a.png`,
		`xmllint http://10.0.0.1/a.xml`,
		`ffmpeg -i concat:http://169.254.169.254/a o.mp4`,
		`ffmpeg -i cache:http://10.0.0.1/a o.mp4`,
		`convert jpg:https://example.com/a b.png`,
		`identify url:http://10.0.0.1/a.png`,
		`ffmpeg -i in.mp4 -hls_base_url http://cdn.example/ out.m3u8`,
	}
	upload := []string{
		`ffmpeg -i in.wav -f wav -method POST http://example.com/up`,
		`ffmpeg -i in.mp4 -f mpegts udp://10.0.0.1:1234`,
		`ffmpeg -re -i in.mp4 -f flv rtmp://example.com/live/key`,
		`ffmpeg -i in.mp4 -f mpegts async:tcp://10.0.0.1:1234`,
		`ffmpeg -i in.mp4 -f rtp_mpegts sap://224.0.0.1`,
		`ffmpeg -i in.mp4 -f mpegts amqp://h/x`,
	}
	denyEgress := &DangerousConfig{Classes: map[RiskClass]Action{NetworkEgress: Deny}}
	for _, cmd := range append(append([]string{}, egress...), upload...) {
		if !slices.Contains(Analyze(cmd).Effects, NetworkEgress) {
			t.Errorf("Analyze(%q).Effects = %v, want network_egress", cmd, Analyze(cmd).Effects)
		}
		if act := denyEgress.ActionForCommand(cmd); act != Deny {
			t.Errorf("with network_egress denied, ActionForCommand(%q) = %s, want deny", cmd, act)
		}
	}
	for _, cmd := range upload {
		if !slices.Contains(Analyze(cmd).Effects, NetworkUpload) {
			t.Errorf("Analyze(%q).Effects = %v, want network_upload", cmd, Analyze(cmd).Effects)
		}
	}
	for _, cmd := range []string{
		`ffmpeg -i in.mp4 out.gif`,
		`ffprobe in.mp4`,
		`pandoc a.md -o b.html`,
		`xmllint --noout a.xml`,
		`convert a.png b.jpg`,
	} {
		if effects := Analyze(cmd).Effects; slices.Contains(effects, NetworkEgress) {
			t.Errorf("Analyze(%q).Effects = %v, want no network_egress for local files", cmd, effects)
		}
	}
	// An input URL, or a URL ffmpeg only writes into its output as a string,
	// is egress, not an upload.
	for _, cmd := range []string{
		`ffmpeg -i http://example.com/a.mp4 o.mp4`,
		`ffprobe http://example.com/a.mp4`,
		`ffmpeg -i in.mp4 -hls_base_url http://cdn.example/ out.m3u8`,
		`ffmpeg -i in.mp4 -metadata comment=https://example.com o.mp4`,
	} {
		if slices.Contains(Analyze(cmd).Effects, NetworkUpload) {
			t.Errorf("Analyze(%q).Effects = %v, want no network_upload for an input URL", cmd, Analyze(cmd).Effects)
		}
	}
}
