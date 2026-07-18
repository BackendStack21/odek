package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/danger"
)

// ── ReadFile Tool ──────────────────────────────────────────────────────

const maxLines = 2000

// maxReadBytes caps the content returned by read_file / batch_read to prevent
// memory exhaustion from huge files.
const maxReadBytes = 1 << 20 // 1 MiB

// maxWriteFileContentBytes caps the content argument of write_file to prevent
// disk exhaustion and memory pressure from a single enormous tool call.
const maxWriteFileContentBytes = maxReadBytes // 1 MiB

// maxSearchLimit caps the number of matches returned by search_files to
// prevent unbounded result JSON from exhausting memory.
const maxSearchLimit = 500

// maxSearchResultBytes caps the total returned content bytes for a single
// search_files / multi_grep content query.
const maxSearchResultBytes = maxReadBytes

// maxGlobMatches caps the number of paths returned by the glob tool to prevent
// unbounded JSON responses from broad patterns.
const maxGlobMatches = 1000

// confinedGlob walks root and returns paths matching pattern without using
// filepath.Glob. It is workspace-confined: it resolves root to an absolute
// path, rejects patterns containing ".." or absolute prefixes, skips every
// symlink (files and directories), and verifies every match stays inside root.
// This closes the path-traversal vector described in finding #22.
func confinedGlob(root, pattern string, limit int, includeDirs bool) ([]string, error) {
	if limit <= 0 {
		limit = maxGlobMatches
	}
	// Reject patterns that could escape the workspace. filepath.Match itself
	// does not match "..", but a pattern like "../.ssh/id_*" combined with
	// filepath.Glob would traverse upward. We block it explicitly.
	if strings.Contains(pattern, "..") {
		return nil, fmt.Errorf("pattern cannot contain ..")
	}
	if filepath.IsAbs(pattern) {
		return nil, fmt.Errorf("pattern cannot be absolute")
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	// Ensure absRoot ends with a separator so strings.HasPrefix cannot be
	// tricked by a sibling directory with a matching prefix.
	rootPrefix := absRoot + string(filepath.Separator)

	// Build the matcher based on pattern syntax.
	var matcher func(rel string, isDir bool) bool
	switch {
	case strings.Contains(pattern, "**"):
		globRe, err := globToRegex(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid glob pattern: %w", err)
		}
		matcher = func(rel string, isDir bool) bool {
			return globRe.MatchString(rel)
		}
	case strings.Contains(pattern, "/") || strings.Contains(pattern, string(filepath.Separator)):
		matcher = func(rel string, isDir bool) bool {
			ok, _ := filepath.Match(pattern, rel)
			return ok
		}
	default:
		matcher = func(rel string, isDir bool) bool {
			ok, _ := filepath.Match(pattern, filepath.Base(rel))
			return ok
		}
	}

	var matches []string
	walkErr := filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		// Skip the root itself.
		if path == absRoot {
			return nil
		}
		// Skip every symlink, including symlinked directories. WalkDir uses
		// Lstat, so a symlink-to-directory has type symlink, not directory.
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		// Skip hidden directories and build-artifact directories.
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && d.Name() != "." {
				return fs.SkipDir
			}
			if skipDir(d.Name()) {
				return fs.SkipDir
			}
			if !includeDirs {
				return nil
			}
		}
		// Verify the path is inside root.
		absPath, err := filepath.Abs(path)
		if err != nil {
			return nil
		}
		if absPath != absRoot && !strings.HasPrefix(absPath, rootPrefix) {
			return nil
		}
		rel, err := filepath.Rel(absRoot, absPath)
		if err != nil {
			return nil
		}
		if matcher(rel, d.IsDir()) {
			matches = append(matches, absPath)
			if len(matches) >= limit {
				return fs.SkipAll
			}
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return matches, nil
}

// globToRegex converts a glob pattern with ** to a regex. ** matches any
// characters including path separators; * matches any characters except path
// separators; ? matches any single character except path separators.
func globToRegex(pattern string) (*regexp.Regexp, error) {
	var reStr strings.Builder
	reStr.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		ch := pattern[i]
		switch {
		case ch == '*' && i+1 < len(pattern) && pattern[i+1] == '*':
			reStr.WriteString(".*")
			i++
		case ch == '*':
			reStr.WriteString("[^/]*")
		case ch == '?':
			reStr.WriteString("[^/]")
		case ch == '.' || ch == '+' || ch == '^' || ch == '$' || ch == '|' || ch == '(' || ch == ')' || ch == '[' || ch == ']' || ch == '{' || ch == '}' || ch == '\\':
			reStr.WriteByte('\\')
			reStr.WriteByte(ch)
		default:
			reStr.WriteByte(ch)
		}
	}
	reStr.WriteString("$")
	return regexp.Compile(reStr.String())
}

type readFileTool struct {
	ctxTool
	dangerousConfig danger.DangerousConfig
}

func (t *readFileTool) Name() string { return "read_file" }

func (t *readFileTool) Description() string {
	return `Read a text file with line numbers and pagination.
Returns file content prefixed with line numbers (LINE_NUM|CONTENT).
Use offset and limit to read specific sections of large files.
Cannot read binary files — use base64 or checksum for binary content.`
}

func (t *readFileTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the file to read (absolute or relative).",
			},
			"offset": map[string]any{
				"type":        "integer",
				"description": "Line number to start reading from (1-indexed, default: 1).",
				"default":     1,
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum number of lines to return (default: 500, max: 2000).",
				"default":     500,
			},
		},
		"required": []string{"path"},
	}
}

type readFileArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

type readFileResult struct {
	Content    string `json:"content"`
	TotalLines int    `json:"total_lines"`
	Error      string `json:"error,omitempty"`
}

func (t *readFileTool) Call(argsJSON string) (string, error) {
	var args readFileArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}
	if args.Path == "" {
		return jsonError("path is required")
	}
	if args.Offset < 0 {
		return jsonError("offset must be a positive integer (1-indexed)")
	}
	if args.Offset == 0 {
		args.Offset = 1 // default
	}
	if args.Limit <= 0 {
		args.Limit = 500
	}
	if args.Limit > maxLines {
		args.Limit = maxLines
	}

	// Security: resolve directory symlinks before classification so a path that
	// traverses a symlinked directory is classified by its real target.
	resolvedPath, err := resolveReadPath(args.Path)
	if err != nil {
		return jsonError(err.Error())
	}

	// Security: check if this path requires approval
	risk := danger.ClassifyPath(resolvedPath)
	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "read_file", Resource: resolvedPath, Risk: risk,
	}, nil); err != nil {
		return jsonError(err.Error())
	}

	// Security: open the file without following symlinks (O_NOFOLLOW).
	// This atomically handles the existence check, type check, and open
	// in a single syscall — eliminating the TOCTOU window between
	// os.Stat (check) and os.Open (use). If the path is a symlink, the
	// open fails with ELOOP.
	f, err := os.OpenFile(resolvedPath, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return jsonError(fmt.Sprintf("file not found: %s", args.Path))
		}
		// ELOOP means the path is a symlink — refuse to follow it
		return jsonError(fmt.Sprintf("cannot open %q: %v", args.Path, err))
	}
	defer f.Close()

	// Check it's a regular file, not a directory
	info, err := f.Stat()
	if err != nil {
		return jsonError(fmt.Sprintf("cannot stat %q: %v", args.Path, err))
	}
	if info.IsDir() {
		return jsonError(fmt.Sprintf("%q is a directory — use tree or search_files(target='files') to list its contents, or glob(pattern='*', path=%q) to find files inside it", args.Path, args.Path))
	}

	// Single pass: binary check from sample → seek → read+count
	sample := make([]byte, 8192)
	n, _ := f.Read(sample)
	if isBinary(sample[:n]) {
		return jsonError(fmt.Sprintf("%q appears to be a binary file — use shell to read it", args.Path))
	}

	// Seek back to start for the full scan
	if _, err := f.Seek(0, 0); err != nil {
		return jsonError(fmt.Sprintf("cannot seek %q: %v", args.Path, err))
	}

	content, totalLines, err := readLinesWithCount(f, args.Offset, args.Limit)
	if err != nil {
		return jsonError(fmt.Sprintf("cannot read %q: %v", args.Path, err))
	}

	result := readFileResult{
		Content:    wrapUntrusted(t.toolCtx(), resolvedPath, content),
		TotalLines: totalLines,
	}
	return jsonResult(result)
}

// ── WriteFile Tool ─────────────────────────────────────────────────────

type writeFileTool struct {
	ctxTool
	dangerousConfig danger.DangerousConfig
	trustedClasses  map[danger.RiskClass]bool
	restrictToCWD   bool // when true, reject paths escaping the working directory
	// containerName, when set, routes writes through the sandbox container so
	// that read-only workspace mounts are enforced.
	containerName string
}

func (t *writeFileTool) Name() string { return "write_file" }

func (t *writeFileTool) Description() string {
	return `Write content to a file, completely replacing existing content.
Creates parent directories automatically. OVERWRITES the entire file.
Use patch for targeted edits.
CRITICAL: Use the EXACT path specified in the task. Do not simplify or drop directories from the path.`
}

func (t *writeFileTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the file to write (will be created/overwritten). Use the EXACT path from the task — never drop or simplify directories.",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "Complete content to write to the file.",
			},
		},
		"required": []string{"path", "content"},
	}
}

type writeFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type writeFileResult struct {
	Success bool   `json:"success"`
	Path    string `json:"path,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (t *writeFileTool) Call(argsJSON string) (string, error) {
	var args writeFileArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}
	if args.Path == "" {
		return jsonError("path is required")
	}
	if len(args.Content) > maxWriteFileContentBytes {
		return jsonError(fmt.Sprintf("content too large (%d bytes, max %d)", len(args.Content), maxWriteFileContentBytes))
	}

	// Path confinement: when restrictToCWD is enabled, reject paths that
	// escape the working directory via ".." traversal or absolute paths.
	if t.restrictToCWD {
		resolved, err := confineToCWD(args.Path)
		if err != nil {
			return jsonError(err.Error())
		}
		args.Path = resolved
	}

	// Security: classify and check write operation
	risk := danger.ClassifyPath(args.Path)
	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "write_file", Resource: args.Path, Risk: risk,
	}, t.trustedClasses); err != nil {
		return jsonError(err.Error())
	}

	// Preserve the original file's mode when overwriting, so a temp file
	// created with default permissions does not change the accessibility
	// of an existing file (e.g., making a 0640 file world-readable).
	var origMode os.FileMode = 0644
	if st, err := os.Stat(args.Path); err == nil {
		origMode = st.Mode().Perm()
	}

	// When sandbox mode is active, route the write through the container so a
	// read-only workspace mount is actually enforced.
	if t.containerName != "" {
		if err := sandboxWriteFile(t.containerName, args.Path, []byte(args.Content), origMode); err != nil {
			return jsonError(fmt.Sprintf("cannot write %q via sandbox: %v", args.Path, err))
		}
		return jsonResult(writeFileResult{
			Success: true,
			Path:    args.Path,
		})
	}

	// Create parent directories
	dir := filepath.Dir(args.Path)
	if dir != "." && dir != "/" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return jsonError(fmt.Sprintf("cannot create directory %q: %v", dir, err))
		}
	}

	// Atomic write via temp file + rename to prevent TOCTOU symlink races.
	// os.CreateTemp creates the file in the same directory (same filesystem),
	// and os.Rename atomically replaces the directory entry without following
	// symlinks — closing the window between check and write.
	tmpFile, err := os.CreateTemp(dir, ".tmp_write_*")
	if err != nil {
		return jsonError(fmt.Sprintf("cannot create temp file: %v", err))
	}
	tmpPath := tmpFile.Name()

	if _, err := tmpFile.Write([]byte(args.Content)); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return jsonError(fmt.Sprintf("cannot write %q: %v", args.Path, err))
	}
	if err := tmpFile.Chmod(origMode); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return jsonError(fmt.Sprintf("cannot set permissions %q: %v", args.Path, err))
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return jsonError(fmt.Sprintf("cannot close temp file: %v", err))
	}

	// Atomic rename — replaces target directory entry without following symlinks
	if err := os.Rename(tmpPath, args.Path); err != nil {
		os.Remove(tmpPath)
		return jsonError(fmt.Sprintf("cannot rename %q: %v", args.Path, err))
	}

	return jsonResult(writeFileResult{
		Success: true,
		Path:    args.Path,
	})
}

// skipDir returns true for directories that should be excluded from
// recursive file searches (.git is already excluded by the hidden-dir
// check — these are non-hidden build/cache/artifact directories).
func skipDir(name string) bool {
	switch name {
	case "node_modules", "vendor", "__pycache__", "target", "dist", "build", ".next", ".venv":
		return true
	}
	return false
}

// ── SearchFiles Tool ───────────────────────────────────────────────────

const maxMatches = 50

type searchFilesTool struct {
	ctxTool
	dangerousConfig danger.DangerousConfig
}

func (t *searchFilesTool) Name() string { return "search_files" }

func (t *searchFilesTool) Description() string {
	return `Search file contents or find files by name.
Two modes: target="content" searches inside files for a regex pattern,
target="files" finds files by glob pattern.
Results are sorted by modification time (newest first).
For performance, ALWAYS use file_glob (e.g. '*.go', '*.py', '*.md') and a
narrow path — without file_glob, every file in the tree is scanned.`
}

func (t *searchFilesTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Regex pattern for content search, or glob pattern for file search.",
			},
			"target": map[string]any{
				"type":        "string",
				"enum":        []string{"content", "files"},
				"description": "'content' searches inside files, 'files' searches by filename (default: 'content').",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Directory or file to search in (default: '.'). Narrow to the most specific directory possible. NEVER use '/' or '/root' without file_glob.",
			},
			"file_glob": map[string]any{
				"type":        "string",
				"description": "Filter files by pattern in content mode (e.g. '*.go'). ALWAYS use this on broad paths — without it every file is scanned (slow on large trees).",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum results to return (default: 50).",
			},
		},
		"required": []string{"pattern"},
	}
}

type searchFilesArgs struct {
	Pattern  string `json:"pattern"`
	Target   string `json:"target"`
	Path     string `json:"path"`
	FileGlob string `json:"file_glob"`
	Limit    int    `json:"limit"`
}

type searchMatch struct {
	Path    string `json:"path"`
	Line    int    `json:"line,omitempty"`
	Content string `json:"content,omitempty"`
}

type searchFilesResult struct {
	Matches []searchMatch `json:"matches"`
	Skipped []string      `json:"skipped,omitempty"`
	Error   string        `json:"error,omitempty"`
}

func (t *searchFilesTool) Call(argsJSON string) (string, error) {
	var args searchFilesArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}
	if args.Pattern == "" {
		return jsonError("pattern is required")
	}
	if args.Target == "" {
		args.Target = "content"
	}
	if args.Path == "" {
		args.Path = "."
	}
	if args.Limit <= 0 {
		args.Limit = maxMatches
	}
	if args.Limit > maxSearchLimit {
		args.Limit = maxSearchLimit
	}

	// Security: check search path
	risk := danger.ClassifyPath(args.Path)
	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "search_files", Resource: args.Path, Risk: risk,
	}, nil); err != nil {
		return jsonError(err.Error())
	}

	switch args.Target {
	case "content":
		return t.searchContent(args)
	case "files":
		return t.searchFiles(args)
	default:
		return jsonError(fmt.Sprintf("invalid target %q: must be 'content' or 'files'", args.Target))
	}
}

// checkSearchPath classifies a discovered path the same way the root path was
// classified. If the path is more restrictive (e.g. a file under ~/.odek
// discovered while searching $HOME), it returns skip=true so the walker does
// not silently read sensitive files.
func (t *searchFilesTool) checkSearchPath(path string) (skip bool, reason string) {
	risk := danger.ClassifyPath(path)
	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "search_files", Resource: path, Risk: risk,
	}, nil); err != nil {
		return true, err.Error()
	}
	return false, ""
}

func (t *searchFilesTool) searchContent(args searchFilesArgs) (string, error) {
	re, err := regexp.Compile(args.Pattern)
	if err != nil {
		return jsonError(fmt.Sprintf("invalid regex pattern %q: %v", args.Pattern, err))
	}

	var matches []searchMatch
	var skipped []string
	limit := args.Limit
	resultBytes := 0

	err = filepath.Walk(args.Path, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip inaccessible files
		}
		if info.IsDir() {
			// Skip hidden directories
			if strings.HasPrefix(info.Name(), ".") && info.Name() != "." {
				return filepath.SkipDir
			}
			// Skip known-large build/artifact directories
			if skipDir(info.Name()) {
				return filepath.SkipDir
			}
			// Security: a broad search root may contain a sensitive subtree
			// (e.g. ~/.odek under $HOME). Classify the directory before
			// descending; if it would require approval/denial, skip it.
			if skip, reason := t.checkSearchPath(path); skip {
				skipped = append(skipped, path+": "+reason)
				return filepath.SkipDir
			}
			return nil
		}
		// Skip symlinks — prevents TOCTOU on the path and avoids listing
		// files the agent can't read (O_NOFOLLOW opens would fail anyway).
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}

		// Security: classify each file before reading. This prevents a broad
		// search from silently returning files that read_file would gate.
		if skip, reason := t.checkSearchPath(path); skip {
			skipped = append(skipped, path+": "+reason)
			return nil
		}

		// Apply file_glob filter
		if args.FileGlob != "" {
			match, _ := filepath.Match(args.FileGlob, info.Name())
			if !match {
				return nil
			}
		}

		// Skip binary files — single open for check then search
		f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return nil
		}
		defer f.Close()

		sample := make([]byte, 512)
		n, _ := f.Read(sample)
		if isBinary(sample[:n]) {
			return nil
		}

		// Seek back to start for content search
		if _, err := f.Seek(0, 0); err != nil {
			return nil
		}

		// Search line by line

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if re.MatchString(line) {
				trimmed := strings.TrimSpace(line)
				if resultBytes+len(trimmed) > maxSearchResultBytes {
					limit = len(matches)
					break
				}
				resultBytes += len(trimmed)
				matches = append(matches, searchMatch{
					Path:    path,
					Line:    lineNum,
					Content: wrapUntrusted(t.toolCtx(), fmt.Sprintf("%s:%d", path, lineNum), trimmed),
				})
				if len(matches) >= limit {
					break
				}
			}
		}
		if len(matches) >= limit {
			return filepath.SkipAll
		}
		return nil
	})

	if err != nil && len(matches) == 0 {
		return jsonError(fmt.Sprintf("search failed: %v", err))
	}

	return jsonResult(searchFilesResult{Matches: matches, Skipped: skipped})
}

func (t *searchFilesTool) searchFiles(args searchFilesArgs) (string, error) {
	// Use the pattern as a glob
	pattern := args.Pattern
	searchDir := args.Path

	paths, err := confinedGlob(searchDir, pattern, args.Limit, false)
	if err != nil {
		return jsonError(fmt.Sprintf("invalid search pattern %q: %v", pattern, err))
	}

	var matches []searchMatch
	var skipped []string
	for _, p := range paths {
		// Security: each discovered path is classified the same way the root
		// path is. Sensitive files (e.g. under ~/.odek, /etc) are skipped
		// rather than returned silently.
		if skip, reason := t.checkSearchPath(p); skip {
			skipped = append(skipped, p+": "+reason)
			continue
		}
		matches = append(matches, searchMatch{Path: wrapUntrusted(t.toolCtx(), "search_files:"+p, p)})
	}

	// Sort by modification time (newest first). Use Lstat so symlinks are not
	// followed and their own metadata is used for sorting.
	sort.Slice(matches, func(i, j int) bool {
		fi, _ := os.Lstat(unwrapUntrusted(matches[i].Path))
		fj, _ := os.Lstat(unwrapUntrusted(matches[j].Path))
		if fi == nil || fj == nil {
			return unwrapUntrusted(matches[i].Path) < unwrapUntrusted(matches[j].Path)
		}
		return fi.ModTime().After(fj.ModTime())
	})

	return jsonResult(searchFilesResult{Matches: matches, Skipped: skipped})
}

// ── Patch Tool ─────────────────────────────────────────────────────────

type patchTool struct {
	ctxTool
	dangerousConfig danger.DangerousConfig
	trustedClasses  map[danger.RiskClass]bool
	restrictToCWD   bool // when true, reject paths escaping the working directory
	// containerName, when set, routes writes through the sandbox container so
	// that read-only workspace mounts are enforced.
	containerName string
}

func (t *patchTool) Name() string { return "patch" }

func (t *patchTool) Description() string {
	return `Find and replace text in a file. Replaces the first occurrence by default.
Use replace_all=true to replace all occurrences. Returns a unified diff showing changes.
Use empty new_string to delete matched text.`
}

func (t *patchTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "File path to edit.",
			},
			"old_string": map[string]any{
				"type":        "string",
				"description": "Text to find and replace. Must be unique in the file unless replace_all=true.",
			},
			"new_string": map[string]any{
				"type":        "string",
				"description": "Replacement text. Pass empty string to delete the matched text.",
			},
			"replace_all": map[string]any{
				"type":        "boolean",
				"description": "Replace all occurrences instead of just the first (default: false).",
			},
		},
		"required": []string{"path", "old_string"},
	}
}

type patchArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

type patchResult struct {
	Success bool   `json:"success"`
	Diff    string `json:"diff,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (t *patchTool) Call(argsJSON string) (string, error) {
	var args patchArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}
	if args.Path == "" {
		return jsonError("path is required")
	}
	if args.OldString == "" {
		return jsonError("old_string is required")
	}

	// Path confinement: same as write_file — reject paths that escape the
	// working directory via ".." traversal or absolute paths.
	if t.restrictToCWD {
		resolved, err := confineToCWD(args.Path)
		if err != nil {
			return jsonError(err.Error())
		}
		args.Path = resolved
	}

	// Security: classify and check patch operation
	risk := danger.ClassifyPath(args.Path)
	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "patch", Resource: args.Path, Risk: risk,
	}, t.trustedClasses); err != nil {
		return jsonError(err.Error())
	}

	// Read the file without following symlinks
	f, err := os.OpenFile(args.Path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return jsonError(fmt.Sprintf("file not found: %s", args.Path))
		}
		return jsonError(fmt.Sprintf("cannot read %q: %v", args.Path, err))
	}
	defer f.Close()

	// Reject files that would exhaust memory during the read/edit/write cycle.
	info, err := f.Stat()
	if err != nil {
		return jsonError(fmt.Sprintf("cannot stat %q: %v", args.Path, err))
	}
	if info.Size() > maxFileReadBytes {
		return jsonError(fmt.Sprintf("file too large (%d bytes, max %d)", info.Size(), maxFileReadBytes))
	}
	origMode := info.Mode().Perm()

	// Read content through the opened fd (not re-opening the path)
	var sb strings.Builder
	_, err = io.Copy(&sb, f)
	if err != nil {
		return jsonError(fmt.Sprintf("cannot read %q: %v", args.Path, err))
	}
	original := sb.String()

	// Check that old_string exists
	if !strings.Contains(original, args.OldString) {
		return jsonError(fmt.Sprintf("old_string not found in %q. Use search_files to find the correct string.", args.Path))
	}

	var modified string
	if args.ReplaceAll {
		modified = strings.ReplaceAll(original, args.OldString, args.NewString)
	} else {
		modified = strings.Replace(original, args.OldString, args.NewString, 1)
	}
	if len(modified) > maxFileReadBytes {
		return jsonError(fmt.Sprintf("patch result too large (%d bytes, max %d)", len(modified), maxFileReadBytes))
	}

	// Generate a simple diff
	diff := fmt.Sprintf("--- a/%s\n+++ b/%s\n@@ -1 +1 @@\n-%s\n+%s\n",
		args.Path, args.Path,
		truncateDiff(original, 100),
		truncateDiff(modified, 100),
	)

	// When sandbox mode is active, route the write through the container so a
	// read-only workspace mount is actually enforced.
	if t.containerName != "" {
		if err := sandboxWriteFile(t.containerName, args.Path, []byte(modified), origMode); err != nil {
			return jsonError(fmt.Sprintf("cannot write %q via sandbox: %v", args.Path, err))
		}
		return jsonResult(patchResult{
			Success: true,
			Diff:    wrapUntrusted(t.toolCtx(), "patch:"+args.Path, diff),
		})
	}

	// Atomic write via temp file + rename to prevent TOCTOU symlink races.
	// The temp file is created in the same directory (same filesystem),
	// and os.Rename atomically replaces the directory entry without
	// following symlinks.
	dir := filepath.Dir(args.Path)
	tmpFile, err := os.CreateTemp(dir, ".tmp_patch_*")
	if err != nil {
		return jsonError(fmt.Sprintf("cannot create temp file: %v", err))
	}
	tmpPath := tmpFile.Name()

	if _, err := tmpFile.Write([]byte(modified)); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return jsonError(fmt.Sprintf("cannot write %q: %v", args.Path, err))
	}
	if err := tmpFile.Chmod(origMode); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return jsonError(fmt.Sprintf("cannot set permissions %q: %v", args.Path, err))
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return jsonError(fmt.Sprintf("cannot close temp file: %v", err))
	}

	// Atomic rename — replaces target directory entry without following symlinks
	if err := os.Rename(tmpPath, args.Path); err != nil {
		os.Remove(tmpPath)
		return jsonError(fmt.Sprintf("cannot write %q: %v", args.Path, err))
	}

	return jsonResult(patchResult{
		Success: true,
		Diff:    wrapUntrusted(t.toolCtx(), "patch:"+args.Path, diff),
	})
}

// ── Helpers ────────────────────────────────────────────────────────────

func jsonError(msg string) (string, error) {
	data, _ := json.Marshal(map[string]string{"error": msg})
	return string(data), nil
}

func jsonResult(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return jsonError("marshal error: " + err.Error())
	}
	return string(data), nil
}

// isBinary checks if a byte slice looks like binary content.
// Returns true if a null byte is found or if more than 30% of bytes
// are non-printable (excluding common whitespace like \n, \r, \t).
func isBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	nonPrintable := 0
	for _, b := range data {
		if b == 0 {
			return true
		}
		if b < 0x09 || (b > 0x0d && b < 0x20) || b > 0x7e {
			nonPrintable++
		}
	}
	return float64(nonPrintable)/float64(len(data)) > 0.30
}

// readLinesWithCount reads lines from an open file, returning content
// and total line count in a single pass. offset is 1-based, limit caps lines.
// The returned content is capped at maxReadBytes to avoid unbounded memory
// consumption from huge lines or huge limits.
func readLinesWithCount(f *os.File, offset, limit int) (string, int, error) {
	var out strings.Builder
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	lineNum := 0
	start := offset
	end := offset + limit - 1
	truncated := false

	for scanner.Scan() {
		lineNum++
		if lineNum < start {
			continue
		}
		if lineNum > end {
			continue // count total even beyond limit
		}
		line := scanner.Text()
		formatted := fmt.Sprintf("%d|%s\n", lineNum, line)
		if !truncated && out.Len()+len(formatted) > maxReadBytes {
			out.WriteString("... [truncated]\n")
			truncated = true
			// Continue scanning only to count total lines.
			continue
		}
		if !truncated {
			out.WriteString(formatted)
		}
	}

	// If no limit was set (limit=0), continue counting past start
	if limit > 0 {
		for scanner.Scan() {
			lineNum++
		}
	}

	return strings.TrimSuffix(out.String(), "\n"), lineNum, scanner.Err()
}

// resolveReadPath resolves symlinks in the directory components of path,
// leaving the final path component untouched. This prevents intermediate
// directory symlinks from bypassing risk classification: a path like
// "workspace/link_to_etc/passwd" where link_to_etc -> /etc resolves to
// "/etc/passwd" and is classified as system_write instead of local_write.
//
// The final component is kept unresolved so callers can still open with
// O_NOFOLLOW, preserving the existing policy of rejecting symlink final
// components. If the directory part does not exist, the original path is
// returned (the open will fail with a not-found error).
func resolveReadPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is empty")
	}

	// Work with an absolute, cleaned path.
	abs := path
	if !filepath.IsAbs(abs) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cannot determine working directory: %v", err)
		}
		abs = filepath.Join(cwd, path)
	}
	abs = filepath.Clean(abs)

	dir := filepath.Dir(abs)
	base := filepath.Base(abs)

	// Resolve only directory symlinks; keep the final component as-is.
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		// The directory part doesn't exist. Return the original path so the
		// caller can produce a sensible "not found" error.
		return path, nil
	}

	return filepath.Join(resolvedDir, base), nil
}

// classifyResolvedPath resolves directory symlinks in path (leaving the final
// component untouched) and returns the danger classification of the resolved
// path. Read-only tools use this so that a symlinked directory pointing outside
// the workspace is classified by its real target rather than by the lexical
// workspace path.
func classifyResolvedPath(path string) danger.RiskClass {
	resolved, err := resolveReadPath(path)
	if err != nil {
		return danger.ClassifyPath(path)
	}
	return danger.ClassifyPath(resolved)
}

// confineToCWD resolves path relative to the current working directory and
// rejects paths that escape the working directory via ".." traversal or are
// absolute paths outside the CWD. Returns the cleaned absolute path on success.
func confineToCWD(path string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("cannot determine working directory: %v", err)
	}
	cwdResolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", fmt.Errorf("cannot resolve working directory: %v", err)
	}

	// Resolve to absolute path
	var abs string
	if filepath.IsAbs(path) {
		abs = filepath.Clean(path)
	} else {
		abs = filepath.Join(cwd, path)
	}

	// Resolve symlinks so a path that is lexically under CWD but traverses a
	// symlink cannot escape (e.g., cwd/link -> /etc, cwd/link/file would
	// resolve to /etc/file). If the full path or an intermediate directory
	// does not exist yet (common for write_file), walk up to the deepest
	// existing ancestor, resolve that, and re-attach the missing suffix.
	// Missing directories cannot be symlinks, so they cannot be used to escape.
	absResolved := abs
	resolved := false
	cur := abs
	for cur != "/" && cur != "" {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			suffix := strings.TrimPrefix(abs, cur)
			if suffix == "" {
				absResolved = r
			} else {
				absResolved = r + suffix
			}
			resolved = true
			break
		}
		cur = filepath.Dir(cur)
	}
	if !resolved {
		// Nothing resolvable along the path (should not happen in practice,
		// since / always exists). Fall back to lexical path.
		absResolved = abs
	}

	// Allow paths under ~/.odek/ even when outside CWD — the agent
	// frequently writes memory and other state to this directory. The
	// carve-out deliberately EXCLUDES odek's trust anchors (config.json,
	// secrets.env, skills/): a confined agent that can rewrite its own
	// config can disable the sandbox or enable YOLO mode on the next run,
	// and a dropped SKILL.md is auto-loaded into future prompts. Skills
	// are legitimately written through the dedicated skill_save/skill_patch
	// tools, not the generic file tools.
	home, homeErr := os.UserHomeDir()
	if homeErr == nil {
		odekPrefix := home + "/.odek/"
		if strings.HasPrefix(absResolved, odekPrefix) {
			if isProtectedOdekPath(strings.TrimPrefix(absResolved, odekPrefix)) {
				return "", fmt.Errorf("path %q is a protected odek configuration path and cannot be written by file tools", path)
			}
			return abs, nil
		}
	}

	// Check that the resolved path is within CWD
	if !strings.HasPrefix(absResolved, cwdResolved+string(filepath.Separator)) && absResolved != cwdResolved {
		return "", fmt.Errorf("path %q escapes the working directory", path)
	}

	return abs, nil
}

// isProtectedOdekPath reports whether rel (a path relative to ~/.odek/,
// already cleaned by confineToCWD) names one of odek's trust anchors that
// must not be writable through the generic file tools. Keep in sync with
// the SystemWrite escalation in danger.ClassifyPath.
func isProtectedOdekPath(rel string) bool {
	// Case-folding defends against case-insensitive filesystems (macOS APFS):
	// ~/.odek/CONFIG.JSON and ~/.odek/config.json are the same file.
	rel = strings.ToLower(filepath.Clean(rel))

	// Exact-file trust anchors. Rewriting any of these can disable safety
	// policy, exfiltrate secrets, or persist attacker control.
	protectedExact := []string{
		"config.json",
		"secrets.env",
		"identity.md",
		"schedules.json",
		"schedule-state.json",
		"schedules.lock",
		"mcp_approvals.json",
		"mcp_tool_approvals.json",
		"restart.json",
		"telegram.lock",
		"telegram.pid",
		"schedule.pid",
		"schedule.log",
	}
	for _, p := range protectedExact {
		if rel == p {
			return true
		}
	}

	// Directory trust anchors. Anything inside these directories is protected.
	protectedDirs := []string{
		"skills",   // auto-loaded into future prompts
		"sessions", // conversation history & auth tokens
		"audit",    // forensic audit log
		"plans",    // persisted task plans
	}
	for _, d := range protectedDirs {
		if rel == d || strings.HasPrefix(rel, d+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func truncateDiff(s string, maxLen int) string {
	// Take first line for diff display
	firstLine := strings.SplitN(s, "\n", 2)[0]
	if len(firstLine) > maxLen {
		return firstLine[:maxLen] + "..."
	}
	return firstLine
}

// ── BatchRead Tool ────────────────────────────────────────────────────
//
// batch_read reads multiple files in a single tool call, executing reads
// concurrently with bounded goroutines. This replaces N serial read_file
// calls with one parallel batch — the single biggest perf win for
// code-understanding tasks (fast_read benchmark was 23% due to serial
// reads across 10 iterations).
//
// Security: each file path is individually classified and gated through
// the same danger.CheckOperation path as read_file.

const maxBatchFiles = 10

type batchReadTool struct {
	ctxTool
	dangerousConfig danger.DangerousConfig
}

func (t *batchReadTool) Name() string { return "batch_read" }

func (t *batchReadTool) Description() string {
	return `Read multiple files in a single call. Files are read in parallel and results are returned as an array.
Each file entry supports offset and limit for pagination (same as read_file).
Use this when you need to read several files at once — it's faster than N sequential read_file calls.

Returns an array of results, one per file, each with:
  path — the file path requested
  content — file content with line numbers (or truncated by offset/limit)
  total_lines — total lines in the file
  error — error message if the file couldn't be read (file not found, binary, etc.)`
}

type batchReadFileArg struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

type batchReadFileResult struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	TotalLines int    `json:"total_lines"`
	Error      string `json:"error,omitempty"`
}

type batchReadArgs struct {
	Files []batchReadFileArg `json:"files"`
}

type batchReadResult struct {
	Results []batchReadFileResult `json:"results"`
}

func (t *batchReadTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"files": map[string]any{
				"type":        "array",
				"description": "Files to read (max 10). Each entry: {path, offset?, limit?}.",
				"minItems":    1,
				"maxItems":    maxBatchFiles,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "Path to the file to read (absolute or relative).",
						},
						"offset": map[string]any{
							"type":        "integer",
							"description": "Line number to start from (1-indexed, default: 1).",
						},
						"limit": map[string]any{
							"type":        "integer",
							"description": "Maximum lines to return (default: 500, max: 2000).",
						},
					},
					"required": []string{"path"},
				},
			},
		},
		"required": []string{"files"},
	}
}

func (t *batchReadTool) Call(argsJSON string) (result string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("batch_read: panic: %v", r)
			result = `{"error":"internal tool error"}`
		}
	}()
	var args batchReadArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}
	if len(args.Files) == 0 {
		return jsonError("at least one file is required")
	}
	if len(args.Files) > maxBatchFiles {
		return jsonError(fmt.Sprintf("max %d files per batch_read call", maxBatchFiles))
	}

	results := parallelMap(args.Files, toolConcurrency(), t.readSingle,
		func(f batchReadFileArg, p any) batchReadFileResult {
			return batchReadFileResult{Path: f.Path, Error: fmt.Sprintf("internal error: %v", p)}
		})

	return jsonResult(batchReadResult{Results: results})
}

func (t *batchReadTool) readSingle(arg batchReadFileArg) batchReadFileResult {
	if arg.Path == "" {
		return batchReadFileResult{Error: "path is required"}
	}
	if arg.Offset < 0 {
		arg.Offset = 1
	}
	if arg.Offset == 0 {
		arg.Offset = 1
	}
	if arg.Limit <= 0 {
		arg.Limit = 500
	}
	if arg.Limit > maxLines {
		arg.Limit = maxLines
	}

	// Security: resolve directory symlinks before classification.
	resolvedPath, err := resolveReadPath(arg.Path)
	if err != nil {
		return batchReadFileResult{Path: arg.Path, Error: err.Error()}
	}

	// Security: classify path and check operation
	risk := danger.ClassifyPath(resolvedPath)
	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "batch_read", Resource: resolvedPath, Risk: risk,
	}, nil); err != nil {
		return batchReadFileResult{Path: arg.Path, Error: err.Error()}
	}

	// Open without following symlinks
	f, err := os.OpenFile(resolvedPath, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return batchReadFileResult{Path: arg.Path, Error: fmt.Sprintf("file not found: %s", arg.Path)}
		}
		return batchReadFileResult{Path: arg.Path, Error: fmt.Sprintf("cannot open %q: %v", arg.Path, err)}
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return batchReadFileResult{Path: arg.Path, Error: fmt.Sprintf("cannot stat %q: %v", arg.Path, err)}
	}
	if info.IsDir() {
		return batchReadFileResult{Path: arg.Path, Error: fmt.Sprintf("%q is a directory — use tree or search_files(target='files') to list its contents", arg.Path)}
	}

	// Binary check from sample
	sample := make([]byte, 8192)
	n, _ := f.Read(sample)
	if isBinary(sample[:n]) {
		return batchReadFileResult{Path: arg.Path, Error: fmt.Sprintf("%q appears to be a binary file", arg.Path)}
	}

	// Seek back and read
	if _, err := f.Seek(0, 0); err != nil {
		return batchReadFileResult{Path: arg.Path, Error: fmt.Sprintf("cannot seek %q: %v", arg.Path, err)}
	}

	content, totalLines, err := readLinesWithCount(f, arg.Offset, arg.Limit)
	if err != nil {
		return batchReadFileResult{Path: arg.Path, Error: fmt.Sprintf("cannot read %q: %v", arg.Path, err)}
	}

	return batchReadFileResult{
		Path:       arg.Path,
		Content:    wrapUntrusted(t.toolCtx(), resolvedPath, content),
		TotalLines: totalLines,
	}
}

// ── Glob Tool ─────────────────────────────────────────────────────────
//
// glob finds files matching a pattern. Uses filepath.Glob for simple
// patterns (avoiding a full directory walk). For complex patterns with
// path separators, falls back to filepath.Walk. Returns files sorted
// by modification time (newest first).

type globTool struct {
	ctxTool
	dangerousConfig danger.DangerousConfig
}

func (t *globTool) Name() string { return "glob" }

func (t *globTool) Description() string {
	return `Find files by glob pattern. Returns matching file paths sorted by modification time (newest first).
Zero-fork — pure Go filepath walk with no subprocess.

Examples:
  glob(pattern="*.go")       — all Go files in current directory
  glob(pattern="**/*.py")    — all Python files recursively
  glob(pattern="*.json", path="config/") — JSON files in config/
  glob(pattern="test_*")     — files starting with test_

Returns an array of {path, size, is_dir} for each match.`
}

type globArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
	Limit   int    `json:"limit"`
}

type globMatch struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	IsDir bool   `json:"is_dir"`
}

type globResult struct {
	Matches []globMatch `json:"matches"`
	Error   string      `json:"error,omitempty"`
}

func (t *globTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Glob pattern to match (e.g. '*.go', '**/*.py', 'test_*').",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Root directory to search in (default: current directory '.').",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum results to return (default: 50).",
			},
		},
		"required": []string{"pattern"},
	}
}

func (t *globTool) Call(argsJSON string) (result string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("glob: panic: %v", r)
			result = `{"error":"internal tool error"}`
		}
	}()
	var args globArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}
	if args.Pattern == "" {
		return jsonError("pattern is required")
	}
	if args.Path == "" {
		args.Path = "."
	}
	if args.Limit <= 0 {
		args.Limit = maxMatches
	}
	if args.Limit > maxGlobMatches {
		args.Limit = maxGlobMatches
	}

	// Security: classify search root path
	risk := danger.ClassifyPath(args.Path)
	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "glob", Resource: args.Path, Risk: risk,
	}, nil); err != nil {
		return jsonError(err.Error())
	}

	paths, err := confinedGlob(args.Path, args.Pattern, args.Limit, true)
	if err != nil {
		return jsonError(fmt.Sprintf("invalid glob %q: %v", args.Pattern, err))
	}

	var matches []globMatch
	for _, p := range paths {
		// Use Lstat so symlinks are not followed to their targets.
		info, err := os.Lstat(p)
		if err != nil {
			continue
		}
		matches = append(matches, globMatch{
			Path:  p,
			Size:  info.Size(),
			IsDir: info.IsDir(),
		})
	}

	// Sort by modification time (newest first)
	sort.Slice(matches, func(i, j int) bool {
		fi, _ := os.Stat(matches[i].Path)
		fj, _ := os.Stat(matches[j].Path)
		if fi == nil || fj == nil {
			return matches[i].Path < matches[j].Path
		}
		return fi.ModTime().After(fj.ModTime())
	})

	for i := range matches {
		matches[i].Path = wrapUntrusted(t.toolCtx(), "glob:"+args.Path, matches[i].Path)
	}

	return jsonResult(globResult{Matches: matches})
}

// ── FileInfo Tool ─────────────────────────────────────────────────────
//
// file_info returns metadata about a file or directory without reading
// content. Uses Lstat (does NOT follow symlinks) for the stat call,
// with an O_NOFOLLOW open to atomically confirm the target exists.
// Replaces shell: ls -la / stat / test -f forks.

type fileInfoTool struct {
	ctxTool
	dangerousConfig danger.DangerousConfig
	restrictToCWD   bool // when true, reject paths escaping the working directory
}

func (t *fileInfoTool) Name() string { return "file_info" }

func (t *fileInfoTool) Description() string {
	return `Get file or directory metadata without reading content.
Returns size, modification time, file mode, and type flags.
Uses Lstat — does NOT follow symlinks.
Zero-fork — pure Go file stat with no subprocess.`
}

type fileInfoArgs struct {
	Path string `json:"path"`
}

type fileInfoResult struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	ModTime   string `json:"mod_time"` // ISO8601
	Mode      string `json:"mode"`     // e.g. -rw-r--r--
	IsDir     bool   `json:"is_dir"`
	IsSymlink bool   `json:"is_symlink"`
	IsRegular bool   `json:"is_regular"`
	Error     string `json:"error,omitempty"`
}

func (t *fileInfoTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the file or directory to stat. Uses Lstat (does not follow symlinks).",
			},
		},
		"required": []string{"path"},
	}
}

func (t *fileInfoTool) Call(argsJSON string) (result string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("file_info: panic: %v", r)
			result = `{"error":"internal tool error"}`
		}
	}()
	var args fileInfoArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}
	if args.Path == "" {
		return jsonError("path is required")
	}

	// Path confinement: when restrictToCWD is enabled, reject paths that
	// escape the working directory via ".." traversal or absolute paths.
	if t.restrictToCWD {
		resolved, err := confineToCWD(args.Path)
		if err != nil {
			return jsonError(err.Error())
		}
		args.Path = resolved
	}

	// Security: classify path
	risk := danger.ClassifyPath(args.Path)
	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "file_info", Resource: args.Path, Risk: risk,
	}, nil); err != nil {
		return jsonError(err.Error())
	}

	// Lstat — doesn't follow symlinks, tells us if it's a symlink
	lInfo, err := os.Lstat(args.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return jsonResult(fileInfoResult{
				Path:  args.Path,
				Error: fmt.Sprintf("file not found: %s", args.Path),
			})
		}
		return jsonResult(fileInfoResult{
			Path:  args.Path,
			Error: fmt.Sprintf("cannot stat %q: %v", args.Path, err),
		})
	}

	fi := fileInfoResult{
		Path:      args.Path,
		Size:      lInfo.Size(),
		ModTime:   lInfo.ModTime().UTC().Format(time.RFC3339),
		Mode:      lInfo.Mode().String(),
		IsDir:     lInfo.IsDir(),
		IsSymlink: lInfo.Mode()&os.ModeSymlink != 0,
		IsRegular: lInfo.Mode().IsRegular(),
	}

	// file_info output originates from the filesystem trust boundary, so
	// mark the returned path as untrusted.
	fi.Path = wrapUntrusted(t.toolCtx(), "file_info:"+args.Path, fi.Path)

	return jsonResult(fi)
}

// Ensure tools implement odek.Tool
var (
	_ odek.Tool = (*batchReadTool)(nil)
	_ odek.Tool = (*globTool)(nil)
	_ odek.Tool = (*fileInfoTool)(nil)
)
