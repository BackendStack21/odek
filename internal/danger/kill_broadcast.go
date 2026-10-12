package danger

import (
	"regexp"
	"strings"
)

// ── pkill / killall broadcast ───────────────────────────────────────────

// patternProbes are process names and command lines a catch-all pattern
// matches together.
var patternProbes = []string{
	"a", "x y", "zsh", "bash", "sshd", "systemd", "launchd", "Finder", "node",
	"kworker/0:1", "nginx: worker process", "/usr/sbin/sshd -D",
	"/usr/bin/python3 -m http.server",
}

// matchesEveryProcess reports whether a pgrep/pkill pattern selects every
// process: it matches every probe, so catch-alls such as `.`, `\w`, `[a-z]`
// and `[[:alnum:]]` are caught alongside `.*`. A pattern that does not compile
// is treated as matching everything (the caller prompts rather than denies).
func matchesEveryProcess(pattern string) bool {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return true
	}
	for _, probe := range patternProbes {
		if !re.MatchString(probe) {
			return false
		}
	}
	return true
}

// killByNameBroadcasts reports whether pkill/killall signals every process:
// a catch-all pattern (`pkill -9 .`, `pkill -f '.*'`, `pkill ”`,
// `killall -r .`), or children of init (`pkill -P 1`). Ordinary
// `pkill -f myserver` stays a plain signal.
func killByNameBroadcasts(name string, tokens []string) bool {
	switch name {
	case "pkill", "killall":
	default:
		return false
	}
	regexNames := name == "pkill"
	after := false
	for i := 1; i < len(tokens); i++ {
		tok := tokens[i]
		if !after && tok == "--" {
			after = true
			continue
		}
		if !after && strings.HasPrefix(tok, "-") && len(tok) > 1 {
			if name == "killall" && (tok == "-r" || tok == "--regexp") {
				regexNames = true
			}
			if (tok == "-P" || tok == "--parent") && i+1 < len(tokens) && tokens[i+1] == "1" {
				return true
			}
			// Every process of root (or uid 0) is the system's own.
			switch tok {
			case "-u", "-U", "--euid", "--uid", "--user":
				if i+1 < len(tokens) && (tokens[i+1] == "root" || tokens[i+1] == "0") {
					return true
				}
			}
			for _, prefix := range []string{"-u", "-U", "--euid=", "--uid=", "--user="} {
				if v, ok := strings.CutPrefix(tok, prefix); ok && (v == "root" || v == "0") {
					return true
				}
			}
			if tok == "-P1" || tok == "--parent=1" {
				return true
			}
			continue
		}
		// An operand: pkill's regular expression, or killall's process
		// name. Values of options such as -u/-s are consumed below.
		if i > 1 && valueOptionBefore(tokens[i-1]) {
			continue
		}
		if regexNames && matchesEveryProcess(tok) {
			return true
		}
	}
	return false
}

// valueOptionBefore reports whether a pkill/killall option takes the word
// after it as its value.
func valueOptionBefore(opt string) bool {
	switch opt {
	case "-u", "-U", "-g", "-G", "-P", "-s", "-t", "-T", "-F",
		"--euid", "--uid", "--pgroup", "--group", "--parent", "--signal",
		"--session", "--terminal", "--pidfile", "--ns", "--nslist":
		return true
	}
	return false
}
