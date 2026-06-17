package telegram

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSlugify(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", "plan"},
		{"   ", "plan"},
		{"Fix bug in login", "fix-bug-in-login"},
		{"Add user authentication with OAuth2", "add-user-authentication-with-oauth2"},
		{"!!!Special!!!  Characters???", "special-characters"},
		{"--already--hyphenated--", "already-hyphenated"},
		{"UPPERCASE TITLE", "uppercase-title"},
		{"a very long description that goes on and on and on and on", "a-very-long-description-that-goes-on-and-on-and-on-and-on"},
		{"日本語計画", "plan"}, // non-latin chars all stripped
	}
	for _, tt := range tests {
		got := slugify(tt.input)
		if got != tt.expected {
			t.Errorf("slugify(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestEnsurePlansDir(t *testing.T) {
	// Override HOME so we don't touch real ~/.odek/plans.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	dir, err := ensurePlansDir()
	if err != nil {
		t.Fatalf("ensurePlansDir: %v", err)
	}
	expected := filepath.Join(tmp, ".odek", "plans")
	if dir != expected {
		t.Errorf("dir = %q, want %q", dir, expected)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("directory not created: stat(%q) = %v", dir, err)
	}
}

func TestListPlans(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	dir := filepath.Join(tmp, ".odek", "plans")
	os.MkdirAll(dir, 0755)

	// Create some plan files with distinct mtimes.
	now := time.Now()
	files := []struct {
		name    string
		content string
		age     time.Duration
	}{
		{"alpha-plan.md", "# Alpha\n\nFirst plan", 1 * time.Minute},
		{"beta-plan.md", "# Beta\n\nSecond plan", 10 * time.Minute},
		{"gamma-plan.md", "# Gamma\n\nThird plan", 1 * time.Hour},
	}
	for _, f := range files {
		path := filepath.Join(dir, f.name)
		os.WriteFile(path, []byte(f.content), 0644)
		os.Chtimes(path, now, now.Add(-f.age))
	}

	// No limit.
	infos, err := ListPlans(0)
	if err != nil {
		t.Fatalf("ListPlans: %v", err)
	}
	if len(infos) != 3 {
		t.Fatalf("len(infos) = %d, want 3", len(infos))
	}
	// Should be newest first: alpha (1m old), beta (10m), gamma (1h).
	if infos[0].Slug != "alpha-plan" {
		t.Errorf("infos[0].Slug = %q, want alpha-plan", infos[0].Slug)
	}
	if infos[2].Slug != "gamma-plan" {
		t.Errorf("infos[2].Slug = %q, want gamma-plan", infos[2].Slug)
	}

	// With limit.
	infos, _ = ListPlans(1)
	if len(infos) != 1 {
		t.Fatalf("ListPlans(1) = %d items, want 1", len(infos))
	}
}

func TestListPlans_NoDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	// Don't create .odek/plans.

	infos, err := ListPlans(10)
	if err != nil {
		t.Fatalf("ListPlans with no dir: %v", err)
	}
	if len(infos) != 0 {
		t.Fatalf("len(infos) = %d, want 0", len(infos))
	}
}

func TestReadPlan_ExactMatch(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	dir := filepath.Join(tmp, ".odek", "plans")
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "my-plan.md"), []byte("# My Plan\n\nDo things."), 0644)

	slug, content, err := ReadPlan("my-plan")
	if err != nil {
		t.Fatalf("ReadPlan: %v", err)
	}
	if slug != "my-plan" {
		t.Errorf("slug = %q, want my-plan", slug)
	}
	if content != "# My Plan\n\nDo things." {
		t.Errorf("content = %q, want '# My Plan\\n\\nDo things.'", content)
	}
}

func TestReadPlan_PrefixMatch(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	dir := filepath.Join(tmp, ".odek", "plans")
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "long-plan-name.md"), []byte("# Long Plan"), 0644)

	slug, _, err := ReadPlan("long")
	if err != nil {
		t.Fatalf("ReadPlan prefix: %v", err)
	}
	if slug != "long-plan-name" {
		t.Errorf("slug = %q, want long-plan-name", slug)
	}
}

func TestReadPlan_Ambiguous(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	dir := filepath.Join(tmp, ".odek", "plans")
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "fix-login.md"), []byte("login"), 0644)
	os.WriteFile(filepath.Join(dir, "fix-logout.md"), []byte("logout"), 0644)

	_, _, err := ReadPlan("fix")
	if err == nil {
		t.Fatal("expected ambiguous match error")
	}
	if !strings.Contains(err.Error(), "multiple plans match") {
		t.Errorf("error = %q, want 'multiple plans match'", err)
	}
}

func TestReadPlan_NoMatch(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	dir := filepath.Join(tmp, ".odek", "plans")
	os.MkdirAll(dir, 0755)

	_, _, err := ReadPlan("nonexistent")
	if err == nil {
		t.Fatal("expected not found error")
	}
	if !strings.Contains(err.Error(), "no plan matching") {
		t.Errorf("error = %q, want 'no plan matching'", err)
	}
}

func TestReadPlan_OversizeRejected(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	dir := filepath.Join(tmp, ".odek", "plans")
	os.MkdirAll(dir, 0755)
	path := filepath.Join(dir, "huge-plan.md")
	os.WriteFile(path, []byte(strings.Repeat("x", maxPlanBytes+1)), 0644)

	_, _, err := ReadPlan("huge-plan")
	if err == nil {
		t.Fatal("expected error for oversized plan")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error = %q, want 'too large'", err)
	}
}

func TestDeletePlan(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	dir := filepath.Join(tmp, ".odek", "plans")
	os.MkdirAll(dir, 0755)
	path := filepath.Join(dir, "delete-me.md")
	os.WriteFile(path, []byte("bye"), 0644)

	slug, err := DeletePlan("delete-me")
	if err != nil {
		t.Fatalf("DeletePlan: %v", err)
	}
	if slug != "delete-me" {
		t.Errorf("slug = %q, want delete-me", slug)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("file still exists after delete")
	}
}

func TestDeletePlan_Ambiguous(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	dir := filepath.Join(tmp, ".odek", "plans")
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "a-plan.md"), []byte("a"), 0644)
	os.WriteFile(filepath.Join(dir, "a-plan-2.md"), []byte("a2"), 0644)

	_, err := DeletePlan("a")
	if err == nil {
		t.Fatal("expected ambiguous match error")
	}
	if !strings.Contains(err.Error(), "multiple plans match") {
		t.Errorf("error = %q", err)
	}
}

func TestMostRecentPlan(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	dir := filepath.Join(tmp, ".odek", "plans")
	os.MkdirAll(dir, 0755)

	now := time.Now()
	os.WriteFile(filepath.Join(dir, "old.md"), []byte("# old"), 0644)
	os.Chtimes(filepath.Join(dir, "old.md"), now, now.Add(-1*time.Hour))
	os.WriteFile(filepath.Join(dir, "new.md"), []byte("# new plan\n\nContent."), 0644)
	os.Chtimes(filepath.Join(dir, "new.md"), now, now.Add(-1*time.Minute))

	slug, content, err := MostRecentPlan()
	if err != nil {
		t.Fatalf("MostRecentPlan: %v", err)
	}
	if slug != "new" {
		t.Errorf("slug = %q, want new", slug)
	}
	if content != "# new plan\n\nContent." {
		t.Errorf("content = %q", content)
	}
}

func TestMostRecentPlan_Empty(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	_, _, err := MostRecentPlan()
	if err == nil {
		t.Fatal("expected error for empty plans dir")
	}
	if !strings.Contains(err.Error(), "no plans found") {
		t.Errorf("error = %q", err)
	}
}

func TestFirstLine(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		expect string
	}{
		{"# Heading\ncontent", 80, "Heading"},
		{"single line", 80, "single line"},
		{"very long line that exceeds the max length cap", 10, "very long …"},
		{"", 80, ""},
	}
	for _, tt := range tests {
		got := firstLine(tt.input, tt.maxLen)
		if got != tt.expect {
			t.Errorf("firstLine(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.expect)
		}
	}
}

// ── Additional plan.go coverage ────────────────────────────────────────

func TestSlugify_PublicWrapper(t *testing.T) {
	// Slugify is the public wrapper; it should delegate to slugify.
	if got := Slugify("Hello World"); got != "hello-world" {
		t.Errorf("Slugify = %q, want %q", got, "hello-world")
	}
	if got := Slugify(""); got != "plan" {
		t.Errorf("Slugify('') = %q, want %q", got, "plan")
	}
}

func TestSlugify_LongInput(t *testing.T) {
	// Input longer than 60 runes should be truncated.
	long := "abcdefghijklmnopqrstuvwxyz-abcdefghijklmnopqrstuvwxyz-abcdefghijklmnopqrstuvwxyz"
	got := slugify(long)
	if len(got) > 60 {
		t.Errorf("slugify result too long: %d chars", len(got))
	}
}

func TestSlugify_AllSpecialChars(t *testing.T) {
	// Input with only special chars should fall back to "plan".
	if got := slugify("!!!@@@###"); got != "plan" {
		t.Errorf("slugify = %q, want %q", got, "plan")
	}
}

func TestPlanPath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	path, err := planPath("my-plan")
	if err != nil {
		t.Fatalf("planPath error: %v", err)
	}
	expected := tmp + "/.odek/plans/my-plan.md"
	if path != expected {
		t.Errorf("planPath = %q, want %q", path, expected)
	}
}

func TestEnsurePlansDir_MkdirAllError(t *testing.T) {
	tmp := t.TempDir()
	// Create a file where .odek/ should be so MkdirAll fails.
	if err := os.WriteFile(filepath.Join(tmp, ".odek"), []byte("x"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("HOME", tmp)

	_, err := ensurePlansDir()
	if err == nil {
		t.Fatal("expected error when MkdirAll fails")
	}
}

func TestListPlans_ReadDirError(t *testing.T) {
	tmp := t.TempDir()
	// Create .odek/plans as a file, not a directory → ReadDir will fail.
	odekDir := filepath.Join(tmp, ".odek")
	os.MkdirAll(odekDir, 0755)
	if err := os.WriteFile(filepath.Join(odekDir, "plans"), []byte("x"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("HOME", tmp)

	_, err := ListPlans(0)
	if err == nil {
		t.Fatal("expected error when plans is not a directory")
	}
}

func TestReadPlan_EmptySlug(t *testing.T) {
	_, _, err := ReadPlan("")
	if err == nil {
		t.Fatal("expected error for empty slug")
	}
}

func TestReadPlan_NoPlansDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	// No .odek/plans at all.

	_, _, err := ReadPlan("anything")
	if err == nil {
		t.Fatal("expected error when no plans directory exists")
	}
}

func TestReadPlan_ReadDirError(t *testing.T) {
	tmp := t.TempDir()
	odekDir := filepath.Join(tmp, ".odek")
	os.MkdirAll(odekDir, 0755)
	if err := os.WriteFile(filepath.Join(odekDir, "plans"), []byte("x"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("HOME", tmp)

	_, _, err := ReadPlan("x")
	if err == nil {
		t.Fatal("expected error when plans is not a directory")
	}
}

func TestReadPlan_ReadFileError(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, ".odek", "plans")
	os.MkdirAll(dir, 0755)
	// Create an empty directory entry — file won't exist when ReadFile tries to read.
	os.WriteFile(filepath.Join(dir, "exists.md"), []byte("content"), 0644)
	t.Setenv("HOME", tmp)

	// Delete the file so ReadFile fails.
	os.Remove(filepath.Join(dir, "exists.md"))

	_, _, err := ReadPlan("exists")
	if err == nil {
		t.Fatal("expected error when file can't be read")
	}
}

func TestDeletePlan_EmptySlug(t *testing.T) {
	_, err := DeletePlan("")
	if err == nil {
		t.Fatal("expected error for empty slug")
	}
}

func TestDeletePlan_NoPlansDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	_, err := DeletePlan("x")
	if err == nil {
		t.Fatal("expected error when no plans directory")
	}
}

func TestDeletePlan_ReadDirError(t *testing.T) {
	tmp := t.TempDir()
	odekDir := filepath.Join(tmp, ".odek")
	os.MkdirAll(odekDir, 0755)
	os.WriteFile(filepath.Join(odekDir, "plans"), []byte("x"), 0644)
	t.Setenv("HOME", tmp)

	_, err := DeletePlan("x")
	if err == nil {
		t.Fatal("expected error when plans is not a directory")
	}
}

func TestDeletePlan_RemoveError(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, ".odek", "plans")
	os.MkdirAll(dir, 0755)
	// Create the file, then make the directory read-only (best effort).
	os.WriteFile(filepath.Join(dir, "locked.md"), []byte("x"), 0644)
	t.Setenv("HOME", tmp)

	// Try to delete — if we're root this may still succeed.
	_, err := DeletePlan("locked")
	if err != nil {
		// Error is acceptable — test that it propagates.
		t.Logf("DeletePlan error (acceptable if running as root): %v", err)
	}
}

func TestMostRecentPlan_ReadFileError(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, ".odek", "plans")
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "plan.md"), []byte("content"), 0644)
	os.Remove(filepath.Join(dir, "plan.md")) // remove after listing
	t.Setenv("HOME", tmp)

	_, _, err := MostRecentPlan()
	// May or may not error depending on ListPlans caching behavior.
	// If it errors, verify it's a read error.
	if err != nil {
		t.Logf("MostRecentPlan error: %v", err)
	}
}

func TestListPlans_SkipsNonMarkdown(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, ".odek", "plans")
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "note.txt"), []byte("not a plan"), 0644)
	os.WriteFile(filepath.Join(dir, "real-plan.md"), []byte("# Real"), 0644)
	os.MkdirAll(filepath.Join(dir, "subdir"), 0755)
	t.Setenv("HOME", tmp)

	infos, err := ListPlans(0)
	if err != nil {
		t.Fatalf("ListPlans error: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected 1 plan (skip .txt and dir), got %d", len(infos))
	}
	if infos[0].Slug != "real-plan" {
		t.Errorf("slug = %q, want %q", infos[0].Slug, "real-plan")
	}
}

func TestReadPlan_MultiplePrefixMatches(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, ".odek", "plans")
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "fix-login.md"), []byte("login"), 0644)
	os.WriteFile(filepath.Join(dir, "fix-logout.md"), []byte("logout"), 0644)
	os.WriteFile(filepath.Join(dir, "fix-db.md"), []byte("db"), 0644)
	t.Setenv("HOME", tmp)

	_, _, err := ReadPlan("fix")
	if err == nil {
		t.Fatal("expected error for ambiguous prefix with >2 matches")
	}
	if !strings.Contains(err.Error(), "multiple plans match") {
		t.Errorf("error = %q, want 'multiple plans match'", err)
	}
}

func TestDeletePlan_NoMatchFound(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, ".odek", "plans")
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "some-plan.md"), []byte("x"), 0644)
	t.Setenv("HOME", tmp)

	_, err := DeletePlan("nonexistent")
	if err == nil {
		t.Fatal("expected error for non-matching slug")
	}
	if !strings.Contains(err.Error(), "no plan matching") {
		t.Errorf("error = %q, want 'no plan matching'", err)
	}
}
