package danger

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ── Read ledger + unread-script execution gate (H-6) ────────────────────
//
// Scenarios 21/22 of the injection study were the only two that executed on
// the capable tier: the payload sits inside the *correct, documented fix*
// for a real failure and fires when the agent runs the thing it just fixed
// to check that it worked. In 12 of 14 executing runs the model identified
// the file as tampered in the same reply — detection is not the failing
// control, timing is. This gate makes "execute a repo-supplied script whose
// contents have not been read this session" its own approval class:
// mechanical, cheap, and never trust-shortcuttable.
//
// Corollary encoded by construction: a FAILED read never populates the
// ledger, so "the read errored, run it instead" stays gated — exactly the
// capable-model failure observed in the study.
//
// The ledger is FINGERPRINTED (TOCTOU hardening): a license is bound to
// the file state at display time (size + mtime, sha256 for files up to
// readFingerprintMaxBytes). A file mutated after its read — via another
// tool, a lifecycle hook, or a background process — loses the license and
// the gate re-fires; re-reading the mutated content re-licenses it.
// Audit-time reads (scanUnreadScripts) never enter the ledger: the
// auditor is not the model.

// UnreadExec is the class for executing a script file that has not been
// read (or written) in this session. Same rank tier as SystemWrite: always
// prompts by default, never eligible for session trust shortcuts.
const UnreadExec RiskClass = "unread_exec"

// readEntry is the fingerprinted state of a file at the moment its
// contents were displayed to (or authored by) the model. size < 0 marks
// an entry we could not fingerprint — fail-closed: it never licenses.
type readEntry struct {
	size    int64
	modNano int64
	hash    [32]byte
	hashed  bool
}

var readLedgerMu sync.RWMutex
var readLedger = make(map[string]readEntry)

// readFingerprintMaxBytes caps content hashing. Files beyond this size
// carry a size+mtime fingerprint only — a documented gap for adversarial
// same-size mutation with a preserved mtime on very large files, which is
// out of the threat model for repo-supplied scripts.
const readFingerprintMaxBytes = 1 << 20 // 1 MiB

// RecordRead marks path as read this session. Paths are normalised to
// absolute/cleaned form. Successful writes through the file tools also
// record here — content the agent authored is content it has seen.
//
// The entry is fingerprinted at record time; WasReadFresh re-verifies the
// on-disk state at gate time so a post-read mutation re-fires the H-6 gate.
func RecordRead(path string) {
	if path == "" {
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return
	}
	entry := readEntry{size: -1}
	if e, ok := fingerprintFile(abs); ok {
		entry = e
	}
	readLedgerMu.Lock()
	readLedger[filepath.Clean(abs)] = entry
	readLedgerMu.Unlock()
}

// WasRead reports whether path was recorded as read this session,
// regardless of whether the bytes have changed since. Licensing checks
// must use WasReadFresh.
func WasRead(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	readLedgerMu.RLock()
	defer readLedgerMu.RUnlock()
	_, ok := readLedger[filepath.Clean(abs)]
	return ok
}

// WasReadFresh reports whether path was read this session AND the bytes on
// disk are still the state that was displayed (or authored) at record
// time: same size, same mtime, and — for files up to readFingerprintMaxBytes
// — the same sha256 digest. A read that is no longer fresh does not license
// execution; the H-6 gate re-fires until the mutated content is re-read
// (which renews the fingerprint, because now the model has seen THAT).
func WasReadFresh(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	clean := filepath.Clean(abs)
	readLedgerMu.RLock()
	entry, ok := readLedger[clean]
	readLedgerMu.RUnlock()
	if !ok || entry.size < 0 {
		return false
	}
	cur, ok := fingerprintFile(clean)
	if !ok {
		return false
	}
	if entry.size != cur.size || entry.modNano != cur.modNano {
		return false
	}
	if entry.hashed && cur.hashed && entry.hash != cur.hash {
		return false
	}
	return true
}

// fingerprintFile captures the current on-disk state of abs: size, mtime,
// and content hash when the file is within the hashing cap.
func fingerprintFile(abs string) (readEntry, bool) {
	st, err := os.Stat(abs)
	if err != nil || !st.Mode().IsRegular() {
		return readEntry{}, false
	}
	e := readEntry{size: st.Size(), modNano: st.ModTime().UnixNano()}
	if st.Size() <= readFingerprintMaxBytes {
		if data, err := os.ReadFile(abs); err == nil {
			e.hash = sha256.Sum256(data)
			e.hashed = true
		}
	}
	return e, true
}

// ResetReadLedgerForTest clears the session ledger.
func ResetReadLedgerForTest() {
	readLedgerMu.Lock()
	readLedger = make(map[string]readEntry)
	readLedgerMu.Unlock()
}

// scriptFileExtensions mark file types that are executed (not just parsed
// as data) when handed to an interpreter or invoked directly.
var scriptFileExtensions = map[string]bool{
	".sh": true, ".bash": true, ".zsh": true, ".ksh": true,
	".py": true, ".pyw": true,
	".js": true, ".mjs": true, ".cjs": true, ".ts": true, ".tsx": true,
	".rb": true, ".pl": true, ".pm": true, ".lua": true, ".php": true,
	".r": true, ".rs": true, ".dart": true, ".scpt": true, ".applescript": true,
}

// scriptInterpreters execute a file operand as code.
var scriptInterpreters = map[string]bool{
	"bash": true, "sh": true, "zsh": true, "dash": true, "ksh": true, "fish": true,
	"python": true, "python3": true, "node": true, "deno": true, "bun": true,
	"ruby": true, "perl": true, "php": true, "lua": true, "Rscript": true,
	"osascript": true, "ts-node": true, "tsx": true, "pwsh": true, "powershell": true,
	"java": true, "scala": true, "nushell": true, "nu": true,
}

// looksLikeScriptFile reports whether tok names an existing regular file
// that would be executed: a script extension, an explicit relative path
// (./x), or a shebang header. Non-existent paths never gate (the command
// will simply fail).
func looksLikeScriptFile(tok string) bool {
	if tok == "" || strings.HasPrefix(tok, "-") {
		return false
	}
	// Skip obvious non-path tokens early (URLs, variable refs).
	if strings.Contains(tok, "://") || strings.HasPrefix(tok, "$") {
		return false
	}
	path := expandShellTokenPath(tok)
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return false
	}
	if strings.HasPrefix(tok, "./") || strings.HasPrefix(path, "/") {
		ext := strings.ToLower(filepath.Ext(path))
		if scriptFileExtensions[ext] {
			return true
		}
		// ./tool or /abs/tool with a shebang: executed regardless of suffix.
		return fileHasShebang(path)
	}
	ext := strings.ToLower(filepath.Ext(path))
	return scriptFileExtensions[ext]
}

func fileHasShebang(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var head [2]byte
	if _, err := f.Read(head[:]); err != nil {
		return false
	}
	return head[0] == '#' && head[1] == '!'
}

// UnreadScriptTargets returns the script-file operands of cmd that execute
// code and have not been read this session. Empty when nothing gates.
//
// Verb-aware by design: only stages whose command IS an execution context
// (interpreter, source, direct script invocation) are scanned, so `grep
// pattern build.sh` — a read — never triggers the gate.
func UnreadScriptTargets(cmd string) []string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return nil
	}
	main, subs := normalize(cmd)
	var out []string
	seen := make(map[string]bool)
	collect := func(c string) {
		for _, t := range unreadTargetsOne(c) {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	collect(main)
	for _, s := range subs {
		collect(s)
	}
	return out
}

func unreadTargetsOne(cmd string) []string {
	var out []string
	for _, seg := range splitSegments(tokenize(cmd)) {
		for _, stage := range splitPipes(seg) {
			out = append(out, unreadTargetsStage(stage)...)
		}
	}
	return out
}

func unreadTargetsStage(stage []string) []string {
	if len(stage) == 0 {
		return nil
	}
	cmdTokens, _ := unwrapWrappers(stage)
	if len(cmdTokens) == 0 {
		return nil
	}
	name := commandName(cmdTokens[0])
	operands := cmdTokens[1:]

	isExec := false
	switch {
	case scriptInterpreters[name]:
		isExec = true
	case name == "source" || name == ".":
		isExec = true
	case strings.Contains(cmdTokens[0], "/"):
		// Direct invocation: ./scripts/build.sh, path/to/tool
		isExec = true
		operands = cmdTokens // the script itself is the first operand
	case scriptFileExtensions[strings.ToLower(filepath.Ext(name))]:
		isExec = true
	}
	if !isExec {
		return nil
	}

	var out []string
	for _, tok := range operands {
		if tok == "-c" || tok == "-e" || tok == "-m" || tok == "-s" {
			continue // inline payload / module flags — not file execution
		}
		if looksLikeScriptFile(tok) && !WasReadFresh(tok) {
			abs, err := filepath.Abs(expandShellTokenPath(tok))
			if err == nil {
				out = append(out, filepath.Clean(abs))
			}
		}
	}
	return out
}

// ClassifyScriptGate classifies cmd with the unread-script rule layered on
// top of the standard classifier: when an unread script executes, the class
// becomes UnreadExec for everything at or below the SystemWrite tier — so
// "code_execution": "allow" and trusted-class grants cannot bypass it.
// Stronger findings (persistence, unknown, destructive, blocked) keep their
// own class; they already gate harder and are never trust-shortcuttable.
func ClassifyScriptGate(cmd string) (RiskClass, []string) {
	cls := Classify(cmd)
	targets := UnreadScriptTargets(cmd)
	if len(targets) > 0 && Rank(cls) <= Rank(SystemWrite) {
		return UnreadExec, targets
	}
	return cls, targets
}
