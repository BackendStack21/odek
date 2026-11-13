package memory

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/BackendStack21/odek/internal/guard"
)

// ScanContent checks memory content for security threats. Returns an error if
// the content contains patterns that could compromise the agent.
//
// Checks:
//   - Invisible Unicode characters (zero-width spaces, direction overrides, BOM)
//   - Mixed confusable scripts (Cyrillic/Greek homoglyphs mixed with Latin)
//   - Prompt injection markers ("ignore previous instructions", etc.)
//   - Credential exfiltration patterns (API keys, private keys, bearer tokens)
func ScanContent(content string) error {
	if err := guard.ScanContent(context.Background(), content, nil, nil); err != nil {
		return fmt.Errorf("memory: %v", err)
	}
	return nil
}

// FactLooksUnsafe reports whether a fact embeds a download-and-execute /
// pipe-to-shell instruction — the shape a poisoned "fact" would take to turn
// the always-injected fact files into a persistent backdoor. It is applied to
// every path that persists a fact — auto-extraction at session end and
// agent-driven memory add/replace (AddFact/ReplaceFact) — because facts are
// injected into every system prompt. It does not catch every malicious fact —
// turning conversation into durable memory has an irreducible residual risk —
// but it closes the concrete download-and-run class.
//
// A fact is unsafe when any of these holds:
//
//   - The release rules, kept verbatim: a fetcher (curl, wget, fetch, iwr,
//     Invoke-WebRequest) followed on its line by a pipe into a word ending in
//     "sh", or eval/exec/source followed on the line by $( and a fetcher. A
//     line that is a Markdown table row of single bare words, such as
//     `| tool | curl | wget | sh |`, names tools and is left out of the pipe
//     rule.
//   - pipelineRunsFetch: in one pipeline, a stage that mentions a fetcher
//     feeds a later stage, through any number of stages, whose command runs
//     its input.
//   - powerShellRunsFetch: a line both runs a string as code (iex,
//     Invoke-Expression, [scriptblock]::Create) and makes a web request.
//   - substitutionFetchRes: a shell, eval, exec, source or `.` handed a
//     command or process substitution of a fetcher, or such a substitution
//     standing where a command starts.
//
// Backslash line continuations are joined before the last three checks, and
// they run on the fact as written and again with every ', " and \ removed,
// since c'u'rl and s\h are curl and sh to a shell. Every check is a regular
// expression (RE2) or a scan that visits each byte a bounded number of times,
// so the cost is linear in the fact length.
func FactLooksUnsafe(fact string) bool {
	if releasePipeRe.MatchString(blankBareTableRows(fact)) || releaseEvalRe.MatchString(fact) {
		return true
	}
	joined := lineContinuations.Replace(fact)
	if runsFetch(joined) {
		return true
	}
	return strings.ContainsAny(joined, `'"\`) && runsFetch(unquoted.Replace(joined))
}

// unquoted removes shell quoting characters.
var unquoted = strings.NewReplacer(`'`, "", `"`, "", `\`, "")

// runsFetch applies the checks after the release rules to one reading of a
// fact.
func runsFetch(text string) bool {
	// Every substitution form contains a ( or a backtick.
	if strings.ContainsAny(text, "(`") {
		for _, re := range substitutionFetchRes {
			if re.MatchString(text) {
				return true
			}
		}
	}
	scan := blankBareTableRows(text)
	return pipelineRunsFetch(scan) || powerShellRunsFetch(scan)
}

// releasePipeRe and releaseEvalRe are the rules FactLooksUnsafe shipped with,
// unchanged, so the filter never accepts a fact it used to reject (bare-word
// table rows aside).
var (
	releasePipeRe = regexp.MustCompile(`(?i)\b(curl|wget|fetch|iwr|invoke-webrequest)\b[^\n|]*\|\s*\w*sh\b`)
	releaseEvalRe = regexp.MustCompile(`(?i)\b(eval|exec|source)\b[^\n]*\$\(\s*(curl|wget|fetch)\b`)
)

// lineContinuations joins backslash-newline continuations. Each is replaced by
// as many blanks, so no word boundary the release rules rely on moves.
var lineContinuations = strings.NewReplacer("\\\r\n", "   ", "\\\n", "  ")

// blankBareTableRows replaces with blanks every line that is a Markdown table
// row whose cells are empty or a single bare word (letters, digits, _ and -):
// `| tool | curl | wget | sh |` names tools, it fetches nothing. A cell of two
// words, or with a URL, a path or a quote, keeps the row.
func blankBareTableRows(text string) string {
	if strings.IndexByte(text, '|') < 0 {
		return text
	}
	var out []byte
	for start := 0; start < len(text); {
		end := strings.IndexByte(text[start:], '\n')
		if end < 0 {
			end = len(text)
		} else {
			end += start
		}
		if isBareTableRow(text[start:end]) {
			if out == nil {
				out = []byte(text)
			}
			for i := start; i < end; i++ {
				out[i] = ' '
			}
		}
		start = end + 1
	}
	if out == nil {
		return text
	}
	return string(out)
}

func isBareTableRow(line string) bool {
	row := strings.Trim(line, " \t\r")
	if len(row) < 2 || row[0] != '|' || row[len(row)-1] != '|' {
		return false
	}
	inCell, ended := false, false
	for i := 1; i < len(row)-1; i++ {
		switch c := row[i]; {
		case c == '|':
			inCell, ended = false, false
		case c == ' ' || c == '\t':
			if inCell {
				ended = true
			}
		case isWordByte(c) || c == '-':
			if ended {
				return false
			}
			inCell = true
		default:
			return false
		}
	}
	return true
}

// pipelineRunsFetch reports whether some pipeline feeds what a fetcher
// downloads into a stage that runs it. It reads the text the way a shell
// reads a pipeline, except where that would let quoting hide a pipe:
//
//   - Every `|` and `|&` is a pipe, also inside quotes, so a quoted bar
//     (grep -v '||', sed 's|a|b|') or a prose apostrophe never ends the
//     pipeline.
//   - `||` is a logical or, which ends it, only when it is not
//     backslash-escaped and no quote pair can enclose it (no ' or " both
//     before and after it on its line): `curl … || bash restart.sh` does not
//     run the fetch's output.
//   - A pipe at the end of a line continues the pipeline on the next line;
//     any other newline ends it. `;`, `&` and `&&` do not end it, as in the
//     release rule.
//   - A fetcher word (curl, wget, fetch, iwr, irm, Invoke-WebRequest,
//     Invoke-RestMethod, DownloadString) anywhere in a stage — also behind
//     xargs — marks the pipeline as carrying a download, which every later
//     stage passes on.
//   - A stage after the download, or an output process substitution such as
//     tee >(sh), runs it when its command (see stageRunsInput) is a sink.
func pipelineRunsFetch(text string) bool {
	for start := 0; start < len(text); {
		end := pipelineLineEnd(text, start)
		if pipelineLineRunsFetch(text[start:end]) {
			return true
		}
		start = end + 1
	}
	return false
}

// pipelineLineEnd returns the index of the newline that ends the pipeline line
// starting at start: the first newline whose line does not end in a pipe.
func pipelineLineEnd(text string, start int) int {
	var last byte
	for i := start; i < len(text); i++ {
		switch c := text[i]; {
		case c == '\n':
			if last != '|' {
				return i
			}
		case !isBlank(c):
			last = c
		}
	}
	return len(text)
}

func pipelineLineRunsFetch(line string) bool {
	if strings.IndexByte(line, '|') < 0 {
		return false
	}
	sq0, sq1 := strings.IndexByte(line, '\''), strings.LastIndexByte(line, '\'')
	dq0, dq1 := strings.IndexByte(line, '"'), strings.LastIndexByte(line, '"')
	fed := false
	for i := 0; i < len(line); {
		c := line[i]
		switch {
		case c == '|':
			j := i
			for j < len(line) && line[j] == '|' {
				j++
			}
			if j-i == 2 && (i == 0 || line[i-1] != '\\') && !mayEnclose(sq0, sq1, i, j) && !mayEnclose(dq0, dq1, i, j) {
				fed = false
				i = j
				continue
			}
			if j < len(line) && line[j] == '&' {
				j++
			}
			if fed && stageRunsInput(line, j) {
				return true
			}
			i = j
		case c == '>' && i+1 < len(line) && line[i+1] == '(':
			// An output process substitution (tee >(sh)) reads like a stage.
			if fed && stageRunsInput(line, i+2) {
				return true
			}
			i += 2
		case isWordByte(c):
			j := wordEnd(line, i)
			if isPipeFetcher(line, i, j) {
				fed = true
			}
			i = j
		default:
			i++
		}
	}
	return false
}

// mayEnclose reports whether a quote character at first and another at last
// can enclose the bytes [i, j).
func mayEnclose(first, last, i, j int) bool {
	return first >= 0 && first < i && last >= j
}

// isPipeFetcher reports whether the word s[i:j] names a fetcher.
func isPipeFetcher(s string, i, j int) bool {
	w := s[i:j]
	if equalFoldAny(w, "curl", "wget", "fetch", "iwr", "irm", "downloadstring") {
		return true
	}
	if strings.EqualFold(w, "invoke") && j < len(s) && s[j] == '-' {
		return equalFoldAny(s[j+1:wordEnd(s, j+1)], "webrequest", "restmethod")
	}
	return false
}

// stageWrappers run the command that follows them (after their own options):
// sudo -u root bash, env -i bash, xargs -n 1 sh, busybox sh, nice -n 10 sh.
var stageWrappers = map[string]bool{
	"sudo": true, "doas": true, "pkexec": true, "run0": true, "env": true, "nice": true, "ionice": true,
	"nohup": true, "setsid": true, "time": true, "timeout": true, "stdbuf": true, "exec": true,
	"command": true, "builtin": true, "xargs": true, "parallel": true, "busybox": true,
}

// stageRunsInput reports whether the pipeline stage starting at p runs its
// input. Its command — after VAR=value assignments, redirects and wrappers
// with their options — must run what it reads (runsInput), or be
// source/`.` of the standard input (nothing, -, /dev/stdin, /dev/fd/0,
// /proc/self/fd/0), or sudo/doas with -s/-i (a shell). A wrapper option's
// argument (sudo -u root, xargs -n 1, timeout 10s) is skipped. In a group
// that opens the stage — (cd /tmp && sh), { sh; } — every word is checked.
func stageRunsInput(line string, p int) bool {
	wrapper, optArg, group := "", false, false
	for {
		tok, next := stageWord(line, p)
		if tok == "" {
			return false
		}
		p = next
		if runsInput(tok) {
			return true
		}
		if group {
			continue
		}
		if tok[0] == ';' || tok == "&" || tok == "&&" {
			return false
		}
		name := commandName(tok)
		switch {
		case name == "source" || name == ".":
			arg, _ := stageWord(line, p)
			return arg == "" || arg == "-" || stdinPaths[strings.Trim(arg, `'"`)]
		case isRedirect(tok):
			if strings.TrimLeft(tok, "0123456789&<>") == "" {
				_, p = stageWord(line, p)
			}
		case tok[0] == '(' || tok[0] == '{':
			group = true
		case name == "" || isAssignment(tok):
		case stageWrappers[name]:
			wrapper, optArg = name, false
		case wrapper == "":
			return false
		case tok[0] == '-':
			if (wrapper == "sudo" || wrapper == "doas") && shellFlags[tok] {
				return true
			}
			optArg = true
		case optArg || isCount(tok):
			optArg = false
		default:
			return false
		}
	}
}

var (
	stdinPaths = map[string]bool{"/dev/stdin": true, "/dev/fd/0": true, "/proc/self/fd/0": true}
	// shellFlags make sudo or doas start a shell, which reads the pipe.
	shellFlags = map[string]bool{"-s": true, "-i": true, "--shell": true, "--login": true}
)

// runsInput reports whether a command word runs what it reads: a word
// ending in "sh" (sh, bash, zsh, tclsh, wish, ssh host, … as the release
// rule matched it, also spelled by path, quoted, escaped or with .exe),
// PowerShell, Invoke-Expression (iex), su or $SHELL.
func runsInput(tok string) bool {
	if endsInSh(tok) {
		return true
	}
	// The name with / as the path separator, and with \ as one (a Windows
	// path such as C:\Windows\…\powershell.exe).
	for _, name := range [2]string{commandName(tok), commandName(tok[strings.LastIndexByte(tok, '\\')+1:])} {
		if endsInSh(name) {
			return true
		}
		switch name {
		case "powershell", "iex", "invoke-expression", "su", "$shell":
			return true
		}
	}
	return false
}

// endsInSh reports whether the word starting s ends in "sh", as the release
// rule's \w*sh\b matched it.
func endsInSh(s string) bool {
	w := s[:wordEnd(s, 0)]
	return len(w) >= 2 && strings.EqualFold(w[len(w)-2:], "sh")
}

// commandName returns the lower-cased command name of a word: its base name
// (after the last /), without quotes, backslashes, backticks, brackets or !,
// and without an .exe suffix.
func commandName(tok string) string {
	if k := strings.LastIndexByte(tok, '/'); k >= 0 {
		tok = tok[k+1:]
	}
	b := make([]byte, 0, len(tok))
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		if strings.IndexByte("'\"\\`(){}!", c) >= 0 {
			continue
		}
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		b = append(b, c)
	}
	return strings.TrimSuffix(string(b), ".exe")
}

// stageWord returns the next blank-separated word of the stage at p and the
// index after it, or "" at the end of the stage (a pipe or the line end).
func stageWord(line string, p int) (string, int) {
	for p < len(line) && (isBlank(line[p]) || line[p] == '\n') {
		p++
	}
	start := p
	for p < len(line) && !isBlank(line[p]) && line[p] != '\n' && line[p] != '|' {
		p++
	}
	return line[start:p], p
}

func isRedirect(tok string) bool {
	t := strings.TrimLeft(tok, "0123456789")
	return strings.HasPrefix(t, "<") || strings.HasPrefix(t, ">") || strings.HasPrefix(tok, "&>")
}

func isAssignment(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 || ('0' <= tok[0] && tok[0] <= '9') {
		return false
	}
	return wordEnd(tok, 0) == eq
}

// isCount reports whether a word is a number or duration (xargs -n 1,
// timeout 10s) or the xargs placeholder {}.
func isCount(tok string) bool {
	if tok == "{}" {
		return true
	}
	t := strings.TrimRight(tok, "smhd")
	if t == "" || len(tok)-len(t) > 1 {
		return false
	}
	return strings.Trim(t, "0123456789.") == ""
}

// powerShellRunsFetch reports whether a line both runs a string as code —
// Invoke-Expression (iex) or [scriptblock]::Create — and makes a web request:
// Invoke-RestMethod / Invoke-WebRequest (irm, iwr, curl, wget) followed by a
// request target, or a WebClient DownloadString/DownloadFile/DownloadData
// call. The line is the whole statement list: `$s = irm …; iex $s` counts. It
// continues past a newline while a bracket is open or after a trailing pipe
// or backtick. Naming a cmdlet without a target ("mention iex and irm",
// Elixir's "IEx (curl is not installed)") makes no request.
func powerShellRunsFetch(text string) bool {
	runs, fetches := false, false
	depth := 0
	var last byte
	for i := 0; i < len(text); {
		c := text[i]
		switch {
		case c == '\n':
			if depth == 0 && last != '|' && last != '`' {
				runs, fetches = false, false
			}
			i++
			continue
		case isWordByte(c):
			r, f, j := powerShellWord(text, i)
			runs, fetches = runs || r, fetches || f
			if runs && fetches {
				return true
			}
			last = text[j-1]
			i = j
			continue
		case c == '(' || c == '{':
			depth++
		case c == ')' || c == '}':
			if depth > 0 {
				depth--
			}
		}
		if !isBlank(c) {
			last = c
		}
		i++
	}
	return false
}

// powerShellWord classifies the word starting at i: whether it runs a string
// as code, whether it makes a web request, and where it ends.
func powerShellWord(text string, i int) (runs, fetches bool, end int) {
	j := wordEnd(text, i)
	w := text[i:j]
	switch {
	case equalFoldAny(w, "iex", "scriptblock"):
		return true, false, j
	case equalFoldAny(w, "irm", "iwr", "curl", "wget"):
		return false, hasRequestTarget(text, j), j
	case equalFoldAny(w, "downloadstring", "downloadfile", "downloaddata"):
		k := j
		for k < len(text) && isBlank(text[k]) {
			k++
		}
		return false, k < len(text) && text[k] == '(', j
	case strings.EqualFold(w, "invoke") && j < len(text) && text[j] == '-':
		k := wordEnd(text, j+1)
		switch verb := text[j+1 : k]; {
		case strings.EqualFold(verb, "expression"):
			return true, false, k
		case equalFoldAny(verb, "webrequest", "restmethod"):
			return false, hasRequestTarget(text, k), k
		}
		return false, false, k
	}
	return false, false, j
}

// hasRequestTarget reports whether the cmdlet name ending at k is followed by
// something shaped like a request target: an attached or separate
// parenthesised expression, an option (-Uri), a variable, a quoted string, or
// a word with a scheme, a slash, a dotted host or a port.
func hasRequestTarget(text string, k int) bool {
	if k < len(text) && text[k] == '(' {
		return true
	}
	p := k
	for p < len(text) && isBlank(text[p]) {
		p++
	}
	if p == k || p == len(text) {
		return false
	}
	switch text[p] {
	case '-', '$', '\'', '"', '(', '@', '[':
		return true
	}
	for q := p; q < len(text) && !isBlank(text[q]) && strings.IndexByte("\n)|;}", text[q]) < 0; q++ {
		switch c := text[q]; {
		case c == '/':
			return true
		case q+1 < len(text) && c == '.' && isWordByte(text[q+1]):
			return true
		case q+1 < len(text) && c == ':' && '0' <= text[q+1] && text[q+1] <= '9':
			return true
		}
	}
	return false
}

func isWordByte(c byte) bool {
	return c == '_' || ('0' <= c && c <= '9') || ('a' <= c|0x20 && c|0x20 <= 'z')
}

func isBlank(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v'
}

// wordEnd returns the end of the run of word bytes starting at i.
func wordEnd(s string, i int) int {
	for i < len(s) && isWordByte(s[i]) {
		i++
	}
	return i
}

func equalFoldAny(w string, names ...string) bool {
	for _, n := range names {
		if strings.EqualFold(w, n) {
			return true
		}
	}
	return false
}

// The substitution forms. Shell names are an explicit list here (a word that
// merely ends in "sh" before a code span is often prose: "Refresh `curl …`").
const (
	fetcherAlt   = `(?:curl|wget|fetch|iwr|irm|invoke-webrequest|invoke-restmethod|downloadstring)`
	psFetcherAlt = `(?:iwr|irm|invoke-webrequest|invoke-restmethod)`
	shellNames   = `(?:sh|bash|zsh|dash|ksh|ash|fish|csh|tcsh|mksh|rbash|yash|oksh|pdksh|loksh|posh|hush|osh|ysh|elvish|xonsh|tclsh|jimsh|wish|scsh|pwsh|powershell)`
	shellWord    = `(?:(?:[\w./-]*/)?busybox\s+)?(?:[\w./-]*/)?` + shellNames
	// shellStart is what may precede a shell word: the start of the fact or a
	// character that cannot continue a word or a file name (deploy.sh).
	shellStart = `(?:^|[^\w.-])`
	shellOpts  = `(?:\s+-\S+)*`
	// shellCOpts are shell options that include a -c form (-c, -ec, -e -c).
	shellCOpts = shellOpts + `\s+-[a-z]*c[a-z]*` + shellOpts
	wrapperOpt = `(?:\s+-\S+(?:\s+[^-\s|]\S*)?|\s+\w+=\S+)*`
	wrapperPre = `(?:(?:[\w./-]*/)?(?:sudo|doas|env|nice|nohup|time|exec|command)` + wrapperOpt + `\s+)*`
	// substOpen opens a command or process substitution.
	substOpen = `(?:\$\(|<\()`
	// substOpenTick also accepts a backtick, where the syntax around it (a
	// redirect, here-string or here-doc) is unambiguous shell.
	substOpenTick = "(?:\\$\\(|<\\(|`)"
	// hereOp is a redirect, here-string or here-doc operator with an optional
	// here-doc delimiter line.
	hereOp    = `<{1,3}-?\s*(?:["']?\w+["']?\s*\n\s*)?`
	stdinPath = `(?:/dev/stdin|/dev/fd/0|/proc/self/fd/0)`
	// cmdLead is what may stand right before a command a README or a prompt
	// line shows: a line start; a punctuation character (a prompt sigil
	// $ # > %, a list marker * +, a label colon, a lead-in comma, a shell
	// separator, an opening quote, bracket or backtick); a dot or dash
	// followed by a blank (1. or - list markers); or run, then, do or else.
	// A word before the command ("prefers bash `curl -s`", "In bash `curl`")
	// is prose, and a dot or dash glued to the shell word is a file name
	// (deploy.sh).
	cmdLead = `(?:(?m:^)|[^\w\s./-]|[./-]\s|\b(?:run|then|do|else)\s)\s*`
	// lineLead is a prompt sigil, list marker or short label or lead-in
	// ("Setup: ", "To install, ") before a command.
	lineLead = `(?:[$#%>]\s+|[-*+]\s+|\d+[.)]\s+|[^\n:;,|&]{1,40}[:,]\s+)?`
)

var substitutionFetchRes = []*regexp.Regexp{
	// eval, exec or source with a command or process substitution of a
	// fetcher anywhere later on the line.
	regexp.MustCompile(`(?i)\b(?:eval|exec|source)\b[^\n]*` + substOpen + `\s*` + fetcherAlt + `\b`),
	// A shell (with options such as -c) whose argument is a command or
	// process substitution of a fetcher: bash -c "$(curl …)", bash <(curl …).
	regexp.MustCompile(`(?i)` + shellStart + shellWord + `\b` + shellOpts + `\s*["']?` + substOpen + `\s*` + fetcherAlt + `\b`),
	// A shell, or source/`.` of the standard input, fed a substitution of a
	// fetcher through a redirect, a here-string or a here-doc: sh < <(wget …),
	// bash <<< "$(curl …)", bash <<EOF $(curl …).
	regexp.MustCompile(`(?i)(?:` + shellStart + shellWord + `\b` + shellOpts + `|` + shellStart + `(?:source|\.)\s+` + stdinPath + `)\s*` + hereOp + `["']?` + substOpenTick + `\s*` + fetcherAlt + `\b`),
	// The `.` builtin applied directly to a substitution of a fetcher.
	regexp.MustCompile(`(?i)(?:^|[\s;&|(])\.\s+["']?` + substOpen + `\s*` + fetcherAlt + `\b`),
	// A backtick substitution of a fetcher as the argument of a sink: anywhere
	// after eval (which re-parses its arguments), after a shell's -c option,
	// or right after a shell, exec, source or `.` that starts a command
	// (cmdLead; wrappers such as sudo may come between). A shell or exec
	// handed the output as words runs it.
	regexp.MustCompile("(?i)(?:\\beval\\b[^\\n]*|" + shellStart + shellWord + `\b` + shellCOpts + `\s+["']?|` +
		cmdLead + wrapperPre + `(?:` + shellWord + `\b` + shellOpts + `|exec|source|\.)\s+["']?)` + "`\\s*" + fetcherAlt + `\b`),
	// $( of a fetcher where a command starts — at a line start (after a prompt
	// sigil, list marker or short label), after ; & | ( { or then/do/else, or
	// behind a wrapper: the downloaded text runs as a command. An assignment
	// (VERSION=$(curl …)) or an argument (echo $(curl …)) does not.
	regexp.MustCompile(`(?i)(?:(?m:^)|[;&|({]|\b(?:then|do|else)\s)\s*` + lineLead + wrapperPre + `\$\(\s*` + fetcherAlt + `\b`),
	// A backtick substitution of a fetcher at a line start (after a prompt
	// sigil) or after ;, && or ||. Elsewhere a backtick span is a Markdown
	// code span in prose.
	regexp.MustCompile("(?i)(?:(?m:^)|;|&&|\\|\\|)\\s*(?:[$#%>]\\s+)?" + wrapperPre + "`\\s*" + fetcherAlt + `\b`),
	// Invoke-Expression or [scriptblock]::Create applied directly to a
	// PowerShell request, whatever its argument: iex (irm …),
	// iex "& { $(irm …) }".
	regexp.MustCompile(`(?i)(?:\b(?:iex|invoke-expression)\b(?:\s+-\w+)*\s*["']?(?:&\s*\{\s*)?|\[scriptblock\]::create\s*)(?:\$?\(\s*)+` + psFetcherAlt + `\b`),
}
