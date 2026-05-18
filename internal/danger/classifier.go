// Package danger classifies shell commands by risk level and provides
// a configurable approval system for dangerous operations.
//
// Classification is token-based (not regex) — it respects quotes, pipes,
// redirects, compound commands (&&, ||, ;), and multi-line input. Each
// command is classified into one of 8 risk classes, and the user can
// configure which actions (allow/prompt/deny) apply to each class.
package danger

import (
	"strings"
)

// ── Types ──────────────────────────────────────────────────────────────

// RiskClass represents the risk level of a shell command.
type RiskClass string

const (
	Safe           RiskClass = "safe"
	LocalWrite     RiskClass = "local_write"
	SystemWrite    RiskClass = "system_write"
	Destructive    RiskClass = "destructive"
	NetworkEgress  RiskClass = "network_egress"
	CodeExecution  RiskClass = "code_execution"
	Install        RiskClass = "install"
	Blocked        RiskClass = "blocked"
)

// Action represents what to do when a command of a given risk class is detected.
type Action string

const (
	Allow  Action = "allow"
	Prompt Action = "prompt"
	Deny   Action = "deny"
)

// ── Config ─────────────────────────────────────────────────────────────

// DangerousConfig defines how dangerous operations are handled.
// Configurable via the standard 4-layer kode config chain.
//
// Default behavior per class (no sandbox):
//
//	safe → allow, local_write → allow, system_write → prompt,
//	destructive → deny, network_egress → prompt,
//	code_execution → prompt, install → prompt, blocked → deny
type DangerousConfig struct {
	// Classes maps risk classes to their configured action.
	// Only overrides for non-default values need to be set.
	Classes map[RiskClass]Action `json:"classes,omitempty"`

	// Allowlist is a list of command strings that are always allowed,
	// regardless of their risk classification. Exact match only.
	// Takes priority over Denylist.
	Allowlist []string `json:"allowlist,omitempty"`

	// Denylist is a list of command strings that are always denied,
	// regardless of their risk classification. Exact match only.
	Denylist []string `json:"denylist,omitempty"`

	// DefaultAction is used when no /dev/tty is available (non-interactive mode).
	// Values: "allow" (default), "deny", "prompt"
	// "prompt" in non-interactive mode falls back to "allow".
	DefaultAction *string `json:"action,omitempty"`

	// NonInteractive specifies what to do when running without a TTY.
	// "allow" (default) — run everything, "deny" — block all prompted ops.
	NonInteractive *string `json:"non_interactive,omitempty"`
}

// defaultActions defines the base per-class behavior.
var defaultActions = map[RiskClass]Action{
	Safe:          Allow,
	LocalWrite:    Allow,
	SystemWrite:   Prompt,
	Destructive:   Deny,
	NetworkEgress: Prompt,
	CodeExecution: Prompt,
	Install:       Prompt,
	Blocked:       Deny,
}

// ActionFor returns the configured action for the given risk class.
// Falls back to defaults for unknown classes and unconfigured classes.
func (c *DangerousConfig) ActionFor(cls RiskClass) Action {
	// If the user explicitly configured an action for this class, use it.
	// Falls back to built-in defaults for unconfigured classes.
	if c.Classes != nil {
		if a, ok := c.Classes[cls]; ok {
			return a
		}
	}
	// Fallback to built-in defaults
	if a, ok := defaultActions[cls]; ok {
		return a
	}
	// Unknown class: use default action from config, or prompt
	if c.DefaultAction != nil {
		return parseAction(*c.DefaultAction)
	}
	return Prompt
}

// ActionForCommand returns the action for a specific command string.
// Allowlist and denylist are checked first (exact match), then falls
// back to the risk-class-based action.
func (c *DangerousConfig) ActionForCommand(cmd string) Action {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return Allow
	}
	// Allowlist has highest priority
	for _, pattern := range c.Allowlist {
		if cmd == pattern {
			return Allow
		}
	}
	// Denylist is checked before classification
	for _, pattern := range c.Denylist {
		if strings.HasPrefix(cmd, pattern) {
			return Deny
		}
	}
	// Classify and use class-based action
	cls := Classify(cmd)
	return c.ActionFor(cls)
}

// NonInteractiveAction returns the action to use when no TTY is available.
func (c *DangerousConfig) NonInteractiveAction() Action {
	if c.NonInteractive != nil {
		return parseAction(*c.NonInteractive)
	}
	return Allow
}

func parseAction(s string) Action {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "allow":
		return Allow
	case "deny":
		return Deny
	default:
		return Prompt
	}
}

// ── Tokenizer ──────────────────────────────────────────────────────────

// tokenize splits a shell command into tokens, respecting:
//   - Single and double quotes (content preserved as-is)
//   - Pipes (|), redirects (>, >>), compound (&&, ||, ;)
//   - Newlines mapped to semicolons (command separators)
//
// Output: flattened token slice including operators as tokens.
func tokenize(input string) []string {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil
	}

	// Normalize newlines to semicolons
	input = strings.NewReplacer("\r\n", ";", "\n", ";", "\r", ";").Replace(input)

	var tokens []string
	var current strings.Builder
	inSingle := false
	inDouble := false
	escapeNext := false

	flush := func() {
		if current.Len() > 0 {
			tokens = append(tokens, current.String())
			current.Reset()
		}
	}

	for i := 0; i < len(input); i++ {
		ch := input[i]

		if escapeNext {
			current.WriteByte(ch)
			escapeNext = false
			continue
		}

		if ch == '\\' && inDouble {
			// In double quotes, \ escapes \, ", $, `, and newline
			next := i + 1
			if next < len(input) {
				switch input[next] {
				case '\\', '"', '$', '`':
					escapeNext = true
					continue
				case '\n':
					i++ // skip newline
					continue
				}
			}
			current.WriteByte(ch)
			continue
		}

		if ch == '\'' && !inDouble {
			if !inSingle {
				inSingle = true
				continue // start quote — don't write the quote char
			}
			// End single quote
			inSingle = false
			flush()
			continue
		}

		if ch == '"' && !inSingle {
			if !inDouble {
				inDouble = true
				continue // start double quote — don't write the quote char
			}
			// End double quote
			inDouble = false
			flush()
			continue
		}

		if inSingle || inDouble {
			current.WriteByte(ch)
			continue
		}

		// Outside quotes — handle operators and whitespace
		if ch == ' ' || ch == '\t' {
			flush()
			continue
		}

		// Multi-char operators: &&, ||, >>
		// Check for two-character operators
		if i+1 < len(input) {
			op2 := string(input[i]) + string(input[i+1])
			switch op2 {
			case "&&", "||", ">>":
				flush()
				tokens = append(tokens, op2)
				i++
				continue
			}
		}

		// Single-char operators: |, >, ;
		switch ch {
		case '|', '>', ';':
			flush()
			tokens = append(tokens, string(ch))
			continue
		}

		// Regular character
		current.WriteByte(ch)
	}

	flush()
	return tokens
}

// ── Safe command prefixes ──────────────────────────────────────────────
// (Unused — classification falls through to Safe by default. Kept as
// documentation of what's considered read-only.)

var writePrefixes = map[string]bool{
	"echo": true, "sed": true, "awk": true, "tee": true,
	"rm": true, "mv": true, "cp": true, "touch": true,
	"mkdir": true, "rmdir": true, "chmod": true, "chown": true,
}

var systemPrefixes = map[string]bool{
	"sudo": true, "apt": true, "apt-get": true, "yum": true,
	"brew": true, "dpkg": true, "systemctl": true, "service": true,
	"useradd": true, "groupadd": true, "passwd": true, "chown": true,
}

var destructivePrefixes = map[string]bool{
	"dd": true, "mkfs": true, "mkfs.ext4": true, "mkfs.ext3": true,
	"mkfs.xfs": true, "fdisk": true, "parted": true, "mke2fs": true,
}

var networkPrefixes = map[string]bool{
	"curl": true, "wget": true, "scp": true, "rsync": true,
	"nc": true, "ncat": true, "ssh": true, "sftp": true,
	"ftp": true, "telnet": true, "git": true,
}

var pipedShells = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true, "ksh": true,
}

var codeEvalPrefixes = map[string]bool{
	"eval": true, "node": true, "python": true, "python3": true,
	"perl": true, "ruby": true, "php": true,
}

var installPrefixes = map[string]bool{
	"npm": true, "pip": true, "pip3": true, "gem": true,
	"cargo": true, "brew": true, "go": true,
}

// ── Classifier ─────────────────────────────────────────────────────────

// Classify determines the risk class of a shell command using token-level
// heuristics. Returns the highest-severity class detected.
//
// Priority (highest to lowest):
// blocked > destructive > system_write > code_execution > network_egress >
// install > local_write > safe
func Classify(cmd string) RiskClass {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return Safe
	}

	// Check blocked patterns on raw command (before tokenization mangles them)
	if isRawBlocked(cmd) {
		return Blocked
	}

	tokens := tokenize(cmd)
	if len(tokens) == 0 {
		return Safe
	}

	// Split on command separators (;, &&, ||, |)
	// Each segment is analyzed independently; the worst class wins.
	segments := splitSegments(tokens)

	worst := Safe
	for _, seg := range segments {
		cls := classifySegment(seg)
		if rank(cls) > rank(worst) {
			worst = cls
		}
	}
	return worst
}

// isRawBlocked checks the raw command string for patterns that are
// blocked regardless of tokenization artifacts.
func isRawBlocked(cmd string) bool {
	// Fork bomb
	if cmd == ":(){ :|:& };:" {
		return true
	}
	if strings.Contains(cmd, ":{") && strings.Contains(cmd, "}:") {
		return true
	}
	return false
}

// splitSegments splits token sequences on command separators.
// ;, &&, || all start a new segment. Pipe (|) is NOT a segment separator
// — it stays within a segment so code_execution detection can find it.
func splitSegments(tokens []string) [][]string {
	var segments [][]string
	var current []string

	for _, tok := range tokens {
		switch tok {
		case ";", "&&", "||":
			if len(current) > 0 {
				segments = append(segments, current)
				current = nil
			}
		default:
			current = append(current, tok)
		}
	}
	if len(current) > 0 {
		segments = append(segments, current)
	}
	return segments
}

// classifySegment classifies a single command segment (no separators).
func classifySegment(tokens []string) RiskClass {
	if len(tokens) == 0 {
		return Safe
	}

	first := tokens[0]

	// Blocked
	if isBlocked(tokens) {
		return Blocked
	}

	// Destructive
	if isDestructive(first, tokens) {
		return Destructive
	}

	// System write
	if isSystemWrite(first, tokens) {
		return SystemWrite
	}

	// Code execution checks (pipe to shell, eval, -e/-c flags)
	if isCodeExecution(first, tokens) {
		return CodeExecution
	}

	// Network egress
	if isNetworkEgress(first, tokens) {
		return NetworkEgress
	}

	// Install
	if isInstall(first, tokens) {
		return Install
	}

	// Local write
	if isLocalWrite(first, tokens) {
		return LocalWrite
	}

	// Check for redirect targets that are system paths
	if hasSystemRedirectTarget(tokens) {
		return SystemWrite
	}

	return Safe
}

// ── Detection helpers ──────────────────────────────────────────────────

func isBlocked(tokens []string) bool {
	// dd to block device
	if len(tokens) >= 4 && tokens[0] == "dd" {
		for i, tok := range tokens {
			if tok == "of=" && i+2 < len(tokens) && strings.HasPrefix(tokens[i+2], "/dev/sd") {
				// of=/dev/sda (no space)
				return true
			}
			if strings.HasPrefix(tok, "of=") && strings.Contains(tok, "/dev/sd") {
				return true
			}
			if strings.HasPrefix(tok, "of=") && strings.Contains(tok, "/dev/nvme") {
				return true
			}
		}
	}
	return false
}

func isDestructive(first string, tokens []string) bool {
	// rm with -rf targeting root paths
	if first == "rm" {
		hasRF := false
		for _, tok := range tokens[1:] {
			if tok == "-rf" || tok == "-fr" || tok == "--no-preserve-root" || tok == "-r" || tok == "-f" {
				hasRF = true
			}
		}
		if !hasRF {
			return false
		}
		for _, tok := range tokens[1:] {
			if strings.HasPrefix(tok, "/") && !strings.HasPrefix(tok, "/tmp") && !strings.HasPrefix(tok, "/workspace") {
				return true
			}
		}
		return false
	}

	if !destructivePrefixes[first] || len(tokens) < 2 {
		return false
	}

	// mkfs, fdisk, parted, etc. — any usage is destructive
	if first != "dd" {
		return len(tokens) >= 1
	}

	// dd with of=/dev/sd* or of=/dev/nvme*
	for _, tok := range tokens {
		if strings.HasPrefix(tok, "of=") && strings.Contains(tok, "/dev/") {
			return true
		}
		if tok == "of=" && len(tokens) > 1 {
			for j := 0; j < len(tokens); j++ {
				if strings.HasPrefix(tokens[j], "/dev/sd") || strings.HasPrefix(tokens[j], "/dev/nvme") {
					return true
				}
			}
		}
	}
	return false
}

func isSystemWrite(first string, tokens []string) bool {
	if first == "sudo" {
		return true
	}
	if systemPrefixes[first] {
		return true
	}
	// Check redirect targets for system paths
	for _, tok := range tokens {
		if tok == ">" || tok == ">>" {
			continue
		}
		if isSystemPath(tok) {
			// Check if it's a redirect target (token follows > or >>)
			for i, t := range tokens {
				if (t == ">" || t == ">>") && i+1 < len(tokens) && tokens[i+1] == tok {
					return true
				}
			}
		}
	}
	return false
}

func isLocalWrite(first string, tokens []string) bool {
	// echo without redirect is safe (just displaying text)
	if first == "echo" {
		for _, tok := range tokens {
			if tok == ">" || tok == ">>" {
				return true
			}
		}
		return false
	}
	if writePrefixes[first] {
		return true
	}
	// Any command with > or >> is a write
	for _, tok := range tokens {
		if tok == ">" || tok == ">>" {
			return true
		}
	}
	return false
}

func isNetworkEgress(first string, tokens []string) bool {
	if !networkPrefixes[first] {
		return false
	}
	// git push requires a remote argument
	if first == "git" {
		return hasArgAfter(tokens, "git", "push") && hasArgAfter(tokens, "push", "")
	}
	// rsync with remote target (contains :)
	if first == "rsync" {
		for _, tok := range tokens[1:] {
			if strings.Contains(tok, "@") && strings.Contains(tok, ":") {
				return true
			}
			if strings.Contains(tok, "::") {
				return true
			}
		}
		return false
	}
	// All other network commands are inherently egress
	return true
}

func isCodeExecution(first string, tokens []string) bool {
	// Pipe to shell interpreter
	for i, tok := range tokens {
		if tok == "|" && i+1 < len(tokens) && pipedShells[tokens[i+1]] {
			return true
		}
	}

	if !codeEvalPrefixes[first] {
		// Check go run / go tool — compiles and executes code
		if first == "go" {
			for _, tok := range tokens[1:] {
				if tok == "run" || tok == "tool" || tok == "generate" {
					return true
				}
			}
		}
		return false
	}

	// eval is always code execution
	if first == "eval" {
		return true
	}

	// node/python/perl/ruby/php with -e, -c, -r flags
	for _, tok := range tokens[1:] {
		if tok == "-e" || tok == "-c" || tok == "-r" {
			return true
		}
	}

	return false
}

func isInstall(first string, tokens []string) bool {
	if !installPrefixes[first] {
		return false
	}

	// npm install / npm ci / npm i
	if first == "npm" || first == "pip" || first == "pip3" || first == "gem" {
		for _, tok := range tokens[1:] {
			if tok == "install" || tok == "i" || tok == "ci" {
				return true
			}
		}
	}

	// cargo install
	if first == "cargo" {
		return hasArgAfter(tokens, "cargo", "install")
	}

	// go install <remote> OR go install <local-path>
	if first == "go" {
		hasInstall := false
		for _, tok := range tokens[1:] {
			if tok == "install" {
				hasInstall = true
				continue
			}
			if hasInstall {
				return true // go install <something> downloads deps
			}
		}
		return false // bare "go install" = local build only
	}

	// brew install
	if first == "brew" {
		return hasArgAfter(tokens, "brew", "install")
	}

	return false
}

// hasArgAfter returns true if the token after 'after' is 'target'.
// If target is empty, just checks that 'after' exists and has a successor.
func hasArgAfter(tokens []string, after, target string) bool {
	for i, tok := range tokens {
		if tok == after && i+1 < len(tokens) {
			if target == "" {
				return true
			}
			// Check next non-flag token
			for j := i + 1; j < len(tokens); j++ {
				if !strings.HasPrefix(tokens[j], "-") {
					return tokens[j] == target || target == ""
				}
			}
			return false
		}
	}
	return false
}

// hasSystemRedirectTarget checks if any redirect target is a system path.
func hasSystemRedirectTarget(tokens []string) bool {
	for _, tok := range tokens {
		if tok == ">" || tok == ">>" {
			continue
		}
		if isSystemPath(tok) {
			return true
		}
	}
	return false
}

// isSystemPath returns true if the path targets a system directory.
var systemPathPrefixes = []string{"/etc/", "/usr/", "/bin/", "/lib/", "/var/", "/opt/", "/boot/", "/sbin/"}

func isSystemPath(path string) bool {
	for _, p := range systemPathPrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// ── Ranking ────────────────────────────────────────────────────────────

// rank returns the severity order for priority comparison.
func rank(cls RiskClass) int {
	switch cls {
	case Blocked:
		return 8
	case Destructive:
		return 7
	case SystemWrite:
		return 6
	case CodeExecution:
		return 5
	case NetworkEgress:
		return 4
	case Install:
		return 3
	case LocalWrite:
		return 2
	case Safe:
		return 1
	default:
		return 0
	}
}
