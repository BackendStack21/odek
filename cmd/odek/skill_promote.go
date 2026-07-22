package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BackendStack21/odek/internal/skills"
)

// promoteSkill clears Provenance.NeedsReview on a skill stored under
// userDir, allowing it to be auto-loaded. If the skill is not in userDir,
// the project skills dir (./.odek/skills) is searched as a fallback —
// project-dir skills are forced NeedsReview at scan time (see ScanDirs),
// so they must be promotable too. The skill body is left
// unchanged. The user is expected to have read the body before
// invoking this command; we do not enforce a confirmation prompt
// because shipping a noisy mandatory `--yes` flag teaches users to
// type --yes by reflex.
//
// If the skill's provenance shows it originated from untrusted content
// (Untrusted=true or non-empty Sources), promotion is refused unless
// force is true. This prevents a prompt-injection-derived skill from
// being accidentally auto-loaded after a cursory review.
func promoteSkill(userDir, name string, force bool) error {
	skillDir := filepath.Join(userDir, name)
	path := filepath.Join(skillDir, "SKILL.md")
	if _, err := os.Stat(path); err != nil {
		projDir := filepath.Join(skills.ProjectSkillsDir(), name)
		projPath := filepath.Join(projDir, "SKILL.md")
		if _, perr := os.Stat(projPath); perr != nil {
			return fmt.Errorf("promote: skill %q not found at %s", name, path)
		}
		skillDir, path = projDir, projPath
	}

	// We re-parse via the scanner to keep all other fields intact, then
	// write back with the provenance fields cleared. scanSingleSkill
	// reads the file itself, so we don't need to read it twice here.
	loaded := scanSingleSkill(skillDir, path)
	if loaded == nil {
		return fmt.Errorf("promote: could not parse skill %q", name)
	}
	if !loaded.Provenance.NeedsReview {
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
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("promote: write: %w", err)
	}
	fmt.Printf("odek: promoted skill %q — NeedsReview cleared\n", name)
	return nil
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
	tmp, err := os.MkdirTemp("", "odek-promote-*")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(tmp)
	dstDir := filepath.Join(tmp, filepath.Base(skillDir))
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return nil
	}
	in, err := os.ReadFile(path)
	if err != nil {
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
