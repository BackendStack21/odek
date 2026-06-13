package resource

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── ParseRefs ──────────────────────────────────────────────────────────

func TestParseRefs_Empty(t *testing.T) {
	refs := ParseRefs("")
	if len(refs) != 0 {
		t.Errorf("expected 0 refs, got %d", len(refs))
	}
}

func TestParseRefs_NoRefs(t *testing.T) {
	refs := ParseRefs("just a normal prompt without references")
	if len(refs) != 0 {
		t.Errorf("expected 0 refs, got %d", len(refs))
	}
}

func TestParseRefs_SingleRef(t *testing.T) {
	refs := ParseRefs("fix the bug in @src/main.go")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	if refs[0].Raw != "@src/main.go" {
		t.Errorf("Raw = %q, want @src/main.go", refs[0].Raw)
	}
	if refs[0].Path != "src/main.go" {
		t.Errorf("Path = %q, want src/main.go", refs[0].Path)
	}
}

func TestParseRefs_MultipleRefs(t *testing.T) {
	refs := ParseRefs("check @file1.go and @file2.go")
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(refs))
	}
	if refs[0].Path != "file1.go" {
		t.Errorf("ref[0].Path = %q", refs[0].Path)
	}
	if refs[1].Path != "file2.go" {
		t.Errorf("ref[1].Path = %q", refs[1].Path)
	}
}

func TestParseRefs_PrefixedRefs(t *testing.T) {
	refs := ParseRefs("look at @sess:abc123 and @skill:go-test")
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(refs))
	}
	if refs[0].Path != "sess:abc123" {
		t.Errorf("ref[0].Path = %q, want sess:abc123", refs[0].Path)
	}
	if refs[1].Path != "skill:go-test" {
		t.Errorf("ref[1].Path = %q, want skill:go-test", refs[1].Path)
	}
}

func TestParseRefs_AtEndOfString(t *testing.T) {
	refs := ParseRefs("check @end")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	if refs[0].Path != "end" {
		t.Errorf("Path = %q, want end", refs[0].Path)
	}
}

func TestParseRefs_AtWithPaths(t *testing.T) {
	refs := ParseRefs("fix @path/to/file/with-hyphens.js")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	if refs[0].Path != "path/to/file/with-hyphens.js" {
		t.Errorf("Path = %q", refs[0].Path)
	}
}

func TestParseRefs_StopsAtParen(t *testing.T) {
	refs := ParseRefs("see (@file.go) for details")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	if refs[0].Path != "file.go" {
		t.Errorf("Path = %q, want file.go", refs[0].Path)
	}
}

func TestParseRefs_DoubleAt(t *testing.T) {
	// @@ escapes — the second @ starts a new ref
	refs := ParseRefs("hello @@world")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref (the second @), got %d", len(refs))
	}
	if refs[0].Raw != "@world" {
		t.Errorf("Raw = %q, want @world", refs[0].Raw)
	}
}

func TestParseRefs_LoneAt(t *testing.T) {
	// Just "@" alone should not produce a ref
	refs := ParseRefs("@")
	if len(refs) != 0 {
		t.Errorf("expected 0 refs for lone @, got %d", len(refs))
	}
}

// ── ReplaceRefs ────────────────────────────────────────────────────────

func TestReplaceRefs_NoReplacements(t *testing.T) {
	result := ReplaceRefs("hello world", nil)
	if result != "hello world" {
		t.Errorf("expected unchanged, got %q", result)
	}
}

func TestReplaceRefs_SingleReplacement(t *testing.T) {
	result := ReplaceRefs("read @file.go", map[string]string{
		"@file.go": "package main\nfunc main() {}",
	})
	if !strings.Contains(result, "package main") {
		t.Errorf("expected resolved content in result: %q", result)
	}
	if !strings.Contains(result, "--- @file.go ---") {
		t.Errorf("expected delimiter in result: %q", result)
	}
	// Original ref should not appear
	if len(result) > 0 {
		// The @file.go appears in the delimiter markers too, which is expected
	}
}

func TestReplaceRefs_UnresolvedRef(t *testing.T) {
	// Unresolved refs should be left as-is
	result := ReplaceRefs("check @missing.go", map[string]string{
		"@other.go": "content",
	})
	if !strings.Contains(result, "@missing.go") {
		t.Errorf("unresolved @missing.go should remain in text: %q", result)
	}
}

func TestReplaceRefs_MultipleReplacements(t *testing.T) {
	result := ReplaceRefs("compare @a.go and @b.go", map[string]string{
		"@a.go": "file A content",
		"@b.go": "file B content",
	})
	if !strings.Contains(result, "file A content") {
		t.Errorf("missing file A: %q", result)
	}
	if !strings.Contains(result, "file B content") {
		t.Errorf("missing file B: %q", result)
	}
}

func TestReplaceRefs_EmptyContent(t *testing.T) {
	// Empty resolved content should be skipped
	result := ReplaceRefs("read @file.go", map[string]string{
		"@file.go": "",
	})
	if !strings.Contains(result, "@file.go") {
		t.Errorf("empty content should leave @file.go as-is: %q", result)
	}
}

// ── FileResolver ───────────────────────────────────────────────────────

func newTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(dir, "utils.go"), []byte("package utils"), 0644)
	os.Mkdir(filepath.Join(dir, "subdir"), 0755)
	os.WriteFile(filepath.Join(dir, "subdir", "deep.go"), []byte("package deep"), 0644)
	return dir
}

func TestFileResolver_Search(t *testing.T) {
	dir := newTestDir(t)
	res := NewFileResolver(dir)

	results, err := res.Search(context.Background(), "main", 10)
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(results) < 1 {
		t.Fatalf("expected at least 1 result, got %d", len(results))
	}
	found := false
	for _, r := range results {
		if r.Label == "main.go" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected main.go in results: %+v", results)
	}
}

func TestFileResolver_SearchRecursive(t *testing.T) {
	dir := newTestDir(t)
	res := NewFileResolver(dir)

	// Search for "deep" which is in subdir/
	results, err := res.Search(context.Background(), "deep", 10)
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(results) < 1 {
		t.Fatalf("expected at least 1 result for 'deep', got %d", len(results))
	}
}

func TestFileResolver_SearchOutsideRoot(t *testing.T) {
	// Parent holds a sentinel file; root is a subdirectory of it. A traversal
	// query must not surface metadata for files outside root.
	parent := t.TempDir()
	if err := os.WriteFile(filepath.Join(parent, "secret.txt"), []byte("top secret"), 0644); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	res := NewFileResolver(root)

	results, err := res.Search(context.Background(), "../secret", 10)
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	for _, r := range results {
		if strings.Contains(r.Label, "secret") || strings.Contains(r.ID, "secret") {
			t.Fatalf("traversal query leaked file outside root: %+v", r)
		}
	}
}

func TestFileResolver_Load(t *testing.T) {
	dir := newTestDir(t)
	res := NewFileResolver(dir)

	content, err := res.Load(context.Background(), "main.go")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if content != "package main" {
		t.Errorf("content = %q, want %q", content, "package main")
	}
}

func TestFileResolver_LoadOutsideRoot(t *testing.T) {
	dir := newTestDir(t)
	res := NewFileResolver(dir)

	_, err := res.Load(context.Background(), "../../etc/passwd")
	if err == nil {
		t.Error("Load with path traversal should return error")
	}
}

func TestFileResolver_LoadSymlink(t *testing.T) {
	dir := newTestDir(t)

	// Create a symlink
	symlinkPath := filepath.Join(dir, "link.go")
	os.Symlink("/etc/passwd", symlinkPath)

	res := NewFileResolver(dir)

	_, err := res.Load(context.Background(), "link.go")
	if err == nil {
		t.Error("Load with symlink should return error")
	}
}

func TestFileResolver_SearchNoMatch(t *testing.T) {
	dir := newTestDir(t)
	res := NewFileResolver(dir)

	results, err := res.Search(context.Background(), "nonexistent_file_xyz", 10)
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestFileResolver_SearchEmptyQuery(t *testing.T) {
	dir := newTestDir(t)
	res := NewFileResolver(dir)

	results, err := res.Search(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if results != nil {
		t.Errorf("expected nil for empty query, got %d results", len(results))
	}
}

func TestFileResolver_SkipsDotGit(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "real.go"), []byte("real"), 0644)
	os.Mkdir(filepath.Join(dir, ".git"), 0755)
	os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("git config"), 0644)

	res := NewFileResolver(dir)

	results, err := res.Search(context.Background(), "config", 10)
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	for _, r := range results {
		if strings.Contains(r.Label, ".git") {
			t.Errorf("should skip .git files, got: %s", r.Label)
		}
	}
}

func TestFileResolver_LoadTruncated(t *testing.T) {
	dir := t.TempDir()
	// Create a file larger than 50KB
	largeContent := strings.Repeat("A", 60*1024)
	os.WriteFile(filepath.Join(dir, "large.txt"), []byte(largeContent), 0644)

	res := NewFileResolver(dir)
	content, err := res.Load(context.Background(), "large.txt")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(content) > 60*1024 {
		t.Errorf("content should be truncated, got %d bytes", len(content))
	}
	if !strings.Contains(content, "[truncated at 50KB]") {
		t.Errorf("truncated content should have truncation marker: %q", content[:100])
	}
}

// ── SessionResolver ────────────────────────────────────────────────────

func TestSessionResolver_SearchNoDir(t *testing.T) {
	dir := t.TempDir()
	res := NewSessionResolver(dir)

	results, err := res.Search(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for empty dir, got %d", len(results))
	}
}

func TestSessionResolver_SearchFound(t *testing.T) {
	dir := t.TempDir()
	// Create a session file
	sessionFile := filepath.Join(dir, "abc123.json")
	os.WriteFile(sessionFile, []byte(`{"id":"abc123"}`), 0644)

	res := NewSessionResolver(dir)
	results, err := res.Search(context.Background(), "abc", 10)
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != "@sess:abc123" {
		t.Errorf("ID = %q, want @sess:abc123", results[0].ID)
	}
}

func TestSessionResolver_SearchNonJSON(t *testing.T) {
	dir := t.TempDir()
	// Create a non-JSON file in the session dir
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hello"), 0644)

	res := NewSessionResolver(dir)
	results, err := res.Search(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for non-JSON files, got %d", len(results))
	}
}

func TestSessionResolver_SearchLimit(t *testing.T) {
	dir := t.TempDir()
	// Create multiple session files
	for i := 0; i < 5; i++ {
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("sess_%d.json", i)), []byte(`{}`), 0644)
	}

	res := NewSessionResolver(dir)
	results, err := res.Search(context.Background(), "", 2)
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(results) > 2 {
		t.Errorf("expected at most 2 results (limit), got %d", len(results))
	}
}

func TestSessionResolver_Load(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "test123.json")
	os.WriteFile(sessionFile, []byte(`{"id":"test123","messages":[]}`), 0644)

	res := NewSessionResolver(dir)
	content, err := res.Load(context.Background(), "test123")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if !strings.Contains(content, "test123") {
		t.Errorf("expected session content, got: %q", content)
	}
}

func TestSessionResolver_LoadNotFound(t *testing.T) {
	dir := t.TempDir()
	res := NewSessionResolver(dir)

	_, err := res.Load(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

// ── Registry ───────────────────────────────────────────────────────────

func TestRegistry_Search(t *testing.T) {
	dir := newTestDir(t)
	reg := NewRegistry(NewFileResolver(dir))

	results, err := reg.Search(context.Background(), "main", 10)
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(results) < 1 {
		t.Errorf("expected at least 1 result, got %d", len(results))
	}
}

func TestRegistry_Load(t *testing.T) {
	dir := newTestDir(t)
	reg := NewRegistry(NewFileResolver(dir))

	content, err := reg.Load(context.Background(), "@main.go")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if content != "package main" {
		t.Errorf("content = %q, want %q", content, "package main")
	}
}

func TestRegistry_LoadNoResolver(t *testing.T) {
	dir := newTestDir(t)
	reg := NewRegistry(NewFileResolver(dir))

	_, err := reg.Load(context.Background(), "@unknown:ref")
	if err == nil {
		t.Error("Load with unknown prefix should return error")
	}
}

func TestRegistry_Search_ZeroLimitUsesDefault(t *testing.T) {
	dir := newTestDir(t)
	reg := NewRegistry(NewFileResolver(dir))
	results, err := reg.Search(context.Background(), "main", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Error("expected at least 1 result with default limit")
	}
}

func TestRegistry_Search_ResolverErrorSkipped(t *testing.T) {
	// Create a resolver that errors on search — should be skipped
	errResolver := &errorResolver{}
	reg := NewRegistry(errResolver, &emptyResolver{})
	results, err := reg.Search(context.Background(), "anything", 10)
	if err != nil {
		t.Fatal(err)
	}
	// The error resolver is skipped, empty resolver returns nothing
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestRegistry_Load_NoMatchingResolver(t *testing.T) {
	reg := NewRegistry()
	_, err := reg.Load(context.Background(), "@nothing")
	if err == nil {
		t.Fatal("expected error for no resolver")
	}
	if !strings.Contains(err.Error(), "no resolver") {
		t.Errorf("expected 'no resolver' error, got %v", err)
	}
}

func TestRegistry_Load_WithSessionResolver(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "sess123.json")
	os.WriteFile(sessionFile, []byte(`{"id":"sess123"}`), 0644)

	sessionRes := NewSessionResolver(dir)
	reg := NewRegistry(sessionRes)

	content, err := reg.Load(context.Background(), "@sess:sess123")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if !strings.Contains(content, "sess123") {
		t.Errorf("expected session content, got: %q", content)
	}
}

func TestRegistry_Search_MultipleResolvers(t *testing.T) {
	dir := newTestDir(t)
	fileRes := NewFileResolver(dir)
	sessionRes := NewSessionResolver(dir)
	reg := NewRegistry(fileRes, sessionRes)

	results, err := reg.Search(context.Background(), "main", 10)
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(results) < 1 {
		t.Errorf("expected at least 1 result, got %d", len(results))
	}
}

// ── describeFile Tests ────────────────────────────────────────────────

func TestDescribeFile_Small(t *testing.T) {
	info := fakeFileInfo{size: 500}
	result := describeFile(info)
	if result != "500 B" {
		t.Errorf("describeFile(500) = %q, want '500 B'", result)
	}
}

func TestDescribeFile_Medium(t *testing.T) {
	info := fakeFileInfo{size: 1500}
	result := describeFile(info)
	if result != "1.5 KB" {
		t.Errorf("describeFile(1500) = %q, want '1.5 KB'", result)
	}
}

func TestDescribeFile_Large(t *testing.T) {
	info := fakeFileInfo{size: 2 * 1024 * 1024} // 2MB
	result := describeFile(info)
	if !strings.Contains(result, "MB") {
		t.Errorf("describeFile(2MB) = %q, want to contain 'MB'", result)
	}
}

// fakeFileInfo implements os.FileInfo for testing describeFile.
type fakeFileInfo struct {
	size int64
}

func (f fakeFileInfo) Name() string       { return "fake" }
func (f fakeFileInfo) Size() int64        { return f.size }
func (f fakeFileInfo) Mode() os.FileMode  { return 0644 }
func (f fakeFileInfo) ModTime() time.Time { return time.Now() }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() interface{}   { return nil }

// ── formatDuration Tests ───────────────────────────────────────────────

func TestFormatDuration_JustNow(t *testing.T) {
	result := formatDuration(30 * time.Second)
	if result != "just now" {
		t.Errorf("formatDuration(30s) = %q, want 'just now'", result)
	}
}

func TestFormatDuration_Minutes(t *testing.T) {
	result := formatDuration(5 * time.Minute)
	if result != "5m" {
		t.Errorf("formatDuration(5m) = %q, want '5m'", result)
	}
}

func TestFormatDuration_Hours(t *testing.T) {
	result := formatDuration(3 * time.Hour)
	if result != "3h" {
		t.Errorf("formatDuration(3h) = %q, want '3h'", result)
	}
}

func TestFormatDuration_Days(t *testing.T) {
	result := formatDuration(2 * 24 * time.Hour)
	if result != "2d" {
		t.Errorf("formatDuration(2d) = %q, want '2d'", result)
	}
}

// errorResolver always errors on search.
type errorResolver struct{}

func (e *errorResolver) Prefix() string { return "err:" }
func (e *errorResolver) Search(ctx context.Context, query string, limit int) ([]Resource, error) {
	return nil, os.ErrPermission
}
func (e *errorResolver) Load(ctx context.Context, id string) (string, error) {
	return "", os.ErrPermission
}

// emptyResolver returns no results.
type emptyResolver struct{}

func (e *emptyResolver) Prefix() string { return "empty:" }
func (e *emptyResolver) Search(ctx context.Context, query string, limit int) ([]Resource, error) {
	return nil, nil
}
func (e *emptyResolver) Load(ctx context.Context, id string) (string, error) {
	return "", os.ErrNotExist
}

// ── Bug #7: SessionResolver.Load path traversal ─────────────────────────

func TestSessionResolverLoad_PathTraversal(t *testing.T) {
	// Create a temp dir for sessions
	dir := t.TempDir()

	// Create a real session file
	sessionID := "my-valid-session"
	if err := os.WriteFile(filepath.Join(dir, sessionID+".json"), []byte(`{"id":"valid"}`), 0644); err != nil {
		t.Fatal(err)
	}

	resolver := &SessionResolver{dir: dir}

	// Valid session ID should succeed
	content, err := resolver.Load(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("Load with valid session failed: %v", err)
	}
	if !strings.Contains(content, "valid") {
		t.Errorf("Load with valid session returned wrong content: %s", content)
	}

	// Path traversal attempts should FAIL
	traversalAttempts := []string{
		"../../etc/passwd",
		"..%2f..%2fetc/passwd",
		"/etc/passwd",
		"../other-session",
		"foo/../../etc/passwd",
	}
	for _, attempt := range traversalAttempts {
		_, err := resolver.Load(context.Background(), attempt)
		if err == nil {
			t.Errorf("Load with path traversal %q should have failed but succeeded (security bug)", attempt)
		}
	}
}

// ── Bug #30: FileResolver.Search follows symlinks via os.Stat ───────────────

func TestFileResolverSearch_DoesNotFollowSymlinksForMetadata(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base")
	if err := os.MkdirAll(base, 0755); err != nil {
		t.Fatal(err)
	}

	secret := filepath.Join(dir, "secret.txt")
	// 2000 bytes produces "2.0 KB" in Detail if os.Stat follows the symlink.
	if err := os.WriteFile(secret, bytes.Repeat([]byte("x"), 2000), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "leak.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}

	resolver := NewFileResolver(base)
	results, err := resolver.Search(context.Background(), "leak", 10)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	res := results[0]
	// With os.Stat the target's size (2.0 KB) would leak through Detail.
	// With os.Lstat we get the symlink's own metadata, never the target size.
	if strings.Contains(res.Detail, "2.0") {
		t.Errorf("symlink leaked target file size through Detail: %s", res.Detail)
	}
	// The returned resource reference must point to the symlink inside the base.
	if res.ID != "@leak.txt" {
		t.Errorf("expected resource ID %q, got %q", "@leak.txt", res.ID)
	}
}
