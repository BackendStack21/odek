package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTestSkill writes a SKILL.md with the given frontmatter and body into
// <userDir>/<name>/SKILL.md and returns the directory path.
func writeTestSkill(t *testing.T, userDir, name, frontmatter, body string) string {
	t.Helper()
	dir := filepath.Join(userDir, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	content := "---\n" + frontmatter + "---\n" + body
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	return dir
}

func TestPromoteSkill_NotFound(t *testing.T) {
	userDir := t.TempDir()
	err := promoteSkill(userDir, "nope", false)
	if err == nil {
		t.Fatal("expected error for missing skill")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, want 'not found'", err)
	}
}

func TestPromoteSkill_AlreadyTrusted(t *testing.T) {
	userDir := t.TempDir()
	writeTestSkill(t, userDir, "trusted",
		"name: trusted\ndescription: a trusted skill\n",
		"# body\n")

	// Already trusted (no provenance block) — promote should be a no-op.
	if err := promoteSkill(userDir, "trusted", false); err != nil {
		t.Fatalf("promote: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(userDir, "trusted", "SKILL.md"))
	if err != nil {
		t.Fatalf("read after promote: %v", err)
	}
	if strings.Contains(string(data), "needs_review") {
		t.Errorf("trusted skill should not gain needs_review flag, got:\n%s", data)
	}
}

func TestPromoteSkill_ClearsNeedsReview(t *testing.T) {
	userDir := t.TempDir()
	frontmatter := "name: review-me\n" +
		"description: a skill that only needs review\n" +
		"odek:\n" +
		"  provenance:\n" +
		"    needs_review: true\n"
	writeTestSkill(t, userDir, "review-me", frontmatter, "# body\n")

	// NeedsReview without untrusted sources can be promoted without --force.
	if err := promoteSkill(userDir, "review-me", false); err != nil {
		t.Fatalf("promote: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(userDir, "review-me", "SKILL.md"))
	if err != nil {
		t.Fatalf("read after promote: %v", err)
	}
	out := string(data)
	if strings.Contains(out, "needs_review: true") {
		t.Errorf("needs_review flag should be cleared, got:\n%s", out)
	}
}

func TestPromoteSkill_RefusesTaintedWithoutForce(t *testing.T) {
	userDir := t.TempDir()
	frontmatter := "name: tainted\n" +
		"description: an untrusted skill\n" +
		"odek:\n" +
		"  provenance:\n" +
		"    untrusted: true\n" +
		"    needs_review: true\n" +
		"    sources: browser\n"
	writeTestSkill(t, userDir, "tainted", frontmatter, "# body\n")

	err := promoteSkill(userDir, "tainted", false)
	if err == nil {
		t.Fatal("expected refusal for tainted skill without --force")
	}
	if !strings.Contains(err.Error(), "refusing to promote tainted skill") {
		t.Errorf("error = %v, want refusal message", err)
	}

	data, err := os.ReadFile(filepath.Join(userDir, "tainted", "SKILL.md"))
	if err != nil {
		t.Fatalf("read after refusal: %v", err)
	}
	if !strings.Contains(string(data), "needs_review: true") {
		t.Errorf("tainted skill should remain needs_review after refused promotion, got:\n%s", data)
	}
}

func TestPromoteSkill_AlreadyPromotedWithSourcesIsNoop(t *testing.T) {
	userDir := t.TempDir()
	frontmatter := "name: promoted\n" +
		"description: already promoted\n" +
		"odek:\n" +
		"  provenance:\n" +
		"    sources: browser\n"
	writeTestSkill(t, userDir, "promoted", frontmatter, "# body\n")

	// NeedsReview=false means the skill is already trusted; re-promoting
	// should be a no-op even though Sources is preserved for audit.
	if err := promoteSkill(userDir, "promoted", false); err != nil {
		t.Fatalf("promote: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(userDir, "promoted", "SKILL.md"))
	if err != nil {
		t.Fatalf("read after promote: %v", err)
	}
	if !strings.Contains(string(data), "sources: browser") {
		t.Errorf("sources audit trail should be preserved, got:\n%s", data)
	}
}

func TestPromoteSkill_ForcePromotesTainted(t *testing.T) {
	userDir := t.TempDir()
	frontmatter := "name: tainted\n" +
		"description: an untrusted skill\n" +
		"odek:\n" +
		"  provenance:\n" +
		"    untrusted: true\n" +
		"    needs_review: true\n" +
		"    sources: https://example.com\n"
	writeTestSkill(t, userDir, "tainted", frontmatter, "# body\n")

	if err := promoteSkill(userDir, "tainted", true); err != nil {
		t.Fatalf("promote --force: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(userDir, "tainted", "SKILL.md"))
	if err != nil {
		t.Fatalf("read after promote: %v", err)
	}
	out := string(data)
	if strings.Contains(out, "needs_review: true") {
		t.Errorf("needs_review flag should be cleared, got:\n%s", out)
	}
	if strings.Contains(out, "untrusted: true") {
		t.Errorf("untrusted flag should be cleared, got:\n%s", out)
	}
	if !strings.Contains(out, "https://example.com") {
		t.Errorf("sources audit trail should be preserved, got:\n%s", out)
	}
}

func TestScanSingleSkill_ParsesProvenance(t *testing.T) {
	userDir := t.TempDir()
	frontmatter := "name: needs-review\n" +
		"description: an untrusted skill\n" +
		"odek:\n" +
		"  provenance:\n" +
		"    untrusted: true\n" +
		"    needs_review: true\n"
	dir := writeTestSkill(t, userDir, "needs-review", frontmatter, "# body\n")

	s := scanSingleSkill(dir, filepath.Join(dir, "SKILL.md"))
	if s == nil {
		t.Fatal("scanSingleSkill returned nil for valid skill")
	}
	if s.Name != "needs-review" {
		t.Errorf("Name = %q, want 'needs-review'", s.Name)
	}
	if !s.Provenance.NeedsReview {
		t.Error("NeedsReview should be true")
	}
	if !s.Provenance.Untrusted {
		t.Error("Untrusted should be true")
	}
}

func TestScanSingleSkill_MissingFile(t *testing.T) {
	tmp := t.TempDir()
	got := scanSingleSkill(tmp, filepath.Join(tmp, "does-not-exist", "SKILL.md"))
	if got != nil {
		t.Errorf("expected nil for missing file, got %+v", got)
	}
}
