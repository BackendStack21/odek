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
	switch {
	case recursiveSearchTools[name] || recursiveGrepTools[name]:
		operands = recursiveOperands(name, inner)
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
	}
	for _, op := range operands {
		path := expandShellTokenPath(op)
		if path == "" || strings.Contains(path, dynamicSubstToken) {
			continue
		}
		abs := path
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(cwd, abs)
		}
		abs = filepath.Clean(abs)
		if isSecretTreeRoot(abs) || isSecretTreeRoot(stripPrivate(resolveInDirectory(path, cwd))) {
			return true
		}
		if resolved, err := resolvePathTarget(abs); err == nil && filepath.Clean(resolved) != filepath.Clean(abs) && isSecretTreeRoot(resolved) {
			return true
		}
	}
	return false
}

// findRootsRunningReaders returns find's start points when an -exec/-ok
// action runs a program that reads file content.
func findRootsRunningReaders(inner []string) []string {
	reads := false
	var roots []string
	rootsDone := false
	for i := 1; i < len(inner); i++ {
		a := inner[i]
		if !rootsDone {
			if strings.HasPrefix(a, "-") || a == "(" || a == "!" {
				rootsDone = true
			} else {
				roots = append(roots, a)
			}
		}
		switch a {
		case "-exec", "-execdir", "-ok", "-okdir":
			if i+1 < len(inner) {
				cmd := inner[i+1]
				if findReaderCommands[commandName(cmd)] {
					reads = true
				}
			}
		}
	}
	if !reads {
		return nil
	}
	if len(roots) == 0 {
		roots = []string{"."}
	}
	return roots
}
