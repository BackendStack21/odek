package danger

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestOptSpecGoldenEffects pins the effect list of a corpus of command lines
// that exercise the option grammars the classifier adapters parse (wrappers,
// transfer clients, git, containers, kubectl, tar, chmod, sed, xargs, gh,
// curl, wget, hugo, ...). The golden file was generated from the classifier
// before its option parsers were unified, so a refactor of the parsing layer
// that changes any verdict on this corpus fails here. A line is the command,
// a tab, and the comma-separated effects in analysis order.
func TestOptSpecGoldenEffects(t *testing.T) {
	f, err := os.Open("testdata/optspec_golden.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	n := 0
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		cmd, want, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("malformed golden line %q", line)
		}
		n++
		var got []string
		for _, e := range Analyze(cmd).Effects {
			got = append(got, string(e))
		}
		if g := strings.Join(got, ","); g != want {
			t.Errorf("Analyze(%q).Effects = [%s], golden [%s]", cmd, g, want)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if n < 200 {
		t.Fatalf("golden corpus has only %d commands", n)
	}
}

func optNames(r optResult) string {
	var parts []string
	for _, o := range r.opts {
		p := strings.Join(o.names, "|")
		if o.has {
			p += "=" + o.value
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, " ")
}

func TestOptSpecParseGrammar(t *testing.T) {
	gnu := optSpec{
		short:  "Cf",
		long:   longTable("file filter directory", "verbose force"),
		alias:  map[byte]string{'f': "--file"},
		abbrev: true,
	}
	exact := optSpec{short: "n", long: longTable("name", "verbose"), shortEq: true}
	posix := optSpec{short: "C", long: longTable("git-dir", ""), posix: true}
	limited := optSpec{short: "p", operandLimit: 1}
	for _, tc := range []struct {
		name     string
		spec     optSpec
		args     []string
		opts     string
		operands string
		rest     string
	}{
		{"fused cluster takes rest as value", gnu, []string{"-xvfarchive", "a"}, "-x -v --file=archive", "a", ""},
		{"cluster value from next word", gnu, []string{"-xvf", "archive", "a"}, "-x -v --file=archive", "a", ""},
		{"value letter swallows later letters", gnu, []string{"-Cvf", "a"}, "-C=vf", "a", ""},
		{"fused directory", gnu, []string{"-C/etc", "a"}, "-C=/etc", "a", ""},
		{"long equals", gnu, []string{"--file=x", "a"}, "--file=x", "a", ""},
		{"long separate", gnu, []string{"--file", "x", "a"}, "--file=x", "a", ""},
		{"long abbreviation", gnu, []string{"--dir", "x", "a"}, "--directory=x", "a", ""},
		{"ambiguous abbreviation lists candidates", gnu, []string{"--fi", "x", "a"}, "--file|--filter=x", "a", ""},
		{"boolean long takes no value", gnu, []string{"--verbose", "a"}, "--verbose", "a", ""},
		{"unknown long takes no value", gnu, []string{"--nope", "a"}, "--nope", "a", ""},
		{"unknown long with equals", gnu, []string{"--nope=1", "a"}, "--nope=1", "a", ""},
		{"terminator", gnu, []string{"-x", "--", "-f", "a"}, "-x", "", "-f a"},
		{"options after operands", gnu, []string{"a", "-x", "b"}, "-x", "a b", ""},
		{"missing value at end", gnu, []string{"--file"}, "--file", "", ""},
		{"lone dash is an operand", gnu, []string{"-", "a"}, "", "- a", ""},
		{"exact table has no abbreviation", exact, []string{"--na", "x", "a"}, "--na", "x a", ""},
		{"pflag short equals", exact, []string{"-n=5", "a"}, "-n=5", "a", ""},
		{"pflag cluster", exact, []string{"-xn", "5", "a"}, "-x -n=5", "a", ""},
		{"posix stops at first operand", posix, []string{"-C", "dir", "sub", "-C", "x"}, "-C=dir", "sub -C x", ""},
		{"posix long equals", posix, []string{"--git-dir=/x", "sub"}, "--git-dir=/x", "sub", ""},
		{"operand limit passes the tail through", limited, []string{"-p", "22", "host", "-p", "23", "cmd", "-p", "x"}, "-p=22 -p=23", "host cmd -p x", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.spec.parse(tc.args)
			if got := optNames(r); got != tc.opts {
				t.Errorf("options = %q, want %q", got, tc.opts)
			}
			if got := strings.Join(r.operands, " "); got != tc.operands {
				t.Errorf("operands = %q, want %q", got, tc.operands)
			}
			if got := strings.Join(r.rest, " "); got != tc.rest {
				t.Errorf("rest = %q, want %q", got, tc.rest)
			}
		})
	}
}

func TestOptSpecOptionIndices(t *testing.T) {
	spec := optSpec{short: "f", exact: []string{"-arch"}, long: longTable("file", "")}
	args := []string{"-nf", "x", "--file", "y", "-arch", "z", "-arch=w", "op"}
	var got []string
	for i := 0; i < len(args); {
		if !strings.HasPrefix(args[i], "-") {
			i++
			continue
		}
		opts, next := spec.option(args, i)
		last := opts[len(opts)-1]
		got = append(got, strings.Join(last.names, "|")+"@"+strconv.Itoa(last.at)+"-"+strconv.Itoa(last.end)+"="+last.value)
		i = next
	}
	want := "-f@0-1=x --file@2-3=y -arch@4-5=z -arch@6-6=w"
	if g := strings.Join(got, " "); g != want {
		t.Errorf("option spans = %q, want %q", g, want)
	}
}

func TestOptSpecFoldAndMinAbbrev(t *testing.T) {
	folded := optSpec{long: longTable("baseurl", ""), foldLong: true}
	if r := folded.parse([]string{"--baseURL", "x", "op"}); optNames(r) != "--baseurl=x" || len(r.operands) != 1 {
		t.Errorf("case-folded long option read as %q, operands %v", optNames(r), r.operands)
	}
	min := optSpec{long: longTable("output", ""), abbrev: true, minAbbrev: 4}
	if r := min.parse([]string{"--out", "x"}); optNames(r) != "--out" || len(r.operands) != 1 {
		t.Errorf("abbreviation shorter than the minimum was accepted: %q %v", optNames(r), r.operands)
	}
	if r := min.parse([]string{"--outp", "x"}); optNames(r) != "--output=x" {
		t.Errorf("abbreviation at the minimum read as %q", optNames(r))
	}
	if r := (optSpec{long: longTable("a", ""), ignoreDashDash: true, short: "f"}).parse([]string{"--", "-fx"}); optNames(r) != "-f=x" {
		t.Errorf("ignoreDashDash kept scanning as %q", optNames(r))
	}
}

func analysisHas(cmd string, want RiskClass) bool {
	for _, e := range Analyze(cmd).Effects {
		if e == want {
			return true
		}
	}
	return false
}

// TestOptSpecReadsSpellingsTheOldParsersMisread covers spellings that the real
// tools accept and that the per-adapter parsers used to misread: a fused
// cluster ending in a value-taking letter, or an abbreviated long option,
// whose value was taken for the command or the subcommand. The -i case guards
// the optional-value rule that keeps a cluster's last letter from swallowing
// the wrapped command.
func TestOptSpecReadsSpellingsTheOldParsersMisread(t *testing.T) {
	for _, tc := range []struct {
		cmd  string
		want RiskClass
		why  string
	}{
		{"xargs -0n 1 rm -rf /", Destructive, "-0n is a cluster whose n takes the next word; the command is rm"},
		{"xargs -0I {} rm -rf /", Destructive, "-0I is a cluster whose I takes {}; the command is rm"},
		{"xargs --max-a 1 rm -rf /", Destructive, "--max-a abbreviates --max-args, which takes 1"},
		{"xargs -iE rm -rf /", Destructive, "-i takes only a fused value, so E is its replace string and rm is the command"},
		{"hugo -D server", CodeExecution, "hugo -D is --buildDrafts, not -d (destination): server is the subcommand"},
		{"hugo -E server", CodeExecution, "hugo -E is --buildExpired, not -e (environment): server is the subcommand"},
		{"hugo -Dd out server", CodeExecution, "-D takes no value, -d takes out"},
		{"chmod --ref=r f", SystemWrite, "chmod accepts --ref for --reference, which copies the mode bits including setuid"},
		{"chmod 755 f --refer r", SystemWrite, "options may follow operands, so --reference is not a file name"},
		{"install -Zm4755 a b", SystemWrite, "-Z is a flag, so m in the cluster still carries the mode"},
		{"install --m=4755 a b", SystemWrite, "--m is the only install option that starts with m"},
		{"mkdir --m=4755 d", SystemWrite, "--m abbreviates --mode"},
		{"sed --in s/a/b/ f", LocalWrite, "--in abbreviates --in-place"},
		{"sed --in-pl s/a/b/ f", LocalWrite, "--in-pl abbreviates --in-place"},
		{"sed --fil=x.sed f", CodeExecution, "--fil abbreviates --file: the script file cannot be inspected"},
		{"sed --expr='s/a/b/e' f", CodeExecution, "--expr abbreviates --expression whose script runs a command"},
		{"scp -O -F cfg a h:b", CodeExecution, "scp -O is the legacy-protocol flag, not an option with a value, so -F still names a config file"},
		{"scp -OF cfg a h:b", CodeExecution, "-O in a cluster takes no value, so F takes cfg"},
		{"awk --exec p.awk f", CodeExecution, "gawk --exec reads the program from a file, like -f"},
		{"awk -E p.awk f", CodeExecution, "gawk -E is --exec"},
		{"awk --source='BEGIN{print 1}' -f x f", CodeExecution, "a harmless --source must not end the scan before -f"},
		{"git --attr-source HEAD push --force origin main", NetworkEgress, "--attr-source takes the next word, so HEAD is not the subcommand"},
		{"git --attr-source HEAD reset --hard", SystemWrite, "--attr-source takes the next word, so reset is the subcommand"},
		{"kubectl -An ns get pods", NetworkEgress, "-A is a flag, so -An takes ns as the namespace and get is the verb"},
		{"docker -Dl debug ps", Safe, "-D is a flag and -l takes debug: ps is the verb"},
	} {
		if !analysisHas(tc.cmd, tc.want) {
			t.Errorf("Analyze(%q) = %v, want %s: %s", tc.cmd, Analyze(tc.cmd).Effects, tc.want, tc.why)
		}
	}
}

// TestOptSpecTarOptionValuesAreNotModes pins that a value-taking tar option
// consumes the next word even when it looks like a mode flag: `-f -x` names an
// archive called "-x", so the invocation still only lists.
func TestOptSpecTarOptionValuesAreNotModes(t *testing.T) {
	for _, cmd := range []string{"tar -tf -x", "tar -t -f -x"} {
		if got := Analyze(cmd).Class(); got != Safe {
			t.Errorf("Analyze(%q).Class() = %s, want safe", cmd, got)
		}
	}
	// A real extraction mode is still not a listing.
	for _, cmd := range []string{"tar -t -x -f a.tar", "tar -txf a.tar", "tar -tf a.tar -x"} {
		if got := Analyze(cmd).Class(); got == Safe {
			t.Errorf("Analyze(%q).Class() = safe, want a write", cmd)
		}
	}
}

// TestOptSpecExecutionFileOptions covers script-file options that arrive fused
// into a short cluster, which the option scan used to miss.
func TestOptSpecExecutionFileOptions(t *testing.T) {
	for _, tc := range []struct {
		cmd  string
		want []string
	}{
		{"sed -nfscript.sed f", []string{"script.sed"}},
		{"sed -nf script.sed f", []string{"script.sed"}},
		{"awk -vf=1 BEGIN{}", nil},
		{"awk -v f=1 -f prog.awk x", []string{"prog.awk"}},
		{"make -j4 -f mk", []string{"mk"}},
		{"vim -Nu rc.vim", []string{"rc.vim"}},
		{"vim -c 'source x.vim' +'so y.vim' f", []string{"x.vim", "y.vim"}},
		// getopt gives -I the rest of the word, so f is the program here.
		{"tar -xIf ./prog a.tar", []string{"f"}},
		{"tar -xf a.tar -I ./prog", []string{"./prog"}},
	} {
		toks := tokenize(tc.cmd)
		got := executionFileTargets(commandName(toks[0]), toks)
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("executionFileTargets(%q) = %q, want %q", tc.cmd, got, tc.want)
		}
	}
}

// TestOptSpecWriteTargets covers where the output-file options of curl, wget
// and git put their files, including spellings the old scans misread.
func TestOptSpecWriteTargets(t *testing.T) {
	for _, tc := range []struct {
		cmd  string
		want []string
	}{
		{"curl -o /tmp/x http://h", []string{"/tmp/x"}},
		{"curl -sSLo /tmp/x http://h", []string{"/tmp/x"}},
		{"curl --output-dir /tmp -o x http://h", []string{"/tmp/x"}},
		// curl -P is --ftp-port, not a download directory.
		{"curl -P eth0 -o x http://h", []string{"x"}},
		// -H takes "-o" as the header, so there is no output file.
		{"curl -H -o x http://h", nil},
		{"curl -sEo out http://h", nil},
		{"wget -qO /tmp/x http://h/x", []string{"/tmp/x"}},
		// -O- writes to stdout: no file is named after the URL.
		{"wget -qO- http://h/x", []string{"-"}},
		{"wget --output-doc=/tmp/x http://h", []string{"/tmp/x"}},
		{"wget -qP /tmp http://h/x", []string{"/tmp/x"}},
		{"git archive --output=/tmp/a.tar HEAD", []string{"/tmp/a.tar"}},
		{"git archive -o/tmp/a.tar HEAD", []string{"/tmp/a.tar"}},
		{"git log --output=/tmp/a", []string{"/tmp/a"}},
		{"git log -- --output=/tmp/a", nil},
		{"git log --out /tmp/a", nil},
		{"tar --dir /etc -xf a", []string{"/etc"}},
		{"sort -ro /tmp/x f", []string{"/tmp/x"}},
		{"find . -fprint /tmp/x", []string{"/tmp/x"}},
		{"cp -rt /tmp a b", []string{"/tmp"}},
		{"unzip -qd /tmp/x f.zip", []string{"/tmp/x"}},
	} {
		toks := tokenize(tc.cmd)
		got := semanticWriteTargets(commandName(toks[0]), toks)
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("semanticWriteTargets(%q) = %q, want %q", tc.cmd, got, tc.want)
		}
	}
}
