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
	err := promoteSkill(userDir, "nope")
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
	if err := promoteSkill(userDir, "trusted"); err != nil {
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
		"description: an untrusted skill\n" +
		"odek:\n" +
		"  provenance:\n" +
		"    untrusted: true\n" +
		"    needs_review: true\n" +
		"    sources: https://example.com\n"
	writeTestSkill(t, userDir, "review-me", frontmatter, "# body\n")

	if err := promoteSkill(userDir, "review-me"); err != nil {
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
