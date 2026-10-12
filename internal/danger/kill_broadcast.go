package danger

import (
	"regexp"
	"strings"
)

// ── pkill / killall broadcast ───────────────────────────────────────────

// nameProbes are process names a catch-all pattern matches; cmdlineProbes are
// the full command lines pkill -f matches against (mostly absolute paths with
// arguments, plus the odd kernel thread or login shell).
var (
	nameProbes = []string{
		"bash", "zsh", "sshd", "systemd", "launchd", "Finder", "node", "python3",
		"kworker/0:1", "nginx", "login", "Xorg", "a",
	}
	cmdlineProbes = []string{
		"/usr/sbin/sshd -D", "/sbin/init splash", "/usr/lib/systemd/systemd --user",
		"/bin/bash -i", "/usr/bin/python3 app.py", "/usr/bin/node /srv/server.js",
		"/usr/bin/Xorg :0", "/usr/sbin/cron -f", "/usr/bin/dbus-daemon --session",
		"/usr/sbin/nginx -g daemon off;", "login -- root", "-zsh", "kworker/0:1",
	}
)

// broadcastShare is the fraction of realistic probes a pattern must match to
// count as selecting (nearly) every process. A single literal character such
// as `\.` matches only a few; `.`, `\w`, `\S`, `/` and a space match almost
// all.
const broadcastShare = 0.8

// matchesEveryProcess reports whether a pgrep/pkill pattern selects (nearly)
// every process: it matches at least broadcastShare of the realistic probes
// (command lines when full is set, names otherwise), so catch-alls such as
// `.`, `\w`, `\S`, `/`, `[a-z]` and `.*` are caught while a literal fragment
// is not. A pattern that does not compile is treated as matching everything
// (the caller prompts rather than denies).
func matchesEveryProcess(pattern string, full bool) bool {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return true
	}
	probes := nameProbes
	if full {
		probes = cmdlineProbes
	}
	n := 0
	for _, probe := range probes {
		if re.MatchString(probe) {
			n++
		}
	}
	return float64(n) >= broadcastShare*float64(len(probes))
}

// killByNameBroadcasts reports whether pkill/killall (or the pgrep that feeds
// a kill) selects every process: a catch-all pattern (`pkill -9 .`,
// `pkill -f '.*'`, `pkill ”`, `killall -r .`, `killall -m .`), the inverse
// match (`pkill -v x`, `pkill --inverse x`: everything but x), children of
// init (`pkill -P 1`, `-P 2,1`), or every process of root (`-u root`,
// `-G 0`). Ordinary `pkill -f myserver` stays a plain signal.
func killByNameBroadcasts(name string, tokens []string) bool {
	if name == "pgrep" {
		return false // listing pids signals nothing; see pgrepFeedsKill
	}
	return selectsEveryProcess(name, tokens)
}

// selectsEveryProcess is the shared selector analysis for pkill, killall and
// pgrep (whose options mirror pkill's).
func selectsEveryProcess(name string, tokens []string) bool {
	switch name {
	case "pkill", "killall", "pgrep":
	default:
		return false
	}
	regexNames := name != "killall"
	full := false
	for _, tok := range tokens[1:] {
		if tok == "--full" || (len(tok) > 1 && tok[0] == '-' && tok[1] != '-' && strings.Contains(tok, "f")) {
			full = true
		}
	}
	userSelector, operands := false, 0
	after := false
	for i := 1; i < len(tokens); i++ {
		tok := tokens[i]
		if !after && tok == "--" {
			after = true
			continue
		}
		if !after && strings.HasPrefix(tok, "-") && len(tok) > 1 {
			if name == "killall" && (tok == "-r" || tok == "--regexp" || tok == "-m" || tok == "--match") {
				regexNames = true
			}
			if name == "killall" && isShortFlagToken(tok) && strings.ContainsAny(tok[1:], "rm") {
				regexNames = true
			}
			// pkill's inverse match signals every process but the named
			// ones (killall -v only means verbose).
			if name != "killall" && (tok == "--inverse" || (isShortFlagToken(tok) && strings.Contains(tok[1:], "v"))) {
				return true
			}
			// Option values: the next word, or the glued/`=` form.
			value := func(short string, long string) (string, bool) {
				if tok == short || tok == long {
					if i+1 < len(tokens) {
						return tokens[i+1], true
					}
					return "", true
				}
				if v, ok := strings.CutPrefix(tok, long+"="); ok {
					return v, true
				}
				if len(short) == 2 && strings.HasPrefix(tok, short) && len(tok) > 2 && tok[1] != '-' && !strings.HasPrefix(tok, "--") {
					return tok[2:], true
				}
				return "", false
			}
			if v, ok := value("-P", "--parent"); ok && commaListHas(v, "1") {
				return true
			}
			// Every process of root (or uid/gid 0) is the system's own.
			for _, sel := range [][2]string{{"-u", "--euid"}, {"-U", "--uid"}, {"-u", "--user"}} {
				if v, ok := value(sel[0], sel[1]); ok {
					userSelector = true
					if commaListHas(v, "root", "0") {
						return true
					}
				}
			}
			if v, ok := value("-G", "--group"); ok {
				userSelector = true
				if commaListHas(v, "root", "0") {
					return true
				}
			}
			continue
		}
		// An operand: pkill's regular expression, or killall's process
		// name. Values of options such as -u/-s are consumed below.
		if i > 1 && valueOptionBefore(tokens[i-1]) {
			continue
		}
		operands++
		if regexNames && matchesEveryProcess(tok, full) {
			return true
		}
	}
	// A user selector with no name pattern signals every process of that
	// user, as `-u root` does for the system's own.
	return userSelector && operands == 0
}

// commaListHas reports whether a comma-separated option value names one of
// the wanted entries.
func commaListHas(list string, wanted ...string) bool {
	for _, item := range strings.Split(list, ",") {
		for _, w := range wanted {
			if item == w {
				return true
			}
		}
	}
	return false
}

// emptyArgPattern is the pattern substituted for a whole-word empty quoted
// operand of pkill/killall/pgrep: the tokenizer drops `”`, but an empty
// pattern matches every process.
const emptyArgPattern = `'.*'`

// markEmptyKillPatterns rewrites an empty quoted word (`”`, `""`) that
// follows pkill, killall or pgrep in the same simple command into a
// match-everything pattern, so the empty operand survives tokenization.
func markEmptyKillPatterns(cmd string) string {
	if !strings.Contains(cmd, "''") && !strings.Contains(cmd, `""`) {
		return cmd
	}
	var out strings.Builder
	var word strings.Builder // unquoted text of the current word
	inWord := false
	killSeen := false
	endWord := func() {
		switch name := commandName(word.String()); {
		case name == "pkill", name == "killall", name == "pgrep",
			recursiveSearchTools[name], recursiveGrepTools[name]:
			killSeen = true
		}
		word.Reset()
		inWord = false
	}
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case c == '\\' && i+1 < len(cmd):
			out.WriteByte(c)
			out.WriteByte(cmd[i+1])
			word.WriteByte(cmd[i+1])
			inWord = true
			i++
		case c == '\'' || c == '"':
			j := strings.IndexByte(cmd[i+1:], c)
			if j < 0 {
				out.WriteString(cmd[i:])
				return out.String()
			}
			end := i + 1 + j
			if j == 0 && !inWord && killSeen && (end+1 >= len(cmd) || strings.IndexByte(" \t\n;|&()", cmd[end+1]) >= 0) {
				out.WriteString(emptyArgPattern)
			} else {
				out.WriteString(cmd[i : end+1])
				word.WriteString(cmd[i+1 : end])
			}
			inWord = true
			i = end
		case c == ' ' || c == '\t':
			if inWord {
				endWord()
			}
			out.WriteByte(c)
		case strings.IndexByte("\n;|&(", c) >= 0:
			if inWord {
				endWord()
			}
			killSeen = false
			out.WriteByte(c)
		default:
			out.WriteByte(c)
			word.WriteByte(c)
			inWord = true
		}
	}
	return out.String()
}

// pgrepFeedsKill reports whether a command line takes kill's pids from a pgrep
// that selects every process: `kill $(pgrep .)`, `pgrep . | xargs kill`,
// `pgrep . | while read p; do kill $p; done`, `kill $(echo $(pgrep .))`. It is
// deliberately coarse: a catch-all pgrep anywhere (in the words or in a
// substitution body) together with a kill anywhere in the same command line.
// tokens are the words of the main line and subs the substitution bodies.
func pgrepFeedsKill(tokens []string, subs []string) bool {
	killSeen := hasKillWord(tokens)
	broadcast := tokensRunUniversalPgrep(tokens)
	for _, sub := range subs {
		if killSeen && broadcast {
			break
		}
		words := tokenize(sub)
		killSeen = killSeen || hasKillWord(words)
		broadcast = broadcast || tokensRunUniversalPgrep(words)
	}
	return killSeen && broadcast
}

// hasKillWord reports whether a word names kill, ignoring the grouping and
// substitution punctuation glued to it.
func hasKillWord(tokens []string) bool {
	for _, tok := range tokens {
		if commandName(strings.TrimLeft(tok, "$(`{")) == "kill" {
			return true
		}
	}
	return false
}

// tokensRunUniversalPgrep reports whether the words run a pgrep with a
// match-everything selection. The words may carry substitution punctuation
// (`$(pgrep`, `.)`), which is stripped.
func tokensRunUniversalPgrep(tokens []string) bool {
	for i, tok := range tokens {
		if commandName(strings.TrimLeft(tok, "$(`{<")) != "pgrep" {
			continue
		}
		args := []string{"pgrep"}
		for _, w := range tokens[i+1:] {
			if isListSeparator(w) || w == "|" || w == "do" || w == "done" || w == "then" {
				break
			}
			closes := strings.HasSuffix(w, ")") || strings.HasSuffix(w, "`")
			if w = strings.TrimRight(w, ")`"); w != "" {
				args = append(args, w)
			}
			if closes {
				break
			}
		}
		if selectsEveryProcess("pgrep", args) {
			return true
		}
	}
	return false
}

// valueOptionBefore reports whether a pkill/killall option takes the word
// after it as its value.
func valueOptionBefore(opt string) bool {
	switch opt {
	case "-u", "-U", "-g", "-G", "-P", "-s", "-t", "-T", "-F", "-d",
		"--euid", "--uid", "--user", "--pgroup", "--group", "--parent", "--signal",
		"--session", "--terminal", "--pidfile", "--ns", "--nslist":
		return true
	}
	return false
}
