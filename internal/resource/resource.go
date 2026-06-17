// Package resource implements @-prefixed resource discovery and
// inline resolution. When a user types "@path/to/file" or "@sess:id"
// in a prompt, the reference is resolved to its content before the
// LLM sees it.
//
// Resolvers are composable — add a new resolver by implementing the
// Resolver interface and registering it.
package resource

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/BackendStack21/odek/internal/pathutil"
	"github.com/BackendStack21/odek/internal/session"
)

// maxResourceFileBytes caps how much of a file the @-resource resolver will
// read into memory. It still truncates the returned content to 50 KB, but this
// guard prevents OOM from files larger than 1 MiB.
const maxResourceFileBytes = 1 << 20 // 1 MiB

// maxResourceSearchLimit caps the number of autocomplete results any caller can
// request. This prevents a prompt-injected or attacker-controlled `limit`
// value from forcing an unbounded directory walk / scan.
const maxResourceSearchLimit = 100

// Resource is a discovered resource returned by a Resolver.
type Resource struct {
	ID      string `json:"id"`     // Full @ reference (e.g. "@src/main.go")
	Type    string `json:"type"`   // "file", "session", "skill"
	Label   string `json:"label"`  // Display label (e.g. "src/main.go")
	Detail  string `json:"detail"` // One-line description (e.g. "Go source, 142 lines")
	Content string `json:"-"`      // Full content (not sent in search results)
}

// A Resolver discovers and loads resources by type.
type Resolver interface {
	// Prefix is the resource prefix (e.g. "sess:" for @sess:...).
	// Empty string means the resolver handles bare @references (like files).
	Prefix() string
	// Search finds resources matching the query. Called for autocomplete.
	Search(ctx context.Context, query string, limit int) ([]Resource, error)
	// Load retrieves the full content of a resource by its full ID (e.g. "@src/main.go").
	Load(ctx context.Context, id string) (string, error)
}

// Registry holds all available resource resolvers.
type Registry struct {
	resolvers []Resolver
}

// NewRegistry creates a resource registry with the given resolvers.
func NewRegistry(resolvers ...Resolver) *Registry {
	return &Registry{resolvers: resolvers}
}

// Search queries all resolvers and merges results.
func (r *Registry) Search(ctx context.Context, query string, limit int) ([]Resource, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > maxResourceSearchLimit {
		limit = maxResourceSearchLimit
	}
	var results []Resource
	for _, res := range r.resolvers {
		matches, err := res.Search(ctx, query, limit)
		if err != nil {
			continue
		}
		results = append(results, matches...)
	}
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// Load resolves a full @reference (e.g. "@src/main.go" or "@sess:abc123")
// to its content by routing to the correct resolver.
func (r *Registry) Load(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimPrefix(ref, "@")
	for _, res := range r.resolvers {
		prefix := res.Prefix()
		if prefix == "" {
			// Bare resolver handles anything not claimed by others
			if !hasPrefixMatch(ref, r.resolvers) {
				return res.Load(ctx, ref)
			}
			continue
		}
		if strings.HasPrefix(ref, prefix) {
			return res.Load(ctx, strings.TrimPrefix(ref, prefix))
		}
	}
	return "", fmt.Errorf("resource: no resolver for %q", ref)
}

func hasPrefixMatch(ref string, resolvers []Resolver) bool {
	for _, res := range resolvers {
		p := res.Prefix()
		if p != "" && strings.HasPrefix(ref, p) {
			return true
		}
	}
	return false
}

// ── @ reference parsing ────────────────────────────────────────────────

// Ref represents a single @reference found in text.
type Ref struct {
	Start int    // Byte offset of "@"
	End   int    // Byte offset after the last character of the reference
	Raw   string // Full matched string including "@"
	Path  string // Content after "@" (e.g. "src/main.go")
}

// ParseRefs extracts all @references from text. A reference starts with
// @ and continues until whitespace, end-of-string, or a closing
// bracket/paren.
func ParseRefs(text string) []Ref {
	var refs []Ref
	for i := 0; i < len(text); i++ {
		if text[i] != '@' {
			continue
		}
		// Must have a non-@ character after
		if i+1 >= len(text) || text[i+1] == '@' {
			continue
		}
		start := i
		i++ // skip @
		// Read until whitespace, EOF, or closing chars
		for i < len(text) {
			ch := text[i]
			if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' ||
				ch == ')' || ch == ']' || ch == '}' || ch == ',' || ch == ';' {
				break
			}
			i++
		}
		end := i
		raw := text[start:end]
		if len(raw) <= 1 {
			continue
		}
		refs = append(refs, Ref{
			Start: start,
			End:   end,
			Raw:   raw,
			Path:  raw[1:],
		})
	}
	return refs
}

// ReplaceRefs replaces all @references in text with inline content
// blocks. The resolved map is keyed by the full @reference string
// (e.g. "@src/main.go"). Unresolved references are left as-is.
func ReplaceRefs(text string, resolved map[string]string) string {
	refs := ParseRefs(text)
	if len(refs) == 0 {
		return text
	}

	// Build replacement from right to left to preserve offsets
	type replacement struct {
		start, end int
		newText    string
	}
	var replacements []replacement
	for _, ref := range refs {
		content, ok := resolved[ref.Raw]
		if !ok || content == "" {
			continue
		}
		block := fmt.Sprintf("\n\n--- %s ---\n%s\n--- end %s ---\n", ref.Raw, content, ref.Raw)
		replacements = append(replacements, replacement{
			start:   ref.Start,
			end:     ref.End,
			newText: block,
		})
	}

	// Apply right-to-left so offsets stay valid
	result := []byte(text)
	for i := len(replacements) - 1; i >= 0; i-- {
		r := replacements[i]
		result = append(result[:r.start], append([]byte(r.newText), result[r.end:]...)...)
	}
	return string(result)
}

// ── File resolver ──────────────────────────────────────────────────────

// FileResolver resolves @path/to/file references by glob-matching
// against a root directory.
type FileResolver struct {
	root string
}

// NewFileResolver creates a file resolver rooted at the given directory.
func NewFileResolver(root string) *FileResolver {
	return &FileResolver{root: root}
}

func (f *FileResolver) Prefix() string { return "" } // bare references

func (f *FileResolver) Search(ctx context.Context, query string, limit int) ([]Resource, error) {
	if query == "" {
		return nil, nil
	}

	// Reject traversal attempts before touching the filesystem. A query is a
	// bare filename/prefix for autocomplete, not a path expression.
	if err := validateSearchQuery(query); err != nil {
		return nil, err
	}

	// Try exact match first
	pattern := filepath.Join(f.root, query)
	matches, err := filepath.Glob(pattern + "*")
	if err != nil {
		return nil, nil
	}

	// If no match, try recursive by walking the directory tree
	if len(matches) == 0 {
		matches = f.walkAndMatch(query)
	}

	// Resolve the root once so every match can be confined to it.
	absRoot, err := filepath.Abs(f.root)
	if err != nil {
		return nil, nil
	}

	var resources []Resource
	for _, match := range matches {
		if len(resources) >= limit {
			break
		}
		if !pathutil.WithinRoot(absRoot, match) {
			continue
		}
		rel, _ := filepath.Rel(f.root, match)
		// Use Lstat so that symlinks do not leak metadata from their targets.
		info, err := os.Lstat(match)
		if err != nil {
			continue
		}
		if info.IsDir() {
			continue
		}
		detail := describeFile(info)
		resources = append(resources, Resource{
			ID:     "@" + rel,
			Type:   "file",
			Label:  rel,
			Detail: detail,
		})
	}
	return resources, nil
}

// validateSearchQuery rejects queries that could escape the configured root.
// Search queries are autocomplete prefixes, not filesystem paths.
func validateSearchQuery(query string) error {
	if filepath.IsAbs(query) {
		return fmt.Errorf("resource: search query must not be an absolute path")
	}
	if strings.Contains(query, "..") {
		return fmt.Errorf("resource: search query must not contain parent references")
	}
	if strings.ContainsAny(query, "/\\") {
		return fmt.Errorf("resource: search query must not contain path separators")
	}
	return nil
}

func (f *FileResolver) Load(ctx context.Context, id string) (string, error) {
	// id is the path after @ (e.g. "src/main.go")
	target := filepath.Join(f.root, id)

	// Security: resolve symlinks in the directory components of the path and
	// verify the resolved location stays within the root. This prevents an
	// attacker from using a symlinked directory inside the workspace (e.g.
	// workspace/link -> /etc) to read files outside the workspace via
	// @link/passwd. The final path component is left unresolved so the
	// O_NOFOLLOW open below still rejects symlink final components.
	resolvedTarget := pathutil.ResolveDirSymlinks(target)
	if !pathutil.WithinRoot(f.root, resolvedTarget) {
		return "", fmt.Errorf("resource: path %q is outside root", id)
	}

	// Open with O_NOFOLLOW to atomically prevent the final component from
	// being a symlink. If the path is a symlink, the open fails with ELOOP —
	// closing the TOCTOU window between a separate Lstat check and the read.
	fd, err := os.OpenFile(resolvedTarget, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer fd.Close()

	info, err := fd.Stat()
	if err != nil {
		return "", err
	}
	if info.Size() > maxResourceFileBytes {
		return "", fmt.Errorf("resource: file too large (%d bytes, max %d)", info.Size(), maxResourceFileBytes)
	}

	data, err := io.ReadAll(fd)
	if err != nil {
		return "", err
	}

	// Truncate at 50KB to avoid context overflow
	maxSize := 50 * 1024
	content := string(data)
	if len(content) > maxSize {
		content = content[:maxSize] + "\n... [truncated at 50KB]"
	}

	return content, nil
}

func (f *FileResolver) walkAndMatch(searchTerm string) []string {
	base := f.root

	var results []string
	_ = filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		// Skip symlinks — resource resolver uses O_NOFOLLOW on Load,
		// so symlinks are unreadable anyway. Skip symlinked directories
		// entirely so traversal cannot follow them.
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(base, path)
		if strings.HasPrefix(rel, searchTerm) || strings.Contains(rel, searchTerm) {
			results = append(results, path)
		}
		return nil
	})

	// Sort by shortest path first (most relevant)
	sort.Slice(results, func(i, j int) bool {
		return len(results[i]) < len(results[j])
	})
	return results
}

// skipDir returns true for directories that should be excluded from
// recursive file searches (.git, node_modules, vendor, etc.).
func skipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", ".venv", "__pycache__", "target", "dist", "build", ".next":
		return true
	}
	return false
}

func describeFile(info os.FileInfo) string {
	size := info.Size()
	switch {
	case size < 1024:
		return fmt.Sprintf("%d B", size)
	case size < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(size)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(size)/(1024*1024))
	}
}

// ── Session resolver ───────────────────────────────────────────────────

// SessionResolver resolves @sess:id references from a session store.
type SessionResolver struct {
	dir string // ~/.odek/sessions/
}

// NewSessionResolver creates a session resolver.
func NewSessionResolver(sessionDir string) *SessionResolver {
	return &SessionResolver{dir: sessionDir}
}

func (s *SessionResolver) Prefix() string { return "sess:" }

func (s *SessionResolver) Search(ctx context.Context, query string, limit int) ([]Resource, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}

	var resources []Resource
	for _, e := range entries {
		if len(resources) >= limit {
			break
		}
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		if query == "" || strings.Contains(id, query) {
			info, _ := e.Info()
			detail := "session"
			if info != nil {
				detail = fmt.Sprintf("updated %s ago", formatDuration(time.Since(info.ModTime())))
			}
			resources = append(resources, Resource{
				ID:     "@sess:" + id,
				Type:   "session",
				Label:  id,
				Detail: detail,
			})
		}
	}
	return resources, nil
}

func (s *SessionResolver) Load(ctx context.Context, id string) (string, error) {
	// id is the session ID (without "sess:" prefix)
	// Security: validate session ID to prevent path traversal.
	if err := session.ValidateSessionID(id); err != nil {
		return "", fmt.Errorf("resource: invalid session ID: %w", err)
	}
	data, err := os.ReadFile(filepath.Join(s.dir, id+".json"))
	if err != nil {
		return "", err
	}

	// Return the full session JSON (it's already compact enough)
	return string(data), nil
}

func formatDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
