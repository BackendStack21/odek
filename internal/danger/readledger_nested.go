package danger

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	// maxNestedScriptBytes bounds how much of a licensed script is inspected.
	// A larger script fails closed: it stays gated rather than half-read.
	maxNestedScriptBytes = 256 << 10
	// maxNestedDepth bounds how far a chain of licensed scripts is followed.
	maxNestedDepth = 6
	// maxShellSubstitutionDepth bounds nested $( ). Past it the parse fails closed,
	// which also keeps hostile nesting from costing quadratic time.
	maxShellSubstitutionDepth = 64
	// maxShellWordBytes is the longest word worth resolving as a path.
	maxShellWordBytes = 4 << 10
)

var (
	// nestedPathToken finds path-shaped words in any language: ./tool,
	// bin/tool, "tools/x", TOOL=./x, subprocess.run(["./x"]).
	nestedPathToken = regexp.MustCompile(`(?:^|[\s"'` + "`" + `(=\[,;:|&])((?:\.{1,2}/|/)?[\w.\-]+(?:/[\w.\-]+)+|\.{1,2}/[\w.\-]+)`)
	// nestedPrefixedPath finds a path built on a value the script computes:
	// $DIR/tool, ${DIR}/tool, $(dirname "$0")/tool.
	nestedPrefixedPath = regexp.MustCompile(`(?:\$\{?\w+\}?|\$\([^)\n]*\))/([\w.\-]+(?:/[\w.\-]+)*)`)
	// shellAssignment is VAR=value in command position.
	shellAssignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\[[^\]]*\])?\+?=`)
)

// nestedUnreadTargetsAs returns the workspace-local programs that a licensed
// script launches and that have not themselves been licensed.
//
// The read ledger exists so a person or model has seen what runs. A script that
// was read but launches a binary no one read defeats it: the read licensed the
// wrapper and the payload ran unseen. Native binaries are always returned,
// because reading one proves nothing about what it does. Text scripts are
// followed once read. The inspection is static and best effort, so a path
// assembled at run time can escape it; the sandbox and approval gates remain.
//
// runner names what executes script ("shell" or a language), when known. What runs
// a file decides what it is, whatever its name says: `bash x.py` is shell code.
func nestedUnreadTargetsAs(key, script string, visited map[string]bool, depth int, runner string) []string {
	visited[filepath.Clean(script)] = true
	if depth >= maxNestedDepth {
		return []string{script}
	}
	text, ok := readNestedScript(script)
	if !ok {
		return []string{script}
	}
	var out []string
	isShell := runner == nestedRunnerShell || (runner == "" && nestedIsShellScript(script, text))
	if isShell && shellProgramIsComputed(text) {
		// What runs is not on the page, so the read cannot license it.
		out = append(out, script)
	}
	if isShell {
		out = append(out, inlinePayloadTargets(key, text)...)
	} else {
		language := runner
		if language == "" {
			language = nestedLanguageOfFile(script, text)
		}
		if nestedCodeCanSpawn(language, text) {
			// A primitive that can launch or assemble a program: the page does not say what runs.
			out = append(out, script)
		}
	}
	return append(out, nestedTargetsInText(key, text, filepath.Dir(script), visited, depth, isShell)...)
}

// nestedRunnerShell marks a file a shell runs or sources.
const nestedRunnerShell = "shell"

// commandRunner names what runs the operand of this command, or "" if unknown.
func (c shellCommand) runner(program int) string {
	name := commandName(strings.Trim(c.words[program], `"'`))
	if pipedShells[name] || name == "source" || name == "." {
		return nestedRunnerShell
	}
	return nestedLanguageOfInterpreter(name)
}

// nestedSpawn lists, per language, the primitives that can launch a program or
// build code at run time. Literal paths are not needed for a hit: the point is
// that a string assembled at run time reaches them. A language with no entry
// fails closed.
var nestedSpawn = map[string]*regexp.Regexp{
	"python": regexp.MustCompile(`\b(pty|ctypes|multiprocessing|runpy|attrgetter)\b|\bimportlib\.import_module\s*\(|from\s+subprocess\s+import|from\s+os\s+import\s+(\*|.*\b(system|popen|exec|spawn))|(^|[^.\w])(exec|eval|__import__)\s*\(|(^|[^.\w])getattr\s*\([^,]+,\s*['"][^'"]*['"]\s*[+%.]|(^|[^.\w])(globals|locals|vars)\s*\([^)]*\)\s*\[|__(builtins|subclasses|getattribute)__|\.__dict__\s*\[`),
	"node":   regexp.MustCompile(`process\.(binding|dlopen)|(^|[^.\w])(eval|Function)\s*\(|new\s+(Function|Worker)\b|\bimport\s*\(\s*[^'"\s]|\brequire\s*\(\s*[^'"\s]|\brequire\s*\(\s*['"][^'"]*['"]\s*[^)\s]|require\(['"]vm['"]\)|Bun\.(spawn|spawnSync|\$)|Deno\.(Command|run)|\[\s*['"\x60][^'"\x60]*['"\x60]\s*\+`),
	"ruby":   regexp.MustCompile("\\b(system|exec|spawn|fork|eval|instance_eval|class_eval|popen|Open3|PTY|Process|const_get|Fiddle|load)\\b|\\.(public_)?send\\s*\\(|\\bsend\\s*\\(|`|%x"),
	"perl":   regexp.MustCompile("\\b(system|exec|fork|eval|qx|syscall|IPC::Open[23])\\b|\\bopen\\b[^;]*(\\||-\\|)|\\b(do|require)\\s+\\$|/\\w*ee\\w*\\b|`"),
	"php":    regexp.MustCompile("\\b(shell_exec|exec|system|passthru|popen|proc_open|pcntl_\\w+|eval|assert|call_user_func\\w*|dl)\\s*\\(|\\b(include|require)(_once)?\\s*\\(?\\s*\\$|`|\\$\\w+\\s*\\("),
	"lua":    regexp.MustCompile(`\b(os\.execute|io\.popen|loadstring|dofile|loadfile|package\.loadlib)\b|\bload\s*\(|_G\b`),
}

func nestedLanguageOfInterpreter(name string) string {
	base := strings.TrimRight(name, "0123456789.")
	switch base {
	case "Rscript", "osascript", "pwsh", "powershell", "java", "scala", "nu", "nushell", "tclsh", "wish":
		return nestedUnanalyzed
	case "python", "ipython", "pypy":
		return "python"
	case "node", "deno", "bun", "ts-node", "tsx":
		return "node"
	case "ruby", "perl", "php":
		return base
	case "lua", "luajit":
		return "lua"
	}
	return ""
}

// nestedUnanalyzed marks a general-purpose runtime the gate has no table for.
const nestedUnanalyzed = "unanalyzed"

func nestedLanguageOfFile(path, text string) string {
	// The extension picks how a read script is inspected; it is not a
	// protected-name check and is not folded: an extension recognised only
	// after full folding falls through to the shebang and the shell reading,
	// as for any unknown suffix.
	switch strings.ToLower(filepath.Ext(path)) {
	case ".tcl", ".ps1", ".psm1", ".r", ".scala", ".java", ".groovy", ".kts", ".applescript", ".scpt", ".nu", ".swift", ".jl", ".ex", ".exs", ".erl", ".hs", ".clj", ".dart":
		return nestedUnanalyzed
	case ".py":
		return "python"
	case ".js", ".mjs", ".cjs", ".ts", ".tsx", ".jsx":
		return "node"
	case ".rb":
		return "ruby"
	case ".pl", ".pm":
		return "perl"
	case ".php":
		return "php"
	case ".lua":
		return "lua"
	}
	if first, _, _ := strings.Cut(text, "\n"); strings.HasPrefix(first, "#!") {
		for _, field := range strings.Fields(first) {
			if lang := nestedLanguageOfInterpreter(filepath.Base(field)); lang != "" {
				return lang
			}
		}
	}
	return ""
}

// nestedSpawnCall finds a call that launches a program, per language. A call
// whose arguments are all string literals is on the page for whoever read the
// script, so it stays licensed; any computed argument gates.
var (
	nestedPythonSpawnCall       = regexp.MustCompile(`\.(system|popen|execv\w*|execl\w*|spawn\w*|posix_spawn\w*)\s*\(`)
	nestedPythonSubprocess      = regexp.MustCompile(`\.(run|call|check_call|check_output|Popen|getoutput|getstatusoutput)\s*\(`)
	nestedNodeSpawnCall         = regexp.MustCompile(`\b(exec|execSync|execFile|execFileSync|spawn|spawnSync|fork)\s*\(`)
	nestedPythonSensitiveImport = regexp.MustCompile(`(?m)^\s*(from|import)\s+(os|subprocess|sys|builtins|importlib|shutil)\b`)
	nestedPythonDynamicGetattr  = regexp.MustCompile(`(^|[^.\w])getattr\s*\([^,]+,\s*[^'"\s]`)
	nestedLiteralArgs           = regexp.MustCompile(`^\s*\[?\s*(?:'[^'+%{}]*'|"[^"+%{}]*")(?:\s*,\s*(?:'[^'+%{}]*'|"[^"+%{}]*"))*\s*\]?\s*(?:,\s*\w+\s*=\s*\w+\s*)*\)`)
)

// A spawn primitive named but not called on the spot is being passed around:
// f = os.system, partial(os.system), {'x': os.system}. What it runs is not on the page.
var (
	nestedPythonSpawnRef   = regexp.MustCompile(`\.(system|popen|execv\w*|execl\w*|spawn[lv]\w*|posix_spawn\w*)\b\s*([^\s(]|$)|\bsubprocess\.(run|call|check_call|check_output|Popen|getoutput|getstatusoutput)\b\s*([^\s(]|$)`)
	nestedPythonNamedSpawn = regexp.MustCompile(`(^|[^.\w])getattr\s*\([^,]+,\s*['"](system|popen|exec\w*|spawn\w*|posix_spawn\w*|run|call|check_call|check_output|Popen|getoutput|getstatusoutput)['"]`)
	nestedNodeSpawnRef     = regexp.MustCompile(`\.(exec|execSync|execFile|execFileSync|spawn|spawnSync|fork)\b\s*([^\s(]|$)`)
	nestedNodeDestructure  = regexp.MustCompile(`\{[^}]*\b(exec|execSync|execFile|execFileSync|spawn|spawnSync|fork)\b[^}]*:[^}]*\}\s*=\s*require`)
)

// nestedNodeLiteralPathLoad is a require or import of a path built only from literals
// and the file's own location, which says what it loads as plainly as a string does.
var nestedNodeLiteralPathLoad = regexp.MustCompile(`\b(require|import)\s*\(\s*(path\.(join|resolve)\s*\(\s*(__dirname|import\.meta\.dirname)(\s*,\s*(['"][^'"+]*['"]))*\s*\)|new\s+URL\s*\(\s*['"][^'"+]*['"]\s*,\s*import\.meta\.url\s*\)(\.href)?)\s*\)`)

func nestedComputedCall(re *regexp.Regexp, line string) bool {
	for _, loc := range re.FindAllStringIndex(line, -1) {
		if !nestedLiteralArgs.MatchString(line[loc[1]:]) {
			return true
		}
	}
	return false
}

func nestedCodeCanSpawn(language, text string) bool {
	re, known := nestedSpawn[language]
	if !known {
		// awk, sed, make, gdb and the like: a read licenses them, as it always has,
		// and their literal paths are still checked. Runtimes with no table fail closed.
		return language == nestedUnanalyzed
	}
	usesSubprocess := language == "python" && strings.Contains(text, "subprocess")
	// getattr with a computed name only matters where the module it could reach is loaded.
	reflective := language == "python" && nestedPythonSensitiveImport.MatchString(text)
	usesChildProcess := language == "node" && strings.Contains(text, "child_process")
	if language == "node" {
		text = nestedNodeLiteralPathLoad.ReplaceAllString(text, "")
	}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") {
			continue
		}
		if re.MatchString(line) {
			return true
		}
		switch {
		case language == "python" && (nestedPythonSpawnRef.MatchString(line) || nestedPythonNamedSpawn.MatchString(line)):
			return true
		case usesChildProcess && (nestedNodeSpawnRef.MatchString(line) || nestedNodeDestructure.MatchString(line)):
			return true
		case reflective && nestedPythonDynamicGetattr.MatchString(line):
			return true
		case language == "python" && nestedComputedCall(nestedPythonSpawnCall, line):
			return true
		case usesSubprocess && nestedComputedCall(nestedPythonSubprocess, line):
			return true
		case usesChildProcess && nestedComputedCall(nestedNodeSpawnCall, line):
			return true
		}
	}
	return false
}

// nestedLocalImports finds modules a script loads from beside itself: they run
// with it, so they need reading too.
var (
	nestedPythonImport = regexp.MustCompile(`(?m)^\s*(?:from\s+([\w.]+)\s+import|import\s+([\w.]+))`)
	nestedNodeImport   = regexp.MustCompile(`(?:require\s*\(\s*|from\s+|import\s+)['"](\.{1,2}/[^'"]+)['"]`)
)

func nestedLocalImports(language, text, dir string) []string {
	var out []string
	add := func(path string) {
		if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() {
			out = append(out, filepath.Clean(path))
		}
	}
	switch language {
	case "python":
		for _, m := range nestedPythonImport.FindAllStringSubmatch(text, -1) {
			name := m[1] + m[2]
			rel := strings.ReplaceAll(strings.TrimLeft(name, "."), ".", string(filepath.Separator))
			if rel != "" {
				add(filepath.Join(dir, rel+".py"))
				add(filepath.Join(dir, rel, "__init__.py"))
			}
		}
	case "node":
		for _, m := range nestedNodeImport.FindAllStringSubmatch(text, -1) {
			for _, suffix := range []string{"", ".js", ".mjs", ".cjs", ".ts"} {
				add(filepath.Join(dir, m[1]+suffix))
			}
		}
	}
	return out
}

func nestedTargetsInText(key, text, dir string, visited map[string]bool, depth int, isShell bool) []string {
	var out []string
	for _, language := range []string{"python", "node"} {
		for _, module := range nestedLocalImports(language, text, dir) {
			if visited[module] {
				continue
			}
			visited[module] = true
			// A module runs with the script that loads it. The gate reads it here
			// rather than asking for a read: only one that can spawn needs a person.
			if body, ok := readNestedScript(module); !ok || nestedCodeCanSpawn(language, body) {
				out = append(out, module)
			} else if depth < maxNestedDepth {
				out = append(out, nestedTargetsInText(key, body, filepath.Dir(module), visited, depth+1, false)...)
			}
		}
	}
	runners := nestedOperandRunners(text, dir)
	for _, target := range nestedCandidates(text, dir, isShell) {
		if visited[target] {
			continue
		}
		visited[target] = true
		if !executableTextFile(target) || !wasReadFreshKey(key, target) {
			out = append(out, target)
			continue
		}
		out = append(out, nestedUnreadTargetsAs(key, target, visited, depth+1, runners[target])...)
	}
	return out
}

// nestedOperandRunners maps the files text hands to an interpreter to what runs them.
func nestedOperandRunners(text, dir string) map[string]string {
	cwd, _ := os.Getwd()
	out := map[string]string{}
	commands, _ := parseShellCommands(text)
	for _, c := range commands {
		i := c.program()
		if i < 0 {
			continue
		}
		runner := c.runner(i)
		if runner == "" {
			continue
		}
		if operand, ok := c.operand(i); ok {
			operand = shellUnquote(operand)
			for _, base := range []string{dir, cwd} {
				path := operand
				if !filepath.IsAbs(path) {
					path = filepath.Join(base, operand)
				}
				out[filepath.Clean(path)] = runner
			}
		}
	}
	return out
}

// inlinePayloadTargets applies the same inspection to code passed on the command
// line: python3 -c "subprocess.run(['./tool'])" launches ./tool as surely as a
// script file would. Only the program of each command counts, so `which python3`
// and `brew install node` are not code. Shells are analyzed by the classifier.
func inlinePayloadTargets(key, cmd string) []string {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	commands, _ := parseShellCommands(cmd)
	var out []string
	for _, c := range commands {
		i := c.program()
		if i < 0 {
			continue
		}
		name := commandName(strings.Trim(c.words[i], `"'`))
		if !isScriptInterpreter(name) || pipedShells[name] {
			continue
		}
		if payload, ok := c.inlineFlagPayload(i, name); ok {
			if c.inlineFlagIsComputed(i, name) || nestedCodeCanSpawn(nestedLanguageOfInterpreter(name), payload) {
				out = append(out, inlineCodeTarget(name))
			}
			out = append(out, nestedTargetsInText(key, payload, cwd, map[string]bool{}, 0, false)...)
			continue
		}
		// Code on stdin (python3 - <<EOF, python3 < x, echo x | python3) is not on any page.
		operand, ok := c.operand(i)
		if (!ok && (c.pipedIn || c.redirectIn)) || operand == "-" || operand == shellProcessSubstitution || (ok && operandIsComputed(operand)) {
			out = append(out, inlineCodeTarget(name))
		}
	}
	return out
}

// nestedIsShellScript reports whether text runs under a shell: a shell shebang,
// a shell suffix, or no shebang at all (the ENOEXEC fallback).
func nestedIsShellScript(path, text string) bool {
	// Not folded, like nestedLanguageOfFile: the suffix is a hint about the
	// content, and an unknown one is judged by the text.
	switch strings.ToLower(filepath.Ext(path)) {
	case ".sh", ".bash", ".zsh", ".ksh", ".dash":
		return true
	case ".awk", ".sed", ".mk", ".gdb", ".lldb", ".sql", ".el", ".vim":
		return false // formats read by their own tool
	}
	if nestedLanguageOfFile(path, text) != "" {
		return false
	}
	if !strings.HasPrefix(text, "#!") {
		return true // a shell runs extensionless and unknown text through the ENOEXEC fallback
	}
	first, _, _ := strings.Cut(text, "\n")
	for _, sh := range []string{"sh", "bash", "zsh", "dash", "ksh", "ash"} {
		if strings.HasSuffix(first, "/"+sh) || strings.HasSuffix(first, " "+sh) || strings.Contains(first, "/"+sh+" ") {
			return true
		}
	}
	return false
}

// nestedRunnerOfCommand names what runs the script operands of cmd.
func nestedRunnerOfCommand(cmd string) string {
	commands, _ := parseShellCommands(cmd)
	for _, c := range commands {
		if i := c.program(); i >= 0 {
			if runner := c.runner(i); runner != "" {
				return runner
			}
		}
	}
	return ""
}

// shellProgramIsComputed reports whether a shell script chooses a program at
// run time: a variable, substitution or glob in command position (behind any
// wrapper), eval, a command that runs what it finds, code on stdin, or a change
// to PATH that puts local files in front of bare names. A here-document that
// never ends fails closed, since what follows it cannot be told apart from data.
func shellProgramIsComputed(text string) bool {
	commands, complete := parseShellCommands(text)
	if !complete {
		return true
	}
	for _, c := range commands {
		if c.isComputed() {
			return true
		}
	}
	return false
}

// shellCommand is one simple command, with its words as written.
type shellCommand struct {
	words      []string
	pipedIn    bool
	redirectIn bool
	computed   bool // a nested form the parser could not follow
}

type shellScan struct {
	text    string
	pos     int
	depth   int
	cmds    []shellCommand
	pending []shellHeredoc
	inCase  int
	pattern bool
	failed  bool
	// pipeNext marks the next command as reading from a pipe.
	pipeNext bool
	// honorSingle reads a single quote inside a double-quoted ${...} as a
	// quote: the second reading paramExpansion compares against.
	honorSingle bool
}

type shellHeredoc struct {
	delim    string
	stripTab bool
	quoted   bool
}

// parseShellCommands reads text into simple commands, including those inside
// substitutions. It reports false when a here-document has no terminator.
func parseShellCommands(text string) ([]shellCommand, bool) {
	s := &shellScan{text: text}
	s.scan()
	for i := range s.cmds {
		s.cmds[i].words = expandEnvSplit(s.cmds[i].words)
	}
	return s.cmds, !s.failed
}

// expandEnvSplit opens `env -S 'cmd args'` into the words it runs.
func expandEnvSplit(words []string) []string {
	for i, word := range words {
		if commandName(strings.Trim(word, `"'`)) != "env" {
			continue
		}
		for j := i + 1; j < len(words); j++ {
			flag := shellUnquote(words[j])
			var payload string
			rest := j + 1
			switch {
			case (flag == "-S" || flag == "--split-string") && j+1 < len(words):
				payload, rest = words[j+1], j+2
			case strings.HasPrefix(flag, "--split-string="):
				payload = strings.TrimPrefix(flag, "--split-string=")
			case strings.HasPrefix(flag, "-S") && len(flag) > 2:
				payload = flag[2:]
			default:
				if !strings.HasPrefix(flag, "-") {
					return words
				}
				continue
			}
			out := append([]string{}, words[:j]...)
			out = append(out, strings.Fields(shellUnquote(payload))...)
			return append(out, words[rest:]...)
		}
		return words
	}
	return words
}

// scan reads commands until the end of the text, or the ) closing a substitution.
func (s *shellScan) scan() {
	for s.pos < len(s.text) {
		if s.scanCommand() {
			return
		}
	}
}

// skipBlanks moves past spaces, line continuations and comments.
func (s *shellScan) skipBlanks() {
	for s.pos < len(s.text) {
		c := s.text[s.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			s.pos++
		case c == '\\' && s.pos+1 < len(s.text) && s.text[s.pos+1] == '\n':
			s.pos += 2
		case c == '#':
			for s.pos < len(s.text) && s.text[s.pos] != '\n' {
				s.pos++
			}
		default:
			return
		}
	}
}

// word reads one shell word, keeping its quotes. Substitutions inside it are
// parsed as commands of their own.
func (s *shellScan) word() string {
	start := s.pos
	var quote byte
	for s.pos < len(s.text) {
		c := s.text[s.pos]
		if quote != '\'' && c != '\\' && s.expansion(quote == '"') {
			continue
		}
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case c == '\\':
			s.pos++
		case quote == 0 && c == '$' && s.pos+1 < len(s.text) && s.text[s.pos+1] == '\'':
			// $'...' reads backslash escapes, so \' does not end it.
			end, ok := skipAnsiC(s.text, s.pos+1)
			s.pos = end
			if !ok {
				s.failed = true
			}
			continue
		case quote == '"':
			if c == '"' {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case strings.IndexByte(" \t\r\n;|&()<>", c) >= 0:
			return s.text[start:s.pos]
		}
		s.pos++
	}
	if quote != 0 {
		s.failed = true // the text ends inside a quote: what follows cannot be told from data
	}
	return s.text[start:min(s.pos, len(s.text))]
}

// expansion consumes exactly the expansion that starts at s.pos — $(command),
// $((arithmetic)), `command` or ${parameter} — following the commands it
// runs, and reports whether there was one. Nothing after its closing
// delimiter is read, so the caller keeps its own quote context. inDouble says
// the expansion sits inside double quotes or a here-document body, where
// bash reads a single quote inside ${...} as an ordinary character.
func (s *shellScan) expansion(inDouble bool) bool {
	text, at := s.text, s.pos
	if at+1 >= len(text) || (text[at] != '$' && text[at] != '`') {
		return false
	}
	switch {
	case text[at] == '`':
		end := strings.IndexByte(text[at+1:], '`')
		if end < 0 {
			s.pos = len(text)
			s.failed = true
			return true
		}
		inner, complete := parseShellCommands(text[at+1 : at+1+end])
		s.cmds = append(s.cmds, inner...)
		s.failed = s.failed || !complete
		s.pos = at + end + 2
	case text[at+1] == '(' && at+2 < len(text) && text[at+2] == '(' && arithmeticExpansion(text, at):
		// The arithmetic can still hold a substitution. Its end must be the
		// same whether or not a single quote hides a parenthesis.
		s.skipBalanced('(', ')', true)
		s.scanRegion(at+3, s.pos, true)
	case text[at+1] == '(':
		if s.depth >= maxShellSubstitutionDepth {
			s.failed = true
			s.pos = len(text)
			return true
		}
		s.pos = at + 2
		s.depth++
		s.scan()
		s.depth--
	case text[at+1] == '{':
		s.paramExpansion(inDouble) // ${X:-$(cmd)} runs cmd
	default:
		return false
	}
	return true
}

// paramExpansion consumes the ${...} whose `$` is at s.pos in one pass,
// following the commands of the expansions nested in it ($(...), `...`,
// ${...}, $((...)), each read as one construct, so a `}` inside a nested
// substitution does not end it). As in bash, the construct ends at the first
// `}` outside quotes and nested expansions; a bare `{` opens nothing.
//
// Inside a double-quoted construct (inDouble) bash reads a single quote as an
// ordinary character, while other shells (and bash for some operators) read
// it as a quote. When such a single quote occurs, the construct is read again
// honouring it, over the same text only, and the parse fails closed if the two
// readings end in different places.
func (s *shellScan) paramExpansion(inDouble bool) {
	start := s.pos
	if s.depth >= maxShellSubstitutionDepth {
		s.failed = true
		s.pos = len(s.text)
		return
	}
	s.depth++
	defer func() { s.depth-- }()
	s.pos += 2
	double := false // inside "..." within the construct
	ambiguous := false
	for s.pos < len(s.text) {
		c := s.text[s.pos]
		switch {
		case c == '\\':
			s.pos += 2
		case c == '}' && !double:
			s.pos++
			if ambiguous && !s.honorSingle {
				alt := &shellScan{text: s.text[:s.pos], pos: start, depth: s.depth - 1, honorSingle: true}
				alt.paramExpansion(true)
				if alt.failed || alt.pos != s.pos {
					s.failed = true
				}
			}
			return
		case s.expansion(inDouble || double):
		case c == '"':
			double = !double
			s.pos++
		case c == '\'' && double:
			s.pos++ // an ordinary character inside "..."
		case c == '\'' && inDouble && !s.honorSingle:
			ambiguous = true
			s.pos++
		case c == '\'':
			if s.pos > start+2 && s.text[s.pos-1] == '$' {
				end, ok := skipAnsiC(s.text, s.pos)
				s.pos = end
				if !ok {
					s.failed = true
				}
				continue
			}
			end := strings.IndexByte(s.text[s.pos+1:], '\'')
			if end < 0 {
				s.pos = len(s.text)
				s.failed = true
				return
			}
			s.pos += end + 2
		default:
			s.pos++
		}
	}
	s.pos = len(s.text)
	s.failed = true // the construct never ends
}

// arithmeticExpansion reports whether the `$((` at text[at] is arithmetic:
// bash reads it so only when the group the second parenthesis opens closes
// right before the first one does. `$((cmd) )` and `$((cmd) | tr a b)` are a
// command substitution holding a subshell, whose command runs.
func arithmeticExpansion(text string, at int) bool {
	inner, ok := skipBalancedEnd(text, at+2, '(', ')', true)
	outer, outerOK := skipBalancedEnd(text, at+1, '(', ')', true)
	return !ok || !outerOK || outer == inner+1
}

// skipAnsiC returns the offset after the $'...' string whose opening quote is
// at text[quote], and whether it was closed.
func skipAnsiC(text string, quote int) (int, bool) {
	for i := quote + 1; i < len(text); i++ {
		switch text[i] {
		case '\\':
			i++
		case '\'':
			return i + 1, true
		}
	}
	return len(text), false
}

// skipDoubleBracket returns the offset after the ]] that closes the [[ whose
// opener ends at pos. A ]] inside quotes is data.
func skipDoubleBracket(text string, pos int) (int, bool) {
	var quote byte
	for i := pos; i < len(text); i++ {
		c := text[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case quote == '"':
			switch c {
			case '\\':
				i++
			case '"':
				quote = 0
			}
		case c == '\\':
			i++
		case c == '"' || c == '\'':
			if c == '\'' && i > 0 && text[i-1] == '$' {
				end, ok := skipAnsiC(text, i)
				if !ok {
					return len(text), false
				}
				i = end - 1
				continue
			}
			quote = c
		case c == ']' && i+1 < len(text) && text[i+1] == ']':
			return i + 2, true
		}
	}
	return len(text), false
}

// skipBalanced moves past the construct that starts at the open byte under the
// pointer, to the byte after its matching closer. Quotes inside it hide the
// delimiters they hold: '...', "...", $'...' (with backslash escapes) and
// backslash-escaped bytes. Single quotes inside a double-quoted parameter
// expansion are read differently by different shells, so when the text is
// double-quoted and the two readings end in different places, or the construct
// never ends, the scan is marked failed and callers treat the script as one
// they cannot follow.
func (s *shellScan) skipBalanced(open, closer byte, inDouble bool) {
	end, ok := skipBalancedEnd(s.text, s.pos, open, closer, true)
	if inDouble {
		other, otherOK := skipBalancedEnd(s.text, s.pos, open, closer, false)
		if ok != otherOK || other != end {
			ok = false
		}
	}
	s.pos = end
	if !ok {
		s.failed = true
	}
}

// skipBalancedEnd returns the offset after the closer matching the open byte at
// text[pos] and whether it was found. honorSingle says whether a single quote
// begins a quoted string.
func skipBalancedEnd(text string, pos int, open, closer byte, honorSingle bool) (int, bool) {
	depth := 0
	var quote byte
	for i := pos; i < len(text); i++ {
		c := text[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case quote == 'a': // $'...': backslash escapes the next byte
			switch c {
			case '\\':
				i++
			case '\'':
				quote = 0
			}
		case quote == '"':
			switch c {
			case '\\':
				i++
			case '"':
				quote = 0
			}
		case c == '\\':
			i++
		case c == '"':
			quote = '"'
		case c == '\'' && honorSingle:
			if i > 0 && text[i-1] == '$' {
				quote = 'a'
			} else {
				quote = '\''
			}
		case c == open:
			depth++
		case c == closer:
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
	}
	return len(text), false
}

// scanCommand reads one simple command. It reports true when it reached the )
// that closes the substitution being scanned.
func (s *shellScan) scanCommand() (closed bool) {
	var cmd shellCommand
	nextPiped := false
	defer func() {
		if len(cmd.words) > 0 || cmd.computed {
			s.cmds = append(s.cmds, cmd)
		}
		if nextPiped && s.pos < len(s.text) {
			s.pipeNext = true
		}
	}()
	cmd.pipedIn = s.pipeNext
	s.pipeNext = false
	if s.pattern {
		s.skipBlanksAndLines()
		if !strings.HasPrefix(s.text[s.pos:], "esac") {
			s.skipCasePattern()
		}
		s.pattern = false
	}
	for s.pos < len(s.text) {
		s.skipBlanks()
		if s.pos >= len(s.text) {
			return false
		}
		switch c := s.text[s.pos]; c {
		case '\n':
			s.pos++
			s.readHeredocs()
			return false
		case ';':
			s.pos++
			for s.pos < len(s.text) && (s.text[s.pos] == ';' || s.text[s.pos] == '&') {
				s.pos++
				s.pattern = s.inCase > 0
			}
			return false
		case '&':
			s.pos++
			if s.pos < len(s.text) && s.text[s.pos] == '&' {
				s.pos++
			}
			return false
		case '|':
			s.pos++
			if s.pos < len(s.text) && s.text[s.pos] == '|' {
				s.pos++
			} else {
				if s.pos < len(s.text) && s.text[s.pos] == '&' {
					s.pos++
				}
				nextPiped = true
			}
			return false
		case '(':
			s.pos++
			if n := len(cmd.words); n > 0 && strings.HasSuffix(cmd.words[n-1], "=") {
				begin := s.pos
				s.pos--
				s.skipBalanced('(', ')', false) // an array literal holds data, and perhaps a substitution
				s.scanRegion(begin, s.pos, false)
				continue
			}
			return false
		case ')':
			s.pos++
			return s.depth > 0
		case '<', '>':
			s.scanRedirect(&cmd)
			continue
		}
		before := s.pos
		word := s.word()
		if s.pos == before {
			s.pos++ // always make progress on input the word reader will not take
			continue
		}
		if isDigits(word) && s.pos < len(s.text) && strings.IndexByte("<>", s.text[s.pos]) >= 0 {
			continue // the file descriptor of a redirect
		}
		if word == "[[" {
			begin := s.pos
			if end, ok := skipDoubleBracket(s.text, s.pos); ok {
				s.pos = end
			} else {
				s.pos = len(s.text)
				s.failed = true
			}
			s.scanRegion(begin, s.pos, false)
			cmd.words = append(cmd.words, word)
			continue
		}
		if (word == "{" || word == "}") && len(cmd.words) > 0 && strings.Trim(cmd.words[0], `"'`) != "echo" {
			return false // a group begins or ends: what follows is a new command
		}
		cmd.words = append(cmd.words, word)
		if len(cmd.words) == 1 && word == "esac" && s.inCase > 0 {
			s.inCase--
		}
		if word == "in" && len(cmd.words) >= 3 && cmd.words[0] == "case" {
			s.inCase++
			s.pattern = true
			return false
		}
	}
	return false
}

func (s *shellScan) skipBlanksAndLines() {
	for {
		s.skipBlanks()
		if s.pos < len(s.text) && s.text[s.pos] == '\n' {
			s.pos++
			continue
		}
		return
	}
}

// skipCasePattern moves past `a|b)`, which is a pattern rather than a command.
func (s *shellScan) skipCasePattern() {
	var quote byte
	for s.pos < len(s.text) {
		c := s.text[s.pos]
		s.pos++
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '\\':
			s.pos++
		case c == ')':
			return
		}
	}
}

// scanRedirect reads a redirection, noting input redirects and here-documents.
func (s *shellScan) scanRedirect(cmd *shellCommand) {
	rest := s.text[s.pos:]
	switch {
	case strings.HasPrefix(rest, "<<<"):
		s.pos += 3
		cmd.redirectIn = true
	case strings.HasPrefix(rest, "<<"):
		s.pos += 2
		h := shellHeredoc{}
		if s.pos < len(s.text) && s.text[s.pos] == '-' {
			h.stripTab = true
			s.pos++
		}
		s.skipBlanks()
		raw := s.word()
		h.delim = strings.TrimRight(strings.Trim(raw, `"'\`), "\r")
		h.quoted = raw != h.delim
		s.pending = append(s.pending, h)
		cmd.redirectIn = true
		return
	case strings.HasPrefix(rest, "<("):
		s.pos += 2
		s.depth++
		s.scan()
		s.depth--
		cmd.words = append(cmd.words, shellProcessSubstitution)
		return
	case rest[0] == '<':
		s.pos++
		cmd.redirectIn = true
	default:
		s.pos++
	}
	for s.pos < len(s.text) && strings.IndexByte("<>&|", s.text[s.pos]) >= 0 {
		s.pos++
	}
	s.skipBlanks()
	s.word()
}

// readHeredocs consumes the bodies of the here-documents opened on the line just
// ended. A body with no terminator fails the parse.
func (s *shellScan) readHeredocs() {
	for _, h := range s.pending {
		start := s.pos
		found := false
		for s.pos < len(s.text) {
			end := strings.IndexByte(s.text[s.pos:], '\n')
			line := s.text[s.pos:]
			next := len(s.text)
			if end >= 0 {
				line = s.text[s.pos : s.pos+end]
				next = s.pos + end + 1
			}
			line = strings.TrimRight(line, "\r")
			if h.stripTab {
				line = strings.TrimLeft(line, "\t")
			}
			if line == h.delim {
				body := s.text[start:s.pos]
				s.pos = next
				found = true
				if !h.quoted {
					s.scanHeredocBody(body)
				}
				break
			}
			s.pos = next
		}
		if !found {
			s.failed = true
		}
	}
	s.pending = nil
}

// scanRegion follows the substitutions inside text[begin:end], a construct the
// parser otherwise treats as data whose end is already known: a ${...} body,
// $((...)), [[ ... ]] or an array literal. Substitutions run unquoted and
// inside double quotes; a single-quoted span hides them, except where a
// single quote is an ordinary character — inside double quotes, and
// throughout a construct that is itself double-quoted (inDouble), as bash
// reads `"${x:-'$(cmd)'}"`.
func (s *shellScan) scanRegion(begin, end int, inDouble bool) {
	if begin >= end || end > len(s.text) {
		return
	}
	sub := &shellScan{text: s.text[:end], pos: begin, depth: s.depth}
	double := false
	for sub.pos < end {
		c := sub.text[sub.pos]
		switch {
		case c == '\\':
			sub.pos += 2
		case sub.expansion(inDouble || double):
		case c == '"':
			double = !double
			sub.pos++
		case c == '\'' && !inDouble && !double:
			if sub.pos > begin && sub.text[sub.pos-1] == '$' {
				sub.pos, _ = skipAnsiC(sub.text, sub.pos)
				continue
			}
			if close := strings.IndexByte(sub.text[sub.pos+1:], '\''); close >= 0 {
				sub.pos += close + 2
			} else {
				sub.pos = end
			}
		default:
			sub.pos++
		}
	}
	s.cmds = append(s.cmds, sub.cmds...)
	s.failed = s.failed || sub.failed
}

// scanHeredocBody follows the substitutions of an unquoted here-document, which
// run even though the rest of it is data. Quotes are ordinary characters there:
// only a backslash and the expansions ($(...), `...`, ${...}, $((...))) are
// active, each read as one construct and never as the start of a shell word.
func (s *shellScan) scanHeredocBody(body string) {
	sub := &shellScan{text: body, depth: s.depth}
	for sub.pos < len(body) {
		switch {
		case body[sub.pos] == '\\':
			sub.pos += 2
		case sub.expansion(true):
		default:
			sub.pos++
		}
	}
	s.cmds = append(s.cmds, sub.cmds...)
	s.failed = s.failed || sub.failed
}

// shellProcessSubstitution stands in for a <( ) operand.
const shellProcessSubstitution = "<(...)"

// shellKeywords come before the program of a command.
var shellKeywords = map[string]bool{
	"then": true, "do": true, "else": true, "elif": true, "if": true, "while": true,
	"until": true, "!": true, "{": true, "time": true, "function": true,
}

// shellWrappers run the command named by their remaining words.
var shellWrappers = map[string]bool{
	"sudo": true, "doas": true, "timeout": true, "gtimeout": true, "nice": true, "ionice": true,
	"env": true, "command": true, "builtin": true, "exec": true, "nohup": true, "setsid": true,
	"stdbuf": true, "chroot": true, "strace": true, "ltrace": true, "watch": true, "caffeinate": true,
	"unbuffer": true, "xvfb-run": true, "valgrind": true, "firejail": true, "taskset": true, "numactl": true,
	"chrt": true, "unshare": true, "nsenter": true,
}

// shellOpaqueRunners run a command given in a form too varied to parse. Any
// computed word among their arguments fails closed.
var shellOpaqueRunners = map[string]bool{
	"busybox": true, "toybox": true, "script": true, "flock": true, "su": true, "systemd-run": true,
	"perf": true, "gdb": true, "lldb": true, "ssh": true, "dtach": true, "tmux": true, "screen": true,
	"at": true, "batch": true, "crontab": true, "sg": true, "runuser": true, "pkexec": true,
}

// shellHelperTools run a program file named by an option (awk -f prog).
var shellHelperTools = map[string]bool{
	"awk": true, "gawk": true, "mawk": true, "nawk": true, "sed": true, "gsed": true, "gdb": true, "lldb": true,
	"make": true, "gmake": true, "sqlite3": true, "emacs": true, "vim": true, "nvim": true, "vi": true,
}

// shellWrapperValueFlags take the next word as their value.
var shellWrapperValueFlags = map[string]bool{
	"-n": true, "-u": true, "-g": true, "-k": true, "-s": true, "-c": true, "-C": true, "-o": true,
	"-p": true, "-t": true, "-e": true, "-I": true, "-i0": true, "--signal": true, "--kill-after": true,
	"--user": true, "--group": true, "--adjustment": true, "--interval": true,
}

func shellWrapperTakesValue(wrapper, flag string) bool {
	switch wrapper {
	case "command", "builtin":
		return false
	case "env":
		return flag == "-u" || flag == "-C" || flag == "--unset" || flag == "--chdir"
	case "exec":
		return flag == "-a"
	}
	return shellWrapperValueFlags[flag]
}

// program strips keywords, assignments and wrappers, returning the index of the
// word that names the program, or -1 when the command runs none.
func (c shellCommand) program() int {
	for i := 0; i < len(c.words); {
		word := c.words[i]
		bare := strings.Trim(word, `"'`)
		// A wrapper is known by its command name, however it is spelled:
		// /usr/bin/env and "/usr/bin/env" are env.
		name := commandName(bare)
		switch {
		case shellAssignment.MatchString(word):
			i++
		case shellKeywords[bare] && name != "time":
			i++
		case shellWrappers[name] || name == "time":
			// command -v and friends ask about a name without running it.
			if (name == "command" || name == "builtin") && i+1 < len(c.words) && strings.HasPrefix(c.words[i+1], "-v") {
				return -1
			}
			if (name == "command") && i+1 < len(c.words) && c.words[i+1] == "-V" {
				return -1
			}
			// time's options and the fixed operands of taskset, chrt, flock
			// and script come from the grammar the classifier unwraps with.
			if name == "time" || wrapperOperands[name] > 0 {
				tokens := make([]string, len(c.words))
				for k, w := range c.words {
					tokens[k] = strings.Trim(w, `"'`)
				}
				if step, ok := wrapperAt(tokens, i); ok && step.payload == "" && len(step.splits) == 0 {
					i = step.next
					continue
				}
			}
			i++
			for i < len(c.words) && strings.HasPrefix(c.words[i], "-") {
				flag := c.words[i]
				i++
				if shellWrapperTakesValue(name, flag) && i < len(c.words) {
					i++
				}
			}
			if (name == "timeout" || name == "gtimeout") && i < len(c.words) {
				i++ // the duration
			}
			if name == "chroot" && i < len(c.words) {
				i++ // the new root
			}
		default:
			return i
		}
	}
	return -1
}

// operand returns the first word after flags: the file an interpreter runs.
func (c shellCommand) operand(program int) (string, bool) {
	isShell := pipedShells[commandName(strings.Trim(c.words[program], `"'`))]
	for i := program + 1; i < len(c.words); i++ {
		word := c.words[i]
		// A shell's -o takes an option name, alone or at the end of a cluster (-euo pipefail).
		if isShell && len(word) >= 2 && (word[0] == '-' || word[0] == '+') && word[1] != '-' && word[len(word)-1] == 'o' {
			i++
			continue
		}
		if word == "-" {
			return word, true
		}
		if strings.HasPrefix(word, "-") || strings.HasPrefix(word, "+") {
			continue
		}
		return word, true
	}
	return "", false
}

func (c shellCommand) inlineFlagPayload(program int, name string) (string, bool) {
	for i := program + 1; i < len(c.words) && strings.HasPrefix(c.words[i], "-"); i++ {
		if inlinePayloadFlag(name, c.words[i]) && i+1 < len(c.words) {
			return shellUnquote(c.words[i+1]), true
		}
	}
	return "", false
}

// inlineFlagIsComputed reports an inline payload built at run time, which is
// not on the page: python3 -c "$(cat x)". Single quotes expand nothing.
func (c shellCommand) inlineFlagIsComputed(program int, name string) bool {
	for i := program + 1; i < len(c.words) && strings.HasPrefix(c.words[i], "-"); i++ {
		if inlinePayloadFlag(name, c.words[i]) && i+1 < len(c.words) {
			word := c.words[i+1]
			return !strings.HasPrefix(word, "'") && strings.ContainsAny(word, "$`")
		}
	}
	return false
}

// operandIsComputed reports an operand whose file is chosen at run time. A value
// joined to a literal name ("$DIR/lib.sh") is followed instead, if the file is there.
func operandIsComputed(word string) bool {
	if shellWordIsComputed(word) || strings.ContainsAny(shellUnquote(word), "*?[") {
		if m := nestedPrefixedPath.FindStringSubmatch(shellUnquote(word)); m != nil && !strings.ContainsAny(m[1], "$`*?[") {
			return false
		}
		return true
	}
	return false
}

func shellUnquote(word string) string {
	if len(word) >= 2 && (word[0] == '"' || word[0] == '\'') && word[len(word)-1] == word[0] {
		return word[1 : len(word)-1]
	}
	return word
}

// isComputed reports whether this command's program is not on the page.
func (c shellCommand) isComputed() bool {
	if c.computed {
		return true
	}
	for _, word := range c.words {
		if shellPathAssignmentIsUnsafe(shellUnquote(word)) {
			return true
		}
	}
	i := c.program()
	if i < 0 {
		return false
	}
	word := c.words[i]
	name := commandName(strings.Trim(word, `"'`))
	switch name {
	case "eval", "xargs", "parallel", "alias", "coproc":
		return true
	case "mapfile", "readarray":
		// -C names code to run every -c lines, a handler like trap's.
		if handler, ok := c.mapfileCallback(i); ok {
			return shellProgramIsComputed(handler)
		}
		return false
	case "hash":
		return hasWord(c.words[i+1:], "-p") // binds a name to a file
	case "enable":
		return hasWord(c.words[i+1:], "-f") // loads a shared object
	case "find", "fd", "fdfind":
		for _, w := range c.words[i+1:] {
			if strings.HasPrefix(w, "-exec") || strings.HasPrefix(w, "-ok") || w == "-x" || w == "-X" {
				return true
			}
		}
		return false
	case "trap":
		// The handler is code that runs later, whatever quotes it wears now.
		return i+1 < len(c.words) && shellProgramIsComputed(shellUnquote(c.words[i+1]))
	}
	if shellOpaqueRunners[name] {
		for _, w := range c.words[i+1:] {
			if shellWordIsComputed(w) {
				return true
			}
		}
	}
	if pipedShells[name] || name == "source" || name == "." {
		if payload, ok := c.inlineFlagPayload(i, name); ok {
			return shellProgramIsComputed(payload) || strings.ContainsAny(payload, "$`")
		}
		operand, ok := c.operand(i)
		switch {
		case !ok:
			return c.pipedIn || c.redirectIn || hasWord(c.words[i+1:], "-s")
		case operand == "-" || operand == shellProcessSubstitution || operand == "/dev/stdin" || hasWord(c.words[i+1:], "-s"):
			return true
		}
		return operandIsComputed(operand)
	}
	if isScriptInterpreter(name) {
		if _, inline := c.inlineFlagPayload(i, name); inline {
			return c.inlineFlagIsComputed(i, name)
		}
		operand, ok := c.operand(i)
		return (!ok && (c.pipedIn || c.redirectIn)) || operand == "-" || operand == shellProcessSubstitution || (ok && operandIsComputed(operand))
	}
	return shellWordIsComputed(word)
}

// mapfileCallback returns the code given to mapfile -C.
func (c shellCommand) mapfileCallback(program int) (string, bool) {
	for i := program + 1; i < len(c.words); i++ {
		word := shellUnquote(c.words[i])
		if word == "-C" && i+1 < len(c.words) {
			return shellUnquote(c.words[i+1]), true
		}
		if strings.HasPrefix(word, "-C") && len(word) > 2 {
			return word[2:], true
		}
	}
	return "", false
}

func hasWord(words []string, want string) bool {
	for _, w := range words {
		if w == want {
			return true
		}
	}
	return false
}

// shellPathAssignmentIsUnsafe reports a PATH value that puts something other
// than absolute directories, the old PATH, or the home directory in front of
// bare names.
func shellPathAssignmentIsUnsafe(word string) bool {
	var value string
	switch {
	case strings.HasPrefix(word, "PATH+="):
		value = word[len("PATH+="):]
	case strings.HasPrefix(word, "PATH="):
		value = word[len("PATH="):]
	default:
		return false
	}
	value = shellUnquote(value)
	for _, entry := range strings.Split(value, ":") {
		switch {
		case entry == "$PATH", entry == "${PATH}":
		case strings.HasPrefix(entry, "$HOME/"), strings.HasPrefix(entry, "${HOME}/"):
		case strings.HasPrefix(entry, "/") && !strings.ContainsAny(entry, "$`*?"):
		default:
			return true
		}
	}
	return false
}

// shellWordIsComputed reports whether a word in command position names a
// program only at run time.
func shellWordIsComputed(word string) bool {
	if len(word) > maxShellWordBytes {
		return true
	}
	if len(word) >= 2 && word[0] == '\'' && word[len(word)-1] == '\'' && !strings.Contains(word[1:len(word)-1], "'") {
		return false // single quotes expand nothing: the name is on the page
	}
	bare := shellUnquote(word)
	if strings.ContainsAny(bare, "$`\\{\"'") {
		return true // an expansion, or a name spelled with quotes or escapes to hide it
	}
	return strings.Contains(bare, "/") && strings.ContainsAny(bare, "*?[")
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

func readNestedScript(path string) (string, bool) {
	f, err := openRegularFile(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxNestedScriptBytes+1))
	if err != nil || len(data) > maxNestedScriptBytes {
		return "", false
	}
	return string(data), true
}

// nestedCandidates returns the local files text would execute, in order of first
// appearance. Shell text is read through its parsed commands, so a path that is only
// mentioned (ls ./bin, echo "see ./bin") does not count. Other languages have no
// such structure, so any path-shaped string there is a candidate.
func nestedCandidates(text, dir string, isShell bool) []string {
	cwd, _ := os.Getwd()
	bases := []string{dir, cwd}
	roots := nestedRoots(dir, cwd)
	makesExecutable := strings.Contains(text, "chmod")
	var out []string
	seen := map[string]bool{}
	add := func(path string) {
		path = filepath.Clean(path)
		if !seen[path] && !nestedIsSystemFile(path) {
			seen[path] = true
			out = append(out, path)
		}
	}
	consider := func(token string, interpreterOperand bool) {
		for _, base := range bases {
			path := token
			if !filepath.IsAbs(path) {
				path = filepath.Join(base, token)
			}
			if !nestedInsideRoots(path, roots) {
				continue
			}
			st, err := os.Stat(path)
			if err != nil || !st.Mode().IsRegular() || st.Size() == 0 {
				continue
			}
			if interpreterOperand || st.Mode()&0o111 != 0 || (makesExecutable && !executableTextFile(path)) {
				add(path)
				return
			}
		}
	}
	considerWord := func(word string, interpreterOperand bool) {
		if len(word) > maxShellWordBytes {
			return
		}
		bare := shellUnquote(word)
		if m := nestedPrefixedPath.FindStringSubmatch(bare); m != nil {
			consider(m[1], interpreterOperand)
			return
		}
		if !strings.ContainsAny(bare, "$`") {
			consider(bare, interpreterOperand)
		}
	}
	considerTokens := func(text string) {
		for _, m := range nestedPrefixedPath.FindAllStringSubmatch(text, -1) {
			consider(m[1], false)
		}
		for _, m := range nestedPathToken.FindAllStringSubmatch(text, -1) {
			consider(m[1], false)
		}
	}
	bareLocalBinary := func(word string) {
		bare := strings.Trim(word, "\"'`;|&()")
		if bare == "" || strings.ContainsAny(bare, "/$=") {
			return
		}
		for _, base := range bases {
			// Any local executable, not only a binary: an unread script reached this way runs as unseen.
			path := filepath.Join(base, bare)
			if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() && st.Size() > 0 && st.Mode()&0o111 != 0 {
				add(path)
				return
			}
		}
	}
	if !isShell {
		for _, line := range strings.Split(text, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			considerTokens(line)
		}
		return out
	}
	commands, _ := parseShellCommands(text)
	for _, c := range commands {
		if len(c.words) >= 2 && c.words[0] == "cd" && !strings.ContainsAny(c.words[1], "$`") {
			// A literal directory change: later relative names mean files there.
			target := shellUnquote(c.words[1])
			if filepath.IsAbs(target) {
				bases = append(bases, target)
			} else {
				bases = append(bases, filepath.Join(dir, target), filepath.Join(cwd, target))
			}
			roots = append(roots, nestedRoots(bases[len(bases)-1])...)
		}
		i := c.program()
		for j, word := range c.words {
			if shellAssignment.MatchString(word) && (i < 0 || j < i) {
				_, value, _ := strings.Cut(word, "=")
				considerTokens(" " + shellUnquote(value))
			}
		}
		if i < 0 {
			continue
		}
		word := shellUnquote(c.words[i])
		name := commandName(word)
		switch {
		case shellHelperTools[name]:
			for _, f := range Analyze(strings.Join(c.words[i:], " ")).ExecutionFiles {
				if nestedInsideRoots(f, roots) {
					if st, err := os.Stat(f); err == nil && st.Mode().IsRegular() {
						add(f)
					}
				}
			}
		case shellOpaqueRunners[name]:
			for _, w := range c.words[i+1:] {
				considerTokens(" " + shellUnquote(w))
				bareLocalBinary(w)
			}
		case nestedCopyCommands[name] || (makesExecutable && (name == "cat" || name == "dd" || name == "tee")):
			for _, w := range c.words[i+1:] {
				considerTokens(" " + shellUnquote(w))
				bareLocalBinary(w)
				// Copied and made runnable: its mode bits now do not say what it becomes.
				if bare := strings.TrimPrefix(shellUnquote(w), "if="); (makesExecutable || name == "install") && !strings.ContainsAny(bare, "$`") && !strings.HasPrefix(bare, "-") {
					for _, base := range bases {
						path := filepath.Join(base, bare)
						if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() && st.Size() > 0 && !executableTextFile(path) {
							add(path)
							break
						}
					}
				}
			}
			continue
		case name == "trap":
			if i+1 < len(c.words) {
				for _, f := range nestedCandidates(shellUnquote(c.words[i+1]), dir, true) {
					add(f)
				}
			}
			continue
		case name == "mapfile" || name == "readarray":
			if handler, ok := c.mapfileCallback(i); ok {
				for _, f := range nestedCandidates(handler, dir, true) {
					add(f)
				}
			}
			continue
		}
		switch {
		case name == "source" || name == "." || isScriptInterpreter(name):
			if payload, inline := c.inlineFlagPayload(i, name); inline {
				if pipedShells[name] {
					for _, f := range nestedCandidates(payload, dir, true) {
						add(f)
					}
				}
				continue
			}
			if operand, ok := c.operand(i); ok && operand != shellProcessSubstitution {
				considerWord(operand, true)
			}
		case strings.Contains(word, "/"):
			considerWord(word, false)
		case !strings.ContainsAny(word, "$`"):
			// A bare name reaches a local binary once the directory is on PATH, which
			// the script may have arranged or may simply inherit.
			bareLocalBinary(word)
		}
	}
	return out
}

// nestedIsSystemFile reports a path that resolves outside anything a repository
// could have planted: a venv's python is a symlink to the system's.
func nestedIsSystemFile(path string) bool {
	real, err := filepath.EvalSymlinks(path)
	if err != nil || real == filepath.Clean(path) {
		return false
	}
	for _, prefix := range []string{"/usr/", "/bin/", "/sbin/", "/lib/", "/lib64/", "/opt/homebrew/", "/System/", "/Library/", "/nix/store/"} {
		if strings.HasPrefix(real, prefix) {
			return true
		}
	}
	return false
}

// nestedRoots returns the directories that count as local, spelled both as
// given and with symlinks resolved. /usr/bin and the like are never local.
func nestedRoots(dirs ...string) []string {
	var roots []string
	for _, d := range dirs {
		if d == "" {
			continue
		}
		roots = append(roots, filepath.Clean(d))
		if real, err := filepath.EvalSymlinks(d); err == nil {
			roots = append(roots, filepath.Clean(real))
		}
	}
	return roots
}

func nestedInsideRoots(path string, roots []string) bool {
	path = filepath.Clean(path)
	for _, root := range roots {
		if root == string(filepath.Separator) {
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

var nestedCopyCommands = map[string]bool{"cp": true, "mv": true, "install": true, "ln": true, "rsync": true}

// inlineCodeTarget names code that has no file for the ledger to license.
func inlineCodeTarget(name string) string {
	return "inline code run by " + name
}
