package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFactStore_ReadMissing(t *testing.T) {
	dir := t.TempDir()
	fs := NewFactStore(dir, 5000, 5000)

	content, err := fs.Read("user")
	if err != nil {
		t.Fatal(err)
	}
	if content != "" {
		t.Errorf("expected empty for missing file, got %q", content)
	}
}

func TestFactStore_ReadInvalidTarget(t *testing.T) {
	dir := t.TempDir()
	fs := NewFactStore(dir, 5000, 5000)

	_, err := fs.Read("invalid")
	if err == nil {
		t.Fatal("expected error for invalid target")
	}
}

func TestFactStore_ReadDirectory(t *testing.T) {
	dir := t.TempDir()
	fs := NewFactStore(dir, 5000, 5000)

	// Create a directory at the target path so os.ReadFile returns EISDIR
	targetPath := fs.path("user")
	if err := os.MkdirAll(targetPath, 0755); err != nil {
		t.Fatal(err)
	}

	_, err := fs.Read("user")
	if err == nil {
		t.Fatal("expected error for read failure on directory")
	}
}

func TestFactStore_AddAndRead(t *testing.T) {
	dir := t.TempDir()
	fs := NewFactStore(dir, 5000, 5000)

	if err := fs.Add("user", "User prefers dark mode"); err != nil {
		t.Fatal(err)
	}
	content, err := fs.Read("user")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "User prefers dark mode") {
		t.Errorf("expected entry in content, got %q", content)
	}
}

func TestFactStore_AddDedup(t *testing.T) {
	dir := t.TempDir()
	fs := NewFactStore(dir, 5000, 5000)

	if err := fs.Add("user", "duplicate entry"); err != nil {
		t.Fatal(err)
	}
	// Second add of same content should be silently rejected
	if err := fs.Add("user", "duplicate entry"); err != nil {
		t.Fatal(err)
	}
	entries, err := fs.Entries("user")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 entry after dedup, got %d", len(entries))
	}
}

func TestFactStore_CapEnforced(t *testing.T) {
	dir := t.TempDir()
	// Very small cap
	fs := NewFactStore(dir, 20, 5000)

	err := fs.Add("user", "this is a long fact that exceeds the twenty character cap")
	if err == nil {
		t.Fatal("expected cap error, got nil")
	}
	if !strings.Contains(err.Error(), "cap") {
		t.Errorf("expected cap error, got %v", err)
	}
}

func TestFactStore_ReplaceBySubstring(t *testing.T) {
	dir := t.TempDir()
	fs := NewFactStore(dir, 5000, 5000)

	if err := fs.Add("env", "Project uses Go 1.22"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Add("env", "Uses chi router"); err != nil {
		t.Fatal(err)
	}

	if err := fs.Replace("env", "Go 1.22", "Project uses Go 1.24"); err != nil {
		t.Fatal(err)
	}

	content, _ := fs.Read("env")
	if !strings.Contains(content, "Go 1.24") {
		t.Errorf("expected updated content, got %q", content)
	}
	if strings.Contains(content, "Go 1.22") {
		t.Errorf("old text should not remain, got %q", content)
	}
}

func TestFactStore_ReplaceNotFound(t *testing.T) {
	dir := t.TempDir()
	fs := NewFactStore(dir, 5000, 5000)

	if err := fs.Add("user", "existing fact"); err != nil {
		t.Fatal(err)
	}
	err := fs.Replace("user", "nonexistent", "new fact")
	if err == nil {
		t.Fatal("expected error for missing old_text")
	}
}

func TestFactStore_RemoveBySubstring(t *testing.T) {
	dir := t.TempDir()
	fs := NewFactStore(dir, 5000, 5000)

	if err := fs.Add("user", "fact one"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Add("user", "fact two"); err != nil {
		t.Fatal(err)
	}

	if err := fs.Remove("user", "one"); err != nil {
		t.Fatal(err)
	}

	content, _ := fs.Read("user")
	if strings.Contains(content, "fact one") {
		t.Errorf("removed entry should not appear, got %q", content)
	}
	if !strings.Contains(content, "fact two") {
		t.Errorf("remaining entry should appear, got %q", content)
	}
}

func TestFactStore_RemoveNotFound(t *testing.T) {
	dir := t.TempDir()
	fs := NewFactStore(dir, 5000, 5000)

	err := fs.Remove("user", "nothing")
	if err == nil {
		t.Fatal("expected error for missing old_text")
	}
}

func TestFactStore_EntriesEmpty(t *testing.T) {
	dir := t.TempDir()
	fs := NewFactStore(dir, 5000, 5000)

	entries, err := fs.Entries("user")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestFactStore_InvalidTarget(t *testing.T) {
	dir := t.TempDir()
	fs := NewFactStore(dir, 5000, 5000)

	err := fs.Add("invalid", "content")
	if err == nil {
		t.Fatal("expected error for invalid target")
	}
}

func TestFactStore_ProjectFactsOverride(t *testing.T) {
	dir := t.TempDir()
	globalDir := filepath.Join(dir, "global")
	projectDir := filepath.Join(dir, "project")
	os.MkdirAll(globalDir, 0755)
	os.MkdirAll(projectDir, 0755)

	global := NewFactStore(globalDir, 5000, 5000)
	project := NewFactStore(projectDir, 5000, 5000)

	global.Add("user", "global preference")
	project.Add("user", "project preference")

	// Verify they're independent
	gContent, _ := global.Read("user")
	pContent, _ := project.Read("user")
	if !strings.Contains(gContent, "global preference") {
		t.Errorf("expected global content, got %q", gContent)
	}
	if !strings.Contains(pContent, "project preference") {
		t.Errorf("expected project content, got %q", pContent)
	}
}

func TestFactStore_AddToEnvNotUser(t *testing.T) {
	dir := t.TempDir()
	fs := NewFactStore(dir, 5000, 5000)

	if err := fs.Add("env", "Server runs Ubuntu 24.04"); err != nil {
		t.Fatal(err)
	}
	content, _ := fs.Read("env")
	if !strings.Contains(content, "Ubuntu 24.04") {
		t.Errorf("expected env content, got %q", content)
	}
	// User should be empty
	userContent, _ := fs.Read("user")
	if userContent != "" {
		t.Errorf("user should be empty, got %q", userContent)
	}
}

func TestFactStore_NewFactStoreZeroCaps(t *testing.T) {
	// Zero caps should use defaults
	dir := t.TempDir()
	fs := NewFactStore(dir, 0, 0)
	if fs.capUser != 1500 {
		t.Errorf("expected default capUser 1500, got %d", fs.capUser)
	}
	if fs.capEnv != 2500 {
		t.Errorf("expected default capEnv 2500, got %d", fs.capEnv)
	}
}

func TestFactStore_AddEmptyContent(t *testing.T) {
	dir := t.TempDir()
	fs := NewFactStore(dir, 5000, 5000)
	err := fs.Add("user", "")
	if err == nil {
		t.Fatal("expected error for empty content")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("expected empty content error, got %v", err)
	}
}

func TestFactStore_AddOnlyWhitespace(t *testing.T) {
	dir := t.TempDir()
	fs := NewFactStore(dir, 5000, 5000)
	err := fs.Add("user", "   ")
	if err == nil {
		t.Fatal("expected error for whitespace-only content")
	}
}

func TestFactStore_ReplaceEmptyOldText(t *testing.T) {
	dir := t.TempDir()
	fs := NewFactStore(dir, 5000, 5000)
	fs.Add("user", "existing fact")
	err := fs.Replace("user", "", "new content")
	if err == nil {
		t.Fatal("expected error for empty old_text")
	}
}

func TestFactStore_ReplaceEmptyContent(t *testing.T) {
	dir := t.TempDir()
	fs := NewFactStore(dir, 5000, 5000)
	fs.Add("user", "existing fact")
	err := fs.Replace("user", "existing", "")
	if err == nil {
		t.Fatal("expected error for empty replacement content")
	}
}

func TestFactStore_RemoveEmptyOldText(t *testing.T) {
	dir := t.TempDir()
	fs := NewFactStore(dir, 5000, 5000)
	err := fs.Remove("user", "")
	if err == nil {
		t.Fatal("expected error for empty old_text")
	}
}

func TestFactStore_ReplaceMultipleMatches(t *testing.T) {
	dir := t.TempDir()
	fs := NewFactStore(dir, 5000, 5000)
	fs.Add("user", "uses Go for backend")
	fs.Add("user", "Go is fast")
	// "Go" matches both — should error
	err := fs.Replace("user", "Go", "replacement")
	if err == nil {
		t.Fatal("expected error for ambiguous old_text matching multiple entries")
	}
	if !strings.Contains(err.Error(), "entries") {
		t.Errorf("expected 'entries' in error, got %v", err)
	}
}

func TestFactStore_RemoveMultipleMatches(t *testing.T) {
	dir := t.TempDir()
	fs := NewFactStore(dir, 5000, 5000)
	fs.Add("user", "likes Go")
	fs.Add("user", "Go is a language")
	err := fs.Remove("user", "Go")
	if err == nil {
		t.Fatal("expected error for ambiguous old_text matching multiple entries")
	}
}

func TestFactStore_AddCapExceeded(t *testing.T) {
	dir := t.TempDir()
	// Very small cap to force overflow
	fs := NewFactStore(dir, 10, 5000)
	err := fs.Add("user", "this is way more than ten characters")
	if err == nil {
		t.Fatal("expected cap error")
	}
	if !strings.Contains(err.Error(), "cap") {
		t.Errorf("expected cap error, got %v", err)
	}
}

func TestFactStore_ReplaceCapExceeded(t *testing.T) {
	dir := t.TempDir()
	fs := NewFactStore(dir, 30, 5000)
	fs.Add("user", "short fact")
	// Replace with something much longer
	err := fs.Replace("user", "short fact", "this is a very long replacement that should overflow the tiny 30 character cap")
	if err == nil {
		t.Fatal("expected cap error on replace")
	}
}
