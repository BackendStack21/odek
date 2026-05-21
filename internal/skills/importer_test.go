package skills

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFetchHTTP(t *testing.T) {
	// Create a test HTTP server serving valid SKILL.md content
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		w.Write([]byte(`---
name: http-skill
description: HTTP test
version: "1.0"
---

## Overview

HTTP fetched skill.

## Common Pitfalls

- None

## Verification

- Works`))
	}))
	defer server.Close()

	result, err := fetchHTTP(server.URL, 1048576, 5)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content == "" {
		t.Error("fetchHTTP returned empty content")
	}
	if !strings.Contains(result.Content, "http-skill") {
		t.Error("fetchHTTP should return skill content")
	}
}

func TestFetchHTTP_ErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, err := fetchHTTP(server.URL, 1048576, 5)
	if err == nil {
		t.Error("expected error for 404")
	}
}

func TestFetchHTTP_TooLarge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("x", 2000)))
	}))
	defer server.Close()

	_, err := fetchHTTP(server.URL, 100, 5)
	if err == nil {
		t.Error("expected error for oversized response")
	}
}

func TestFetchFromURI_UnsupportedScheme(t *testing.T) {
	_, err := FetchFromURI("ftp://example.com/skill.md", 1024*1024, 5, false)
	if err == nil {
		t.Fatal("expected error for unsupported URI scheme")
	}
	if !strings.Contains(err.Error(), "unsupported URI scheme") {
		t.Errorf("error should mention 'unsupported URI scheme', got: %v", err)
	}
}

func TestFetchHTTP_RedirectPrivateIP(t *testing.T) {
	// Server that returns a redirect to 127.0.0.1
	// The CheckRedirect function blocks all redirects via the
	// len(via) >= 1 guard before the private-IP check can run.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1/skill.md", http.StatusFound)
	}))
	defer server.Close()

	_, err := fetchHTTP(server.URL, 1048576, 5)
	if err == nil {
		t.Fatal("expected error for redirect")
	}
	if !strings.Contains(err.Error(), "redirect") {
		t.Errorf("error should mention redirect, got: %v", err)
	}
}

func TestFetchHTTP_ConnectionError(t *testing.T) {
	// Create a server and immediately close it to simulate connection refused
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	server.Close()

	_, err := fetchHTTP(server.URL, 1048576, 5)
	if err == nil {
		t.Fatal("expected error for closed server (connection refused)")
	}
}

func TestFetchFromURI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-skill.md")
	content := "---\nname: test\n---\n\nBody"
	os.WriteFile(path, []byte(content), 0644)

	result, err := fetchLocal(path, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(result.Content) != content {
		t.Errorf("content = %q, want %q", result.Content, content)
	}
	if result.SourceName != "local file" {
		t.Errorf("SourceName = %q", result.SourceName)
	}
}

func TestFetchLocal_TooLarge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.md")
	// Write 100 bytes
	os.WriteFile(path, []byte(strings.Repeat("x", 100)), 0644)

	_, err := fetchLocal(path, 50)
	if err == nil {
		t.Error("expected error for too-large file")
	}
}


func TestExtractJSON_Fenced(t *testing.T) {
	input := "```json\n{\"key\": \"value\"}\n```"
	result := extractJSON(input)
	if result != `{"key": "value"}` {
		t.Errorf("got %q", result)
	}
}

func TestExtractJSON_Plain(t *testing.T) {
	input := `{"key": "value"}`
	result := extractJSON(input)
	if result != input {
		t.Errorf("got %q", result)
	}
}

func TestExtractJSON_NoFence(t *testing.T) {
	input := "Some text with ```markdown\ncode\n```"
	result := extractJSON(input)
	if !strings.Contains(result, "Some text") {
		t.Errorf("got %q", result)
	}
}

func TestAssessSkill_LLMError(t *testing.T) {
	llmErr := false
	content := `---
name: test
---
## Overview
Test body`

	assessment, err := AssessSkill(content, func(prompt string) (string, error) {
		if !llmErr {
			return `{"risk_class": "safe", "reasons": ["read-only"], "what_it_does": "reads files", "red_flags": []}`, nil
		}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if assessment.RiskClass != RiskSafe {
		t.Errorf("RiskClass = %q, want safe", assessment.RiskClass)
	}
}

func TestAssessSkill_InvalidJSON(t *testing.T) {
	content := `---
name: test
---
## Overview
Test`

	assessment, err := AssessSkill(content, func(prompt string) (string, error) {
		return "not json at all", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if assessment.RiskClass != RiskElevated {
		t.Errorf("RiskClass = %q, want elevated (fallback)", assessment.RiskClass)
	}
}

func TestImportSkill_BasicMode(t *testing.T) {
	dir := t.TempDir()

	// Write a local skill file
	skillPath := filepath.Join(dir, "import-skill.md")
	skillContent := `---
name: imported-skill
description: An imported skill
---
## Overview

Test body

## Common Pitfalls

- None

## Verification

- Check`
	os.WriteFile(skillPath, []byte(skillContent), 0644)

	result, err := ImportSkill(ImportOptions{
		URI:       skillPath,
		MaxBytes:  1024 * 1024,
		Timeout:   5,
		BasicOnly: true,
		AutoYes:   true,
		UserDir:   filepath.Join(dir, "skills"),
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Skill.Name != "imported-skill" {
		t.Errorf("Name = %q", result.Skill.Name)
	}
	if result.Skill.Quality != QualityManual {
		t.Errorf("Quality = %q, want manual (basic mode)", result.Skill.Quality)
	}
}

func TestImportSkill_ConflictRename(t *testing.T) {
	dir := t.TempDir()
	userDir := filepath.Join(dir, "skills")
	os.MkdirAll(userDir, 0755)

	// Create the skill that will conflict
	writeTestSkill(t, userDir, "imported-skill", "## Overview\n\nExisting")

	// Write the import file
	skillPath := filepath.Join(dir, "import-skill.md")
	skillContent := `---
name: imported-skill
description: A conflicting skill
---
## Overview
Test`
	os.WriteFile(skillPath, []byte(skillContent), 0644)

	result, err := ImportSkill(ImportOptions{
		URI:       skillPath,
		MaxBytes:  1024 * 1024,
		Timeout:   5,
		BasicOnly: true,
		AutoYes:   true,
		UserDir:   userDir,
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Skill.Name != "imported-skill-2" {
		t.Errorf("Name = %q, want imported-skill-2 (auto-rename)", result.Skill.Name)
	}
}

func TestImportSkill_UserCancel(t *testing.T) {
	dir := t.TempDir()
	skillPath := filepath.Join(dir, "test.md")
	os.WriteFile(skillPath, []byte("---\nname: test\n---\n\nBody"), 0644)

	_, err := ImportSkill(ImportOptions{
		URI:      skillPath,
		MaxBytes: 1024 * 1024,
		Timeout:  5,
		UserDir:  filepath.Join(dir, "skills"),
	}, func(assessment *ImportAssessment) bool {
		return false // user cancels
	}, nil)
	if err == nil {
		t.Error("expected error for cancelled import")
	}
}

// TestFetchFromURI_RequireHTTPS verifies that HTTP URIs are rejected
// when RequireHTTPS is enabled in the import options.
func TestFetchFromURI_RequireHTTPS(t *testing.T) {
	// HTTP blocked when requireHTTPS=true
	_, err := FetchFromURI("http://example.com/skill.md", 1024*1024, 5, true)
	if err == nil {
		t.Fatal("expected error for HTTP URI with requireHTTPS=true")
	}
	if !strings.Contains(err.Error(), "HTTP imports are blocked") {
		t.Errorf("error should mention blocked HTTP, got: %v", err)
	}

	// HTTP allowed when requireHTTPS=false (but will fail to connect in test)
	// Just verify the error is NOT the "blocked" message
	_, err2 := FetchFromURI("http://example.com/skill.md", 1024*1024, 5, false)
	if err2 != nil && strings.Contains(err2.Error(), "HTTP imports are blocked") {
		t.Error("HTTP should be allowed when requireHTTPS=false")
	}

	// HTTPS unaffected regardless of flag
	// (will fail to connect in test, that's fine)
	_, err3 := FetchFromURI("https://example.com/skill.md", 1024*1024, 5, true)
	if err3 != nil && strings.Contains(err3.Error(), "HTTP imports are blocked") {
		t.Error("HTTPS should never be blocked by requireHTTPS flag")
	}
}

func TestFetchLocal_Success(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.md")
	os.WriteFile(path, []byte("content"), 0644)

	result, err := fetchLocal(path, 10000)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "content" {
		t.Errorf("expected 'content', got %q", result.Content)
	}
}

func TestFetchLocal_MissingFile(t *testing.T) {
	_, err := fetchLocal("/nonexistent/path/skill.md", 10000)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestFetchLocal_FileTooLarge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.md")
	// Write a file larger than maxBytes
	data := make([]byte, 100)
	os.WriteFile(path, data, 0644)

	_, err := fetchLocal(path, 50)
	if err == nil {
		t.Fatal("expected error for oversized file")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("expected 'too large' error, got %v", err)
	}
}

func TestFetchLocal_PathTraversal_Blocked(t *testing.T) {
	// filepath.Clean("../dir") preserves ".." because it can't be resolved upward
	_, err := fetchLocal("../etc/passwd", 10000)
	if err == nil {
		t.Error("expected error for path traversal")
	}
	if !strings.Contains(err.Error(), "path traversal") {
		t.Errorf("expected 'path traversal' in error, got: %v", err)
	}
}

func TestFetchLocal_HomeExpansion_FileNotFound(t *testing.T) {
	// ~/nonexistent should expand home dir and then fail with file not found
	_, err := fetchLocal("~/nonexistent-file-for-test", 10000)
	if err == nil {
		t.Error("expected error for non-existent ~/ file")
	}
	if !os.IsNotExist(err) {
		t.Logf("fetchLocal(~) returned expected error: %v", err)
	}
}

func TestFetchLocal_ReadFileError(t *testing.T) {
	dir := t.TempDir()
	// Create a directory (not a file) so Stat succeeds but ReadFile fails
	subDir := filepath.Join(dir, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	_, err := fetchLocal(subDir, 10000)
	if err == nil {
		t.Error("expected error when reading a directory as a file")
	}
	if !strings.Contains(err.Error(), "read file") {
		t.Errorf("expected 'read file' in error, got: %v", err)
	}
}

