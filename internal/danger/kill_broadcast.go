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
			if name == "killall" && (tok == "-r" || tok == "--regexp") {
				regexNames = true
			}
			if (tok == "-P" || tok == "--parent") && i+1 < len(tokens) && tokens[i+1] == "1" {
				return true
			}
			// Every process of root (or uid 0) is the system's own.
			switch tok {
			case "-u", "-U", "--euid", "--uid", "--user":
				userSelector = true
				if i+1 < len(tokens) && (tokens[i+1] == "root" || tokens[i+1] == "0") {
					return true
				}
			}
			for _, prefix := range []string{"-u", "-U", "--euid=", "--uid=", "--user="} {
				if v, ok := strings.CutPrefix(tok, prefix); ok && v != "" {
					userSelector = true
					if v == "root" || v == "0" {
						return true
					}
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
		operands++
		if regexNames && matchesEveryProcess(tok, full) {
			return true
		}
	}
	// A user selector with no name pattern signals every process of that
	// user, as `-u root` does for the system's own.
	return userSelector && operands == 0
}

// valueOptionBefore reports whether a pkill/killall option takes the word
// after it as its value.
func valueOptionBefore(opt string) bool {
	switch opt {
	case "-u", "-U", "-g", "-G", "-P", "-s", "-t", "-T", "-F",
		"--euid", "--uid", "--user", "--pgroup", "--group", "--parent", "--signal",
		"--session", "--terminal", "--pidfile", "--ns", "--nslist":
		return true
	}
	return false
}
