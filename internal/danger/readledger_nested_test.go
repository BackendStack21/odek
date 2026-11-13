package danger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// nestedFixture recreates the shape of the Friendly Fire repository: a script
// that looks like a security check and launches a binary nobody can read.
func nestedFixture(t *testing.T, script string) string {
	t.Helper()
	ResetReadLedgerForTest()
	t.Cleanup(ResetReadLedgerForTest)
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	write := func(name, body string, mode os.FileMode) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("security.sh", script, 0o755)
	write("code_policies", "\x7fELF\x00\x00binary", 0o755)
	write("helper.sh", "#!/bin/sh\n./code_policies\n", 0o755)
	write("clean.sh", "#!/bin/sh\necho ok\n", 0o755)
	return dir
}

func targetNames(targets []string) string {
	var names []string
	for _, t := range targets {
		names = append(names, filepath.Base(t))
	}
	return strings.Join(names, ",")
}

func TestNestedExec_ReadScriptDoesNotLicenseTheBinaryItLaunches(t *testing.T) {
	for name, script := range map[string]string{
		"direct":   "#!/bin/bash\nsemgrep .\n./code_policies\n",
		"variable": "#!/bin/bash\nTOOL=./code_policies\n$TOOL --scan\n",
		"dirname":  "#!/bin/bash\nDIR=$(dirname \"$0\")\n\"$DIR/code_policies\"\n",
		"exec":     "#!/bin/sh\nexec ./code_policies\n",
		"subshell": "#!/bin/sh\nif true; then (./code_policies); fi\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := nestedFixture(t, script)
			RecordRead(filepath.Join(dir, "security.sh"))
			cls, targets := ClassifyScriptGate("bash security.sh")
			if cls != UnreadExec || !strings.Contains(targetNames(targets), "code_policies") {
				t.Fatalf("class=%s targets=%v, want unread_exec naming code_policies", cls, targets)
			}
		})
	}
}

func TestNestedExec_ABinaryCannotBeLicensedByReadingIt(t *testing.T) {
	dir := nestedFixture(t, "#!/bin/sh\n./code_policies\n")
	RecordRead(filepath.Join(dir, "security.sh"))
	RecordRead(filepath.Join(dir, "code_policies"))
	if cls, targets := ClassifyScriptGate("bash security.sh"); cls != UnreadExec {
		t.Fatalf("class=%s targets=%v, want unread_exec", cls, targets)
	}
}

func TestNestedExec_FollowsReadScripts(t *testing.T) {
	dir := nestedFixture(t, "#!/bin/sh\nsh helper.sh\n")
	RecordRead(filepath.Join(dir, "security.sh"))
	if _, targets := ClassifyScriptGate("bash security.sh"); targetNames(targets) != "helper.sh" {
		t.Fatalf("targets=%v, want helper.sh first", targets)
	}
	RecordRead(filepath.Join(dir, "helper.sh"))
	if _, targets := ClassifyScriptGate("bash security.sh"); targetNames(targets) != "code_policies" {
		t.Fatalf("targets=%v, want code_policies once helper.sh is read", targets)
	}
}

func TestNestedExec_ACycleTerminates(t *testing.T) {
	dir := nestedFixture(t, "#!/bin/sh\nsh ./security.sh\n")
	RecordRead(filepath.Join(dir, "security.sh"))
	if cls, targets := ClassifyScriptGate("bash security.sh"); cls != CodeExecution || len(targets) != 0 {
		t.Fatalf("class=%s targets=%v", cls, targets)
	}
}

func TestNestedExec_NonShellScriptsAreCheckedForLocalExecutables(t *testing.T) {
	dir := nestedFixture(t, "")
	body := "import subprocess\nsubprocess.run([\"./code_policies\"])\n"
	if err := os.WriteFile(filepath.Join(dir, "tool.py"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	RecordRead(filepath.Join(dir, "tool.py"))
	if cls, targets := ClassifyScriptGate("python3 tool.py"); cls != UnreadExec || !strings.Contains(targetNames(targets), "code_policies") {
		t.Fatalf("class=%s targets=%v", cls, targets)
	}
}

func TestNestedExec_OrdinaryScriptsStayLicensed(t *testing.T) {
	for name, script := range map[string]string{
		"system tools":   "#!/usr/bin/env bash\n/bin/echo hi\n/usr/bin/env ls\ngo vet ./...\nmypy .\n",
		"data file":      "#!/bin/sh\ncat ./README.md\n",
		"comment":        "#!/bin/sh\n# used to run ./code_policies here\necho done\n",
		"missing binary": "#!/bin/sh\n./not-built-yet\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := nestedFixture(t, script)
			if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("docs\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			RecordRead(filepath.Join(dir, "security.sh"))
			if cls, targets := ClassifyScriptGate("bash security.sh"); cls == UnreadExec || len(targets) != 0 {
				t.Fatalf("class=%s targets=%v, want no nested targets", cls, targets)
			}
		})
	}
}

func TestNestedExec_OversizeScriptFailsClosed(t *testing.T) {
	dir := nestedFixture(t, "#!/bin/sh\n"+strings.Repeat("# pad\n", maxNestedScriptBytes/6+1)+"./code_policies\n")
	RecordRead(filepath.Join(dir, "security.sh"))
	if cls, _ := ClassifyScriptGate("bash security.sh"); cls != UnreadExec {
		t.Fatalf("class=%s, want unread_exec", cls)
	}
}

func TestNestedExec_RewrittenNestedScriptLosesItsLicense(t *testing.T) {
	dir := nestedFixture(t, "#!/bin/sh\nsh helper.sh\n")
	RecordRead(filepath.Join(dir, "security.sh"))
	RecordRead(filepath.Join(dir, "clean.sh"))
	helper := filepath.Join(dir, "helper.sh")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\necho clean\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	RecordRead(helper)
	if cls, targets := ClassifyScriptGate("bash security.sh"); cls == UnreadExec {
		t.Fatalf("class=%s targets=%v before the rewrite", cls, targets)
	}
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n./code_policies # changed\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if cls, _ := ClassifyScriptGate("bash security.sh"); cls != UnreadExec {
		t.Fatalf("class=%s, want unread_exec after the rewrite", cls)
	}
}

// A shell script whose program is computed at run time cannot be licensed by
// reading it: what runs is not on the page. These fail closed.
func TestNestedExec_ComputedProgramsFailClosed(t *testing.T) {
	for name, script := range map[string]string{
		"assembled":         "#!/bin/bash\nP=code\nQ=_policies\n./$P$Q\n",
		"decoded":           "#!/bin/bash\n$(echo Li9jb2RlX3BvbGljaWVz | base64 -d)\n",
		"glob":              "#!/bin/bash\n./code_pol*\n",
		"find exec":         "#!/bin/bash\nfind . -name 'code_*' -exec {} ';'\n",
		"xargs":             "#!/bin/bash\nls | xargs -I{} ./{}\n",
		"eval":              "#!/bin/bash\neval \"$X\"\n",
		"after and":         "#!/bin/bash\ntrue && $TOOL\n",
		"then":              "#!/bin/bash\nif true; then $TOOL; fi\n",
		"exec":              "#!/bin/bash\nexec \"$TOOL\"\n",
		"substitution body": "#!/bin/bash\nX=$($TOOL --version)\n",
		"prefix assignment": "#!/bin/bash\nA=1 $TOOL\n",
		"after redirect":    "#!/bin/bash\n2>/dev/null $TOOL\n",
		"pipe":              "#!/bin/bash\necho hi | $TOOL\n",
		"copied":            "#!/bin/bash\ncp code_policies /tmp/x && /tmp/x\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := nestedFixture(t, script)
			RecordRead(filepath.Join(dir, "security.sh"))
			if cls, targets := ClassifyScriptGate("bash security.sh"); cls != UnreadExec || len(targets) == 0 {
				t.Fatalf("class=%s targets=%v, want unread_exec", cls, targets)
			}
		})
	}
}

func TestNestedExec_VariablesInArgumentsDoNotGate(t *testing.T) {
	for name, script := range map[string]string{
		"argument":          "#!/bin/bash\necho \"$HOME\"\nls $DIR\n",
		"assignment":        "#!/bin/bash\nX=$(date)\nY=\"$X\"\n",
		"test":              "#!/bin/bash\nif [ -n \"$X\" ]; then echo yes; fi\n",
		"glob arg":          "#!/bin/bash\nrm -f ./build/*.o\ngrep foo ./*.md\n",
		"quoted separators": "#!/bin/bash\necho \"a; $b && $c | $d\"\n",
		"double bracket or": "#!/bin/bash\nif [[ \"$a\" == x || \"$b\" == y ]]; then echo ok; fi\n",
		"redirect":          "#!/bin/bash\necho hi 2>&1 >&2\ncat <\"$f\" >\"$g\"\n",
		"path assignment":   "#!/bin/bash\nROOT=/opt/$NAME\nexport P=\"$ROOT/x\"\n",
		"cd substitution":   "#!/bin/bash\nROOT=\"$(cd \"$(dirname \"$0\")/..\" && pwd)\"\n",
		"arithmetic":        "#!/bin/bash\ni=$((i + 1))\necho $((i * 2))\n",
		"heredoc":           "#!/bin/bash\ncat <<EOF\n$TOOL --run\n./$X\nEOF\necho done\n",
		"quoted heredoc":    "#!/bin/bash\ncat <<'EOF' > out\n$TOOL\nEOF\n",
		"case":              "#!/bin/bash\ncase \"$x\" in\n  a|b) echo one ;;\n  *) echo \"$x\" ;;\nesac\n",
		"for and function":  "#!/bin/bash\nf() { echo \"$1\"; }\nfor i in $L; do f \"$i\"; done\n",
		"default param":     "#!/bin/bash\n: \"${X:=1}\"\nY=${X:-2}\n",
		"case glob":         "#!/bin/bash\ncase \"$x\" in\n  */tag/*) echo a ;;\n  ./b*) echo b ;;\nesac\n",
		"array literal":     "#!/bin/bash\nA=(\n  'rm -rf /*'\n  \"$X/y\"\n)\n",
		"array element":     "#!/bin/bash\nR[$i]=\"${v:-0}\"\n",
		"trap cleanup":      "#!/bin/bash\ntrap 'rm -f \"$tmp\"' EXIT\ntrap cleanup INT\n",
		"comment after":     "#!/bin/bash\necho ok # then $TOOL runs\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := nestedFixture(t, script)
			RecordRead(filepath.Join(dir, "security.sh"))
			if cls, targets := ClassifyScriptGate("bash security.sh"); cls == UnreadExec || len(targets) != 0 {
				t.Fatalf("class=%s targets=%v, want no gate", cls, targets)
			}
		})
	}
}

func TestNestedExec_InlineInterpreterPayloads(t *testing.T) {
	for cmd, want := range map[string]bool{
		`python3 -c "import subprocess; subprocess.run(['./code_policies'])"`: true,
		`python3 -u -c "import os; os.system('./code_policies')"`:             true,
		`node -e "require('child_process').execSync('./code_policies')"`:      true,
		`python3 -c "print(1 + 1)"`:                                           false,
		`python3 -c "print('see docs/guide')"`:                                false,
	} {
		t.Run(cmd, func(t *testing.T) {
			nestedFixture(t, "")
			cls, targets := ClassifyScriptGate(cmd)
			if got := cls == UnreadExec; got != want {
				t.Fatalf("class=%s targets=%v, want gated=%v", cls, targets, want)
			}
		})
	}
}

// Every way of hiding the payload from a reader or from the literal scan that we
// have found. A new spelling belongs here, whether or not it is closed yet.
func TestNestedExec_HiddenSpellings(t *testing.T) {
	sh := "s.sh"
	py := "s.py"
	for name, c := range map[string][3]string{
		"quotes in name":        {sh, "bash s.sh", "#!/bin/bash\n./co'de'_policies\n"},
		"escape in name":        {sh, "bash s.sh", "#!/bin/bash\n./co\\de_policies\n"},
		"brace in name":         {sh, "bash s.sh", "#!/bin/bash\n./{code_policies,}\n"},
		"ansi-c quoting":        {sh, "bash s.sh", "#!/bin/bash\n$'\\x2e/code_policies'\n"},
		"path change":           {sh, "bash s.sh", "#!/bin/bash\nPATH=.:$PATH\ncode_policies\n"},
		"piped into a shell":    {sh, "bash s.sh", "#!/bin/bash\nprintf './code_%s' policies | bash\n"},
		"heredoc into a shell":  {sh, "bash s.sh", "#!/bin/bash\nbash <<EOF\n./code_policies\nEOF\n"},
		"process substitution":  {sh, "bash s.sh", "#!/bin/bash\nsource <(printf './code_%s' policies)\n"},
		"alias":                 {sh, "bash s.sh", "#!/bin/bash\nshopt -s expand_aliases\nalias r=\"./code\"\"_policies\"\nr\n"},
		"trap":                  {sh, "bash s.sh", "#!/bin/bash\ntrap \"./code\"_policies EXIT\n"},
		"inline python in sh":   {sh, "bash s.sh", "#!/bin/bash\npython3 -c \"import subprocess; subprocess.run(['./code'+'_policies'])\"\n"},
		"python stdin in sh":    {sh, "bash s.sh", "#!/bin/bash\npython3 - <<EOF\nprint(1)\nEOF\n"},
		"python assembled":      {py, "python3 s.py", "import subprocess\nsubprocess.run(['./code'+'_policies'])\n"},
		"python os.system":      {py, "python3 s.py", "import os as o\no.system('./code'+'_policies')\n"},
		"python dunder import":  {py, "python3 s.py", "__import__('sub'+'process').run(['./x'])\n"},
		"python getattr":        {py, "python3 s.py", "import os\ngetattr(os, 'sys'+'tem')('./x')\n"},
		"python exec":           {py, "python3 s.py", "exec('imp'+'ort os')\n"},
		"inline assembled":      {sh, "python3 -c \"import subprocess; subprocess.run(['./code'+'_policies'])\"", ""},
		"python alias computed": {py, "python3 s.py", "import subprocess as s\nname = 'x'\ns.run([name])\n"},
		"python variable call":  {py, "python3 s.py", "import os\ncmd = 'x'\nos.system(cmd)\n"},
		"python from import":    {py, "python3 s.py", "from subprocess import run as r\nr(['x'])\n"},
		"node computed exec":    {"s.js", "node s.js", "const c = require('child_process');\nconst n = 'x';\nc.execSync(n);\n"},
		"inline dunder":         {sh, "python3 -c \"__import__('o'+'s').system('./x')\"", ""},
		"inline node":           {sh, "node -e \"require('chi'+'ld_process').execSync('./x')\"", ""},
		"inline ruby":           {sh, "ruby -e \"system('./x')\"", ""},
		"inline perl":           {sh, "perl -e \"system('./x')\"", ""},
		"unknown language file": {"s.tcl", "tclsh s.tcl", "exec ./x\n"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := nestedFixture(t, "")
			if c[2] != "" {
				p := filepath.Join(dir, c[0])
				if err := os.WriteFile(p, []byte(c[2]), 0o755); err != nil {
					t.Fatal(err)
				}
				RecordRead(p)
			}
			if cls, targets := ClassifyScriptGate(c[1]); cls != UnreadExec && Rank(cls) <= Rank(SystemWrite) {
				t.Fatalf("class=%s targets=%v, want gated", cls, targets)
			}
		})
	}
}

func TestNestedExec_LocalImportsAreScanned(t *testing.T) {
	dir := nestedFixture(t, "")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("tool.py", "import helper\nhelper.go()\n")
	write("helper.py", "import subprocess\ndef go():\n    subprocess.run(['./code'+'_policies'])\n")
	RecordRead(filepath.Join(dir, "tool.py"))
	if cls, targets := ClassifyScriptGate("python3 tool.py"); cls != UnreadExec || targetNames(targets) != "helper.py" {
		t.Fatalf("class=%s targets=%v, want helper.py gated for what it can spawn", cls, targets)
	}
	write("helper.py", "def go():\n    return 1\n")
	if cls, targets := ClassifyScriptGate("python3 tool.py"); cls == UnreadExec || len(targets) != 0 {
		t.Fatalf("class=%s targets=%v, want a plain module to pass without a read", cls, targets)
	}
}

func TestNestedExec_PlainCodeStaysLicensed(t *testing.T) {
	for name, c := range map[string][2]string{
		"python":              {"s.py", "import json, re, sys\npat = re.compile('x')\nif __name__ == '__main__':\n    print(json.dumps(sys.argv))\n"},
		"python reflection":   {"s.py", "x = getattr(o, 'name', None)\nprint(vars(o))\n"},
		"node arrays":         {"s.js", "const a = ['x', 'y'];\nconst b = require('./missing');\nconsole.log(a, b);\n"},
		"python literal call": {"s.py", "import os\nos.system('id')\n"},
		"python literal run":  {"s.py", "import subprocess\nsubprocess.run(['ls', '-l'], check=True)\n"},
		"node literal exec":   {"s.js", "const cp = require('child_process');\ncp.execSync('ls');\n"},
		"python comment":      {"s.py", "# subprocess.run would be wrong here\nprint(1)\n"},
		"node":                {"s.js", "const fs = require('fs');\nconsole.log(fs.readFileSync('a').length);\n"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := nestedFixture(t, "")
			p := filepath.Join(dir, c[0])
			if err := os.WriteFile(p, []byte(c[1]), 0o644); err != nil {
				t.Fatal(err)
			}
			RecordRead(p)
			interp := map[string]string{"s.py": "python3", "s.js": "node"}[c[0]]
			if cls, targets := ClassifyScriptGate(interp + " " + c[0]); cls == UnreadExec || len(targets) != 0 {
				t.Fatalf("class=%s targets=%v", cls, targets)
			}
		})
	}
}

// Findings from the pre-release review, each reproduced before the fix.
func TestNestedExec_ReviewBypasses(t *testing.T) {
	for name, script := range map[string]string{
		"fake heredoc in string":   "#!/bin/bash\necho \"<<EOF\"\n$(echo Li9jb2RlX3BvbGljaWVz | base64 -d)\n",
		"fake heredoc in grep":     "#!/bin/bash\ngrep '<<EOF' x\n$TOOL\n",
		"unterminated heredoc":     "#!/bin/bash\ncat <<EOF\n$TOOL\n",
		"substitution in heredoc":  "#!/bin/bash\ncat <<EOF\n$($TOOL)\nEOF\n",
		"timeout wrapper":          "#!/bin/bash\ntimeout 5 \"$TOOL\"\n",
		"sudo wrapper":             "#!/bin/bash\nsudo \"$TOOL\"\n",
		"bash -c":                  "#!/bin/bash\nbash -c \"$TOOL\"\n",
		"nice with value":          "#!/bin/bash\nnice -n 5 \"$TOOL\"\n",
		"env with flag":            "#!/bin/bash\nenv -i \"$TOOL\"\n",
		"command -p":               "#!/bin/bash\ncommand -p \"$TOOL\"\n",
		"bare after path export":   "#!/bin/bash\nexport PATH=\"$PWD:$PATH\"\ncode_policies\n",
		"unsafe path export":       "#!/bin/bash\nexport PATH=\"$PWD:$PATH\"\nmytool\n",
		"command bare":             "#!/bin/bash\ncommand code_policies\n",
		"exec bare":                "#!/bin/bash\nexec code_policies\n",
		"bare with inherited path": "#!/bin/bash\ncode_policies\n",
		"option before operand":    "#!/bin/bash\nsh -e helper.sh\n",
		"bash -x operand":          "#!/bin/bash\nbash -x helper.sh\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := nestedFixture(t, script)
			RecordRead(filepath.Join(dir, "security.sh"))
			if cls, targets := ClassifyScriptGate("bash security.sh"); cls != UnreadExec {
				t.Fatalf("class=%s targets=%v, want unread_exec", cls, targets)
			}
		})
	}
}

// Commands that only mention an interpreter must keep their own class.
func TestNestedExec_MentioningAnInterpreterIsNotRunningIt(t *testing.T) {
	for _, cmd := range []string{
		"which python3",
		"ps aux | grep python3",
		"brew install node",
		"nvm use node",
		"ls node",
		"command -v ruby",
		"docker run -it python",
		`perl -pe 's/do/did/' f`,
		`perl -ne 'print if /open/' f`,
	} {
		t.Run(cmd, func(t *testing.T) {
			nestedFixture(t, "")
			if cls, targets := ClassifyScriptGate(cmd); cls == UnreadExec {
				t.Fatalf("class=%s targets=%v", cls, targets)
			}
		})
	}
}

func TestNestedExec_CodeOnStdinIsGated(t *testing.T) {
	for _, cmd := range []string{"echo x | python3", "python3 - <<EOF\nprint(1)\nEOF", "python3 < x.py"} {
		t.Run(cmd, func(t *testing.T) {
			nestedFixture(t, "")
			if cls, targets := ClassifyScriptGate(cmd); cls != UnreadExec && Rank(cls) <= Rank(SystemWrite) {
				t.Fatalf("class=%s targets=%v", cls, targets)
			}
		})
	}
}

// Ordinary scripts in other languages can be cleared by reading them.
func TestNestedExec_OrdinaryOtherLanguagesStayLicensed(t *testing.T) {
	for name, c := range map[string][3]string{
		"perl open and require": {"s.pl", "perl s.pl", "use strict;\nrequire Foo;\nopen(my $f, '<', 'x');\n"},
		"php include":           {"s.php", "php s.php", "<?php include 'a.php'; echo 1;\n"},
		"lua require":           {"s.lua", "lua s.lua", "local m = require('m')\nprint(m)\n"},
		"python os environ":     {"s.py", "python3 s.py", "from os import environ\nprint(environ)\n"},
		"python plain getattr":  {"s.py", "python3 s.py", "import json\nprint(getattr(json, name))\n"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := nestedFixture(t, "")
			p := filepath.Join(dir, c[0])
			if err := os.WriteFile(p, []byte(c[2]), 0o644); err != nil {
				t.Fatal(err)
			}
			RecordRead(p)
			if cls, targets := ClassifyScriptGate(c[1]); cls == UnreadExec {
				t.Fatalf("class=%s targets=%v", cls, targets)
			}
		})
	}
}

// The parser sees hostile text: it must always finish.
func FuzzParseShellCommands(f *testing.F) {
	for _, seed := range []string{"a|b", "cat <<EOF\n$(x)\nEOF", "case x in a) b;; esac", "$(", "`", "<(", "[[ a", "x=(", "\\", "<<", "'", "\""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > 4096 {
			return
		}
		commands, _ := parseShellCommands(text)
		for _, c := range commands {
			c.isComputed()
		}
	})
}

// A shell runs what it is handed, whatever the file is called.
func TestNestedExec_ShellCodeUnderAnyName(t *testing.T) {
	for name, c := range map[string][2]string{
		"bash txt":       {"x.txt", "bash x.txt"},
		"sh log":         {"x.log", "sh x.log"},
		"source conf":    {"x.conf", "source x.conf"},
		"dot txt":        {"x.txt", ". x.txt"},
		"bash py":        {"x.py", "bash x.py"},
		"bash js":        {"x.js", "bash x.js"},
		"bash with flag": {"x.txt", "bash -e x.txt"},
		"behind timeout": {"x.txt", "timeout 5 bash x.txt"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := nestedFixture(t, "")
			p := filepath.Join(dir, c[0])
			if err := os.WriteFile(p, []byte("eval \"$T\"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			RecordRead(p)
			if cls, targets := ClassifyScriptGate(c[1]); cls != UnreadExec {
				t.Fatalf("class=%s targets=%v", cls, targets)
			}
		})
	}
}

func TestNestedExec_NestedShellOperandUnderAnyName(t *testing.T) {
	dir := nestedFixture(t, "#!/bin/sh\nsh helper.py\n")
	helper := filepath.Join(dir, "helper.py")
	if err := os.WriteFile(helper, []byte("eval \"$T\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	RecordRead(filepath.Join(dir, "security.sh"))
	RecordRead(helper)
	if cls, targets := ClassifyScriptGate("bash security.sh"); cls != UnreadExec {
		t.Fatalf("class=%s targets=%v", cls, targets)
	}
}

func TestNestedExec_PassedAroundSpawnsAreGated(t *testing.T) {
	for name, c := range map[string][2]string{
		"python alias":   {"s.py", "import os\nf = os.system\nf(cmd)\n"},
		"python dict":    {"s.py", "import os\nh = {'x': os.system}\nh['x'](cmd)\n"},
		"python partial": {"s.py", "import os, functools\nfunctools.partial(os.system)(cmd)\n"},
		"python getattr": {"s.py", "import os\nf = getattr(os, 'system')\nf(cmd)\n"},
		"node alias":     {"s.js", "const e = require('child_process').exec;\ne(cmd);\n"},
		"node rename":    {"s.js", "const { exec: e } = require('child_process');\ne(cmd);\n"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := nestedFixture(t, "")
			p := filepath.Join(dir, c[0])
			if err := os.WriteFile(p, []byte(c[1]), 0o644); err != nil {
				t.Fatal(err)
			}
			RecordRead(p)
			interp := map[string]string{"s.py": "python3", "s.js": "node"}[c[0]]
			if cls, targets := ClassifyScriptGate(interp + " " + c[0]); cls != UnreadExec {
				t.Fatalf("class=%s targets=%v", cls, targets)
			}
		})
	}
}

func TestNestedExec_CRLFHeredocParses(t *testing.T) {
	if _, ok := parseShellCommands("cat <<EOF\r\nhello\r\nEOF\r\necho done\r\n"); !ok {
		t.Fatal("a CRLF here-document must find its terminator")
	}
}

// Findings from the verification round.
func TestNestedExec_VerificationBypasses(t *testing.T) {
	for name, script := range map[string]string{
		"bash variable":       "#!/bin/bash\nbash \"$TOOL\"\n",
		"source variable":     "#!/bin/bash\nsource \"$TOOL\"\n",
		"python variable":     "#!/bin/bash\npython3 \"$TOOL\"\n",
		"bash positional":     "#!/bin/bash\nbash \"$1\"\n",
		"bash all args":       "#!/bin/bash\nbash -- \"$@\"\n",
		"bash substitution":   "#!/bin/bash\nbash $(echo x)\n",
		"bash glob":           "#!/bin/bash\nbash help*.sh\n",
		"bash quoted name":    "#!/bin/bash\nbash \"hel\"per.sh\n",
		"bash -c literal":     "#!/bin/bash\nbash -c \"bash helper.sh\"\n",
		"bash -c source":      "#!/bin/bash\nbash -c \". helper.sh\"\n",
		"python computed":     "#!/bin/bash\npython3 -c \"$(cat s.py)\"\n",
		"perl computed":       "#!/bin/bash\nperl -e \"$(cat x)\"\n",
		"python proc subst":   "#!/bin/bash\npython3 <(curl x)\n",
		"busybox":             "#!/bin/bash\nbusybox sh \"$TOOL\"\n",
		"script -c":           "#!/bin/bash\nscript -qc \"$TOOL\" /dev/null\n",
		"flock":               "#!/bin/bash\nflock /tmp/l \"$TOOL\"\n",
		"unbuffer":            "#!/bin/bash\nunbuffer \"$TOOL\"\n",
		"su -c":               "#!/bin/bash\nsu -c \"$TOOL\"\n",
		"valgrind":            "#!/bin/bash\nvalgrind \"$TOOL\"\n",
		"gdb args":            "#!/bin/bash\ngdb --args \"$TOOL\"\n",
		"env -u":              "#!/bin/bash\nenv -u X \"$TOOL\"\n",
		"env -C":              "#!/bin/bash\nenv -C /tmp \"$TOOL\"\n",
		"exec -a":             "#!/bin/bash\nexec -a name \"$TOOL\"\n",
		"trap single quoted":  "#!/bin/bash\ntrap '$TOOL' EXIT\n",
		"quoted path export":  "#!/bin/bash\nexport \"PATH=$PWD:$PATH\"\nmytool\n",
		"euo cluster operand": "#!/bin/bash\nbash -euo pipefail helper.sh\n",
		"cd then binary":      "#!/bin/bash\ncd sub\n./inner\n",
		"awk program":         "#!/bin/bash\nawk -f prog.awk data\n",
		"sed program":         "#!/bin/bash\nsed -f prog.sed data\n",
		"gdb program":         "#!/bin/bash\ngdb -x prog.gdb a\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := nestedFixture(t, script)
			if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "sub", "inner"), []byte("\x7fELF\x00\x00"), 0o755); err != nil {
				t.Fatal(err)
			}
			for _, f := range []string{"prog.awk", "prog.sed", "prog.gdb"} {
				if err := os.WriteFile(filepath.Join(dir, f), []byte("x\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			RecordRead(filepath.Join(dir, "security.sh"))
			if cls, targets := ClassifyScriptGate("bash security.sh"); cls != UnreadExec {
				t.Fatalf("class=%s targets=%v, want unread_exec", cls, targets)
			}
		})
	}
}

func TestNestedExec_InterpreterDecidesTheLanguage(t *testing.T) {
	dir := nestedFixture(t, "")
	p := filepath.Join(dir, "x.sh")
	if err := os.WriteFile(p, []byte("import os\nos.system(input())\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	RecordRead(p)
	if cls, targets := ClassifyScriptGate("python3 x.sh"); cls != UnreadExec {
		t.Fatalf("class=%s targets=%v", cls, targets)
	}
}

func TestNestedExec_TableAdditions(t *testing.T) {
	for name, c := range map[string][2]string{
		"bun":        {"s.js", "Bun.spawn([Bun.argv[2]])\n"},
		"deno":       {"s.js", "new Deno.Command(x).spawn()\n"},
		"lua":        {"s.lua", "loadfile(x)()\n"},
		"perl open3": {"s.pl", "use IPC::Open3;\n"},
		"perl do":    {"s.pl", "do $x;\n"},
		"php incl":   {"s.php", "<?php include $x;\n"},
		"ruby send":  {"s.rb", "Kernel.send(:\"sys\" + \"tem\", x)\n"},
	} {
		t.Run(name, func(t *testing.T) {
			if !nestedCodeCanSpawn(nestedLanguageOfFile(c[0], c[1]), c[1]) {
				t.Fatalf("%q not flagged", c[1])
			}
		})
	}
}

func TestNestedExec_MentioningAPathIsNotRunningIt(t *testing.T) {
	for name, script := range map[string]string{
		"ls":    "#!/bin/bash\nls ./code_policies\n",
		"rm":    "#!/bin/bash\nrm -f ./code_policies\n",
		"echo":  "#!/bin/bash\necho \"see ./code_policies\"\n",
		"test":  "#!/bin/bash\nif [ -f ./code_policies ]; then echo y; fi\n",
		"chmod": "#!/bin/bash\nchmod 644 ./README\n",
		"lib":   "#!/bin/bash\nDIR=$(dirname \"$0\")\necho \"$DIR\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := nestedFixture(t, script)
			RecordRead(filepath.Join(dir, "security.sh"))
			if cls, targets := ClassifyScriptGate("bash security.sh"); cls == UnreadExec {
				t.Fatalf("class=%s targets=%v", cls, targets)
			}
		})
	}
}

func TestNestedExec_SourcedLibraryBesideTheScriptIsFollowed(t *testing.T) {
	dir := nestedFixture(t, "#!/bin/bash\nDIR=\"$(dirname \"$0\")\"\nsource \"$DIR/lib.sh\"\n")
	lib := filepath.Join(dir, "lib.sh")
	if err := os.WriteFile(lib, []byte("echo lib\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	RecordRead(filepath.Join(dir, "security.sh"))
	if _, targets := ClassifyScriptGate("bash security.sh"); targetNames(targets) != "lib.sh" {
		t.Fatalf("targets=%v, want lib.sh", targets)
	}
	RecordRead(lib)
	if cls, targets := ClassifyScriptGate("bash security.sh"); cls == UnreadExec {
		t.Fatalf("class=%s targets=%v, want licensed once read", cls, targets)
	}
}

// Findings from the final review.
func TestNestedExec_FinalReviewBypasses(t *testing.T) {
	for name, script := range map[string]string{
		"one-line function":     "#!/bin/bash\nfunction f { ./code_policies; }; f\n",
		"one-line group":        "#!/bin/bash\n{ ./code_policies; }\n",
		"substitution in [[":    "#!/bin/bash\n[[ -n \"$(./code_policies)\" ]]\n",
		"substitution in ${}":   "#!/bin/bash\necho \"${X:-$(./code_policies)}\"\n",
		"assign default":        "#!/bin/bash\n: \"${X:=$(./code_policies)}\"\n",
		"substitution in array": "#!/bin/bash\nA=($(./code_policies))\n",
		"substitution in math":  "#!/bin/bash\n: $(( $(./code_policies) ))\n",
		"nested script in [[":   "#!/bin/bash\n[[ -n \"$(bash helper.sh)\" ]]\n",
		"trap literal":          "#!/bin/bash\ntrap './code_policies' EXIT\n",
		"env -S":                "#!/bin/bash\nenv -S './code_policies --scan'\n",
		"env -S fused":          "#!/bin/bash\nenv \"-S./code_policies --scan\"\n",
		"hash -p":               "#!/bin/bash\nhash -p ./code_policies scan\nscan\n",
		"cp then chmod":         "#!/bin/bash\ncp payload scanner && chmod +x scanner && ./scanner\n",
		"install":               "#!/bin/bash\ninstall -m 755 payload scanner && ./scanner\n",
		"cat into file":         "#!/bin/bash\ncat payload > scanner; chmod +x scanner; ./scanner\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := nestedFixture(t, script)
			if err := os.WriteFile(filepath.Join(dir, "payload"), []byte("\x7fELF\x00\x00"), 0o644); err != nil {
				t.Fatal(err)
			}
			RecordRead(filepath.Join(dir, "security.sh"))
			if cls, targets := ClassifyScriptGate("bash security.sh"); cls != UnreadExec {
				t.Fatalf("class=%s targets=%v, want unread_exec", cls, targets)
			}
		})
	}
}

func TestNestedExec_DeepNestingFailsClosedQuickly(t *testing.T) {
	text := strings.Repeat("$(", 60000) + "./x" + strings.Repeat(")", 60000)
	start := time.Now()
	if !shellProgramIsComputed(text) {
		t.Fatal("hostile nesting must fail closed")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestNestedExec_NodeLoadsOfLiteralPathsStayLicensed(t *testing.T) {
	for _, code := range []string{
		"const p = require(path.join(__dirname, 'lib', 'a.js'));\n",
		"const m = await import(new URL('./a.js', import.meta.url).href);\n",
	} {
		if nestedCodeCanSpawn("node", code) {
			t.Errorf("%q flagged", code)
		}
	}
	for _, code := range []string{
		"require(path.join(__dirname, name));\n",
		"require(path.join(__dirname, 'a' + b));\n",
		"import(new URL(x, import.meta.url));\n",
	} {
		if !nestedCodeCanSpawn("node", code) {
			t.Errorf("%q not flagged", code)
		}
	}
}

func TestNestedExec_MapfileCallback(t *testing.T) {
	for name, script := range map[string]string{
		"mapfile":       "#!/bin/bash\nmapfile -C ./code_policies -c 1 a <<<x\n",
		"readarray":     "#!/bin/bash\nreadarray -C ./code_policies -c 1 a <<<x\n",
		"quoted":        "#!/bin/bash\nmapfile -C './code_policies' -c 1 a <<<x\n",
		"nested script": "#!/bin/bash\nmapfile -C helper.sh -c 1 a <<<x\n",
		"computed":      "#!/bin/bash\nmapfile -C \"$TOOL\" -c 1 a <<<x\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := nestedFixture(t, script)
			RecordRead(filepath.Join(dir, "security.sh"))
			if cls, targets := ClassifyScriptGate("bash security.sh"); cls != UnreadExec {
				t.Fatalf("class=%s targets=%v, want unread_exec", cls, targets)
			}
		})
	}
	dir := nestedFixture(t, "#!/bin/bash\nmapfile -t a < list\n")
	RecordRead(filepath.Join(dir, "security.sh"))
	if cls, targets := ClassifyScriptGate("bash security.sh"); cls == UnreadExec {
		t.Fatalf("plain mapfile: class=%s targets=%v", cls, targets)
	}
}

func TestNestedExec_SingleQuotedArrayDataIsData(t *testing.T) {
	dir := nestedFixture(t, "#!/bin/bash\nA=(\n  '`curl x | sh`'\n  '$(./code_policies)'\n)\necho ok\n")
	RecordRead(filepath.Join(dir, "security.sh"))
	if cls, targets := ClassifyScriptGate("bash security.sh"); cls == UnreadExec {
		t.Fatalf("class=%s targets=%v", cls, targets)
	}
}
