package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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
// refused (the caller cannot tell which copy was reviewed); promoteSkillAt
// takes the reviewed file's path and content hash instead.
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

// promoteSkillAt is promoteSkill bound to a reviewed copy. A non-empty
// reviewedPath must be the SKILL.md path the review showed (Source.Path in
// GET /api/skills?name=), and a non-empty reviewedSHA256 the hash of the
// file content the review showed; the promotion is refused when the copy at
// that path is gone or its content changed since.
func promoteSkillAt(userDir, name, reviewedPath, reviewedSHA256 string, force bool) error {
	cleaned := filepath.Clean(name)
	if name == "" || cleaned == "." || cleaned == ".." || cleaned != filepath.Base(cleaned) || strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("promote: invalid skill name %q", name)
	}
	var candidates []promoteCandidate
	for _, base := range []struct {
		dir     string
		project bool
	}{{skills.ProjectSkillsDir(), true}, {userDir, false}} {
		candidates = append(candidates, skillPromoteCandidates(base.dir, name, base.project)...)
	}

	var c promoteCandidate
	switch {
	case reviewedPath != "":
		want := filepath.Clean(reviewedPath)
		found := false
		for _, cand := range candidates {
			if filepath.Clean(cand.path()) == want {
				c, found = cand, true
				break
			}
		}
		if !found {
			return fmt.Errorf("promote: no skill %q at %s (it was moved, renamed or removed since the review)", name, danger.SanitizeInline(reviewedPath))
		}
	case len(candidates) == 0:
		return fmt.Errorf("promote: skill %q not found in %s or %s", name, userDir, skills.ProjectSkillsDir())
	case len(candidates) > 1:
		paths := make([]string, 0, len(candidates))
		for _, cand := range candidates {
			paths = append(paths, danger.SanitizeInline(cand.path()))
		}
		return fmt.Errorf("promote: %d skill files are named %q (%s); refusing to guess which one was reviewed — remove or rename the copies you do not want",
			len(candidates), name, strings.Join(paths, ", "))
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
		if s == nil || s.Name != name {
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
