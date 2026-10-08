package danger

import (
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
)

// ── Read ledger + unread-script execution gate ────────────────────
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
// the file state at display time (size + mtime + sha256). Files above
// readFingerprintMaxBytes never receive an execution-read license. A file mutated after its read — via another
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

// readLedgers is keyed by session (or other caller-supplied) identity, then
// by absolute path. The empty key is the process-global default used by
// Classify()/ClassifyScriptGate and by unit tests that call RecordRead
// without a context — that keeps CLI-shaped tests working. Long-lived
// surfaces (serve, telegram, schedule) stamp WithLedgerKey on the run
// context so a read in session A cannot license execution in session B.
var readLedgers = map[string]map[string]readEntry{}

type ledgerKeyCtx struct{}

type readDeliveryCtx struct{}
type readDelivery struct {
	mu      sync.Mutex
	entries map[string]readEntry
}

// BeginReadDelivery defers file-read receipts until the executor confirms that
// the complete result reached the model. Each tool call gets its own scope.
func BeginReadDelivery(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, readDeliveryCtx{}, &readDelivery{entries: make(map[string]readEntry)})
}

// FinishReadDelivery publishes only receipts from an untruncated successful
// tool result. The receipt keeps the digest captured while producing output;
// a mutation before delivery cannot substitute unseen bytes.
func FinishReadDelivery(ctx context.Context, delivered bool) {
	if ctx == nil {
		return
	}
	d, ok := ctx.Value(readDeliveryCtx{}).(*readDelivery)
	if !ok {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if delivered {
		readLedgerMu.Lock()
		for path, entry := range d.entries {
			ledgerMapLocked(ledgerKeyFrom(ctx))[path] = entry
		}
		readLedgerMu.Unlock()
	}
	d.entries = nil
}

// RecordReadContentCtx records the exact bytes delivered by a tool, after it
// has verified complete coverage and no local truncation. The current path
// must still contain those bytes, and loop-managed reads await delivery.
func RecordReadContentCtx(ctx context.Context, path string, size int64, digest [32]byte) {
	if ctx == nil {
		ctx = context.Background()
	}
	abs, err := resolvePathTarget(path)
	if err != nil {
		return
	}
	entry, ok := fingerprintFile(abs)
	if !ok || !entry.hashed || entry.size != size || entry.hash != digest {
		return
	}
	if d, ok := ctx.Value(readDeliveryCtx{}).(*readDelivery); ok {
		d.mu.Lock()
		if d.entries != nil {
			d.entries[abs] = entry
		}
		d.mu.Unlock()
		return
	}
	readLedgerMu.Lock()
	ledgerMapLocked(ledgerKeyFrom(ctx))[abs] = entry
	readLedgerMu.Unlock()
}

// WithLedgerKey scopes subsequent RecordReadCtx / WasReadFreshCtx /
// ClassifyScriptGateCtx / UnreadScriptTargetsCtx calls on ctx to key.
// An empty key selects the process-global default ledger.
func WithLedgerKey(ctx context.Context, key string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ledgerKeyCtx{}, key)
}

func ledgerKeyFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if k, ok := ctx.Value(ledgerKeyCtx{}).(string); ok {
		return k
	}
	return ""
}

// ledgerMapLocked returns the path map for key. Caller must hold
// readLedgerMu (write lock if the map may be created).
func ledgerMapLocked(key string) map[string]readEntry {
	m := readLedgers[key]
	if m == nil {
		m = make(map[string]readEntry)
		readLedgers[key] = m
	}
	return m
}

// readFingerprintMaxBytes caps content hashing. Files beyond this size fail
// closed rather than receiving a weaker size+mtime-only license.
const readFingerprintMaxBytes = 1 << 20 // 1 MiB

// RecordRead marks path as read this session. Paths are normalised to
// absolute/cleaned form. Successful writes through the file tools also
// record here — content the agent authored is content it has seen.
//
// The entry is fingerprinted at record time; WasReadFresh re-verifies the
// on-disk state at gate time so a post-read mutation re-fires the gate.
func RecordRead(path string) {
	recordReadKey("", path)
}

// RecordReadCtx is RecordRead scoped to the ledger key on ctx (see
// WithLedgerKey). Tools that run inside the agent loop must use this so
// concurrent serve/telegram sessions do not share licenses.
func RecordReadCtx(ctx context.Context, path string) {
	recordReadKey(ledgerKeyFrom(ctx), path)
}

func recordReadKey(key, path string) {
	if path == "" {
		return
	}
	abs, err := resolvePathTarget(path)
	if err != nil {
		return
	}
	entry := readEntry{size: -1}
	if e, ok := fingerprintFile(abs); ok {
		entry = e
	}
	readLedgerMu.Lock()
	ledgerMapLocked(key)[filepath.Clean(abs)] = entry
	readLedgerMu.Unlock()
}

// WasRead reports whether path was recorded as read this session,
// regardless of whether the bytes have changed since. Licensing checks
// must use WasReadFresh.
func WasRead(path string) bool {
	return wasReadKey("", path)
}

// WasReadCtx is WasRead scoped to the ledger key on ctx.
func WasReadCtx(ctx context.Context, path string) bool {
	return wasReadKey(ledgerKeyFrom(ctx), path)
}

func wasReadKey(key, path string) bool {
	abs, err := resolvePathTarget(path)
	if err != nil {
		return false
	}
	readLedgerMu.RLock()
	defer readLedgerMu.RUnlock()
	m := readLedgers[key]
	if m == nil {
		return false
	}
	_, ok := m[filepath.Clean(abs)]
	return ok
}

// WasReadFresh reports whether path was read this session AND the bytes on
// disk are still the state that was displayed (or authored) at record
// time: same size, same mtime, and the same sha256 digest. A read that is no longer fresh does not license
// execution; the gate re-fires until the mutated content is re-read
// (which renews the fingerprint, because now the model has seen THAT).
func WasReadFresh(path string) bool {
	return wasReadFreshKey("", path)
}

// WasReadFreshCtx is WasReadFresh scoped to the ledger key on ctx.
func WasReadFreshCtx(ctx context.Context, path string) bool {
	return wasReadFreshKey(ledgerKeyFrom(ctx), path)
}

func wasReadFreshKey(key, path string) bool {
	abs, err := resolvePathTarget(path)
	if err != nil {
		return false
	}
	clean := filepath.Clean(abs)
	readLedgerMu.RLock()
	m := readLedgers[key]
	var entry readEntry
	ok := false
	if m != nil {
		entry, ok = m[clean]
	}
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

// openRegularFile opens path for reading only when it is a regular file. The
// type is checked with Stat BEFORE any open, because open(2) on a FIFO blocks
// until a writer appears and a device node can block or stream forever. The
// open itself is non-blocking and the handle is re-checked, so a path swapped
// to a FIFO between the Stat and the open cannot stall the caller either.
func openRegularFile(path string) (*os.File, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, os.ErrInvalid
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	hst, err := f.Stat()
	if err != nil || !hst.Mode().IsRegular() {
		f.Close()
		return nil, os.ErrInvalid
	}
	return f, nil
}

// fingerprintFile captures the current on-disk state of abs: size, mtime,
// and content hash when the file is within the hashing cap. The file is
// opened FIRST and stat'd/read through that single handle: the previous
// os.Stat(path) + os.ReadFile(path) form re-resolved the path between the
// two calls, so a swap in that window could license a size/mtime from one
// inode with a hash (when hashed) from another. A file that cannot be
// opened fails closed — it can never have been displayed to the model, so
// it must never yield a stat-only license. Non-regular files (FIFOs,
// devices) are refused before any open so they can never block the caller.
func fingerprintFile(abs string) (readEntry, bool) {
	f, err := openRegularFile(abs)
	if err != nil {
		return readEntry{}, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return readEntry{}, false
	}
	e := readEntry{size: st.Size(), modNano: st.ModTime().UnixNano()}
	if st.Size() > readFingerprintMaxBytes {
		return readEntry{}, false
	}
	data, err := io.ReadAll(io.LimitReader(f, readFingerprintMaxBytes+1))
	if err != nil || int64(len(data)) != st.Size() {
		return readEntry{}, false
	}
	e.hash = sha256.Sum256(data)
	e.hashed = true
	return e, true
}

// ResetReadLedgerForTest clears the session ledger.
func ResetReadLedgerForTest() {
	readLedgerMu.Lock()
	readLedgers = map[string]map[string]readEntry{}
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
	"ash": true, "ipython": true, "luajit": true,
}

// isScriptInterpreter reports whether name executes a file operand as code.
// Beyond the explicit set it accepts the shell and stdin-executing runtimes
// the classifier already treats as interpreters, and versioned spellings
// (python3.12, python2, lua5.4, ruby3.2) by dropping a trailing version
// suffix, so a versioned name is gated exactly like its base name.
func isScriptInterpreter(name string) bool {
	if scriptInterpreters[name] || pipedShells[name] || isStdinExecInterpreter(name) {
		return true
	}
	base := strings.TrimRight(name, "0123456789.")
	if base == "" || base == name {
		return false
	}
	return scriptInterpreters[base] || pipedShells[base] || isStdinExecInterpreter(base)
}

// looksLikeScriptFile reports whether tok names an existing regular file
// that would be executed: a script extension, an explicit relative path
// (./x), a shebang header, or — when interpreterOperand is true — any file
// the interpreter itself would run (the ENOEXEC fallback for extension-less
// files, $VAR-expanded paths). Non-existent paths never gate (the command
// will simply fail).
func looksLikeScriptFile(tok string, interpreterOperand bool) bool {
	if tok == "" || strings.HasPrefix(tok, "-") {
		return false
	}
	// URLs are never script files. $-prefixed tokens are NOT skipped:
	// `bash $HOME/evil.sh` executes exactly like `bash ~/evil.sh`, so the
	// old "variable ref" early-out was an gate bypass. Expand and gate
	// on the resolved file instead; unresolvable variables fail the stat
	// below and stay ungated.
	if strings.Contains(tok, "://") {
		return false
	}
	path := expandShellTokenPath(tok)
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return false
	}
	if strings.HasPrefix(tok, "$") {
		// $-expanded: a script suffix gates in any execution context;
		// extension-less expansions only gate when handed to an
		// interpreter, which would execute them as code.
		if scriptFileExtensions[strings.ToLower(filepath.Ext(path))] {
			return true
		}
		return interpreterOperand
	}
	if strings.HasPrefix(tok, "./") || strings.HasPrefix(path, "/") {
		ext := strings.ToLower(filepath.Ext(path))
		if scriptFileExtensions[ext] {
			return true
		}
		// ./tool or /abs/tool with a shebang: executed regardless of
		// suffix. Without a shebang an interpreter still runs the file
		// via the ENOEXEC fallback (bash ./no-shebang), so gate it there
		// too. Direct invocation also recognizes extensionless text because
		// the shell can interpret it after ENOEXEC; binary files retain
		// execution policy without being treated as text scripts.
		return interpreterOperand || fileHasShebang(path) || executableTextFile(path)
	}
	ext := strings.ToLower(filepath.Ext(path))
	return scriptFileExtensions[ext]
}

// ENOEXEC lets a shell execute extensionless text without a shebang. Binary
// executables retain their code-execution classification without text provenance.
func executableTextFile(path string) bool {
	f, err := openRegularFile(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var head [512]byte
	n, err := f.Read(head[:])
	if err != nil && err != io.EOF {
		return false
	}
	return n > 0 && !strings.ContainsRune(string(head[:n]), 0)
}

func fileHasShebang(path string) bool {
	f, err := openRegularFile(path)
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
	return unreadScriptTargetsKey("", cmd)
}

// UnreadScriptTargetsCtx is UnreadScriptTargets scoped to the ledger key on ctx.
func UnreadScriptTargetsCtx(ctx context.Context, cmd string) []string {
	return unreadScriptTargetsKey(ledgerKeyFrom(ctx), cmd)
}

func unreadScriptTargetsKey(key, cmd string) []string {
	var out []string
	seen := map[string]bool{}
	collect := func(a Analysis) {
		rewritten := map[string]bool{}
		for _, path := range a.RewrittenFiles {
			rewritten[path] = true
		}
		for _, path := range a.ExecutionFiles {
			if seen[path] {
				continue
			}
			if rewritten[path] || !wasReadFreshKey(key, path) {
				seen[path] = true
				out = append(out, path)
			}
		}
	}
	collect(Analyze(cmd))
	// Brace groups distribute over their word before exec (`bash {x,y}.sh`
	// runs x.sh). Analyze the distributed spelling as well so the operand
	// the shell actually opens is the one that is gated.
	if distributed := distributeBraces(cmd); distributed != cmd {
		collect(Analyze(distributed))
	}
	return out
}

// distributeBraces rewrites each word that carries a {a,b} group into the
// words the shell produces: pre{a,b}post becomes prea post preb post.
// Groups without a top-level comma (${VAR}, find's {}) are left alone, and
// the expansion is bounded so a hostile nest cannot blow up.
func distributeBraces(cmd string) string {
	if !strings.Contains(cmd, "{") || !strings.Contains(cmd, ",") {
		return cmd
	}
	isSep := func(c byte) bool {
		switch c {
		case ' ', '\t', '\n', '\r', ';', '|', '&', '<', '>', '(', ')':
			return true
		}
		return false
	}
	var b strings.Builder
	for i := 0; i < len(cmd); {
		if isSep(cmd[i]) {
			b.WriteByte(cmd[i])
			i++
			continue
		}
		j := i
		for j < len(cmd) && !isSep(cmd[j]) {
			j++
		}
		word := cmd[i:j]
		if strings.Contains(word, "{") && !strings.Contains(word, "${") {
			b.WriteString(strings.Join(braceWords(word, 256), " "))
		} else {
			b.WriteString(word)
		}
		i = j
	}
	return b.String()
}

// braceWords expands the first comma-bearing brace group of word, recursively,
// returning at most limit words.
func braceWords(word string, limit int) []string {
	for i := 0; i < len(word); i++ {
		if word[i] != '{' {
			continue
		}
		// Find the matching close and the top-level commas in between.
		depth := 0
		end := -1
		var commas []int
		for j := i; j < len(word) && end < 0; j++ {
			switch word[j] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					end = j
				}
			case ',':
				if depth == 1 {
					commas = append(commas, j)
				}
			}
		}
		if end < 0 || len(commas) == 0 {
			continue
		}
		pre, post := word[:i], word[end+1:]
		bounds := append(append([]int{i}, commas...), end)
		var out []string
		for k := 0; k+1 < len(bounds); k++ {
			for _, w := range braceWords(pre+word[bounds[k]+1:bounds[k+1]]+post, limit) {
				if len(out) >= limit {
					return out
				}
				out = append(out, w)
			}
		}
		return out
	}
	return []string{word}
}

// hasGlobMeta reports whether a path operand is expanded by the shell.
func hasGlobMeta(tok string) bool { return strings.ContainsAny(tok, "*?[") }

// maxGlobPatternBytes is the longest execution-path word expanded as a glob;
// it matches the kernel's PATH_MAX.
const maxGlobPatternBytes = 4096

// executionCandidates resolves one operand to the paths the shell would hand
// to the interpreter: the cleaned absolute path, or every glob match. A glob
// that matches nothing is returned as its own pattern so the operand still
// gates (fail closed) instead of silently disappearing.
func executionCandidates(tok, cwd string) []string {
	path := expandShellTokenPath(tok)
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	// A pattern longer than any real path cannot match a file; matching it
	// against directory entries is superlinear in its length.
	if !hasGlobMeta(path) || len(path) > maxGlobPatternBytes {
		return []string{filepath.Clean(path)}
	}
	matches, err := filepath.Glob(path)
	if err != nil || len(matches) == 0 {
		return []string{filepath.Clean(path)}
	}
	sort.Strings(matches)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, filepath.Clean(m))
	}
	return out
}

// inlinePayloadFlag reports whether tok is a flag whose next word is code (or
// a module name), not a file: -c / -e, including fused short clusters such as
// -lc or -ec for shells. Everything after it is the payload or its arguments.
func inlinePayloadFlag(name, tok string) bool {
	if tok == "-c" || tok == "-e" {
		return true
	}
	if !isShortFlagToken(tok) || len(tok) < 3 {
		return false
	}
	if pipedShells[name] {
		return strings.Contains(tok[1:], "c")
	}
	last := tok[len(tok)-1]
	return last == 'c' || last == 'e'
}

// pathKey is the identity used to match an executed path against paths the
// same command wrote earlier.
func pathKey(path string) string {
	if resolved, err := resolvePathTarget(path); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}

// stageWrittenPaths returns the file paths a stage writes, resolved against
// cwd: shell redirects, semantic output options (curl -o, wget -O, sed -i,
// ...), tee operands, and the destination of cp/mv/install/ln.
func stageWrittenPaths(stage, inner []string, name, cwd string) []string {
	var raw []string
	for j, tok := range stage {
		if isRedirectToken(tok) && j+1 < len(stage) {
			raw = append(raw, stage[j+1])
		}
	}
	if len(inner) > 0 {
		raw = append(raw, semanticWriteTargets(name, inner)...)
		var operands []string
		skipNext := false
		for _, tok := range inner[1:] {
			if skipNext {
				skipNext = false
				continue
			}
			if isRedirectToken(tok) {
				skipNext = true
				continue
			}
			if strings.HasPrefix(tok, "-") || tok == "" || tok == "<" || tok == "<<" || tok == "<<<" {
				continue
			}
			operands = append(operands, tok)
		}
		switch name {
		case "tee":
			raw = append(raw, operands...)
		case "cp", "mv", "install", "ln", "rsync":
			if len(operands) >= 2 {
				dest := operands[len(operands)-1]
				raw = append(raw, dest)
				// Copying into a directory writes dest/<base of each source>;
				// a destination that does not exist yet may be created as one.
				destPath := expandShellTokenPath(dest)
				if !filepath.IsAbs(destPath) {
					destPath = filepath.Join(cwd, destPath)
				}
				if st, err := os.Stat(destPath); err != nil || st.IsDir() {
					for _, src := range operands[:len(operands)-1] {
						raw = append(raw, filepath.Join(dest, filepath.Base(src)))
					}
				}
			}
		case "dd":
			for _, tok := range inner[1:] {
				if strings.HasPrefix(tok, "of=") {
					raw = append(raw, tok)
				}
			}
		}
	}
	var out []string
	for _, tok := range raw {
		if tok == "" || tok == "-" || strings.Contains(tok, dynamicSubstToken) {
			continue
		}
		path := expandShellTokenPath(tok)
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		out = append(out, pathKey(path))
	}
	return out
}

// stageLedgerFiles reports the files a stage executes and, separately, the
// subset that an earlier stage of the same command wrote (so any prior read
// licence describes content that no longer exists when the shell runs it).
// The stage's own writes are then recorded for the stages that follow.
func stageLedgerFiles(stage []string, cwd string, written map[string]bool) (files, rewritten []string) {
	files = stageExecutionFilesWritten(stage, cwd, written)
	for _, path := range files {
		if written[pathKey(path)] {
			rewritten = append(rewritten, path)
		}
	}
	if written != nil {
		inner, _ := unwrapWrappers(stage)
		name := ""
		if len(inner) > 0 {
			name = commandName(inner[0])
		}
		for _, path := range stageWrittenPaths(stage, inner, name, cwd) {
			written[path] = true
		}
	}
	return files, rewritten
}

func stageExecutionFiles(stage []string, cwd string) []string {
	return stageExecutionFilesWritten(stage, cwd, nil)
}

// stageExecutionFilesWritten returns the files a stage executes. Only the
// program operand (and the values of helper options that load code) execute;
// redirect operators and their targets, and data arguments that follow the
// program, never do. Glob operands gate every match, and a path written by an
// earlier stage of the same command gates even when it does not exist yet.
func stageExecutionFilesWritten(stage []string, cwd string, written map[string]bool) []string {
	if len(stage) == 0 {
		return nil
	}
	cmdTokens, _ := unwrapWrappers(stage)
	if len(cmdTokens) == 0 {
		return nil
	}
	name := commandName(cmdTokens[0])
	operands := cmdTokens[1:]
	helperTargets := executionFileTargets(name, cmdTokens)
	if interpreterIsSyntaxCheck(name, cmdTokens) {
		return nil
	}

	isExec := false
	// interpreterStage marks stages where the interpreter itself decides how
	// to execute the operand: shell-family interpreters fall back to ENOEXEC
	// execution for extension-less files, and source/. parse any file as
	// shell regardless of shebang or extension.
	interpreterStage := false
	switch {
	case isScriptInterpreter(name):
		isExec = true
		interpreterStage = true
	case name == "source" || name == ".":
		isExec = true
		interpreterStage = true
	case strings.Contains(cmdTokens[0], "/"):
		// Direct invocation: ./scripts/build.sh, path/to/tool
		isExec = true
		operands = cmdTokens[:1] // the direct execution target
	case scriptFileExtensions[strings.ToLower(filepath.Ext(name))]:
		isExec = true
	}
	if len(helperTargets) > 0 {
		if !isExec || (name == "node" && hasAny(cmdTokens, "--check", "-c")) {
			operands = nil // only the helper option values execute
		}
		isExec = true
		interpreterStage = true
	}
	if !isExec {
		return nil
	}

	var out []string
	seen := map[string]bool{}
	// gate reports every execution candidate for tok and whether any gated.
	gate := func(tok string, interp bool) bool {
		hit := false
		for _, path := range executionCandidates(tok, cwd) {
			if seen[path] {
				hit = true
				continue
			}
			if written[pathKey(path)] || looksLikeScriptFile(path, interp) || (hasGlobMeta(path) && !fileExists(path)) {
				seen[path] = true
				out = append(out, path)
				hit = true
			}
		}
		return hit
	}

	directInvocation := strings.Contains(cmdTokens[0], "/") && !isScriptInterpreter(name) && name != "source" && name != "."
	prevFlag := false
scan:
	for i := 0; i < len(operands); i++ {
		tok := operands[i]
		if tok == "" {
			continue
		}
		if directInvocation {
			gate(tok, interpreterStage)
			break
		}
		next := ""
		if i+1 < len(operands) {
			next = operands[i+1]
		}
		if isAllDigits(tok) && (isRedirectToken(next) || next == "<" || next == "<<" || next == "<<<") {
			continue // file-descriptor prefix of a redirect
		}
		switch {
		case isRedirectToken(tok), tok == "<<", tok == "<<<":
			i++ // redirect target / here-string data is never the program
			continue
		case tok == "<":
			// `bash < x.sh` feeds the file to the interpreter as its program.
			if i+1 < len(operands) {
				i++
				gate(operands[i], interpreterStage)
			}
			break scan
		}
		if inlinePayloadFlag(name, tok) {
			break // inline payload: the rest is code or its arguments
		}
		if tok == "-m" || tok == "-s" {
			prevFlag = true
			continue
		}
		if strings.HasPrefix(tok, "-") {
			prevFlag = tok != "--"
			continue
		}
		if strings.Contains(tok, "://") {
			prevFlag = false
			continue
		}
		hit := gate(tok, interpreterStage)
		wasFlagValue := prevFlag
		prevFlag = false
		if hit && !wasFlagValue {
			break // program found; what follows is data
		}
	}
	for _, tok := range helperTargets {
		if strings.HasPrefix(tok, "-") || strings.Contains(tok, "://") {
			continue
		}
		gate(tok, true)
	}
	return out
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// ClassifyScriptGate classifies cmd with the unread-script rule layered on
// top of the standard classifier: when an unread script executes, the class
// becomes UnreadExec for everything at or below the SystemWrite tier — so
// "code_execution": "allow" and trusted-class grants cannot bypass it.
// Stronger findings (persistence, unknown, destructive, blocked) keep their
// own class; they already gate harder and are never trust-shortcuttable.
func ClassifyScriptGate(cmd string) (RiskClass, []string) {
	return classifyScriptGateKey("", cmd)
}

// ClassifyScriptGateCtx is ClassifyScriptGate scoped to the ledger key on ctx.
func ClassifyScriptGateCtx(ctx context.Context, cmd string) (RiskClass, []string) {
	return classifyScriptGateKey(ledgerKeyFrom(ctx), cmd)
}

func classifyScriptGateKey(key, cmd string) (RiskClass, []string) {
	cls := Classify(cmd)
	targets := unreadScriptTargetsKey(key, cmd)
	if len(targets) > 0 && Rank(cls) <= Rank(SystemWrite) {
		return UnreadExec, targets
	}
	return cls, targets
}
