package danger

import (
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

var projectCodeTools = map[string]bool{
	"cmake": true, "ninja": true, "meson": true, "mvn": true, "mvnw": true, "gradle": true, "gradlew": true,
	"javac": true, "rustc": true, "tsc": true, "eslint": true, "prettier": true, "mypy": true, "flake8": true,
	"rubocop": true, "dotnet": true, "sbt": true, "swift": true, "swiftc": true, "kotlinc": true,
	"golangci-lint": true, "gdb": true, "lldb": true,
}

func adapterRunsCode(name string, tokens []string) bool {
	if hasAny([]string{"awk", "gawk", "mawk", "nawk"}, name) && optionPresent(tokens, "-l", "--load") {
		return true
	}
	if projectCodeTools[name] {
		return len(tokens) == 1 || !networkInfoQuery(tokens)
	}
	if name == "cargo" {
		return hasAny(tokens, "build", "test", "check", "clippy", "run", "bench", "rustc", "doc")
	}
	if name == "go" {
		return hasAny(tokens, "build", "test", "vet", "run", "generate", "tool", "install")
	}
	if name == "rg" {
		return optionPresent(tokens, "--pre")
	}
	if name == "fd" || name == "fdfind" {
		return optionPresent(tokens, "--exec", "--exec-batch", "-x", "-X")
	}
	if name == "gcc" || name == "g++" || name == "cc" || name == "c++" || name == "clang" || name == "clang++" {
		return optionPresent(tokens, "-fplugin", "-specs", "--specs", "-wrapper", "-B", "-Xclang", "-load")
	}
	if name == "protoc" {
		return !networkInfoQuery(tokens)
	}
	if name == "git" {
		sub, args := gitSubcommandAndArgs(tokens)
		switch sub {
		case "commit", "merge", "checkout", "switch", "cherry-pick", "am", "difftool", "mergetool", "add", "status", "restore", "stash", "gc":
			return true
		case "worktree", "submodule":
			return !hasAny(args, "list", "status")
		case "rebase":
			return !hasAny(args, "--abort", "--quit")
		case "diff", "show", "log":
			return hasAny(tokens, "--ext-diff", "--textconv") || (sub == "diff" && (!hasAny(tokens, "--no-ext-diff") || !hasAny(tokens, "--no-textconv")))
		}
	}
	if name == "curl" && optionPresent(tokens, "-K", "--config") {
		return true
	}
	if name == "wget" && optionPresent(tokens, "--config", "-i", "--input-file") {
		return true
	}
	return false
}

func optionPresent(tokens []string, options ...string) bool {
	for _, tok := range tokens[1:] {
		for _, option := range options {
			if tok == option || strings.HasPrefix(tok, option+"=") || (len(option) == 2 && strings.HasPrefix(tok, option) && len(tok) > 2 && tok[1] != '-') {
				return true
			}
		}
	}
	return false
}

func syntaxCheckArgumentsOnly(tokens []string, flags ...string) bool {
	operands := 0
	for _, tok := range tokens[1:] {
		if hasAny(flags, tok) || interpreterInfoFlags[tok] || tok == "--" {
			continue
		}
		if strings.HasPrefix(tok, "-") {
			return false
		}
		operands++
	}
	return operands == 1
}

func hostInspectorMutates(name string, tokens []string) bool {
	switch name {
	case "sysctl":
		for _, tok := range tokens[1:] {
			if strings.Contains(tok, "=") {
				return true
			}
		}
	case "hostname":
		for _, tok := range tokens[1:] {
			if !strings.HasPrefix(tok, "-") {
				return true
			}
		}
	case "ip":
		return hasAny(tokens, "add", "delete", "del", "set", "replace", "change", "flush", "restore", "exec")
	case "ifconfig":
		return len(tokens) > 2 && !networkInfoQuery(tokens)
	}
	return false
}

func formattingMutates(name string, tokens []string) bool {
	switch name {
	case "gofmt", "goimports", "gofumpt", "shfmt":
		return optionPresent(tokens, "-w", "--write")
	case "rustfmt", "black", "isort", "autopep8", "yapf", "stylua":
		return !networkInfoQuery(tokens) && !hasAny(tokens, "--check", "--diff")
	case "ruff":
		return hasAny(tokens, "--fix", "--fix-only") || (hasAny(tokens, "format") && !hasAny(tokens, "--check", "--diff"))
	case "go":
		return hasAny(tokens, "fmt") || (hasAny(tokens, "mod") && hasAny(tokens, "tidy", "edit"))
	case "kill", "pkill", "killall", "bg", "fg", "ulimit":
		return !networkInfoQuery(tokens) && len(tokens) > 1
	case "gcc", "g++", "cc", "c++", "clang", "clang++":
		return !networkInfoQuery(tokens) && !hasAny(tokens, "-E", "-fsyntax-only")
	case "docker", "docker-compose", "podman", "nerdctl":
		_, verb := containerVerb(containerVerbPath(name, tokens))
		return hasAny([]string{"down", "stop", "rm", "kill", "pause", "unpause", "restart", "rename", "update", "start"}, verb)
	}
	return false
}

func commandOnlyReads(name string, tokens []string) bool {
	switch name {
	case "sed":
		return !sedInPlace(tokens) && !sedRunsShellCode(tokens) && !sedHasFileIO(tokens)
	case "tar":
		for _, tok := range tokens[1:] {
			if tok == "--list" || (strings.HasPrefix(tok, "-") && !strings.HasPrefix(tok, "--") && strings.Contains(tok, "t")) {
				return !tarRunsCommand(tokens)
			}
		}
	case "unzip":
		return hasAny(tokens, "-l", "-v", "-Z")
	case "7z", "7za", "7zz":
		return len(tokens) > 1 && tokens[1] == "l"
	}
	return false
}

func sedInPlace(tokens []string) bool {
	for _, tok := range tokens[1:] {
		if tok == "--in-place" || strings.HasPrefix(tok, "--in-place=") {
			return true
		}
		if !isShortFlagToken(tok) {
			continue
		}
		for _, flag := range tok[1:] {
			if flag == 'i' {
				return true
			}
			if flag == 'e' || flag == 'f' {
				break
			}
		}
	}
	return false
}

var sedFileIOPattern = regexp.MustCompile(`(?:^|[;{}\n])\s*(?:[0-9$]+(?:,[0-9$]+)?\s*|/[^/]+/\s*)?([rwRW])\s+([^;}\n]+)`)
var sedSubstitutionPrefix = regexp.MustCompile(`^(?:[0-9$]+(?:,[0-9$]+)?\s*|/[^/]+/\s*)?s`)

// Walk escaped delimiters rather than using the last slash: an s///w
// destination may itself contain slashes, and grouped scripts may continue.
func sedSubstitutionFlags(script string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(script, func(r rune) bool { return r == '{' || r == '}' || r == ';' || r == '\n' }) {
		part = strings.TrimSpace(part)
		prefix := sedSubstitutionPrefix.FindString(part)
		if prefix == "" || len(part) <= len(prefix) {
			continue
		}
		delim := part[len(prefix)]
		separators := 0
		for i := len(prefix) + 1; i < len(part); i++ {
			if part[i] == '\\' {
				i++
				continue
			}
			if part[i] == delim {
				separators++
				if separators == 2 {
					out = append(out, part[i+1:])
					break
				}
			}
		}
	}
	return out
}

func sedHasFileIO(tokens []string) bool {
	for _, tok := range tokens[1:] {
		tok = sedInlineProgram(tok)
		if sedFileIOPattern.MatchString(tok) {
			return true
		}
		for _, flags := range sedSubstitutionFlags(tok) {
			if strings.Contains(flags, "w") {
				return true
			}
		}
	}
	return false
}

func sedInlineProgram(tok string) string {
	if strings.HasPrefix(tok, "--expression=") {
		return strings.TrimPrefix(tok, "--expression=")
	}
	if isShortFlagToken(tok) {
		for i := 1; i < len(tok); i++ {
			if tok[i] == 'e' {
				return tok[i+1:]
			}
			if tok[i] == 'f' {
				break
			}
		}
	}
	return tok
}

func executionFileTargets(name string, tokens []string) []string {
	var options []string
	switch name {
	case "awk", "gawk", "mawk", "nawk":
		options = []string{"-l", "--load"}
	case "rg":
		options = []string{"--pre"}
	case "fd", "fdfind":
		options = []string{"--exec", "--exec-batch", "-x", "-X"}
	case "tar":
		options = []string{"-I", "--use-compress-program", "--to-command", "--checkpoint-action"}
	case "node":
		options = []string{"--require", "-r", "--import", "--loader", "--experimental-loader"}
	case "gcc", "cc", "g++", "c++", "clang", "clang++":
		options = []string{"-fplugin", "-specs", "--specs", "-wrapper", "-load"}
	case "go":
		options = []string{"-toolexec"}
	case "protoc":
		options = []string{"--plugin"}
	case "sqlite3":
		var out []string
		for _, tok := range tokens[1:] {
			parts := strings.Fields(tok)
			if len(parts) > 1 && hasAny([]string{".read", ".load"}, parts[0]) {
				out = append(out, parts[1])
			}
		}
		return out
	}
	var out []string
	for i := 1; i < len(tokens); i++ {
		for _, option := range options {
			var value string
			tok := tokens[i]
			if tok == option {
				if i+1 < len(tokens) {
					i++
					value = tokens[i]
				}
			} else if strings.HasPrefix(tok, option+"=") {
				value = strings.TrimPrefix(tok, option+"=")
			} else if len(option) == 2 && strings.HasPrefix(tok, option) && len(tok) > 2 {
				value = tok[2:]
			}
			if value == "" {
				continue
			}
			if name == "tar" {
				value = strings.TrimPrefix(value, "exec=")
			}
			if name == "protoc" {
				if _, path, ok := strings.Cut(value, "="); ok {
					value = path
				}
			}
			if words := tokenize(value); len(words) > 0 {
				out = append(out, words[0])
			}
			break
		}
	}
	return out
}

// semanticWriteTargets extracts destinations that are not shell redirects.
// Uninspectable destination construction uses the dynamic marker and fails closed.
func semanticWriteTargets(name string, tokens []string) []string {
	var targets []string
	if networkInfoQuery(tokens) {
		return targets
	}
	var flags map[string]bool
	switch name {
	case "curl":
		flags = map[string]bool{"-o": true, "--output": true, "-D": true, "--dump-header": true, "-c": true, "--cookie-jar": true, "--trace": true, "--trace-ascii": true}
	case "wget":
		flags = map[string]bool{"-O": true, "--output-document": true, "-o": true, "--output-file": true, "-a": true, "--append-output": true}
	case "sort":
		flags = map[string]bool{"-o": true, "--output": true}
	case "gcc", "g++", "cc", "c++", "clang", "clang++":
		flags = map[string]bool{"-o": true}
	case "gpg", "gpg2":
		flags = map[string]bool{"-o": true, "--output": true}
	case "find":
		flags = map[string]bool{"-fprint": true, "-fprint0": true, "-fprintf": true}
	case "cp", "mv", "install":
		flags = map[string]bool{"-t": true, "--target-directory": true}
	case "gofmt", "goimports", "gofumpt", "shfmt":
		if formattingMutates(name, tokens) {
			for _, tok := range tokens[1:] {
				if !strings.HasPrefix(tok, "-") {
					targets = append(targets, tok)
				}
			}
		}
	case "sed":
		for _, tok := range tokens[1:] {
			tok = sedInlineProgram(tok)
			for _, match := range sedFileIOPattern.FindAllStringSubmatch(tok, -1) {
				if strings.EqualFold(match[1], "w") {
					targets = append(targets, strings.TrimSpace(match[2]))
				} else {
					targets = append(targets, dynamicSubstToken)
				}
			}
			for _, suffix := range sedSubstitutionFlags(tok) {
				if _, path, ok := strings.Cut(suffix, "w"); ok {
					targets = append(targets, strings.TrimSpace(path))
				}
			}
		}
		if sedInPlace(tokens) {
			for _, tok := range tokens[2:] {
				if !strings.HasPrefix(tok, "-") && !strings.ContainsAny(tok, "; {}") {
					targets = append(targets, tok)
				}
			}
		}
	case "awk", "gawk", "mawk", "nawk":
		for _, tok := range tokens[1:] {
			if strings.ContainsAny(tok, "<>") || strings.Contains(tok, "getline") {
				targets = append(targets, dynamicSubstToken)
			}
		}
	case "sqlite3":
		for _, tok := range tokens[1:] {
			parts := strings.Fields(tok)
			if len(parts) > 1 && hasAny([]string{".output", ".once", ".save", ".backup"}, parts[0]) {
				targets = append(targets, parts[1])
			}
		}
	case "xxd":
		if hasAny(tokens, "-r", "-revert") {
			var operands []string
			for _, tok := range tokens[1:] {
				if !strings.HasPrefix(tok, "-") {
					operands = append(operands, tok)
				}
			}
			if len(operands) > 1 {
				targets = append(targets, operands[len(operands)-1])
			}
		}
	}
	for i := 1; i < len(tokens); i++ {
		tok := tokens[i]
		if isShortFlagToken(tok) && len(tok) > 2 && !flags[tok] {
			for j := 1; j < len(tok); j++ {
				flag := "-" + tok[j:j+1]
				if flags[flag] {
					if j+1 < len(tok) {
						targets = append(targets, tok[j+1:])
					} else if i+1 < len(tokens) {
						i++
						targets = append(targets, tokens[i])
					} else {
						targets = append(targets, dynamicSubstToken)
					}
					break
				}
				// A value-taking flag consumes the rest of its word; its value
				// cannot be reinterpreted as another output option.
				if shortOptionTakesValue(name, tok[j]) {
					break
				}
			}
			continue
		}
		for flag := range flags {
			if tok == flag {
				if i+1 < len(tokens) {
					i++
					targets = append(targets, tokens[i])
				} else {
					targets = append(targets, dynamicSubstToken)
				}
				break
			}
			if strings.HasPrefix(tok, flag+"=") {
				targets = append(targets, strings.TrimPrefix(tok, flag+"="))
				break
			}
			// GNU getopt_long accepts any unambiguous prefix of a long
			// option; an abbreviation is treated as the option it could be.
			if inline, hasInline, ok := longOptionAbbreviation(tok, flag); ok {
				if hasInline {
					targets = append(targets, inline)
				} else if i+1 < len(tokens) {
					i++
					targets = append(targets, tokens[i])
				} else {
					targets = append(targets, dynamicSubstToken)
				}
				break
			}
		}
	}
	if name == "curl" || name == "wget" {
		dir := "."
		for i, tok := range tokens {
			if hasAny([]string{"--output-dir", "-P", "--directory-prefix"}, tok) && i+1 < len(tokens) {
				dir = tokens[i+1]
			}
			if strings.HasPrefix(tok, "--output-dir=") || strings.HasPrefix(tok, "--directory-prefix=") {
				_, dir, _ = strings.Cut(tok, "=")
			}
			if name == "wget" && strings.HasPrefix(tok, "-P") && len(tok) > 2 {
				dir = tok[2:]
			}
			// Abbreviated long spellings of the directory options and -P
			// fused behind other short flags (-qP dir, -qP/dir).
			if !flags[strings.SplitN(tok, "=", 2)[0]] {
				for _, full := range []string{"--output-dir", "--directory-prefix"} {
					if inline, hasInline, ok := longOptionAbbreviation(tok, full); ok {
						if hasInline {
							dir = inline
						} else if i+1 < len(tokens) {
							dir = tokens[i+1]
						}
					}
				}
			}
			if name == "wget" && i > 0 {
				next := ""
				if i+1 < len(tokens) {
					next = tokens[i+1]
				}
				if value, ok := shortClusterValue(name, flags, tok, next, 'P'); ok && value != "" {
					dir = value
				}
			}
		}
		for i, target := range targets {
			if target != "-" && dir != "." && !filepath.IsAbs(target) {
				targets[i] = filepath.Join(dir, target)
			}
		}
		if (name == "wget" && !optionPresent(tokens, "-O", "--output-document")) || (name == "curl" && curlRemoteName(tokens)) {
			found := false
			for _, tok := range tokens[1:] {
				u, err := url.Parse(tok)
				if err == nil && u.Scheme != "" && u.Host != "" {
					base := filepath.Base(u.Path)
					if base == "." || base == "/" {
						base = "index.html"
					}
					targets = append(targets, filepath.Join(dir, base))
					found = true
				}
			}
			if !found {
				targets = append(targets, dynamicSubstToken)
			}
		}
	}
	return targets
}

func shortOptionTakesValue(name string, flag byte) bool {
	switch name {
	case "curl":
		return strings.ContainsRune("XHduxAemTbKFwQrz", rune(flag))
	case "wget":
		return strings.ContainsRune("itTUP", rune(flag))
	case "sort":
		return strings.ContainsRune("ktTS", rune(flag))
	case "gpg", "gpg2":
		return strings.ContainsRune("rpu", rune(flag))
	case "install":
		return strings.ContainsRune("mog", rune(flag))
	}
	return false
}

// longOptionAbbreviation reports whether tok spells the GNU long option full
// (for example "--target-directory") as a strict prefix of it, with an
// optional inline "=value". Exact spellings are matched by the callers.
func longOptionAbbreviation(tok, full string) (inline string, hasInline, ok bool) {
	if !strings.HasPrefix(tok, "--") || !strings.HasPrefix(full, "--") {
		return "", false, false
	}
	spelled, inline, hasInline := strings.Cut(tok, "=")
	if len(spelled) <= 2 || len(spelled) >= len(full) || !strings.HasPrefix(full, spelled) {
		return "", false, false
	}
	return inline, hasInline, true
}

// shortClusterValue looks for the short option want inside one fused cluster
// token (-qP, -sSLO, -qP/dir). A value-taking option consumes the rest of its
// word, so scanning stops there. The value of want, when it takes one, is the
// rest of the word or else the next token.
func shortClusterValue(name string, flags map[string]bool, tok, next string, want byte) (value string, ok bool) {
	if !isShortFlagToken(tok) {
		return "", false
	}
	for j := 1; j < len(tok); j++ {
		if tok[j] == want {
			if j+1 < len(tok) {
				return tok[j+1:], true
			}
			return next, true
		}
		if shortOptionTakesValue(name, tok[j]) || flags["-"+tok[j:j+1]] {
			break
		}
	}
	return "", false
}

// curlRemoteName reports whether curl names its output after the URL (-O,
// --remote-name, --remote-name-all), including -O fused into a short cluster
// and abbreviated long spellings.
func curlRemoteName(tokens []string) bool {
	if optionPresent(tokens, "-O", "--remote-name", "--remote-name-all") {
		return true
	}
	flags := map[string]bool{"-o": true, "-D": true, "-c": true}
	for _, tok := range tokens[1:] {
		if _, ok := shortClusterValue("curl", flags, tok, "", 'O'); ok {
			return true
		}
		for _, full := range []string{"--remote-name", "--remote-name-all"} {
			if _, _, ok := longOptionAbbreviation(tok, full); ok {
				return true
			}
		}
	}
	return false
}
