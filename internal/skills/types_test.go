package skills

import "testing"

func TestValidateSkillName_Empty(t *testing.T) {
	if err := ValidateSkillName(""); err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestValidateSkillName_PathSeparator(t *testing.T) {
	if err := ValidateSkillName("foo/bar"); err == nil {
		t.Fatal("expected error for path separator")
	}
	if err := ValidateSkillName("foo\\bar"); err == nil {
		t.Fatal("expected error for backslash")
	}
}

func TestValidateSkillName_Traversal(t *testing.T) {
	if err := ValidateSkillName("foo..bar"); err == nil {
		t.Fatal("expected error for '..' in name")
	}
}

func TestValidateSkillName_RelativePath(t *testing.T) {
	if err := ValidateSkillName("."); err == nil {
		t.Fatal("expected error for '.'")
	}
	if err := ValidateSkillName(".."); err == nil {
		t.Fatal("expected error for '..'")
	}
}

func TestValidateSkillName_Hidden(t *testing.T) {
	if err := ValidateSkillName(".hidden"); err == nil {
		t.Fatal("expected error for dot-prefixed name")
	}
}

func TestValidateSkillName_Valid(t *testing.T) {
	if err := ValidateSkillName("my-skill"); err != nil {
		t.Errorf("unexpected error for valid name: %v", err)
	}
	if err := ValidateSkillName("deploy_script"); err != nil {
		t.Errorf("unexpected error for valid name: %v", err)
	}
}

func TestSkipList_LoadSave(t *testing.T) {
	dir := t.TempDir()
	sl := LoadSkipList(dir)
	if sl == nil || sl.Skipped == nil {
		t.Fatal("LoadSkipList should return initialized list")
	}

	// Record a skip
	if err := sl.RecordSkip(dir, "test-skill", "multi-step"); err != nil {
		t.Fatal(err)
	}
	if sl.Skipped["test-skill"].TimesSkipped != 1 {
		t.Errorf("TimesSkipped = %d, want 1", sl.Skipped["test-skill"].TimesSkipped)
	}
	if sl.Skipped["test-skill"].Heuristic != "multi-step" {
		t.Errorf("Heuristic = %q", sl.Skipped["test-skill"].Heuristic)
	}

	// Reload and verify persistence
	sl2 := LoadSkipList(dir)
	if !sl2.ShouldSkip("test-skill", 1, 0) {
		t.Error("ShouldSkip should return true after recording skip with threshold=1")
	}
}

func TestSkipList_ShouldSkip_Threshold(t *testing.T) {
	dir := t.TempDir()
	sl := LoadSkipList(dir)

	// With threshold=1, first skip blocks
	sl.RecordSkip(dir, "skill-a", "heuristic")
	if !sl.ShouldSkip("skill-a", 1, 0) {
		t.Error("threshold=1: should skip after first skip")
	}

	// Unknown skill
	if sl.ShouldSkip("unknown", 1, 0) {
		t.Error("unknown skill should not be skipped")
	}
}

func TestSkipList_ShouldSkip_Threshold3(t *testing.T) {
	dir := t.TempDir()
	sl := LoadSkipList(dir)

	// Record 1 skip, threshold=3 → not skipped yet
	sl.RecordSkip(dir, "skill-x", "heuristic")
	if sl.ShouldSkip("skill-x", 3, 0) {
		t.Error("threshold=3: should not skip after 1 skip")
	}

	// Record 2nd skip
	sl.RecordSkip(dir, "skill-x", "heuristic")
	if sl.ShouldSkip("skill-x", 3, 0) {
		t.Error("threshold=3: should not skip after 2 skips")
	}

	// Record 3rd skip → now suppressed
	sl.RecordSkip(dir, "skill-x", "heuristic")
	if !sl.ShouldSkip("skill-x", 3, 0) {
		t.Error("threshold=3: should skip after 3 skips")
	}
}

func TestSkipList_ShouldSkip_Expiry(t *testing.T) {
	dir := t.TempDir()
	sl := LoadSkipList(dir)

	// Record skip, set threshold=1 → should skip
	sl.RecordSkip(dir, "expired-skill", "heuristic")
	if !sl.ShouldSkip("expired-skill", 1, 0) {
		t.Error("should skip with threshold=1, resetDays=0")
	}

	// With resetDays=0 (no expiry), still skipped
	if !sl.ShouldSkip("expired-skill", 1, 0) {
		t.Error("should still skip with no expiry")
	}
}

func TestSkipList_ClearSkip(t *testing.T) {
	dir := t.TempDir()
	sl := LoadSkipList(dir)

	sl.RecordSkip(dir, "clear-me", "heuristic")
	if !sl.ShouldSkip("clear-me", 1, 0) {
		t.Fatal("should be skipped before clear")
	}

	sl.ClearSkip(dir, "clear-me")
	if sl.ShouldSkip("clear-me", 1, 0) {
		t.Error("should not be skipped after clear")
	}
}

func TestSkipList_ClearAllSkips(t *testing.T) {
	dir := t.TempDir()
	sl := LoadSkipList(dir)

	sl.RecordSkip(dir, "skill-1", "h1")
	sl.RecordSkip(dir, "skill-2", "h2")

	sl.ClearAllSkips(dir)
	if len(sl.Skipped) != 0 {
		t.Errorf("expected empty skip list, got %d entries", len(sl.Skipped))
	}
}

func TestFilterSkipped(t *testing.T) {
	dir := t.TempDir()

	// Pre-record a skip
	sl := LoadSkipList(dir)
	sl.RecordSkip(dir, "skip-me", "multi-step")

	suggestions := []SkillSuggestion{
		{Name: "skip-me", Heuristic: "multi-step"},
		{Name: "keep-me", Heuristic: "error-recovery"},
	}

	filtered, skipped := FilterSkipped(suggestions, dir, 1, 0)
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1", skipped)
	}
	if len(filtered) != 1 {
		t.Fatalf("len(filtered) = %d, want 1", len(filtered))
	}
	if filtered[0].Name != "keep-me" {
		t.Errorf("filtered[0].Name = %q, want keep-me", filtered[0].Name)
	}
}
