package skills

// Bug-hunt v3 (fix/bughunt-v3) RED test — promote gate honored in the
// cached scan path.
//
// ScanDirs (loader.go) honors the trusted promotion registry: a project-dir
// skill whose exact content hash was recorded via RecordPromotion stays
// trusted across rescans. The cached twin scanDirsCached (used by
// SkillManager reloads) re-pins project skills unconditionally, making
// `odek skill promote` a persistent no-op for long-lived managers: the
// skill stays NeedsReview forever, excluded from trigger matching and
// refused by skill_load — the operator's promotion silently does nothing.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanDirsCached_HonorsPromotion(t *testing.T) {
	root := t.TempDir()
	projDir := filepath.Join(root, "proj", "skills")
	userDir := filepath.Join(root, "user", "skills")
	for _, d := range []string{projDir, userDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	content := []byte("---\nname: repo-skill\ndescription: A shipped skill.\n---\n\nBody.\n")
	skillPath := filepath.Join(projDir, "repo-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	// Control (parity with the uncached path): before promotion, pinned.
	fc := fileCache{}
	prev := map[string]Skill{}
	res := scanDirsCached(projDir, userDir, nil, fc, prev)
	if s := findCached(res, "repo-skill"); s == nil || !s.Provenance.NeedsReview {
		t.Fatalf("precondition: unpromoted project skill must be NeedsReview, got %+v", s)
	}

	// Operator promotes this exact content in the trusted registry.
	if err := RecordPromotion(userDir, "repo-skill", content); err != nil {
		t.Fatal(err)
	}

	// Cached rescan (SkillManager reload path) must honor the promotion.
	res2 := scanDirsCached(projDir, userDir, nil, fc, prev)
	if s := findCached(res2, "repo-skill"); s == nil {
		t.Fatal("promoted skill missing from cached rescan")
	} else if s.Provenance.NeedsReview {
		t.Fatal("promoted project skill still NeedsReview in the cached scan path — `odek skill promote` is a persistent no-op for SkillManager reloads")
	}

	// Any content edit re-locks it (hash anchor semantics, same as ScanDirs).
	if err := os.WriteFile(skillPath, append(content, []byte("\nedited.\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	// Ensure the mtime cache sees the change.
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(skillPath, future, future); err != nil {
		t.Fatal(err)
	}
	res3 := scanDirsCached(projDir, userDir, nil, fc, prev)
	if s := findCached(res3, "repo-skill"); s == nil || !s.Provenance.NeedsReview {
		t.Fatalf("edited skill must re-lock to NeedsReview, got %+v", s)
	}
}

func findCached(res *ScanResult, name string) *Skill {
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
