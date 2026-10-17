package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/skills"
)

// promoteSkill clears Provenance.NeedsReview on the skill named name (its
// frontmatter name — the name `odek skill list` and the WebUI show), allowing
// it to be auto-loaded. Both the user skills dir and the project skills dir
// (./.odek/skills) are searched: project-dir skills are forced NeedsReview at
// scan time (see ScanDirs), so they must be promotable too. The skill body is
// left unchanged. The user is expected to have read the body before invoking
// this command; we do not enforce a confirmation prompt because shipping a
// noisy mandatory `--yes` flag teaches users to type --yes by reflex.
//
// The skill is identified by its frontmatter name, never by its directory
// name: a repository controls both, so a directory named like one skill can
// hold another. When more than one SKILL.md carries the name, promotion is
// refused whatever their states (the caller cannot tell which copy was
// reviewed) and the error lists every copy; promoteSkillAt takes the reviewed
// file's path (`odek skill promote --path`) and content hash instead.
//
// If the skill's provenance shows it originated from untrusted content
// (Untrusted=true or non-empty Sources), promotion is refused unless
// force is true. This prevents a prompt-injection-derived skill from
// being accidentally auto-loaded after a cursory review. A project-dir
// skill always counts as sourced ("project"), whatever its frontmatter
// says, so it too requires force.
func promoteSkill(userDir, name string, force bool) error {
	return promoteSkillAt(userDir, name, "", "", force)
}

// promoteCandidate is one SKILL.md whose frontmatter name matches the skill
// being promoted.
type promoteCandidate struct {
	baseDir     string // skills dir holding it
	entry       string // its directory under baseDir
	data        []byte // the file content parsed into skill
	skill       *skills.Skill
	fromProject bool
}

func (c promoteCandidate) path() string {
	return filepath.Join(c.baseDir, c.entry, "SKILL.md")
}

// needsPromotion reports whether the copy is pending review as the scanner
// sees it: a project copy unless this exact content was already promoted,
// an operator-dir copy when its provenance flags it.
func (c promoteCandidate) needsPromotion(userDir string) bool {
	if c.fromProject {
		return !skills.IsPromotedContent(userDir, c.skill.Name, c.data)
	}
	return c.skill.Provenance.NeedsReview || c.skill.Provenance.Untrusted
}

// promoteCandidateState names a candidate's review state for operator messages.
func (c promoteCandidate) state(userDir string) string {
	if c.needsPromotion(userDir) {
		return "pending review"
	}
	return "trusted"
}

// skillCandidatesAll lists the candidates for name (every skill when name is
// empty) in the project then the user skills dir, the scanner's order.
func skillCandidatesAll(userDir, name string) []promoteCandidate {
	var out []promoteCandidate
	for _, base := range []struct {
		dir     string
		project bool
	}{{skills.ProjectSkillsDir(), true}, {userDir, false}} {
		out = append(out, skillPromoteCandidates(base.dir, name, base.project)...)
	}
	return out
}

// skillCandidateAtPath returns the candidate whose SKILL.md is the file at
// path. Only files the candidate scan accepted qualify: a SKILL.md directly
// in a skill directory of the project or user skills dir, neither the
// directory nor the file a symlink. The given path may be relative or
// absolute and is matched lexically against the candidates' absolute paths;
// the caller-supplied path is never opened or stat'ed itself.
func skillCandidateAtPath(candidates []promoteCandidate, path string) (promoteCandidate, bool) {
	want, err := filepath.Abs(path)
	if err != nil {
		return promoteCandidate{}, false
	}
	for _, cand := range candidates {
		got, err := filepath.Abs(cand.path())
		if err == nil && got == want {
			return cand, true
		}
	}
	return promoteCandidate{}, false
}

// skillCopiesError lists every SKILL.md carrying a name with its state and
// how to inspect and promote one of them by path, after the lead line.
func skillCopiesError(lead, userDir string, candidates []promoteCandidate) error {
	var b strings.Builder
	b.WriteString(lead)
	for _, cand := range candidates {
		fmt.Fprintf(&b, "\n  %s (%s)", danger.SanitizeInline(cand.path()), cand.state(userDir))
	}
	b.WriteString("\ninspect one with `odek skill view --path <file>` and promote it with `odek skill promote --path <file> [--force]`")
	return errors.New(b.String())
}

// promoteSkillAt is promoteSkill bound to a reviewed copy. A non-empty
// reviewedPath must be the SKILL.md the review showed (Source.Path in
// GET /api/skills?name=, or `odek skill promote --path`); name may then be
// empty, and otherwise must be that file's frontmatter name. A non-empty
// reviewedSHA256 is the hash of the file content the review showed. The
// promotion is refused when the copy at that path is gone or its content
// changed since.
func promoteSkillAt(userDir, name, reviewedPath, reviewedSHA256 string, force bool) error {
	if name != "" || reviewedPath == "" {
		cleaned := filepath.Clean(name)
		if name == "" || cleaned == "." || cleaned == ".." || cleaned != filepath.Base(cleaned) || strings.ContainsAny(name, "/\\") {
			return fmt.Errorf("promote: invalid skill name %q", name)
		}
	}
	candidates := skillCandidatesAll(userDir, name)

	var c promoteCandidate
	switch {
	case reviewedPath != "":
		var found bool
		c, found = skillCandidateAtPath(candidates, reviewedPath)
		if !found {
			if name == "" {
				return fmt.Errorf("promote: %s is not a SKILL.md in %s or %s (or it is a symlink, or was moved or removed)", danger.SanitizeInline(reviewedPath), userDir, skills.ProjectSkillsDir())
			}
			return fmt.Errorf("promote: no skill %q at %s (it was moved, renamed or removed since the review)", name, danger.SanitizeInline(reviewedPath))
		}
		name = c.skill.Name
	case len(candidates) == 0:
		return fmt.Errorf("promote: skill %q not found in %s or %s", name, userDir, skills.ProjectSkillsDir())
	case len(candidates) > 1:
		return skillCopiesError(fmt.Sprintf("promote: %d skill files are named %q; refusing to guess which one you reviewed:", len(candidates), name), userDir, candidates)
	default:
		c = candidates[0]
	}
	if reviewedSHA256 != "" {
		sum := sha256.Sum256(c.data)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), strings.TrimSpace(reviewedSHA256)) {
			return fmt.Errorf("promote: skill %q changed since it was reviewed; review it again", name)
		}
	}

	// Every filesystem access below goes through an os.Root anchored at the
	// skills directory, so the entry can never resolve outside it and a
	// symlinked skill dir or SKILL.md (a repository can plant one) is refused
	// rather than followed by the in-place rewrite.
	root, err := os.OpenRoot(c.baseDir)
	if err != nil {
		return fmt.Errorf("promote: open skills dir: %w", err)
	}
	defer root.Close()
	rel := filepath.Join(c.entry, "SKILL.md")
	for _, p := range []string{c.entry, rel} {
		if fi, err := root.Lstat(p); err != nil || fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("promote: %s is a symlink or unreadable; refusing to follow it", filepath.Join(c.baseDir, p))
		}
	}
	if fi, err := root.Lstat(rel); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("promote: %s is not a regular file", filepath.Join(c.baseDir, rel))
	}

	loaded := c.skill
	if c.fromProject {
		// The candidate was parsed from a copy outside the project dir, so
		// the scan-time project pin is not applied; apply it here. Otherwise
		// a project SKILL.md that simply omits provenance frontmatter would
		// carry no Sources and skip the --force gate below.
		loaded.Provenance.NeedsReview = true
		if !slices.Contains(loaded.Provenance.Sources, "project") {
			loaded.Provenance.Sources = append(slices.Clone(loaded.Provenance.Sources), "project")
		}
	}
	if !loaded.Provenance.NeedsReview && !c.fromProject {
		fmt.Fprintf(os.Stderr, "odek: skill %q is already trusted (NeedsReview=false)\n", name)
		return nil
	}

	// Refuse to promote tainted skills without explicit --force. The user
	// must have reviewed the body and decided the external source(s) are
	// safe to embed as loaded context.
	if loaded.Provenance.Untrusted || len(loaded.Provenance.Sources) > 0 {
		if !force {
			return fmt.Errorf("promote: refusing to promote tainted skill %q (sources: %s); review the body and use --force if you accept the risk",
				name, joinSources(loaded.Provenance.Sources))
		}
		fmt.Fprintf(os.Stderr, "odek: warning: promoting tainted skill %q (sources: %s); audit trail retained\n",
			name, joinSources(loaded.Provenance.Sources))
	}

	// Clear the provenance flags but keep Sources so the audit trail
	// (where the skill originated) is preserved on disk.
	loaded.Provenance.NeedsReview = false
	loaded.Provenance.Untrusted = false

	// MarshalSkill drops blank Provenance entirely.
	content := skills.MarshalSkill(*loaded)
	f, err := root.OpenFile(rel, os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("promote: write: %w", err)
	}
	// The file is opened before it is rewritten and its current content
	// compared with what was matched (and, when bound, reviewed): a swap in
	// between promotes nothing.
	current, rerr := io.ReadAll(io.LimitReader(f, skills.MaxSkillFileBytes+1))
	if rerr != nil || !bytes.Equal(current, c.data) {
		f.Close()
		return fmt.Errorf("promote: skill %q changed while it was being promoted; review it again", name)
	}
	werr := f.Truncate(0)
	if werr == nil {
		_, werr = f.WriteAt([]byte(content), 0)
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("promote: write: %w", werr)
	}
	// Project-dir skills: trust is anchored in the trusted user-dir
	// registry (content hash), not the project frontmatter — the project
	// dir is attacker-controllable and every rescan would otherwise re-pin
	// the skill, silently reverting this promotion.
	if c.fromProject {
		if err := skills.RecordPromotion(userDir, loaded.Name, []byte(content)); err != nil {
			return fmt.Errorf("promote: record promotion: %w", err)
		}
	}
	fmt.Printf("odek: promoted skill %q — NeedsReview cleared\n", name)
	return nil
}

// skillPromoteCandidates lists the SKILL.md files under dir whose frontmatter
// name is name. Symlinked skill dirs and SKILL.md files are skipped, as the
// scanner skips them; reads go through an os.Root anchored at dir.
func skillPromoteCandidates(dir, name string, project bool) []promoteCandidate {
	if dir == "" {
		return nil
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil
	}
	var out []promoteCandidate
	for _, e := range entries {
		if !e.IsDir() || e.Type()&os.ModeSymlink != 0 {
			continue
		}
		rel := filepath.Join(e.Name(), "SKILL.md")
		fi, err := root.Lstat(rel)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() > skills.MaxSkillFileBytes {
			continue
		}
		data, err := root.ReadFile(rel)
		if err != nil {
			continue
		}
		s := parseSkillBytes(e.Name(), data)
		if s == nil || (name != "" && s.Name != name) {
			continue
		}
		out = append(out, promoteCandidate{baseDir: dir, entry: e.Name(), data: data, skill: s, fromProject: project})
	}
	return out
}

// joinSources formats a source list for human-readable messages.
func joinSources(srcs []string) string {
	if len(srcs) == 0 {
		return "unknown"
	}
	return strings.Join(srcs, ", ")
}

// scanSingleSkill loads exactly one SKILL.md by re-using the package
// scanner against a one-file directory. Returns nil on parse failure.
func scanSingleSkill(skillDir, path string) *skills.Skill {
	// scanDir is internal to the skills package; we instead read +
	// parse here via the public MarshalSkill path. As a minimal trick:
	// we use a temp dir containing only this file so ScanDirs returns
	// just this one skill.
	in, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return parseSkillBytes(filepath.Base(skillDir), in)
}

// parseSkillBytes parses one SKILL.md body as the scanner would for a skill
// directory named dirName. Returns nil on parse failure.
func parseSkillBytes(dirName string, in []byte) *skills.Skill {
	tmp, err := os.MkdirTemp("", "odek-promote-*")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(tmp)
	dstDir := filepath.Join(tmp, filepath.Base(dirName))
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return nil
	}
	if err := os.WriteFile(filepath.Join(dstDir, "SKILL.md"), in, 0644); err != nil {
		return nil
	}
	res := skills.ScanDirs("", tmp, nil)
	if res == nil {
		return nil
	}
	all := append([]skills.Skill{}, res.AutoLoad...)
	all = append(all, res.Lazy...)
	if len(all) != 1 {
		return nil
	}
	s := all[0]
	return &s
}

// skillPathFlag extracts `--path <file>` / `--path=<file>` from args and
// returns the remaining arguments.
func skillPathFlag(usage string, args []string) (path string, rest []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--path":
			if i+1 >= len(args) || args[i+1] == "" {
				return "", nil, fmt.Errorf("usage: %s (--path needs a SKILL.md file)", usage)
			}
			if path != "" {
				return "", nil, fmt.Errorf("usage: %s (--path given twice)", usage)
			}
			path = args[i+1]
			i++
		case strings.HasPrefix(a, "--path="):
			if path != "" || a == "--path=" {
				return "", nil, fmt.Errorf("usage: %s (--path needs one SKILL.md file)", usage)
			}
			path = strings.TrimPrefix(a, "--path=")
		default:
			rest = append(rest, a)
		}
	}
	return path, rest, nil
}

const (
	skillPromoteUsage = "odek skill promote <name> [--force] | odek skill promote --path <SKILL.md> [--force]"
	skillViewUsage    = "odek skill view <name> | odek skill view --path <SKILL.md>"
)

// skillPromoteCmd handles `odek skill promote`: by name when exactly one
// SKILL.md carries it, or by --path for one specific file.
func skillPromoteCmd(userDir string, args []string) error {
	path, rest, err := skillPathFlag(skillPromoteUsage, args)
	if err != nil {
		return err
	}
	name, force := "", false
	for _, a := range rest {
		switch {
		case a == "--force" || a == "-f":
			force = true
		case name == "" && path == "" && !strings.HasPrefix(a, "-"):
			name = a
		default:
			return fmt.Errorf("usage: %s (unexpected argument %q)", skillPromoteUsage, danger.SanitizeInline(a))
		}
	}
	if name == "" && path == "" {
		return fmt.Errorf("usage: %s", skillPromoteUsage)
	}
	return promoteSkillAt(userDir, name, path, "", force)
}

// skillViewAtPath prints one SKILL.md exactly as stored — the bytes a
// `promote --path` of it would act on — with its location and review state.
// The path is validated like a promote path: a SKILL.md of the project or
// user skills dir, no symlinks.
func skillViewAtPath(w io.Writer, userDir, path string) error {
	c, ok := skillCandidateAtPath(skillCandidatesAll(userDir, ""), path)
	if !ok {
		return fmt.Errorf("view: %s is not a SKILL.md in %s or %s (or it is a symlink)", danger.SanitizeInline(path), userDir, skills.ProjectSkillsDir())
	}
	fmt.Fprintf(w, "skill %s — %s (%s)\n\n", danger.SanitizeInline(c.skill.Name), danger.SanitizeInline(c.path()), c.state(userDir))
	fmt.Fprintln(w, danger.EscapeTerminalText(string(c.data)))
	return nil
}

// skillViewCmd handles `odek skill view`. By name it shows the copy the
// loader selects and, when other SKILL.md files carry the name, lists them
// on stderr; --path shows one specific file.
func skillViewCmd(userDir string, args []string, viewByName func(name string) error) error {
	path, rest, err := skillPathFlag(skillViewUsage, args)
	if err != nil {
		return err
	}
	if path != "" {
		if len(rest) != 0 {
			return fmt.Errorf("usage: %s", skillViewUsage)
		}
		return skillViewAtPath(os.Stdout, userDir, path)
	}
	if len(rest) != 1 || strings.HasPrefix(rest[0], "-") {
		return fmt.Errorf("usage: %s", skillViewUsage)
	}
	if err := viewByName(rest[0]); err != nil {
		return err
	}
	if cands := skillCandidatesAll(userDir, rest[0]); len(cands) > 1 {
		lead := fmt.Sprintf("odek: note: %d skill files are named %q; the one shown is the copy the loader selects:", len(cands), rest[0])
		fmt.Fprintln(os.Stderr, skillCopiesError(lead, userDir, cands).Error())
	}
	return nil
}
