package danger

import "strings"

// The gh adapter classifies the GitHub CLI by command and verb instead of
// treating every subcommand as network egress:
//
//	read of the GitHub API / local config view       network_egress (allowed)
//	remote mutation, credential disclosure           system_write   (prompt)
//	irreversible remote deletion                     destructive    (deny)
//	verbs that run a local program or shell alias    code_execution (prompt)
//	downloads and clones                             destination routed through the write-target rules
//	help / version / completion                      safe
//	any command or verb not listed here              unknown        (deny)
//
// gh uses cobra and pflag: long options are not abbreviated, a short option
// may be fused with its value (-Rowner/repo, -XPOST, -X=POST) or clustered
// (-at), and `--` ends option parsing.

// ghVerbClasses maps "<command> <verb>" (or "<command> <verb> <sub>") to the
// class of that invocation. Verbs whose class depends on their options are
// handled in classifyGH before this table is consulted.
var ghVerbClasses = map[string]RiskClass{
	// pull requests
	"pr list": NetworkEgress, "pr view": NetworkEgress, "pr status": NetworkEgress,
	"pr checks": NetworkEgress, "pr diff": NetworkEgress,
	"pr create": SystemWrite, "pr merge": SystemWrite, "pr close": SystemWrite,
	"pr reopen": SystemWrite, "pr edit": SystemWrite, "pr review": SystemWrite,
	"pr comment": SystemWrite, "pr ready": SystemWrite, "pr checkout": SystemWrite,
	"pr lock": SystemWrite, "pr unlock": SystemWrite, "pr update-branch": SystemWrite,
	"pr revert": SystemWrite,

	// issues
	"issue list": NetworkEgress, "issue view": NetworkEgress, "issue status": NetworkEgress,
	"issue create": SystemWrite, "issue close": SystemWrite, "issue reopen": SystemWrite,
	"issue edit": SystemWrite, "issue comment": SystemWrite, "issue transfer": SystemWrite,
	"issue pin": SystemWrite, "issue unpin": SystemWrite, "issue lock": SystemWrite,
	"issue unlock": SystemWrite, "issue develop": SystemWrite,
	"issue delete": Destructive,

	// repositories
	"repo view": NetworkEgress, "repo list": NetworkEgress, "repo clone": NetworkEgress,
	"repo gitignore list": NetworkEgress, "repo gitignore view": NetworkEgress,
	"repo license list": NetworkEgress, "repo license view": NetworkEgress,
	"repo deploy-key list": NetworkEgress, "repo autolink list": NetworkEgress,
	"repo autolink view": NetworkEgress,
	"repo create":        SystemWrite, "repo fork": SystemWrite, "repo edit": SystemWrite,
	"repo rename": SystemWrite, "repo sync": SystemWrite, "repo archive": SystemWrite,
	"repo unarchive": SystemWrite, "repo set-default": SystemWrite,
	"repo deploy-key add": SystemWrite, "repo autolink create": SystemWrite,
	"repo delete": Destructive, "repo deploy-key delete": Destructive,
	"repo autolink delete": Destructive,

	// workflow runs and workflows
	"run list": NetworkEgress, "run view": NetworkEgress, "run watch": NetworkEgress,
	"run download": LocalWrite,
	"run cancel":   SystemWrite, "run rerun": SystemWrite, "run delete": Destructive,
	"workflow list": NetworkEgress, "workflow view": NetworkEgress,
	"workflow run": SystemWrite, "workflow enable": SystemWrite, "workflow disable": SystemWrite,

	// releases
	"release list": NetworkEgress, "release view": NetworkEgress,
	"release verify": NetworkEgress, "release verify-asset": NetworkEgress,
	"release download": LocalWrite,
	"release create":   SystemWrite, "release edit": SystemWrite, "release upload": SystemWrite,
	"release delete": Destructive, "release delete-asset": Destructive,

	// gists
	"gist list": NetworkEgress, "gist view": NetworkEgress, "gist clone": NetworkEgress,
	"gist create": SystemWrite, "gist edit": SystemWrite, "gist rename": SystemWrite,
	"gist delete": Destructive,

	// labels, caches
	"label list": NetworkEgress, "label create": SystemWrite, "label edit": SystemWrite,
	"label clone": SystemWrite, "label delete": Destructive,
	"cache list": NetworkEgress, "cache delete": Destructive,

	// projects
	"project list": NetworkEgress, "project view": NetworkEgress,
	"project item-list": NetworkEgress, "project field-list": NetworkEgress,
	"project create": SystemWrite, "project edit": SystemWrite, "project copy": SystemWrite,
	"project item-add": SystemWrite, "project item-create": SystemWrite,
	"project item-edit": SystemWrite, "project item-archive": SystemWrite,
	"project field-create": SystemWrite, "project link": SystemWrite,
	"project unlink": SystemWrite, "project close": SystemWrite,
	"project mark-template": SystemWrite,
	"project delete":        Destructive, "project item-delete": Destructive,
	"project field-delete": Destructive,

	// rulesets, variables, secrets, keys
	"ruleset list": NetworkEgress, "ruleset view": NetworkEgress, "ruleset check": NetworkEgress,
	"variable list": NetworkEgress, "variable get": NetworkEgress,
	"variable set": SystemWrite, "variable delete": Destructive,
	// secret list shows names only; secret set uploads a secret value.
	"secret list": NetworkEgress, "secret set": SystemWrite, "secret delete": Destructive,
	"ssh-key list": NetworkEgress, "ssh-key add": SystemWrite, "ssh-key delete": Destructive,
	"gpg-key list": NetworkEgress, "gpg-key add": SystemWrite, "gpg-key delete": Destructive,

	// organisations, attestations
	"org list":           NetworkEgress,
	"attestation verify": NetworkEgress, "attestation download": SystemWrite,

	// codespaces: ssh/code/cp/logs/jupyter/ports forward spawn local programs.
	"codespace list": NetworkEgress, "codespace view": NetworkEgress, "codespace ports": NetworkEgress,
	"codespace create": SystemWrite, "codespace stop": SystemWrite,
	"codespace rebuild": SystemWrite, "codespace edit": SystemWrite,
	"codespace ports visibility": SystemWrite,
	"codespace ssh":              CodeExecution, "codespace code": CodeExecution,
	"codespace cp": CodeExecution, "codespace logs": CodeExecution,
	"codespace jupyter": CodeExecution, "codespace ports forward": CodeExecution,
	"codespace delete": Destructive,

	// extensions: install/upgrade/exec/create fetch, build or run extension code.
	"extension list": NetworkEgress, "extension browse": NetworkEgress,
	"extension search": NetworkEgress, "extension remove": SystemWrite,
	"extension install": CodeExecution, "extension upgrade": CodeExecution,
	"extension exec": CodeExecution, "extension create": CodeExecution,

	// local configuration and aliases
	"config get": NetworkEgress, "config list": NetworkEgress,
	"config set": SystemWrite, "config clear-cache": SystemWrite,
	"alias list": NetworkEgress, "alias delete": SystemWrite, "alias set": SystemWrite,
	// alias import reads definitions (possibly shell aliases) the command
	// line does not show.
	"alias import": CodeExecution,

	// authentication: every verb but status can disclose or change credentials.
	"auth status": NetworkEgress,
	"auth token":  SystemWrite, "auth login": SystemWrite, "auth logout": SystemWrite,
	"auth refresh": SystemWrite, "auth setup-git": SystemWrite, "auth switch": SystemWrite,
}

// ghCommandAliases are the alternate spellings cobra accepts for a command.
var ghCommandAliases = map[string]string{
	"cs": "codespace", "ext": "extension", "extensions": "extension",
	"rs": "ruleset", "rulesets": "ruleset",
}

// ghVerbAliases are verb spellings that mean the same as another verb. They
// are consulted only when the spelling is not itself a table entry.
var ghVerbAliases = map[string]string{
	"ls": "list", "new": "create", "rm": "delete", "remove": "delete", "del": "delete",
}

// ghNestedGroups are verbs that carry a further sub-verb, per command.
var ghNestedGroups = map[string]map[string]bool{
	"repo":      {"deploy-key": true, "autolink": true, "gitignore": true, "license": true},
	"codespace": {"ports": true},
}

// ghVerblessCommands run without a verb. Their class is decided from options.
var ghVerblessCommands = map[string]bool{
	"api": true, "browse": true, "status": true, "search": true, "copilot": true,
}

// ghKnownCommands are the command groups with at least one listed verb.
var ghKnownCommands = func() map[string]bool {
	m := make(map[string]bool)
	for key := range ghVerbClasses {
		cmd, _, _ := strings.Cut(key, " ")
		m[cmd] = true
	}
	return m
}()

// ghParsed is the outcome of locating a gh command and verb.
type ghParsed struct {
	meta    bool     // help, version or completion: nothing is contacted or run
	invalid bool     // the command line cannot be resolved with certainty
	cmd     string   // normalised command ("pr", "api", ...)
	verb    string   // normalised verb, with a sub-verb appended for nested groups
	args    []string // tokens after the command (verbless) or after the verb
}

// ghTakesValue reports whether tok is a global option that consumes the next
// word as its value (-R, --repo, --hostname). fused is true when the value is
// carried in the same word (--repo=x, -Rx).
func ghTakesValue(tok string) (takesNext, fused bool) {
	switch tok {
	case "-R", "--repo", "--hostname":
		return true, false
	}
	if strings.HasPrefix(tok, "--repo=") || strings.HasPrefix(tok, "--hostname=") {
		return false, true
	}
	if strings.HasPrefix(tok, "-R") && !strings.HasPrefix(tok, "--") && len(tok) > 2 {
		return false, true
	}
	return false, false
}

// ghLocate resolves the command and verb of a gh invocation. Only the
// repo/host options may precede the verb: cobra treats any other unknown
// option before the verb as taking a value and skips the following word, so
// a verb selected past such an option may not be the one gh runs.
func ghLocate(tokens []string) ghParsed {
	i := 1
	// skipOptions advances past repo/host options. It reports help (a help
	// flag), invalid (anything else option-like), or done.
	skipOptions := func() (help, invalid bool) {
		for i < len(tokens) {
			tok := tokens[i]
			if tok == "--" {
				return false, true
			}
			if !strings.HasPrefix(tok, "-") || tok == "-" {
				return false, false
			}
			if tok == "--help" || tok == "--version" || (tok == "-h" && i == len(tokens)-1) {
				return true, false
			}
			takesNext, fused := ghTakesValue(tok)
			switch {
			case takesNext:
				if i+1 >= len(tokens) {
					return false, true
				}
				i += 2
			case fused:
				i++
			default:
				return false, true
			}
		}
		return false, false
	}

	help, invalid := skipOptions()
	if invalid {
		return ghParsed{invalid: true}
	}
	if help || i >= len(tokens) {
		return ghParsed{meta: true}
	}
	cmd := tokens[i]
	i++
	if full, ok := ghCommandAliases[cmd]; ok {
		cmd = full
	}
	switch cmd {
	case "help", "completion", "version":
		return ghParsed{meta: true}
	}
	if ghVerblessCommands[cmd] {
		return ghParsed{cmd: cmd, args: tokens[i:]}
	}

	if !ghKnownCommands[cmd] {
		return ghParsed{invalid: true}
	}

	help, invalid = skipOptions()
	if invalid {
		return ghParsed{invalid: true}
	}
	if help || i >= len(tokens) {
		// A command group without a verb only prints its usage.
		return ghParsed{meta: true}
	}
	verb := tokens[i]
	i++
	out := ghParsed{cmd: cmd, verb: verb}
	if ghNestedGroups[cmd][verb] {
		help, invalid = skipOptions()
		if invalid {
			return ghParsed{invalid: true}
		}
		if i < len(tokens) && !help {
			out.verb = verb + " " + tokens[i]
			i++
		} else if help {
			return ghParsed{meta: true}
		} else if cmd == "repo" {
			// repo deploy-key / autolink / ... alone print usage.
			return ghParsed{meta: true}
		}
	}
	out.args = tokens[i:]
	return out
}

// ghLookup returns the class of "<cmd> <verb>" honouring verb aliases.
func ghLookup(cmd, verb string) (RiskClass, string, bool) {
	if cls, ok := ghVerbClasses[cmd+" "+verb]; ok {
		return cls, verb, true
	}
	// Only the first word of a verb is aliased; a sub-verb is kept as given.
	first, rest, nested := strings.Cut(verb, " ")
	if full, ok := ghVerbAliases[first]; ok {
		if nested {
			full += " " + rest
		}
		if cls, ok := ghVerbClasses[cmd+" "+full]; ok {
			return cls, full, true
		}
	}
	return Unknown, verb, false
}

// ghOptions is the option grammar of a gh verb. gh is a pflag program: short
// letters cluster, `-X=value` is accepted, long options are exact (no
// abbreviations), and only the listed letters and names take a value.
func ghOptions(short string, long ...string) optSpec {
	return optSpec{short: short, long: valueOpts(strings.Join(long, " ")), shortEq: true}
}

// classifyGH classifies one gh invocation. tokens[0] is the program.
func classifyGH(tokens []string) RiskClass {
	p := ghLocate(tokens)
	if p.invalid {
		return Unknown
	}
	if p.meta {
		return Safe
	}
	switch p.cmd {
	case "api":
		return ghAPIClass(p.args)
	case "copilot":
		// Downloads and runs the Copilot CLI binary.
		return CodeExecution
	case "browse", "status", "search":
		return NetworkEgress
	}

	cls, verb, ok := ghLookup(p.cmd, p.verb)
	if !ok {
		return Unknown
	}
	key := p.cmd + " " + verb
	switch key {
	case "auth status":
		if ghOptions("h", "hostname").parse(p.args).has("--show-token", "-t") {
			return SystemWrite
		}
	case "config get", "config list":
		// A token is not a config key, but the hosts file that holds one is
		// read through the same accessor; fail closed on the name.
		for _, o := range ghOptions("h", "host").parse(p.args).args() {
			if strings.Contains(strings.ToLower(o), "token") {
				return SystemWrite
			}
		}
	case "config set":
		operands := ghOptions("h", "host").parse(p.args).args()
		if len(operands) > 0 {
			switch strings.ToLower(operands[0]) {
			case "editor", "pager", "browser":
				// These values are run as programs.
				return CodeExecution
			}
		}
	case "alias set":
		return ghAliasSetClass(p.args)
	case "run download", "release download":
		return ghDownloadClass(p.cmd, p.args)
	case "repo clone", "gist clone":
		return ghCloneClass(p.args)
	}
	return cls
}

// ghAliasSetClass: an alias whose expansion starts with `!` (or one created
// with --shell) runs through the shell whenever it is invoked.
func ghAliasSetClass(args []string) RiskClass {
	r := ghOptions("").parse(args)
	if r.has("--shell", "-s") {
		return CodeExecution
	}
	for _, o := range r.args() {
		if strings.HasPrefix(o, "!") {
			return CodeExecution
		}
	}
	return SystemWrite
}

// ghTargetClass routes a destination through the write-target rules.
func ghTargetClass(path string) RiskClass {
	p := expandShellTokenPath(path)
	if p == "" || strings.ContainsAny(p, "$*?[]`") || strings.Contains(p, dynamicSubstToken) {
		return Unknown
	}
	return worstOf(ClassifyPathWrite(p), classifyResourceToken(p))
}

// ghDownloadTargets lists where run/release download put their files:
// -D/--dir, and for release download -O/--output (`-` is stdout).
func ghDownloadTargets(cmd string, args []string) []string {
	r := ghOptions("DOpnAR", "dir", "output", "pattern", "name", "archive", "repo").parse(args)
	targets := r.values("-D", "--dir")
	output := false
	if cmd == "release" {
		for _, o := range r.values("-O", "--output") {
			output = true
			if o != "-" {
				targets = append(targets, o)
			}
		}
	}
	// Without a destination the files land in the working directory, which a
	// preceding `cd` may have moved somewhere sensitive.
	if len(targets) == 0 && !output {
		targets = append(targets, ".")
	}
	return targets
}

func ghDownloadClass(cmd string, args []string) RiskClass {
	cls := LocalWrite
	for _, t := range ghDownloadTargets(cmd, args) {
		cls = worstOf(cls, ghTargetClass(t))
	}
	return cls
}

// ghCloneOperands returns the clone destination (second operand) and the
// options gh hands to git clone (everything after `--`).
func ghCloneOperands(args []string) (dest string, gitArgs []string) {
	r := ghOptions("u", "upstream-remote-name").parse(args)
	if len(r.operands) > 1 {
		dest = r.operands[1]
	}
	return dest, r.rest
}

func ghCloneClass(args []string) RiskClass {
	dest, gitArgs := ghCloneOperands(args)
	cls := NetworkEgress
	if len(gitArgs) > 0 && isGitCodeExecution(append([]string{"git", "clone"}, gitArgs...)) {
		cls = worstOf(cls, CodeExecution)
	}
	if dest != "" {
		cls = worstOf(cls, ghTargetClass(dest))
	}
	return cls
}

// ghWriteTargets lists local destinations a gh invocation writes, for the
// analysis that resolves them against the working directory.
func ghWriteTargets(tokens []string) []string {
	p := ghLocate(tokens)
	if p.invalid || p.meta {
		return nil
	}
	switch p.cmd + " " + p.verb {
	case "run download", "release download":
		return ghDownloadTargets(p.cmd, p.args)
	case "repo clone", "gist clone":
		if dest, _ := ghCloneOperands(p.args); dest != "" {
			return []string{dest}
		}
		return []string{"."}
	}
	return nil
}

// ghWritesLocalFiles reports whether the invocation writes files into the
// working directory or a named directory.
func ghWritesLocalFiles(tokens []string) bool {
	p := ghLocate(tokens)
	if p.invalid || p.meta {
		return false
	}
	switch p.cmd + " " + p.verb {
	case "run download", "release download":
		return true
	}
	return false
}

// ghContactsNetwork reports whether the invocation talks to GitHub. Help,
// version and completion do not; any other form, including an unrecognised
// one, counts as network-capable.
func ghContactsNetwork(tokens []string) bool {
	p := ghLocate(tokens)
	return !p.meta
}

// ghAPIClass classifies `gh api`: a request that sends a body or uses a
// method other than GET/HEAD changes remote state, DELETE removes it.
func ghAPIClass(args []string) RiskClass {
	r := ghOptions("XfFHqtp",
		"method", "field", "raw-field", "header", "input", "jq", "template", "preview", "hostname", "cache").parse(args)
	operands := r.args()
	if len(operands) == 0 {
		if r.has("--help") {
			return Safe
		}
		return Unknown
	}
	endpoint := strings.ToLower(strings.TrimRight(operands[0], "/"))

	methods := r.values("-X", "--method")
	deleting, mutating := false, false
	for _, m := range methods {
		switch strings.ToUpper(m) {
		case "GET", "HEAD":
		case "DELETE":
			deleting = true
		default:
			mutating = true
		}
	}
	if deleting {
		return Destructive
	}
	if mutating {
		return SystemWrite
	}

	input := r.has("--input")
	hasFields := r.has("-f", "-F", "--field", "--raw-field")

	if endpoint == "graphql" || strings.HasSuffix(endpoint, "/graphql") {
		if input {
			return SystemWrite
		}
		for _, v := range r.values("-f", "-F", "--field", "--raw-field") {
			key, val, _ := strings.Cut(v, "=")
			if key != "query" {
				continue
			}
			// A query read from a file or stdin, or built at run time from
			// a substitution or variable, cannot be inspected.
			if strings.HasPrefix(val, "@") || strings.Contains(strings.ToLower(val), "mutation") ||
				strings.Contains(val, dynamicSubstToken) || strings.Contains(val, "$") {
				return SystemWrite
			}
		}
		return NetworkEgress
	}

	if input {
		return SystemWrite
	}
	if hasFields {
		// With an explicit GET/HEAD the fields travel as query parameters.
		if len(methods) > 0 {
			return NetworkEgress
		}
		return SystemWrite
	}
	return NetworkEgress
}
