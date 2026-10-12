package danger

import (
	"regexp"
	"strings"
)

// ── jq / ps environment disclosure ──────────────────────────────────────

// jqEnvironmentRe matches jq's environment builtins (`env`, `$ENV`,
// `$__prog_env`-style spellings and yq's `strenv`) in a filter program. A
// field access such as `.env` or a quoted key is data and does not match.
var jqEnvironmentRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9_.\-"'$])(?:\$ENV|\$__env|env|strenv)(?:[^A-Za-z0-9_\-]|$)`)

// yqEnvironmentRe matches yq's env(NAME) / strenv(NAME) operators and a
// $ENV reference.
var yqEnvironmentRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9_.\-"'$])(?:strenv|env)\s*\(|\$ENV\b`)

// jqValueOptions take the next one (or two) words as values, not as the
// program.
var jqValueOptions = map[string]int{
	"--arg": 2, "--argjson": 2, "--slurpfile": 2, "--rawfile": 2, "--argfile": 2,
	"--indent": 1, "-L": 1, "--seq": 0, "--args": 0, "--jsonargs": 0,
}

// yqValueOptions are the yq options that take a value word.
var yqValueOptions = map[string]int{
	"-p": 1, "-o": 1, "--input-format": 1, "--output-format": 1, "-I": 1, "--indent": 1,
	"-s": 1, "--split-exp": 1, "--arg": 2, "--argjson": 2, "--slurpfile": 2,
	"--rawfile": 2, "--argfile": 2, "-L": 1,
}

// yqSubcommands precede the expression in the Go yq (`yq e '.a' f.yaml`).
var yqSubcommands = map[string]bool{"e": true, "eval": true, "ea": true, "eval-all": true}

// jqNonFileOperands returns the indexes of the jq-family words that are the
// filter program or an option value, never a file to open: a filter such as
// `.env` is data, not a credential file operand.
func jqNonFileOperands(name string, tokens []string) map[int]bool {
	switch name {
	case "jq", "gojq", "jaq", "yq":
	default:
		return nil
	}
	valueOpts := jqValueOptions
	if name == "yq" {
		valueOpts = yqValueOptions
	}
	skip := map[int]bool{}
	programSeen := false
	for _, tok := range tokens[1:] {
		if tok == "-f" || strings.HasPrefix(tok, "--from-file") {
			programSeen = true // the program is a file operand
		}
	}
	for i := 1; i < len(tokens); i++ {
		tok := tokens[i]
		if tok == "--" {
			if !programSeen && i+1 < len(tokens) {
				skip[i+1] = true
			}
			break
		}
		if n, ok := valueOpts[tok]; ok {
			for k := 1; k <= n && i+k < len(tokens); k++ {
				skip[i+k] = true
			}
			i += n
			continue
		}
		if strings.HasPrefix(tok, "-") && tok != "-" {
			continue
		}
		if name == "yq" && yqSubcommands[tok] {
			continue
		}
		if !programSeen {
			programSeen = true
			skip[i] = true
		}
	}
	return skip
}

// yqDataFileRe matches an operand that names a data file rather than an
// expression.
var yqDataFileRe = regexp.MustCompile(`(?i)\.(ya?ml|json|toml|xml|csv|tsv|properties|env|ini)$`)

// jqDumpsEnvironment reports whether a jq-family invocation's filter reads
// the process environment, which prints every secret in it exactly like a
// bare `env`.
func jqDumpsEnvironment(name string, tokens []string) bool {
	switch name {
	case "yq":
		// The Go yq spells it env(NAME); the Python yq is a jq wrapper whose
		// filter reads the environment with a bare `env` or `$ENV`.
		skip := jqNonFileOperands(name, tokens)
		for i, tok := range tokens[1:] {
			if yqEnvironmentRe.MatchString(tok) {
				return true
			}
			if skip[i+1] && !yqDataFileRe.MatchString(tok) && jqEnvironmentRe.MatchString(tok) {
				return true
			}
		}
		return false
	case "awk", "gawk", "mawk", "nawk":
		// ENVIRON holds the process environment; gawk's PROCINFO exposes
		// more process state.
		for _, tok := range tokens[1:] {
			if strings.Contains(tok, "ENVIRON") || strings.Contains(tok, "PROCINFO") {
				return true
			}
		}
		return false
	case "jq", "gojq", "jaq":
	default:
		return false
	}
	for i := 1; i < len(tokens); i++ {
		tok := tokens[i]
		if tok == "--" {
			if i+1 < len(tokens) {
				return jqEnvironmentRe.MatchString(tokens[i+1])
			}
			return false
		}
		if skip, ok := jqValueOptions[tok]; ok {
			i += skip
			continue
		}
		if strings.HasPrefix(tok, "-") && tok != "-" {
			if tok == "-f" || strings.HasPrefix(tok, "--from-file") {
				// The program is a file; its content is the read ledger's
				// business, not a word here.
				return false
			}
			continue
		}
		// The first operand is the program; later ones are input files.
		return jqEnvironmentRe.MatchString(tok)
	}
	return false
}

// psValueOptions take the next word as a value.
var psValueOptions = map[string]bool{
	"-C": true, "-p": true, "-u": true, "-U": true, "-g": true, "-G": true,
	"-o": true, "-O": true, "-s": true, "-t": true, "-N": true, "-q": true,
	"--pid": true, "--user": true, "--group": true, "--sort": true,
	"--format": true, "--tty": true, "--sid": true, "--ppid": true,
}

var psBSDWordRe = regexp.MustCompile(`^[A-Za-z]+$`)

// psShowsEnvironment reports whether ps is asked to print the environment of
// processes: the BSD `e` modifier in a bare option word (`ps eww`, `ps auxe`),
// the `-E` option of BSD-derived ps, or an output format naming the
// environment.
func psShowsEnvironment(name string, tokens []string) bool {
	if name != "ps" {
		return false
	}
	for i := 1; i < len(tokens); i++ {
		tok := tokens[i]
		if tok == "--" {
			return false
		}
		if strings.HasPrefix(tok, "--") {
			opt, val, hasVal := strings.Cut(tok, "=")
			if psValueOptions[opt] {
				if !hasVal && i+1 < len(tokens) {
					val = tokens[i+1]
					i++
				}
				if opt == "--format" && strings.Contains(strings.ToLower(val), "env") {
					return true
				}
			}
			continue
		}
		if strings.HasPrefix(tok, "-") && len(tok) > 1 {
			for j := 1; j < len(tok); j++ {
				c := tok[j]
				if c == 'E' {
					return true
				}
				if psValueOptions["-"+string(c)] {
					val := tok[j+1:]
					if val == "" && i+1 < len(tokens) {
						val = tokens[i+1]
						i++
					}
					if (c == 'o' || c == 'O') && strings.Contains(strings.ToLower(val), "env") {
						return true
					}
					break
				}
			}
			continue
		}
		// A bare option word is BSD syntax; digits are process ids.
		if psBSDWordRe.MatchString(tok) && strings.ContainsRune(tok, 'e') {
			return true
		}
	}
	return false
}
