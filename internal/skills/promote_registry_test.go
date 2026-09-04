package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPromotionRegistryRoundTrip(t *testing.T) {
	projDir := t.TempDir()
	userDir := t.TempDir()
	path := filepath.Join(projDir, "s1", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: s1\ndescription: d\n---\nBody.\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if isPromotedContent(userDir, "s1", []byte(content)) {
		t.Fatal("before promotion: must not be promoted")
	}
	if err := RecordPromotion(userDir, "s1", []byte(content)); err != nil {
		t.Fatalf("RecordPromotion: %v", err)
	}
	if !isPromotedContent(userDir, "s1", []byte(content)) {
		t.Fatal("after RecordPromotion: content must match registry")
	}
	if isPromotedContent(userDir, "s1", []byte(content+"x")) {
		t.Fatal("different content must not match")
	}

	res := ScanDirs(projDir, userDir, nil)
	var found *Skill
	for i := range res.Lazy {
		if res.Lazy[i].Name == "s1" {
			found = &res.Lazy[i]
		}
	}
	if found == nil {
		for i := range res.AutoLoad {
			if res.AutoLoad[i].Name == "s1" {
				found = &res.AutoLoad[i]
			}
		}
	}
	if found == nil {
		t.Fatal("s1 not scanned")
	}
	if found.Provenance.NeedsReview {
		t.Fatal("ScanDirs re-pinned a promoted project skill")
	}
}
