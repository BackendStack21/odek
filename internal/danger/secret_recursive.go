package danger

import (
	"os"
	"path/filepath"
	"strings"
)

// A recursive reader pointed at a directory reads every file below it, so
// pointing one at /proc/<pid> prints the process environment and pointing one
// at a home directory sweeps up ~/.ssh and ~/.odek. The literal-path rules
// only see a credential file when it is named; these roots are the ancestors
// that contain them.

// recursiveSearchTools recurse into directory operands by default.
var recursiveSearchTools = fieldSet("rg ag ack ug ugrep")

// recursiveGrepTools recurse only with -r/-R/--recursive/-d recurse.
var recursiveGrepTools = fieldSet("grep egrep fgrep zgrep zegrep zfgrep")

// findReaderCommands are the programs `find -exec` runs that read file
// content (or copy it somewhere the model can see).
var findReaderCommands = fieldSet("cat grep egrep fgrep rg ag head tail less more strings xxd od hexdump base64 cp tar zip rsync nl tac sort uniq cut sed awk bat")

// secretTreeRoots are directories that contain credential files at some depth.
var secretTreeRoots = fieldSet("/ /proc /etc /home /users /root /var/root")

// isSecretTreeRoot reports whether abs is a directory whose subtree holds
// process environments or account credential files: a /proc process
// directory, a system root such as /etc, or an account home.
func isSecretTreeRoot(abs string) bool {
	folded := FoldPathCase(strings.TrimRight(abs, "/"))
	if folded == "" {
		folded = "/"
	}
	if secretTreeRoots[folded] {
		return true
	}
	if rest, ok := strings.CutPrefix(folded, "/proc/"); ok {
		first, _, _ := strings.Cut(rest, "/")
		if first == "self" || first == "thread-self" || isAllDigits(first) {
			return true
		}
		return hasGlobMeta(first)
	}
	for _, h := range accountHomes(abs) {
		if FoldPathCase(strings.TrimRight(h, "/")) == folded {
			return true
		}
	}
	if home, _ := os.UserHomeDir(); home != "" && FoldPathCase(strings.TrimRight(home, "/")) == folded {
		return true
	}
	return false
}

// recursiveOperands returns the directory operands a recursive reader will
// walk, or nil when the invocation is not recursive.
func recursiveOperands(name string, inner []string) []string {
	args := inner[1:]
	var operands []string
	skipPattern := recursiveSearchTools[name] || recursiveGrepTools[name]
	recursive := recursiveSearchTools[name]
	explicitPattern := false
	afterDashDash := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !afterDashDash && a == "--" {
			afterDashDash = true
			continue
		}
		if !afterDashDash && strings.HasPrefix(a, "-") && a != "-" {
			switch {
			case a == "--recursive" || a == "--dereference-recursive":
				recursive = true
			case a == "-d" || a == "--directories":
				if i+1 < len(args) && args[i+1] == "recurse" {
					recursive = true
				}
			case a == "--directories=recurse":
				recursive = true
			case a == "-e" || a == "--regexp" || a == "-f" || a == "--file" || strings.HasPrefix(a, "--regexp=") || strings.HasPrefix(a, "--file="):
				explicitPattern = true
			case isShortFlagToken(a):
				if recursiveGrepTools[name] && strings.ContainsAny(a[1:], "rR") {
					recursive = true
				}
				if strings.ContainsAny(a[1:], "ef") && skipPattern {
					explicitPattern = true
				}
			}
			continue
		}
		if skipPattern && !explicitPattern {
			skipPattern = false
			continue
		}
		operands = append(operands, a)
	}
	if !recursive {
		return nil
	}
	if len(operands) == 0 && (recursiveSearchTools[name] || recursive) {
		operands = []string{"."} // no path operand: the search starts at the working directory
	}
	return operands
}

// recursiveReadsSecretTree reports whether a stage walks a secret-bearing
// directory tree with a tool that reads file content.
func recursiveReadsSecretTree(name string, inner []string, cwd string) bool {
	if len(inner) < 1 {
		return false
	}
	var operands []string
	narrow := false
	switch {
	case recursiveSearchTools[name] || recursiveGrepTools[name]:
		operands = recursiveOperands(name, inner)
		narrow = !searchBroadContent(name, inner[1:])
	case name == "cp" || name == "scp" || name == "zip" || name == "rsync":
		recursive := name == "rsync"
		for _, a := range inner[1:] {
			if a == "--recursive" || a == "--archive" || (isShortFlagToken(a) && strings.ContainsAny(a[1:], "rRa")) {
				recursive = true
			}
		}
		if recursive {
			for _, a := range inner[1:] {
				if !strings.HasPrefix(a, "-") {
					operands = append(operands, a)
				}
			}
		}
	case name == "tar":
		for _, a := range inner[1:] {
			if a == "--create" || (isShortFlagToken(a) && strings.Contains(a[1:], "c")) || (len(inner) > 1 && a == inner[1] && !strings.HasPrefix(a, "-") && strings.Contains(a, "c")) {
				for _, b := range inner[1:] {
					if !strings.HasPrefix(b, "-") {
						operands = append(operands, b)
					}
				}
				break
			}
		}
	case name == "find":
		operands = findRootsRunningReaders(inner)
		narrow = len(operands) > 0 && findExecReadersNarrow(inner)
	}
	for _, op := range operands {
		if secretRootOperand(op, cwd, narrow) {
			return true
		}
	}
	return false
}

// secretRootOperand reports whether a path operand names (or, as a wildcard,
// can name) a secret-bearing tree root. A narrow reader (one that prints
// only matching lines of a specific pattern, or only file names) is let
// through for account-home roots; /, /proc and /etc stay strict.
func secretRootOperand(op, cwd string, narrow bool) bool {
	path := expandShellTokenPath(op)
	if path == "" || strings.Contains(path, dynamicSubstToken) {
		return false
	}
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cwd, abs)
	}
	abs = filepath.Clean(abs)
	if hasGlobMeta(path) {
		if matched, home := globNamesSecretRoot(abs); matched && !(narrow && home) {
			return true
		}
	}
	hit := func(p string) bool {
		return isSecretTreeRoot(p) && !(narrow && isHomeTreeRoot(p))
	}
	if hit(abs) || hit(stripPrivate(resolveInDirectory(path, cwd))) {
		return true
	}
	if resolved, err := resolvePathTarget(abs); err == nil && filepath.Clean(resolved) != filepath.Clean(abs) && hit(resolved) {
		return true
	}
	return false
}

// isHomeTreeRoot reports whether a secret tree root is an account home (or
// the directory of account homes) rather than /, /proc or /etc.
func isHomeTreeRoot(abs string) bool {
	folded := FoldPathCase(strings.TrimRight(abs, "/"))
	if folded == "" || folded == "/proc" || folded == "/etc" || strings.HasPrefix(folded, "/proc/") {
		return false
	}
	return true
}

// globNamesSecretRoot reports whether a wildcard path can expand to a secret
// tree root, and whether that root is an account home.
func globNamesSecretRoot(abs string) (matched, home bool) {
	pat := globSegments(FoldPathCase(abs))
	if len(pat) == 0 {
		return false, false
	}
	same := func(segs []string) bool {
		if len(segs) != len(pat) {
			return false
		}
		for i, seg := range segs {
			if !globSegMatch(pat[i], seg) {
				return false
			}
		}
		return true
	}
	for root := range secretTreeRoots {
		if root != "/" && same(globSegments(root)) {
			return true, root != "/proc" && root != "/etc"
		}
	}
	for _, h := range globHomes(abs) {
		if same(h.segs) {
			return true, true
		}
	}
	return false, false
}

// Content printed by a search tool is narrow when the invocation lists names
// only or prints just the lines matching a specific pattern. Any flag outside
// the known value-free set makes the invocation broad (fail closed).
var (
	searchSafeLetters = "rRniIHhwFEsxoau"
	searchLongSafe    = fieldSet("--recursive --dereference-recursive --line-number --ignore-case --no-messages " +
		"--with-filename --no-filename --word-regexp --fixed-strings --extended-regexp --only-matching --text " +
		"--smart-case --hidden --no-ignore --unrestricted --no-heading --heading --directories=recurse")
	searchLongListing = fieldSet("--files --files-with-matches --files-without-match --count --count-matches --quiet")
)

// searchPatternSecretWords mark a pattern aimed at credentials; a search for
// one of them over a home directory is a secret read.
var searchPatternSecretWords = []string{"key", "secret", "token", "pass", "credential", "auth", "bearer",
	"private", "begin", "akia", "cookie", "session", "ssh", "cert", "gpg", "signature"}

// broadSearchPattern reports whether a pattern (or any alternative of it)
// matches nearly every line, or names credentials.
func broadSearchPattern(p string) bool {
	lower := strings.ToLower(p)
	for _, w := range searchPatternSecretWords {
		if strings.Contains(lower, w) {
			return true
		}
	}
	for _, alt := range strings.Split(lower, "|") {
		n := 0
		for _, r := range alt {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
				n++
			}
		}
		if n < 3 {
			return true
		}
	}
	return false
}

// searchBroadContent reports whether a grep-style search invocation may print
// file content broadly. args are the tool's arguments after its name.
func searchBroadContent(name string, args []string) bool {
	listLetters := "lc"
	if recursiveGrepTools[name] {
		listLetters = "lLcq"
	}
	listing, explicit := false, false
	var patterns, operands []string
	afterDashDash := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if afterDashDash || !strings.HasPrefix(a, "-") || a == "-" {
			operands = append(operands, a)
			continue
		}
		switch {
		case a == "--":
			afterDashDash = true
		case a == "-e" || a == "--regexp":
			if i+1 >= len(args) {
				return true
			}
			patterns = append(patterns, args[i+1])
			explicit = true
			i++
		case strings.HasPrefix(a, "--regexp="):
			patterns = append(patterns, strings.TrimPrefix(a, "--regexp="))
			explicit = true
		case a == "-d":
			if i+1 >= len(args) || args[i+1] != "recurse" {
				return true
			}
			i++
		case searchLongSafe[a] || strings.HasPrefix(a, "--color="):
		case searchLongListing[a]:
			listing = true
		case isShortFlagToken(a):
			for _, r := range a[1:] {
				switch {
				case strings.ContainsRune(searchSafeLetters, r):
				case strings.ContainsRune(listLetters, r):
					listing = true
				default:
					return true
				}
			}
		default:
			return true
		}
	}
	if listing {
		return false
	}
	if !explicit {
		if len(operands) == 0 {
			return true
		}
		patterns = operands[:1]
	}
	for _, p := range patterns {
		if broadSearchPattern(p) {
			return true
		}
	}
	return false
}

// findExecReadersNarrow reports whether every file-reading program a find
// -exec/-ok action runs is a search tool restricted to narrow output.
func findExecReadersNarrow(inner []string) bool {
	seen := false
	for i := 1; i < len(inner); i++ {
		switch inner[i] {
		case "-exec", "-execdir", "-ok", "-okdir":
		default:
			continue
		}
		var cmd []string
		for _, t := range inner[i+1:] {
			if t == ";" || t == `\;` || t == "+" {
				break
			}
			if t != "{}" {
				cmd = append(cmd, t)
			}
		}
		if len(cmd) == 0 || !findReaderCommands[commandName(cmd[0])] {
			continue
		}
		name := commandName(cmd[0])
		if !(recursiveSearchTools[name] || recursiveGrepTools[name]) || searchBroadContent(name, cmd[1:]) {
			return false
		}
		seen = true
	}
	return seen
}

// findStartPoints returns the start points of a find invocation, skipping its
// leading -H/-L/-P/-D/-O options.
func findStartPoints(inner []string) []string {
	var roots []string
	for i := 1; i < len(inner); i++ {
		a := inner[i]
		if len(roots) == 0 && (a == "-H" || a == "-L" || a == "-P" || strings.HasPrefix(a, "-D") || strings.HasPrefix(a, "-O")) {
			continue
		}
		if strings.HasPrefix(a, "-") || a == "(" || a == "!" {
			break
		}
		roots = append(roots, a)
	}
	if len(roots) == 0 {
		roots = []string{"."}
	}
	return roots
}

// pipeFedReadsSecretTree reports whether an argv composer stage feeds the
// file names a `find` over a secret tree printed to a program that reads
// their content: `find ~ -type f | xargs cat` reads like `find ~ -exec cat`.
func pipeFedReadsSecretTree(upstream [][]string, stage []string, cwd string) bool {
	composed, ok := argvComposerInnerCommand(stage)
	if !ok || len(composed) == 0 {
		return false
	}
	reader, _ := unwrapWrappers(composed)
	if len(reader) == 0 {
		return false
	}
	rname := commandName(reader[0])
	if !findReaderCommands[rname] {
		return false
	}
	narrow := (recursiveSearchTools[rname] || recursiveGrepTools[rname]) && !searchBroadContent(rname, reader[1:])
	for _, up := range upstream {
		u, _ := unwrapWrappers(up)
		if len(u) == 0 || commandName(u[0]) != "find" {
			continue
		}
		for _, root := range findStartPoints(u) {
			if secretRootOperand(root, cwd, narrow) {
				return true
			}
		}
	}
	return false
}

// findRootsRunningReaders returns find's start points when an -exec/-ok
// action runs a program that reads file content.
func findRootsRunningReaders(inner []string) []string {
	reads := false
	for i := 1; i < len(inner); i++ {
		switch inner[i] {
		case "-exec", "-execdir", "-ok", "-okdir":
			if i+1 < len(inner) && findReaderCommands[commandName(inner[i+1])] {
				reads = true
			}
		}
	}
	if !reads {
		return nil
	}
	return findStartPoints(inner)
}
