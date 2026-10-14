package danger

import "strings"

// scriptSchedulers read the commands to run from standard input (or a
// here-string), like a shell.
var scriptSchedulers = map[string]bool{"at": true, "batch": true}

// tmuxCommandVerbs are the tmux subcommands whose operands are a command line
// or keys typed into a shell.
var tmuxCommandVerbs = map[string]bool{
	"new": true, "new-session": true, "neww": true, "new-window": true,
	"splitw": true, "split-window": true, "run": true, "run-shell": true,
	"if": true, "if-shell": true, "respawn-pane": true, "respawnp": true,
	"respawn-window": true, "respawnw": true, "send-keys": true, "send": true,
	"display-popup": true, "popup": true, "set-hook": true, "bind-key": true, "bind": true,
}

// denyCommandCarrier scans the commands that programs which only carry other
// commands run: runuser, at/batch scripts given as a here-string, tmux and
// screen session commands, and a version-control alias defined with
// -c alias.NAME=VALUE that the subcommand then expands.
func denyCommandCarrier(name string, inner []string, dc *denyCtx, depth int) bool {
	switch name {
	case "runuser":
		// runuser [-u USER] [--] COMMAND…, or -c STRING for the user's shell.
		for i := 1; i < len(inner); i++ {
			t := inner[i]
			switch {
			case t == "--":
				return denyStage(inner[i+1:], dc, depth+1)
			case (t == "-c" || t == "--command") && i+1 < len(inner):
				if denyScan(inner[i+1], dc, depth+1) {
					return true
				}
				i++
			case t == "-u" || t == "--user":
				if i+2 < len(inner) && denyStage(inner[i+2:], dc, depth+1) {
					return true
				}
				i++
			case strings.HasPrefix(t, "--command="):
				if denyScan(strings.TrimPrefix(t, "--command="), dc, depth+1) {
					return true
				}
			}
		}
	case "at", "batch":
		for i := 1; i+1 < len(inner); i++ {
			if inner[i] == "<<<" && inner[i+1] != "" && denyScan(inner[i+1], dc, depth+1) {
				return true
			}
		}
	case "tmux", "screen":
		var operands []string
		for _, tok := range inner[1:] {
			if tok == "" || (strings.HasPrefix(tok, "-") && tok != "-") {
				continue
			}
			operands = append(operands, tok)
		}
		if name == "tmux" {
			if len(operands) == 0 || !tmuxCommandVerbs[operands[0]] {
				return false
			}
			operands = operands[1:] // the subcommand itself
		}
		for _, op := range operands {
			if denyScan(op, dc, depth+1) {
				return true
			}
		}
		return len(operands) > 0 && denyStage(operands, dc, depth+1)
	case "git":
		// -c alias.NAME=VALUE: the subcommand NAME runs VALUE, a command line
		// of the same tool (a shell string after `!` is scanned elsewhere).
		sub, args := gitSubcommandAndArgs(inner)
		if sub == "" {
			return false
		}
		for _, o := range gitGlobalOptions.parse(inner[1:]).opts {
			if !o.has || !o.is("-c") {
				continue
			}
			key, value, ok := strings.Cut(o.value, "=")
			if !ok || !strings.EqualFold(key, "alias."+sub) || strings.HasPrefix(value, "!") {
				continue
			}
			if denyStage(append(append([]string{"git"}, tokenize(value)...), args...), dc, depth+1) {
				return true
			}
		}
	}
	return false
}
