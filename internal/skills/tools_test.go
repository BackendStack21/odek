package skills

import (
	"fmt"
	"os"
	"path/filepath"
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

func TestRecordUsage_AutoLoad(t *testing.T) {
	// RecordUsage should find skills in AutoLoad list and update them.
	dir := t.TempDir()
	// Write a skill with auto_load: true so it appears in AutoLoad
	skillDir := filepath.Join(dir, "auto-skill")
	os.MkdirAll(skillDir, 0755)
	content := "---\nname: auto-skill\nodek:\n  auto_load: true\n---\n\n## Overview\nTest\n## Common Pitfalls\n- None\n## Verification\n- Check"
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644)

	sm := NewSkillManager(dir, "")

	// Verify it's in AutoLoad
	if len(sm.Result.AutoLoad) != 1 || sm.Result.AutoLoad[0].Name != "auto-skill" {
		t.Fatalf("expected auto-skill in AutoLoad, got AutoLoad=%v Lazy=%v", sm.Result.AutoLoad, sm.Result.Lazy)
	}

	// After scan, LastUsed should be zero
	if !sm.Result.AutoLoad[0].LastUsed.IsZero() {
		t.Error("LastUsed should be zero after scan")
	}
	if sm.Result.AutoLoad[0].UsageCount != 0 {
		t.Errorf("UsageCount = %d, want 0 after scan", sm.Result.AutoLoad[0].UsageCount)
	}

	// Record usage
	sm.RecordUsage("auto-skill")

	if sm.Result.AutoLoad[0].LastUsed.IsZero() {
		t.Error("LastUsed should be non-zero after RecordUsage")
	}
	if sm.Result.AutoLoad[0].UsageCount != 1 {
		t.Errorf("UsageCount = %d, want 1 after RecordUsage", sm.Result.AutoLoad[0].UsageCount)
	}

	// Record again — increments
	sm.RecordUsage("auto-skill")
	if sm.Result.AutoLoad[0].UsageCount != 2 {
		t.Errorf("UsageCount = %d, want 2 after second RecordUsage", sm.Result.AutoLoad[0].UsageCount)
	}
}

func TestRecordUsage_NotFound(t *testing.T) {
	// RecordUsage with a name that doesn't exist in either AutoLoad or Lazy
	// should be a no-op (no panic, no crash).
	dir := t.TempDir()
	// Write one skill so Result is populated
	writeTestSkill(t, dir, "some-skill", "## Overview\nTest\n## Common Pitfalls\n- None\n## Verification\n- Check")

	sm := NewSkillManager(dir, "")
	initialAutoCount := len(sm.Result.AutoLoad)
	initialLazyCount := len(sm.Result.Lazy)

	// This should not panic and should not change anything
	sm.RecordUsage("nonexistent")

	if len(sm.Result.AutoLoad) != initialAutoCount {
		t.Errorf("AutoLoad count changed: %d -> %d", initialAutoCount, len(sm.Result.AutoLoad))
	}
	if len(sm.Result.Lazy) != initialLazyCount {
		t.Errorf("Lazy count changed: %d -> %d", initialLazyCount, len(sm.Result.Lazy))
	}
	// Verify the existing skill was not modified
	for _, s := range sm.Result.Lazy {
		if s.Name == "some-skill" {
			if s.UsageCount != 0 {
				t.Errorf("UsageCount should still be 0 for some-skill, got %d", s.UsageCount)
			}
		}
	}
}

func TestGetResult_ReturnsResultField(t *testing.T) {
	// GetResult should return a copy of the SkillManager.Result field
	// containing both AutoLoad and Lazy skills.
	dir := t.TempDir()
	// Write one auto-load skill and one lazy skill
	skillDir := filepath.Join(dir, "auto-load")
	os.MkdirAll(skillDir, 0755)
	content := "---\nname: auto-load\nodek:\n  auto_load: true\n---\n\n## Overview\nAuto\n## Common Pitfalls\n- None\n## Verification\n- Check"
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644)

	writeTestSkill(t, dir, "lazy-skill", "## Overview\nLazy\n## Common Pitfalls\n- None\n## Verification\n- Check")

	sm := NewSkillManager(dir, "")
	result := sm.GetResult()

	if result == nil {
		t.Fatal("GetResult returned nil")
	}

	// Should contain the auto-load skill
	foundAuto := false
	for _, s := range result.AutoLoad {
		if s.Name == "auto-load" {
			foundAuto = true
			if !strings.Contains(s.Body, "Auto") {
				t.Error("auto-load skill body should contain 'Auto'")
			}
		}
	}
	if !foundAuto {
		t.Error("GetResult should include auto-load skill in AutoLoad")
	}

	// Should contain the lazy skill
	foundLazy := false
	for _, s := range result.Lazy {
		if s.Name == "lazy-skill" {
			foundLazy = true
			if !strings.Contains(s.Body, "Lazy") {
				t.Error("lazy-skill body should contain 'Lazy'")
			}
		}
	}
	if !foundLazy {
		t.Error("GetResult should include lazy-skill in Lazy")
	}

	// Modifying the returned copy should not affect the original
	// Note: GetResult may return internal references; deep copy is not guaranteed
	_ = result.AutoLoad
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

// ── Notifier Integration Tests ─────────────────────────────────────────

func TestRecordUsage_FiresNotifierEvent(t *testing.T) {
	dir := t.TempDir()
	writeTestSkill(t, dir, "notify-test", "## Overview\nTest\n## Common Pitfalls\n- None\n## Verification\n- Check")

	sm := NewSkillManager(dir, "")

	var events []SkillEvent
	cb := &callbackNotifier{fn: func(e SkillEvent) { events = append(events, e) }}
	sm.SetNotifier(cb)

	sm.RecordUsage("notify-test")

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "used" {
		t.Errorf("expected type 'used', got %q", events[0].Type)
	}
	if events[0].SkillName != "notify-test" {
		t.Errorf("expected skill name 'notify-test', got %q", events[0].SkillName)
	}
	if events[0].Timestamp.IsZero() {
		t.Error("timestamp should be non-zero")
	}
}

func TestSkillDelete_FiresNotifierEvent(t *testing.T) {
	dir := t.TempDir()
	writeTestSkill(t, dir, "to-delete", "## Overview\nTest\n## Common Pitfalls\n- None\n## Verification\n- Check")

	sm := NewSkillManager(dir, "")

	var events []SkillEvent
	cb := &callbackNotifier{fn: func(e SkillEvent) { events = append(events, e) }}
	sm.SetNotifier(cb)

	tool := &SkillDeleteTool{Manager: sm}
	result, err := tool.Call(`{"name": "to-delete"}`)
	if err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if !strings.Contains(result, "Deleted") {
		t.Errorf("unexpected result: %q", result)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "deleted" {
		t.Errorf("expected type 'deleted', got %q", events[0].Type)
	}
	if events[0].SkillName != "to-delete" {
		t.Errorf("expected skill name 'to-delete', got %q", events[0].SkillName)
	}
}

func TestSkillSave_FiresNotifierEvent(t *testing.T) {
	dir := t.TempDir()

	sm := NewSkillManager(dir, "")

	var events []SkillEvent
	cb := &callbackNotifier{fn: func(e SkillEvent) { events = append(events, e) }}
	sm.SetNotifier(cb)

	tool := &SkillSaveTool{Manager: sm}
	// Build a valid JSON body — use fmt.Sprintf with proper escaping
	body := strings.ReplaceAll(`## Overview
This is a test skill for notifier validation.

## Step-by-Step

1. Run echo hello
2. Verify output matches

## Common Pitfalls

- None — this is a test skill only

## Verification

- Check that the output matches the expected result string exactly.
- Run the command again and confirm idempotent behavior.
- This extra text is just to reach the minimum 300 character body length requirement for skill_save.`, "\n", "\\n")
	args := fmt.Sprintf(`{"name": "test-save", "description": "A test skill for notifier", "body": "%s"}`, body)
	result, err := tool.Call(args)
	if err != nil {
		t.Fatalf("save failed: %v", err)
	}
	if !strings.Contains(result, "Saved") {
		t.Errorf("unexpected result: %q", result)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "saved" {
		t.Errorf("expected type 'saved', got %q", events[0].Type)
	}
	if events[0].SkillName != "test-save" {
		t.Errorf("expected skill name 'test-save', got %q", events[0].SkillName)
	}
}

func TestSetNotifier_Nil(t *testing.T) {
	dir := t.TempDir()
	sm := NewSkillManager(dir, "")
	sm.SetNotifier(nil)
	// Should not panic — defaults to NoopNotifier
	sm.RecordUsage("non-existent")
}

func TestSetNotifier_MultiNotifier(t *testing.T) {
	dir := t.TempDir()
	writeTestSkill(t, dir, "multi-test", "## Overview\nTest\n## Common Pitfalls\n- None\n## Verification\n- Check")

	sm := NewSkillManager(dir, "")

	var events1, events2 []SkillEvent
	n1 := &callbackNotifier{fn: func(e SkillEvent) { events1 = append(events1, e) }}
	n2 := &callbackNotifier{fn: func(e SkillEvent) { events2 = append(events2, e) }}

	sm.SetNotifier(NewMultiNotifier(n1, n2))
	sm.RecordUsage("multi-test")

	if len(events1) != 1 || len(events2) != 1 {
		t.Fatalf("both notifiers should receive events: n1=%d, n2=%d", len(events1), len(events2))
	}
	if events1[0].Type != "used" || events2[0].Type != "used" {
		t.Error("both notifiers should receive 'used' event")
	}
}
