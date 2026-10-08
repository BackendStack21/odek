package danger

import "strings"

// Indirect program delivery for the unread-script gate. A script can reach an
// interpreter without appearing as its path operand: a reader piped into a
// shell, a command substitution that an eval or interpreter executes, or a
// program file named by an option of a tool that loads one. These helpers
// find the files whose content ends up executing so the read ledger gates
// them exactly like `bash x.sh`.

// pipeFeedReaders are commands that emit the content of their file operands
// (possibly transformed). When a pipeline ends in a stage that executes its
// stdin, the operands of these stages are the program.
var pipeFeedReaders = map[string]bool{
	"cat": true, "tac": true, "head": true, "tail": true, "nl": true, "rev": true,
	"bat": true, "sed": true, "grep": true, "egrep": true, "fgrep": true, "rg": true,
	"awk": true, "gawk": true, "mawk": true, "nawk": true, "cut": true, "sort": true,
	"uniq": true, "tee": true, "base64": true, "zcat": true, "gzcat": true,
	"bzcat": true, "xzcat": true, "gunzip": true, "column": true, "fold": true,
	"expand": true, "unexpand": true, "paste": true, "tr": true, "dos2unix": true,
}

// readerFeedFiles returns the files a reader stage hands to whatever consumes
// its output, gated like execution operands (an existing file, or one an
// earlier stage wrote), plus the subset written earlier in the same command.
func readerFeedFiles(stage []string, cwd string, written map[string]bool) (files, rewritten []string) {
	inner, _ := unwrapWrappers(stage)
	if len(inner) == 0 || !pipeFeedReaders[commandName(inner[0])] {
		return nil, nil
	}
	var operands []string
	for i := 1; i < len(inner); i++ {
		t := inner[i]
		switch {
		case t == "<":
			if i+1 < len(inner) {
				operands = append(operands, inner[i+1])
				i++
			}
		case isRedirectToken(t) || t == "<<" || t == "<<<":
			i++ // redirect target / here-string data
		case t == "" || strings.HasPrefix(t, "-") || strings.Contains(t, "://") || strings.Contains(t, dynamicSubstToken):
		default:
			operands = append(operands, t)
		}
	}
	seen := map[string]bool{}
	for _, operand := range operands {
		f, r := stageLedgerFiles([]string{"sh", operand}, cwd, written)
		for _, p := range f {
			if !seen[p] {
				seen[p] = true
				files = append(files, p)
			}
		}
		rewritten = append(rewritten, r...)
	}
	return files, rewritten
}

// stdinProgramStage reports whether an interpreter stage executes its standard
// input as the program: no script operand and no inline payload (`bash`,
// `bash -s -- args`, `python3 -`, `node`).
func stdinProgramStage(name string, inner []string) bool {
	if !isScriptInterpreter(name) {
		return false
	}
	sawS := false
	for i := 1; i < len(inner); i++ {
		t := inner[i]
		switch {
		case t == "<" || t == "<<" || t == "<<<":
			return false // the program comes from a file or here-document
		case isRedirectToken(t):
			i++
		case inlinePayloadFlag(name, t):
			return false
		case t == "-" || isStdinDevice(t):
		case t == "--":
		case strings.HasPrefix(t, "-"):
			switch t {
			case "-s":
				sawS = true
			case "-m", "-p", "--eval", "--print", "-i", "--interactive":
				return false // module run / expression: stdin is data
			case "-W", "-X", "-r", "--require", "--import":
				i++ // option value
			}
		default:
			if !sawS {
				return false // a script operand names the program
			}
		}
	}
	return true
}

// substFeedsProgram reports whether a command substitution in the stage's
// words is executed as code: eval, source, an interpreter's program operand,
// its -c payload, or the stdin it is redirected from.
func substFeedsProgram(name string, inner []string) bool {
	hasSubst := func(t string) bool { return strings.Contains(t, dynamicSubstToken) }
	switch {
	case name == "eval":
		for _, t := range inner[1:] {
			if hasSubst(t) {
				return true
			}
		}
	case name == "source" || name == ".":
		for i, t := range inner[1:] {
			if isStdinDevice(t) {
				return stdinSubstFeed(inner[i+2:])
			}
			if !strings.HasPrefix(t, "-") {
				return hasSubst(t)
			}
		}
	case isScriptInterpreter(name):
		for i := 1; i < len(inner); i++ {
			t := inner[i]
			switch {
			case t == "<" || t == "<<<":
				return i+1 < len(inner) && hasSubst(inner[i+1])
			case inlinePayloadFlag(name, t):
				return i+1 < len(inner) && hasSubst(inner[i+1])
			case strings.HasPrefix(t, "-"):
			case isStdinDevice(t):
				return stdinSubstFeed(inner[i+1:])
			default:
				return hasSubst(t)
			}
		}
	}
	return false
}

// decompressors always decode their input; compressors only do with a
// decode option.
var (
	decompressors = map[string]bool{
		"gunzip": true, "bunzip2": true, "unxz": true, "unlzma": true, "unzstd": true,
		"uncompress": true, "unlz4": true, "zcat": true, "gzcat": true, "bzcat": true,
		"xzcat": true, "lzcat": true, "zstdcat": true, "lz4cat": true,
	}
	compressors = map[string]bool{
		"gzip": true, "bzip2": true, "xz": true, "lzma": true, "zstd": true,
		"lz4": true, "pigz": true, "pbzip2": true,
	}
)

// decodesContent reports whether a stage turns its input (or file operands)
// into different bytes that a later interpreter would run: base64 and
// friends, decompressors, and decrypting tools.
func decodesContent(stage []string) bool {
	inner, _ := unwrapWrappers(stage)
	if len(inner) == 0 {
		return false
	}
	name := commandName(inner[0])
	hasFlag := func(short byte, longs ...string) bool {
		for _, t := range inner[1:] {
			if t == "--" {
				return false
			}
			if strings.HasPrefix(t, "--") {
				for _, l := range longs {
					if t == l || strings.HasPrefix(t, l+"=") {
						return true
					}
				}
			} else if isShortFlagToken(t) && strings.IndexByte(t, short) > 0 {
				return true
			}
		}
		return false
	}
	switch {
	case decompressors[name]:
		return true
	case compressors[name]:
		return hasFlag('d', "--decompress", "--uncompress", "--decode")
	case name == "base64" || name == "basenc":
		return hasFlag('d', "--decode") || hasAny(inner[1:], "-D")
	case name == "openssl":
		return hasAny(inner[1:], "-d", "-decrypt", "-dec")
	case name == "xxd":
		return hasFlag('r', "--revert")
	case name == "gpg" || name == "gpg2":
		return hasFlag('d', "--decrypt")
	case name == "age":
		return hasFlag('d', "--decrypt")
	case name == "uudecode" || name == "b64decode":
		return true
	}
	return false
}

// stagesDecodeContent reports whether any of the pipeline stages decodes.
func stagesDecodeContent(stages [][]string) bool {
	for _, stage := range stages {
		if decodesContent(stage) {
			return true
		}
	}
	return false
}

// substitutionDecodes reports whether a command-substitution body decodes
// content in any of its stages.
func substitutionDecodes(body string) bool {
	main, _ := normalize(body)
	for _, segment := range splitSegments(tokenize(main)) {
		if stagesDecodeContent(splitPipes(segment)) {
			return true
		}
	}
	return false
}

// stdinSubstFeed reports whether the redirects among rest feed a command
// substitution to standard input (`source /dev/stdin <<< "$(cat x.sh)"`).
func stdinSubstFeed(rest []string) bool {
	for i, t := range rest {
		if (t == "<" || t == "<<<") && i+1 < len(rest) && strings.Contains(rest[i+1], dynamicSubstToken) {
			return true
		}
	}
	return false
}

// substitutionReaderFiles returns the files that the reader stages inside a
// command-substitution body emit.
func substitutionReaderFiles(body, cwd string, written map[string]bool) (files, rewritten []string) {
	main, _ := normalize(body)
	for _, segment := range splitSegments(tokenize(main)) {
		for _, stage := range splitPipes(segment) {
			f, r := readerFeedFiles(stage, cwd, written)
			files = append(files, f...)
			rewritten = append(rewritten, r...)
		}
	}
	return files, rewritten
}

// findExecutionFiles returns the program files named by the commands of a
// find stage's -exec/-execdir/-ok/-okdir actions.
func findExecutionFiles(tokens []string, cwd string, written map[string]bool) []string {
	var out []string
	seen := map[string]bool{}
	for i := 1; i < len(tokens); i++ {
		if !hasAny([]string{"-exec", "-execdir", "-ok", "-okdir"}, tokens[i]) {
			continue
		}
		end := i + 1
		for end < len(tokens) && tokens[end] != ";" && tokens[end] != "+" {
			end++
		}
		for _, p := range stageExecutionFilesWritten(tokens[i+1:end], cwd, written) {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
		i = end
	}
	return out
}

// fdExecutionFiles returns the program files named by the command that fd's
// -x/--exec and -X/--exec-batch options run for each match. The command is
// every word after the option up to a `;` terminator, so the interpreter's
// script operand is examined and not only the first word.
func fdExecutionFiles(tokens []string, cwd string, written map[string]bool) []string {
	options := []string{"--exec", "--exec-batch", "-x", "-X"}
	var out []string
	seen := map[string]bool{}
	for i := 1; i < len(tokens); i++ {
		for _, option := range options {
			first, last, ok := optionValue(tokens, i, option, options)
			if !ok {
				continue
			}
			end := last + 1
			for end < len(tokens) && tokens[end] != ";" && tokens[end] != `\;` {
				end++
			}
			command := append([]string{first}, tokens[last+1:end]...)
			for _, p := range stageExecutionFilesWritten(command, cwd, written) {
				if !seen[p] {
					seen[p] = true
					out = append(out, p)
				}
			}
			i = end
			break
		}
	}
	return out
}

// isStdinDevice reports whether tok names the process's standard input as a
// file, so an interpreter given it as its script reads the program from stdin.
func isStdinDevice(tok string) bool {
	switch tok {
	case "/dev/stdin", "/dev/fd/0", "/proc/self/fd/0", "/proc/thread-self/fd/0":
		return true
	}
	return false
}

// sourceCommandFiles extracts the script files that a debugger or editor
// command string loads: gdb `source FILE`, vim `:source FILE`, lldb
// `command source FILE` / `command script import FILE`, sqlite `.read FILE`.
func sourceCommandFiles(command string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(command, func(r rune) bool { return r == ';' || r == '|' || r == '\n' }) {
		fields := strings.Fields(strings.TrimLeft(part, ": \t"))
		if len(fields) > 0 && fields[0] == "command" {
			fields = fields[1:]
		}
		if len(fields) > 1 && fields[0] == "script" && fields[1] == "import" {
			fields = fields[1:]
			fields[0] = "source"
		}
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "source", "so", "sou", "sour", "sourc", ".read", ".load":
			for _, f := range fields[1:] {
				if !strings.HasPrefix(f, "-") || f == "-" {
					if f != "-" {
						out = append(out, f)
					}
					break
				}
			}
		}
	}
	return out
}
