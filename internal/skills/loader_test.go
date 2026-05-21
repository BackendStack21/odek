package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSkillContent_Basic(t *testing.T) {
	content := `---
name: docker-build
description: Build and optimize Docker images
version: 1.0.0
author: odek
odek:
  trigger:
    topic: docker container build
    action: build optimize
  auto_load: false
  quality: verified
---
## Overview

Guide for Docker builds.

## Step-by-Step

1. Check Dockerfile

## Common Pitfalls

- Forgetting cache

## Verification

- docker build .`
	s := parseSkillContent(content, "")
	if s == nil {
		t.Fatal("parseSkillContent returned nil")
	}
	if s.Name != "docker-build" {
		t.Errorf("Name = %q, want docker-build", s.Name)
	}
	if s.Description != "Build and optimize Docker images" {
		t.Errorf("Description = %q", s.Description)
	}
	if s.Quality != QualityVerified {
		t.Errorf("Quality = %q, want verified", s.Quality)
	}
	if !strings.Contains(s.Body, "Guide for Docker builds") {
		t.Errorf("Body should contain body text")
	}
	if len(s.Trigger.TopicKeywords) != 3 {
		t.Errorf("TopicKeywords = %v, want 3", s.Trigger.TopicKeywords)
	}
	if len(s.Trigger.ActionKeywords) != 2 {
		t.Errorf("ActionKeywords = %v, want 2", s.Trigger.ActionKeywords)
	}
	if s.AutoLoad {
		t.Error("AutoLoad should be false")
	}
}

func TestParseSkillContent_NoFrontmatter(t *testing.T) {
	s := parseSkillContent("just some text", "")
	if s != nil {
		t.Error("expected nil for no frontmatter")
	}
}

func TestParseSkillContent_EmptyBody(t *testing.T) {
	content := `---
name: empty
---
`
	s := parseSkillContent(content, "")
	if s != nil {
		t.Error("expected nil for empty body")
	}
}

func TestParseSkillContent_AutoLoad(t *testing.T) {
	content := `---
name: auto-load-test
odek:
  auto_load: true
  quality: manual
---
Body content here`
	s := parseSkillContent(content, "")
	if s == nil {
		t.Fatal("nil")
	}
	if !s.AutoLoad {
		t.Error("AutoLoad should be true")
	}
}

func TestParseSkillContent_NoTriggerDerivesKeywords(t *testing.T) {
	content := `---
name: test-skill
---
## Overview
Docker container build optimization guide.
Use this to build and deploy containers.
Common pitfalls include forgetting cache.
Build with docker build --cache-from.
Run docker buildx for multi-platform.`
	s := parseSkillContent(content, "")
	if s == nil {
		t.Fatal("nil")
	}
	// Should have derived keywords
	if len(s.Trigger.TopicKeywords) == 0 && len(s.Trigger.ActionKeywords) == 0 {
		t.Error("expected derived keywords")
	}
}

func TestWriteAndParseSkill(t *testing.T) {
	dir := t.TempDir()
	skill := Skill{
		Name:        "test-skill",
		Description: "A test skill",
		Version:     "1.0.0",
		Author:      "odek",
		AutoLoad:    true,
		Quality:     QualityDraft,
		Trigger: SkillTrigger{
			TopicKeywords:  []string{"docker", "container"},
			ActionKeywords: []string{"build", "deploy"},
		},
		Body: "## Overview\n\nTest body",
	}

	if err := WriteSkill(dir, skill); err != nil {
		t.Fatal(err)
	}

	// Read it back
	skillPath := filepath.Join(dir, "test-skill", "SKILL.md")
	if _, err := os.Stat(skillPath); err != nil {
		t.Fatal("skill file not found:", err)
	}

	data, _ := os.ReadFile(skillPath)
	parsed := parseSkillContent(string(data), skillPath)
	if parsed == nil {
		t.Fatal("re-parse failed")
	}
	if parsed.Name != "test-skill" {
		t.Errorf("Name = %q", parsed.Name)
	}
	if !parsed.AutoLoad {
		t.Error("AutoLoad should be true")
	}
}

func TestScanDirs_Empty(t *testing.T) {
	dir := t.TempDir() // empty dir, no skills
	result := ScanDirs(dir, "", nil)
	if result == nil {
		t.Fatal("nil result")
	}
	if len(result.AutoLoad) != 0 {
		t.Errorf("AutoLoad = %d, want 0", len(result.AutoLoad))
	}
	if len(result.Lazy) != 0 {
		t.Errorf("Lazy = %d, want 0", len(result.Lazy))
	}
}

func TestScanDirs_ProjectPriority(t *testing.T) {
	userDir := t.TempDir()
	projectDir := t.TempDir()

	// Same skill name in both dirs, different body
	writeTestSkill(t, projectDir, "test-skill", "## Project body")
	writeTestSkill(t, userDir, "test-skill", "## User body")

	result := ScanDirs(projectDir, userDir, nil)

	// Should find 1 skill (project wins)
	total := len(result.AutoLoad) + len(result.Lazy)
	if total != 1 {
		t.Fatalf("expected 1 skill, got %d", total)
	}

	var s Skill
	if len(result.AutoLoad) > 0 {
		s = result.AutoLoad[0]
	} else {
		s = result.Lazy[0]
	}
	if !strings.Contains(s.Body, "Project body") {
		t.Errorf("expected project body, got %q", s.Body[:20])
	}
}

func TestFormatAsContext(t *testing.T) {
	s := Skill{
		Name: "test-skill",
		Body: "## Overview\n\nTest content",
	}
	ctx := FormatAsContext(s)
	if !strings.Contains(ctx, "## Skill: test-skill") {
		t.Error("missing skill header")
	}
	if !strings.Contains(ctx, "Test content") {
		t.Error("missing body")
	}
}

func TestHashBody(t *testing.T) {
	h1 := HashBody("hello world")
	h2 := HashBody("hello world")
	h3 := HashBody("hello world!")
	if h1 != h2 {
		t.Error("same body should produce same hash")
	}
	if h1 == h3 {
		t.Error("different body should produce different hash")
	}
}

func TestParseQualityFlag(t *testing.T) {
	tests := []struct {
		input string
		want  SkillQuality
	}{
		{"draft", QualityDraft},
		{"verified", QualityVerified},
		{"imported", QualityImported},
		{"manual", QualityManual},
		{"stale", QualityStale},
		{"", QualityManual},
		{"unknown", QualityManual},
	}
	for _, tt := range tests {
		got := parseQualityFlag(tt.input)
		if got != tt.want {
			t.Errorf("parseQualityFlag(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseSkillFile_NonExistent(t *testing.T) {
	s := parseSkillFile("/nonexistent/path/SKILL.md")
	if s != nil {
		t.Error("expected nil for non-existent file")
	}
}

func TestParseSkillFile_Valid(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "my-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := `---
name: my-skill
description: A test skill
odek:
  trigger:
    topic: test go
    action: run verify
  quality: draft
---
## Overview

Test body content.
`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	s := parseSkillFile(filepath.Join(skillDir, "SKILL.md"))
	if s == nil {
		t.Fatal("expected parsed skill, got nil")
	}
	if s.Name != "my-skill" {
		t.Errorf("Name = %q, want %q", s.Name, "my-skill")
	}
	if s.Description != "A test skill" {
		t.Errorf("Description = %q", s.Description)
	}
	if s.Quality != QualityDraft {
		t.Errorf("Quality = %q, want draft", s.Quality)
	}
	if !strings.Contains(s.Body, "Test body content") {
		t.Error("Body should contain content")
	}
	if len(s.Trigger.TopicKeywords) != 2 || s.Trigger.TopicKeywords[0] != "test" {
		t.Errorf("TopicKeywords = %v", s.Trigger.TopicKeywords)
	}
	if s.AutoLoad {
		t.Error("AutoLoad should be false")
	}
}

func writeTestSkill(t *testing.T, dir, name, body string) {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	os.MkdirAll(skillDir, 0755)
	content := "---\nname: " + name + "\n---\n\n" + body
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644)
}
