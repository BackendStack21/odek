package danger

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Repository-aware code-execution escalation for ordinary git verbs.
//
// `git status`, `git add`, `git commit`, `git merge`, `git checkout`, ... can
// spawn programs the repository or the user's git configuration names: hook
// scripts, an fsmonitor hook, clean/smudge filters, textconv and external diff
// drivers, merge drivers, an editor. Escalating every such verb made each
// ordinary git command prompt. The verbs are instead escalated only when the
// repository they target is armed, i.e. when something on disk could actually
// make git spawn a program for that verb. Anything that cannot be determined
// (uncertain working directory, no repository found, unreadable files, config
// includes, process-level GIT_* overrides) counts as armed.

const (
	maxGitConfigBytes = 1 << 20
	maxGitfileBytes   = 4096
	maxGitModuleDirs  = 256
	maxGitModuleDepth = 4
)

// gitSystemConfigPath is the system-wide git config. It is a variable so tests
// can point it at a hermetic file.
var gitSystemConfigPath = "/etc/gitconfig"

// gitArmKind classifies what an armed repository would make git run.
type gitArmKind uint16

const (
	armHooks gitArmKind = 1 << iota
	armFsmonitor
	armFilter
	armDiffExternal
	armTextconv
	armMergeDriver
	armEditor
	armSeqEditor
	armInteractive
	armSubmoduleExec
)

// gitVerbArmMask lists, per verb, the arming kinds that verb can trigger.
// Verbs that never run hooks (status, add, diff, restore, log, show) are not
// escalated by a repository whose only armed surface is a hook script.
var gitVerbArmMask = map[string]gitArmKind{
	"status":      armFsmonitor | armFilter,
	"add":         armFsmonitor | armFilter | armInteractive,
	"restore":     armFsmonitor | armFilter | armInteractive,
	"commit":      armHooks | armFsmonitor | armFilter,
	"diff":        armFsmonitor | armFilter | armDiffExternal | armTextconv,
	"merge":       armHooks | armFsmonitor | armFilter | armMergeDriver,
	"checkout":    armHooks | armFsmonitor | armFilter | armMergeDriver | armInteractive,
	"switch":      armHooks | armFsmonitor | armFilter | armMergeDriver | armInteractive,
	"stash":       armHooks | armFsmonitor | armFilter | armMergeDriver | armInteractive | armDiffExternal | armTextconv,
	"gc":          armHooks,
	"rebase":      armHooks | armFsmonitor | armFilter | armMergeDriver,
	"cherry-pick": armHooks | armFsmonitor | armFilter | armMergeDriver,
	"am":          armHooks | armFsmonitor | armFilter | armMergeDriver,
	"worktree":    armHooks | armFsmonitor | armFilter,
	"submodule":   armHooks | armFsmonitor | armFilter | armSubmoduleExec,
	"log":         armTextconv,
	"show":        armTextconv,
}

// gitRepoArms is what scanning a repository and its configuration found.
type gitRepoArms struct {
	kinds gitArmKind
	// failed means some state could not be read or understood; every kind
	// then applies.
	failed bool
}

func (a *gitRepoArms) fail() { a.failed = true }

func (a gitRepoArms) has(mask gitArmKind) bool { return a.failed || a.kinds&mask != 0 }

// gitRepoCtx is the working directory a git invocation runs in, as tracked by
// the shell analysis. A nil context, or one with known == false, means the
// directory cannot be trusted and the repository cannot be resolved.
type gitRepoCtx struct {
	cwd   string
	known bool
}

// newGitRepoCtx builds the context for one stage. prefix is the wrapper and
// assignment tokens in front of the git command; vars are the shell variables
// assigned earlier in the same command line. Either redefining the repository,
// config or a program git runs (see gitEnvNameRedirects), or a
// privilege-switching wrapper, makes the target repository and its config
// unknowable.
func newGitRepoCtx(cwd string, known bool, prefix []string, vars map[string]string) *gitRepoCtx {
	ctx := &gitRepoCtx{cwd: cwd, known: known}
	for _, tok := range prefix {
		if isAssignment(tok) {
			if name, _, _ := strings.Cut(tok, "="); gitEnvNameRedirects(name) {
				ctx.known = false
			}
			continue
		}
		switch commandName(tok) {
		case "sudo", "doas", "pkexec", "su", "runuser", "chroot":
			ctx.known = false
		}
	}
	for name := range vars {
		if gitEnvNameRedirects(name) {
			ctx.known = false
		}
	}
	return ctx
}

// gitEnvNameRedirects reports whether an environment variable name changes
// which repository, config or program git uses (GIT_DIR, GIT_CONFIG_*,
// GIT_EXTERNAL_DIFF, GIT_EDITOR, PATH, ...). Tracing and identity variables
// do not.
func gitEnvNameRedirects(name string) bool {
	upper := strings.ToUpper(name)
	return envExecNames[upper] || strings.HasSuffix(upper, "PAGER") || strings.HasPrefix(upper, "GIT_CONFIG_")
}

// gitGlobalOpts holds the repository-selecting options in front of the verb.
type gitGlobalOpts struct {
	dirs      []string
	gitDir    string
	hasGitDir bool
	// forced is set for options that make the repository state irrelevant
	// (pager, exec path) or whose value is missing.
	forced bool
}

func parseGitGlobalOpts(tokens []string) gitGlobalOpts {
	var o gitGlobalOpts
	seenGit := false
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if !seenGit {
			if commandName(tok) == "git" {
				seenGit = true
			}
			continue
		}
		if !strings.HasPrefix(tok, "-") {
			break
		}
		switch {
		case tok == "-C":
			if i+1 >= len(tokens) {
				o.forced = true
				break
			}
			i++
			o.dirs = append(o.dirs, tokens[i])
		case tok == "--git-dir":
			if i+1 >= len(tokens) {
				o.forced = true
				break
			}
			i++
			o.gitDir, o.hasGitDir = tokens[i], true
		case strings.HasPrefix(tok, "--git-dir="):
			o.gitDir, o.hasGitDir = tok[len("--git-dir="):], true
		case tok == "-c" || tok == "--config-env" || tok == "--namespace" ||
			tok == "--super-prefix" || tok == "--work-tree":
			i++
		case tok == "-p" || tok == "--paginate" || strings.HasPrefix(tok, "--exec-path") ||
			strings.HasPrefix(tok, "--html-path") || strings.HasPrefix(tok, "--man-path") ||
			strings.HasPrefix(tok, "--info-path"):
			o.forced = true
		}
	}
	return o
}

// gitRepoLookup is the outcome of locating a repository.
type gitRepoLookup int

const (
	gitRepoFound gitRepoLookup = iota
	gitRepoMissing
	gitRepoFailed
)

// startDirectory resolves the directory git starts repository discovery from:
// the tracked cwd with every -C applied in order. An absolute -C makes the
// result independent of an uncertain cwd.
func (c *gitRepoCtx) startDirectory(o gitGlobalOpts) (string, bool) {
	var dir string
	known := false
	if c != nil && c.known && filepath.IsAbs(c.cwd) {
		dir, known = c.cwd, true
	}
	for _, d := range o.dirs {
		if d == "" {
			continue
		}
		if strings.ContainsAny(d, "$*?[]`") || strings.Contains(d, dynamicSubstToken) {
			return "", false
		}
		p := expandTilde(d)
		switch {
		case filepath.IsAbs(p):
			dir, known = filepath.Clean(p), true
		case known:
			dir = filepath.Join(dir, p)
		default:
			return "", false
		}
	}
	return dir, known
}

// locateGitDir finds the git directory the invocation targets.
func (c *gitRepoCtx) locateGitDir(o gitGlobalOpts) (string, gitRepoLookup) {
	start, ok := c.startDirectory(o)
	if o.hasGitDir {
		if strings.ContainsAny(o.gitDir, "$*?[]`") || strings.Contains(o.gitDir, dynamicSubstToken) {
			return "", gitRepoFailed
		}
		p := expandTilde(o.gitDir)
		if !filepath.IsAbs(p) {
			if !ok {
				return "", gitRepoFailed
			}
			p = filepath.Join(start, p)
		}
		return resolveGitDirPath(p)
	}
	if !ok {
		return "", gitRepoFailed
	}
	return findGitDir(start)
}

// resolveGitDirPath turns an explicit git directory (or a gitfile) into the
// directory holding the repository metadata.
func resolveGitDirPath(p string) (string, gitRepoLookup) {
	st, err := os.Stat(p)
	if err != nil {
		return "", gitRepoFailed
	}
	if st.IsDir() {
		return p, gitRepoFound
	}
	if st.Mode().IsRegular() {
		return readGitfile(p)
	}
	return "", gitRepoFailed
}

// readGitfile parses a `gitdir: PATH` file (linked worktrees, submodules).
func readGitfile(path string) (string, gitRepoLookup) {
	data, err := readSmallFile(path, maxGitfileBytes)
	if err != nil {
		return "", gitRepoFailed
	}
	line, _, _ := strings.Cut(string(data), "\n")
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
	if !ok {
		return "", gitRepoFailed
	}
	target := strings.TrimSpace(rest)
	if target == "" {
		return "", gitRepoFailed
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	st, err := os.Stat(target)
	if err != nil || !st.IsDir() {
		return "", gitRepoFailed
	}
	return filepath.Clean(target), gitRepoFound
}

// findGitDir walks up from start to the nearest `.git` (directory or gitfile),
// or to a directory that is itself a git directory (bare repository, or the
// inside of `.git`). start is resolved to its physical path first, as git does.
func findGitDir(start string) (string, gitRepoLookup) {
	dir, err := filepath.EvalSymlinks(start)
	if err != nil {
		return "", gitRepoFailed
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return "", gitRepoFailed
	}
	for {
		dotGit := filepath.Join(dir, ".git")
		switch st, err := os.Lstat(dotGit); {
		case err == nil:
			if st.Mode()&fs.ModeSymlink != 0 {
				if st, err = os.Stat(dotGit); err != nil {
					return "", gitRepoFailed
				}
			}
			if st.IsDir() {
				return dotGit, gitRepoFound
			}
			if st.Mode().IsRegular() {
				return readGitfile(dotGit)
			}
			return "", gitRepoFailed
		case !errors.Is(err, fs.ErrNotExist):
			return "", gitRepoFailed
		}
		bare, err := looksLikeGitDir(dir)
		if err != nil {
			return "", gitRepoFailed
		}
		if bare {
			return dir, gitRepoFound
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", gitRepoMissing
		}
		dir = parent
	}
}

func looksLikeGitDir(dir string) (bool, error) {
	for _, name := range []string{"HEAD", "objects", "refs"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return false, nil
			}
			return false, err
		}
	}
	return true, nil
}

// readSmallFile reads a regular file of at most limit bytes. Anything else
// (device, FIFO, oversized) is an error so a hostile path cannot block or
// exhaust memory.
func readSmallFile(path string, limit int64) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if st.Size() > limit {
		return nil, errors.New("file too large")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("file too large")
	}
	return data, nil
}

// gitHookNames are the hook script names git runs. Files such as
// pre-commit.sample are inert and absent from this set.
var gitHookNames = map[string]bool{
	"applypatch-msg": true, "pre-applypatch": true, "post-applypatch": true,
	"pre-commit": true, "pre-merge-commit": true, "prepare-commit-msg": true,
	"commit-msg": true, "post-commit": true, "pre-rebase": true,
	"post-checkout": true, "post-merge": true, "pre-push": true,
	"pre-receive": true, "update": true, "proc-receive": true,
	"post-receive": true, "post-update": true, "reference-transaction": true,
	"push-to-checkout": true, "pre-auto-gc": true, "post-rewrite": true,
	"sendemail-validate": true, "fsmonitor-watchman": true, "p4-changelist": true,
	"p4-prepare-changelist": true, "p4-post-changelist": true, "p4-pre-submit": true,
	"post-index-change": true,
}

// scanGitHooks marks the repository armed when <dir>/hooks holds an
// executable file with a real hook name.
func scanGitHooks(dir string, arms *gitRepoArms) {
	hooks := filepath.Join(dir, "hooks")
	entries, err := os.ReadDir(hooks)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			arms.fail()
		}
		return
	}
	for _, e := range entries {
		if !gitHookNames[e.Name()] {
			continue
		}
		st, err := os.Stat(filepath.Join(hooks, e.Name()))
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				arms.fail()
			}
			continue
		}
		if !st.IsDir() && st.Mode().Perm()&0o111 != 0 {
			arms.kinds |= armHooks
		}
	}
}

// scanGitConfigFile parses a git config file minimally: sections, keys and
// values, comments and line continuations. Includes are never followed; an
// include section marks the scan failed because the included file cannot be
// judged.
func scanGitConfigFile(path string, arms *gitRepoArms) {
	if path == "/dev/null" {
		return
	}
	data, err := readSmallFile(path, maxGitConfigBytes)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			arms.fail()
		}
		return
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	var section, sub string
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		for strings.HasSuffix(line, "\\") && i+1 < len(lines) {
			i++
			line = line[:len(line)-1] + lines[i]
		}
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			sec, subsec, rest, ok := parseGitConfigHeader(line)
			if !ok {
				arms.fail()
				return
			}
			section, sub = sec, subsec
			if section == "include" || section == "includeif" {
				arms.fail()
			}
			line = strings.TrimSpace(rest)
			if line == "" || line[0] == '#' || line[0] == ';' {
				continue
			}
		}
		if section == "" {
			arms.fail()
			return
		}
		end := strings.IndexAny(line, "= \t")
		if end <= 0 {
			arms.fail()
			return
		}
		key := strings.ToLower(line[:end])
		value := "true"
		if eq := strings.IndexByte(line, '='); eq >= 0 {
			value = gitConfigValue(line[eq+1:])
		}
		name := section
		if sub != "" {
			name += "." + strings.ToLower(sub)
		}
		arms.record(name+"."+key, value)
	}
}

func parseGitConfigHeader(line string) (section, sub, rest string, ok bool) {
	inQuote := false
	end := -1
	for i := 1; i < len(line) && end < 0; i++ {
		switch c := line[i]; {
		case c == '\\' && inQuote:
			i++
		case c == '"':
			inQuote = !inQuote
		case c == ']' && !inQuote:
			end = i
		}
	}
	if end < 0 {
		return "", "", "", false
	}
	inner := strings.TrimSpace(line[1:end])
	rest = line[end+1:]
	if k := strings.IndexAny(inner, " \t\""); k >= 0 {
		section = inner[:k]
		sub = strings.TrimSpace(inner[k:])
		sub = strings.Trim(sub, `"`)
	} else if k := strings.IndexByte(inner, '.'); k >= 0 {
		section, sub = inner[:k], inner[k+1:]
	} else {
		section = inner
	}
	section = strings.ToLower(section)
	return section, sub, rest, section != ""
}

// gitConfigValue extracts the value text: quotes removed, escapes decoded,
// trailing comment dropped.
func gitConfigValue(raw string) string {
	var b strings.Builder
	inQuote := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c == '\\' && i+1 < len(raw):
			i++
			switch raw[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'b':
				b.WriteByte('\b')
			default:
				b.WriteByte(raw[i])
			}
		case c == '"':
			inQuote = !inQuote
		case !inQuote && (c == '#' || c == ';'):
			return strings.TrimSpace(b.String())
		default:
			b.WriteByte(c)
		}
	}
	return strings.TrimSpace(b.String())
}

// gitFilterIsLFS reports whether a filter command is the plain git-lfs
// invocation; those filters are inert for hostile repository content.
func gitFilterIsLFS(value string) bool {
	v := strings.TrimSpace(value)
	if !strings.HasPrefix(v, "git-lfs ") && !strings.HasPrefix(v, "git lfs ") {
		return false
	}
	return !strings.ContainsAny(v, ";&|`$<>()\n\\")
}

var gitBooleanValues = map[string]bool{
	"true": true, "false": true, "yes": true, "no": true, "on": true, "off": true, "0": true, "1": true,
}

// record classifies one config assignment (name is lower-cased
// section[.subsection].key) by what it can make git run.
func (a *gitRepoArms) record(name, value string) {
	switch {
	case strings.HasPrefix(name, "include.") || strings.HasPrefix(name, "includeif."):
		a.fail()
	case name == "core.hookspath":
		if value != "" && value != "/dev/null" {
			a.kinds |= armHooks
		}
	case strings.HasPrefix(name, "hook.") && strings.HasSuffix(name, ".command"):
		if value != "" {
			a.kinds |= armHooks
		}
	case name == "core.fsmonitor":
		if value != "" && !gitBooleanValues[strings.ToLower(value)] {
			a.kinds |= armFsmonitor
		}
	case name == "diff.external":
		if value != "" {
			a.kinds |= armDiffExternal
		}
	case strings.HasPrefix(name, "diff.") && strings.HasSuffix(name, ".command"):
		if value != "" {
			a.kinds |= armDiffExternal
		}
	case strings.HasPrefix(name, "diff.") && strings.HasSuffix(name, ".textconv"):
		if value != "" {
			a.kinds |= armTextconv
		}
	case strings.HasPrefix(name, "merge.") && strings.HasSuffix(name, ".driver"):
		if value != "" {
			a.kinds |= armMergeDriver
		}
	case strings.HasPrefix(name, "filter.") && (strings.HasSuffix(name, ".clean") ||
		strings.HasSuffix(name, ".smudge") || strings.HasSuffix(name, ".process")):
		if value != "" && !gitFilterIsLFS(value) {
			a.kinds |= armFilter
		}
	case name == "core.editor":
		if value != "" {
			a.kinds |= armEditor
		}
	case name == "sequence.editor":
		if value != "" {
			a.kinds |= armSeqEditor
		}
	case name == "interactive.difffilter":
		if value != "" {
			a.kinds |= armInteractive
		}
	case strings.HasPrefix(name, "submodule.") && strings.HasSuffix(name, ".update"):
		if strings.HasPrefix(value, "!") {
			a.kinds |= armSubmoduleExec
		}
	}
}

// scanGitDirectory records the arming state held under one git directory:
// hooks and config of the common directory, per-worktree config, and the
// repositories of any cloned submodules.
func scanGitDirectory(gitDir string, depth int, arms *gitRepoArms) {
	common := gitDir
	data, err := readSmallFile(filepath.Join(gitDir, "commondir"), maxGitfileBytes)
	switch {
	case err == nil:
		c := strings.TrimSpace(string(data))
		if c == "" {
			arms.fail()
			return
		}
		if !filepath.IsAbs(c) {
			c = filepath.Join(gitDir, c)
		}
		common = filepath.Clean(c)
	case !errors.Is(err, fs.ErrNotExist):
		arms.fail()
		return
	}
	if st, err := os.Stat(common); err != nil || !st.IsDir() {
		arms.fail()
		return
	}
	dirs := []string{common}
	if gitDir != common {
		dirs = append(dirs, gitDir)
	}
	for _, d := range dirs {
		scanGitHooks(d, arms)
		scanGitConfigFile(filepath.Join(d, "config"), arms)
		scanGitConfigFile(filepath.Join(d, "config.worktree"), arms)
	}
	if depth >= maxGitModuleDepth {
		if _, err := os.Stat(filepath.Join(common, "modules")); err == nil {
			arms.fail()
		}
		return
	}
	budget := maxGitModuleDirs
	for _, d := range dirs {
		scanGitModules(filepath.Join(d, "modules"), depth, &budget, arms)
	}
}

// scanGitModules visits the git directories of cloned submodules. A submodule
// name containing a slash nests its directory, so entries without a HEAD are
// descended into.
func scanGitModules(dir string, depth int, budget *int, arms *gitRepoArms) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			arms.fail()
		}
		return
	}
	for _, e := range entries {
		if *budget--; *budget < 0 {
			arms.fail()
			return
		}
		p := filepath.Join(dir, e.Name())
		st, err := os.Stat(p)
		if err != nil {
			arms.fail()
			return
		}
		if !st.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(p, "HEAD")); err == nil {
			scanGitDirectory(p, depth+1, arms)
		} else if errors.Is(err, fs.ErrNotExist) {
			scanGitModules(p, depth, budget, arms)
		} else {
			arms.fail()
			return
		}
	}
}

// scanGitUserConfig records the system, global and process-environment
// configuration that applies to every repository.
func scanGitUserConfig(arms *gitRepoArms) {
	for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_EXEC_PATH", "GIT_CONFIG_PARAMETERS"} {
		if os.Getenv(name) != "" {
			arms.fail()
		}
	}
	if v := os.Getenv("GIT_EXTERNAL_DIFF"); v != "" {
		arms.kinds |= armDiffExternal
	}
	if v := os.Getenv("GIT_EDITOR"); v != "" && !gitNeutralEditor(v) {
		arms.kinds |= armEditor
	}
	if v := os.Getenv("GIT_SEQUENCE_EDITOR"); v != "" && !gitNeutralEditor(v) {
		arms.kinds |= armSeqEditor
	}
	if count := os.Getenv("GIT_CONFIG_COUNT"); count != "" {
		n, err := strconv.Atoi(count)
		if err != nil || n < 0 || n > 256 {
			arms.fail()
		} else {
			for i := 0; i < n; i++ {
				key, okKey := os.LookupEnv("GIT_CONFIG_KEY_" + strconv.Itoa(i))
				val, okVal := os.LookupEnv("GIT_CONFIG_VALUE_" + strconv.Itoa(i))
				if !okKey || !okVal {
					arms.fail()
					continue
				}
				arms.record(strings.ToLower(key), val)
			}
		}
	}
	if !gitEnvTrue(os.Getenv("GIT_CONFIG_NOSYSTEM")) {
		if v, ok := os.LookupEnv("GIT_CONFIG_SYSTEM"); ok && v != "" {
			scanGitConfigFile(v, arms)
		} else {
			scanGitConfigFile(gitSystemConfigPath, arms)
		}
	}
	if v, ok := os.LookupEnv("GIT_CONFIG_GLOBAL"); ok && v != "" {
		scanGitConfigFile(v, arms)
		return
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		arms.fail()
		return
	}
	scanGitConfigFile(filepath.Join(home, ".gitconfig"), arms)
	scanGitConfigFile(filepath.Join(home, ".config", "git", "config"), arms)
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		scanGitConfigFile(filepath.Join(xdg, "git", "config"), arms)
	}
}

func gitEnvTrue(v string) bool {
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// gitNeutralEditor reports editor values that do nothing (":" and true).
func gitNeutralEditor(v string) bool {
	switch strings.TrimSpace(v) {
	case ":", "true", "/bin/true", "/usr/bin/true":
		return true
	}
	return false
}

// repoArms resolves the invocation's repository and scans it.
func (c *gitRepoCtx) repoArms(o gitGlobalOpts) (gitRepoArms, gitRepoLookup) {
	var arms gitRepoArms
	gitDir, lookup := c.locateGitDir(o)
	if lookup != gitRepoFound {
		return arms, lookup
	}
	scanGitDirectory(gitDir, 0, &arms)
	scanGitUserConfig(&arms)
	return arms, gitRepoFound
}

// gitLongOpt reports whether tok is --full or an abbreviation of it of at
// least minLen characters (git accepts any unambiguous prefix), with or
// without =value.
func gitLongOpt(tok, full string, minLen int) bool {
	if !strings.HasPrefix(tok, "--") {
		return false
	}
	name, _, _ := strings.Cut(tok[2:], "=")
	return len(name) >= minLen && strings.HasPrefix(full, name)
}

func anyGitLongOpt(args []string, full string, minLen int) bool {
	for _, a := range args {
		if a == "--" {
			break
		}
		if gitLongOpt(a, full, minLen) {
			return true
		}
	}
	return false
}

// shortClusterHas reports whether any short-option cluster in args contains
// one of letters.
func shortClusterHas(args []string, letters string) bool {
	for _, a := range args {
		if a == "--" {
			break
		}
		if isShortFlagToken(a) && strings.ContainsAny(a[1:], letters) {
			return true
		}
	}
	return false
}

// builtinMergeStrategies are the strategies implemented inside git; any other
// -s value is run as an external git-merge-<name> program.
var builtinMergeStrategies = map[string]bool{
	"ort": true, "recursive": true, "resolve": true, "octopus": true, "ours": true, "subtree": true,
}

// gitStrategyRunsProgram reports whether a merge/rebase/cherry-pick strategy
// option names an external strategy program.
func gitStrategyRunsProgram(sub string, args []string) bool {
	// cherry-pick has no short strategy option: its -s is --signoff.
	short := sub != "cherry-pick"
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		var val string
		switch {
		case (short && a == "-s") || (strings.HasPrefix(a, "--") && gitLongOpt(a, "strategy", 3) && !strings.Contains(a, "=")):
			if i+1 >= len(args) {
				return true
			}
			i++
			val = args[i]
		case strings.HasPrefix(a, "--") && gitLongOpt(a, "strategy", 3):
			_, val, _ = strings.Cut(a, "=")
		case short && isShortFlagToken(a) && strings.HasPrefix(a, "-s") && len(a) > 2:
			val = a[2:]
		default:
			continue
		}
		if !builtinMergeStrategies[val] {
			return true
		}
	}
	return false
}

// gitOpensEditor reports whether the verb launches the configured editor.
func gitOpensEditor(sub string, args []string) (editor, sequence bool) {
	switch sub {
	case "commit":
		return commitOpensEditor(args), false
	case "merge":
		for _, a := range args {
			if a == "--" {
				break
			}
			if a == "-e" || gitLongOpt(a, "edit", 3) {
				return true, false
			}
		}
		if anyGitLongOpt(args, "no-edit", 5) || anyGitLongOpt(args, "ff-only", 4) ||
			anyGitLongOpt(args, "squash", 3) || anyGitLongOpt(args, "abort", 3) ||
			anyGitLongOpt(args, "quit", 3) || anyGitLongOpt(args, "no-commit", 6) {
			return false, false
		}
		return true, false
	case "rebase":
		if shortClusterHas(args, "i") || anyGitLongOpt(args, "interactive", 3) ||
			anyGitLongOpt(args, "edit-todo", 3) || anyGitLongOpt(args, "continue", 3) {
			return true, true
		}
	case "cherry-pick":
		if shortClusterHas(args, "e") || anyGitLongOpt(args, "edit", 3) || anyGitLongOpt(args, "continue", 3) {
			return true, false
		}
	}
	return false, false
}

// commitOpensEditor reports whether `git commit` asks for a message in an
// editor: it does unless a message source (-m, -F, -C, --no-edit, --fixup)
// is given, and always with -e/--edit, -c or -t.
func commitOpensEditor(args []string) bool {
	given, force := false, false
	for _, a := range args {
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "--") {
			name, val, _ := strings.Cut(a[2:], "=")
			switch {
			case len(name) >= 3 && strings.HasPrefix("message", name),
				len(name) >= 3 && strings.HasPrefix("file", name),
				len(name) >= 3 && strings.HasPrefix("reuse-message", name),
				len(name) >= 5 && strings.HasPrefix("no-edit", name):
				given = true
			case len(name) >= 3 && strings.HasPrefix("fixup", name):
				if !strings.HasPrefix(val, "amend:") && !strings.HasPrefix(val, "reword:") {
					given = true
				}
			case len(name) >= 3 && strings.HasPrefix("edit", name),
				len(name) >= 3 && strings.HasPrefix("reedit-message", name),
				len(name) >= 3 && strings.HasPrefix("template", name):
				force = true
			}
			continue
		}
		if !isShortFlagToken(a) {
			continue
		}
		for _, c := range a[1:] {
			switch c {
			case 'm', 'F', 'C':
				given = true
			case 'e', 'c', 't':
				force = true
			}
			if strings.ContainsRune("mFCctuS", c) {
				break
			}
		}
	}
	return force || !given
}

// gitVerbRunsRepoCode decides, for one of the verbs in gitVerbArmMask, whether
// the repository the command targets can make git spawn a program.
func gitVerbRunsRepoCode(sub string, args, tokens []string, ctx *gitRepoCtx) bool {
	mask, ok := gitVerbArmMask[sub]
	if !ok {
		return true
	}
	o := parseGitGlobalOpts(tokens)
	if o.forced {
		return true
	}
	readOnly := sub == "log" || sub == "show"
	switch sub {
	case "merge", "rebase", "cherry-pick":
		if gitStrategyRunsProgram(sub, args) {
			return true
		}
	}
	if sub == "rebase" && (shortClusterHas(args, "x") || anyGitLongOpt(args, "exec", 3)) {
		return true
	}
	if editor, seq := gitOpensEditor(sub, args); editor {
		mask |= armEditor
		if seq {
			mask |= armSeqEditor
		}
	}
	if sub == "diff" || readOnly || sub == "stash" {
		if hasAny(args, "--no-ext-diff") {
			mask &^= armDiffExternal
		}
		if hasAny(args, "--no-textconv") {
			mask &^= armTextconv
		}
	}
	arms, lookup := ctx.repoArms(o)
	if lookup != gitRepoFound {
		// Read-only history verbs never needed a repository to be safe.
		return !readOnly || lookup == gitRepoFailed && ctx != nil && ctx.known
	}
	return arms.has(mask)
}
