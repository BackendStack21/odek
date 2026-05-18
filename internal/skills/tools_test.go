package skills

import (
	"strings"
	"testing"
)

func TestSkillLoadTool(t *testing.T) {
	dir := t.TempDir()
	writeTestSkill(t, dir, "test-skill", "## Overview\n\nTest content\n\n## Common Pitfalls\n\n- None\n\n## Verification\n\n- Check")
	sm := NewSkillManager(dir, "")
	tool := &SkillLoadTool{Manager: sm}

	result, err := tool.Call(`{"name": "test-skill"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(result, "test-skill") {
		t.Errorf("result should contain skill name: %s", result[:60])
	}
	if !contains(result, "Test content") {
		t.Errorf("result should contain body content")
	}

	// Non-existent skill
	_, err = tool.Call(`{"name": "nonexistent"}`)
	if err == nil {
		t.Error("expected error for nonexistent skill")
	}
}

func TestSkillListTool(t *testing.T) {
	dir := t.TempDir()
	writeTestSkill(t, dir, "skill-a", "## Overview\n\nA")
	writeTestSkill(t, dir, "skill-b", "## Overview\n\nB")
	sm := NewSkillManager(dir, "")
	tool := &SkillListTool{Manager: sm}

	result, err := tool.Call(`{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(result, "skill-a") || !contains(result, "skill-b") {
		t.Errorf("result should list both skills: %s", result)
	}
}

func TestSkillSaveTool(t *testing.T) {
	dir := t.TempDir()
	sm := NewSkillManager(dir, "")
	tool := &SkillSaveTool{Manager: sm}

	body := "## Overview\nBuild Docker images\n## Step-by-Step\n1. Write Dockerfile\n## Common Pitfalls\n- Forgetting cache\n## Verification\n- docker build\nThis part adds length so the body exceeds the 300 char minimum threshold easily. More content here to ensure we pass validation. And more. Just a bit more to be absolutely safe."

	// Escape backslash-n sequences for JSON
	jsonBody := strings.ReplaceAll(body, "\n", "\\n")
	jsonStr := `{"name":"docker-build","description":"Build Docker images","body":"` + jsonBody + `"}`
	result, err := tool.Call(jsonStr)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(result, "docker-build") {
		t.Errorf("result should mention skill name: %s", result)
	}

	// Duplicate name should fail
	_, err = tool.Call(`{
		"name": "docker-build",
		"description": "Duplicate",
		"body": "` + body + `"
	}`)
	if err == nil {
		t.Error("expected error for duplicate name")
	}
}

func TestSkillSaveTool_ShortBody(t *testing.T) {
	dir := t.TempDir()
	sm := NewSkillManager(dir, "")
	tool := &SkillSaveTool{Manager: sm}

	_, err := tool.Call(`{
		"name": "short",
		"description": "Too short",
		"body": "short"
	}`)
	if err == nil {
		t.Error("expected error for short body")
	}
}

func TestSkillPatchTool(t *testing.T) {
	dir := t.TempDir()
	writeTestSkill(t, dir, "test-skill", "## Overview\n\nOld content\n\n## Common Pitfalls\n\n- None")
	sm := NewSkillManager(dir, "")
	tool := &SkillPatchTool{Manager: sm}

	result, err := tool.Call(`{"name": "test-skill", "old_text": "Old content", "new_text": "New content"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(result, "Patched") {
		t.Errorf("expected success message: %s", result)
	}

	// Verify by loading
	load := &SkillLoadTool{Manager: sm}
	loaded, _ := load.Call(`{"name": "test-skill"}`)
	if !contains(loaded, "New content") {
		t.Errorf("patched content not reflected: %s", loaded)
	}
}

func TestSkillDeleteTool(t *testing.T) {
	dir := t.TempDir()
	writeTestSkill(t, dir, "test-skill", "## Overview\n\nContent")
	sm := NewSkillManager(dir, "")
	tool := &SkillDeleteTool{Manager: sm}

	result, err := tool.Call(`{"name": "test-skill"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(result, "Deleted") {
		t.Errorf("expected deletion message: %s", result)
	}

	// Verify it's gone
	sm.Reload()
	if len(sm.Result.Lazy)+len(sm.Result.AutoLoad) != 0 {
		t.Error("skill should be gone after deletion")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsStr(s, substr)
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestRecordUsage_UpdatesLastUsedAndUsageCount(t *testing.T) {
	// Regression: LastUsed was always set to time.Now() during scan,
	// making staleness detection impossible. RecordUsage should be the
	// only path that sets LastUsed and increments UsageCount.
	dir := t.TempDir()
	writeTestSkill(t, dir, "used-skill", "## Overview\nTest\n## Common Pitfalls\n- None\n## Verification\n- Check")

	sm := NewSkillManager(dir, "")

	// After scan, LastUsed should be zero (skill was scanned, not used)
	for _, s := range sm.Result.Lazy {
		if s.Name == "used-skill" {
			if !s.LastUsed.IsZero() {
				t.Error("LastUsed should be zero after scan (not actually used yet)")
			}
			if s.UsageCount != 0 {
				t.Errorf("UsageCount = %d, want 0 after scan", s.UsageCount)
			}
		}
	}

	// Record usage
	sm.RecordUsage("used-skill")

	// After RecordUsage, LastUsed should be non-zero and UsageCount incremented
	for _, s := range sm.Result.Lazy {
		if s.Name == "used-skill" {
			if s.LastUsed.IsZero() {
				t.Error("LastUsed should be non-zero after RecordUsage")
			}
			if s.UsageCount != 1 {
				t.Errorf("UsageCount = %d, want 1 after RecordUsage", s.UsageCount)
			}
		}
	}

	// Record again — increments
	sm.RecordUsage("used-skill")
	for _, s := range sm.Result.Lazy {
		if s.Name == "used-skill" {
			if s.UsageCount != 2 {
				t.Errorf("UsageCount = %d, want 2 after second RecordUsage", s.UsageCount)
			}
		}
	}

	// Non-existent skill — no panic
	sm.RecordUsage("nonexistent")
}

func TestRecordUsage_Concurrent(t *testing.T) {
	// Records to the same skill concurrently should not race.
	dir := t.TempDir()
	writeTestSkill(t, dir, "concurrent-skill", "## Overview\nTest\n## Common Pitfalls\n- None\n## Verification\n- Check")

	sm := NewSkillManager(dir, "")

	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			sm.RecordUsage("concurrent-skill")
			done <- true
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}

	for _, s := range sm.Result.Lazy {
		if s.Name == "concurrent-skill" {
			if s.UsageCount != 10 {
				t.Errorf("UsageCount = %d, want 10 after concurrent RecordUsage", s.UsageCount)
			}
			if s.LastUsed.IsZero() {
				t.Error("LastUsed should be non-zero after RecordUsage")
			}
		}
	}
}
