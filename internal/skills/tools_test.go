package skills

import (
	"strings"
	"testing"
)

func TestSkillLoadTool_NameDescSchema(t *testing.T) {
	tool := &SkillLoadTool{}
	if tool.Name() != "skill_load" {
		t.Errorf("Name = %q", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description should not be empty")
	}
	if tool.Schema() == nil {
		t.Error("Schema should not be nil")
	}
}

func TestSkillListTool_NameDescSchema(t *testing.T) {
	tool := &SkillListTool{}
	if tool.Name() != "skill_list" {
		t.Errorf("Name = %q", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description should not be empty")
	}
	if tool.Schema() == nil {
		t.Error("Schema should not be nil")
	}
}

func TestSkillSaveTool_NameDescSchema(t *testing.T) {
	tool := &SkillSaveTool{}
	if tool.Name() != "skill_save" {
		t.Errorf("Name = %q", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description should not be empty")
	}
	if tool.Schema() == nil {
		t.Error("Schema should not be nil")
	}
}

func TestSkillPatchTool_NameDescSchema(t *testing.T) {
	tool := &SkillPatchTool{}
	if tool.Name() != "skill_patch" {
		t.Errorf("Name = %q", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description should not be empty")
	}
	if tool.Schema() == nil {
		t.Error("Schema should not be nil")
	}
}

func TestSkillDeleteTool_NameDescSchema(t *testing.T) {
	tool := &SkillDeleteTool{}
	if tool.Name() != "skill_delete" {
		t.Errorf("Name = %q", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description should not be empty")
	}
	if tool.Schema() == nil {
		t.Error("Schema should not be nil")
	}
}

func TestGetResult_ReturnsCopy(t *testing.T) {
	dir := t.TempDir()
	writeTestSkill(t, dir, "get-skill", "## Overview\nTest\n## Common Pitfalls\n- None\n## Verification\n- Check")
	sm := NewSkillManager(dir, "")
	result := sm.GetResult()
	if result == nil {
		t.Fatal("GetResult returned nil")
	}
	if len(result.Lazy) == 0 {
		t.Error("GetResult should include skills")
	}
}

func TestGetTrieIndex(t *testing.T) {
	dir := t.TempDir()
	writeTestSkill(t, dir, "trie-skill", "## Overview\nDocker build\n## Common Pitfalls\n- None\n## Verification\n- Check")
	sm := NewSkillManager(dir, "")
	idx := sm.GetTrieIndex()
	if idx == nil {
		t.Fatal("GetTrieIndex returned nil")
	}
}

func TestSkillLoadTool_EmptyName(t *testing.T) {
	tool := &SkillLoadTool{Manager: &SkillManager{}}
	_, err := tool.Call(`{"name": ""}`)
	if err == nil {
		t.Error("expected error for empty name")
	}
}

func TestSkillPatchTool_EmptyOldText(t *testing.T) {
	tool := &SkillPatchTool{Manager: &SkillManager{}}
	_, err := tool.Call(`{"name": "x", "old_text": ""}`)
	if err == nil {
		t.Error("expected error for empty old_text")
	}
}

func TestSkillDeleteTool_EmptyName(t *testing.T) {
	tool := &SkillDeleteTool{Manager: &SkillManager{}}
	_, err := tool.Call(`{"name": ""}`)
	if err == nil {
		t.Error("expected error for empty name")
	}
}

func TestSkillSaveTool_OversizeBody(t *testing.T) {
	sm := NewSkillManager(t.TempDir(), "")
	tool := &SkillSaveTool{Manager: sm}
	// Body over 1MB
	bigBody := strings.Repeat("x", MaxSkillBodySize+1)
	_, err := tool.Call(`{"name":"big","description":"too big","body":"` + bigBody + `"}`)
	if err == nil {
		t.Error("expected error for oversized body")
	}
}

func TestSkillListTool_Filter(t *testing.T) {
	dir := t.TempDir()
	writeTestSkill(t, dir, "docker-build", "## Overview\nBuild Docker images\n## Common Pitfalls\n- Cache misses\n## Verification\n- Check image\nExtra padding to reach 300 char minimum for the body. Docker is great for containerization. More text here to ensure we pass validation. And a bit more. Almost there. Done.")
	writeTestSkill(t, dir, "npm-publish", "## Overview\nPublish npm packages\n## Common Pitfalls\n- Version conflicts\n## Verification\n- Check registry\nExtra padding to reach 300 char minimum. npm is the node package manager. More filler text here to ensure we pass the validation check. And more. Done.")
	sm := NewSkillManager(dir, "")
	tool := &SkillListTool{Manager: sm}

	result, err := tool.Call(`{"filter": "docker"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "docker-build") {
		t.Errorf("filter should include docker-build: %s", result)
	}
	if strings.Contains(result, "npm-publish") {
		t.Error("filter should exclude npm-publish")
	}
}

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
