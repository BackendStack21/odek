package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BackendStack21/odek/internal/skills"
)

func findSkill(res *skills.ScanResult, name string) *skills.Skill {
	for i := range res.AutoLoad {
		if res.AutoLoad[i].Name == name {
			return &res.AutoLoad[i]
		}
	}
	for i := range res.Lazy {
		if res.Lazy[i].Name == name {
			return &res.Lazy[i]
		}
	}
	return nil
}

// `odek skill promote` on a project-dir skill was a persistent no-op: the
// command only touched the (attacker-controllable) SKILL.md frontmatter —
// and for unmarked skills it early-exited as "already trusted" — while
// every rescan unconditionally re-pins project skills via markProjectSkill.
// The fix records the promoted content hash in the TRUSTED user dir; a
// project skill stays trusted only while its file content matches the
// recorded hash (any edit re-locks it).
func TestPromoteSkill_ProjectDirSurvivesRescan(t *testing.T) {
	root := t.TempDir()
	projDir := filepath.Join(root, ".odek", "skills")
	userDir := filepath.Join(t.TempDir(), "user", "skills")
	for _, d := range []string{projDir, userDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	skillPath := filepath.Join(projDir, "repo-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillPath, []byte("---\nname: repo-skill\ndescription: A shipped skill.\n---\n\nBody.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// promoteSkill resolves project skills via ./.odek/skills (cwd-relative).
	t.Chdir(root)

	// Before promotion: pinned.
	res := skills.ScanDirs(projDir, userDir, nil)
	if s := findSkill(res, "repo-skill"); s == nil || !s.Provenance.NeedsReview {
		t.Fatalf("precondition: project skill must start NeedsReview=true, got %+v", s)
	}

	if err := promoteSkill(userDir, "repo-skill", true); err != nil {
		t.Fatalf("promoteSkill: %v", err)
	}

	// After promotion, a rescan must keep the skill trusted.
	res = skills.ScanDirs(projDir, userDir, nil)
	s := findSkill(res, "repo-skill")
	if s == nil {
		t.Fatal("skill vanished after promotion")
	}
	if s.Provenance.NeedsReview {
		t.Fatal("promotion did not survive a rescan — project-skill promote is a persistent no-op")
	}
}

// Editing the promoted skill's body re-locks it: the recorded hash no
// longer matches.
func TestPromoteSkill_ProjectDirEditsReLock(t *testing.T) {
	root := t.TempDir()
	projDir := filepath.Join(root, ".odek", "skills")
	userDir := filepath.Join(t.TempDir(), "user", "skills")
	for _, d := range []string{projDir, userDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	skillPath := filepath.Join(projDir, "repo-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillPath, []byte("---\nname: repo-skill\ndescription: A shipped skill.\n---\n\nBody.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	if err := promoteSkill(userDir, "repo-skill", true); err != nil {
		t.Fatalf("promoteSkill: %v", err)
	}

	// Attacker (or accident) edits the body after promotion.
	if err := os.WriteFile(skillPath, []byte("---\nname: repo-skill\ndescription: A shipped skill.\n---\n\nMalicious body.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	res := skills.ScanDirs(projDir, userDir, nil)
	if s := findSkill(res, "repo-skill"); s == nil || !s.Provenance.NeedsReview {
		t.Fatalf("edited promoted skill must re-lock to NeedsReview=true, got %+v", s)
	}
}
