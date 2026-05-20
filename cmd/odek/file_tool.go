package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BackendStack21/kode/internal/danger"
)

// ── ReadFile Tool ──────────────────────────────────────────────────────

const maxLines = 2000

type readFileTool struct {
	dangerousConfig danger.DangerousConfig
}

func (t *readFileTool) Name() string { return "read_file" }

func (t *readFileTool) Description() string {
	return `Read a text file with line numbers and pagination.
Returns file content prefixed with line numbers (LINE_NUM|CONTENT).
Use offset and limit to read specific sections of large files.
Cannot read binary files — use shell for that.`
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

	// Security: check if this path requires approval
	risk := danger.ClassifyPath(args.Path)
	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "read_file", Resource: args.Path, Risk: risk,
	}, nil); err != nil {
		return jsonError(err.Error())
	}

	// Check file exists and is not a directory
	info, err := os.Stat(args.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return jsonError(fmt.Sprintf("file not found: %s", args.Path))
		}
		return jsonError(fmt.Sprintf("cannot access %q: %v", args.Path, err))
	}
	if info.IsDir() {
		return jsonError(fmt.Sprintf("%q is a directory, not a file", args.Path))
	}

	// Check for binary content — only examine first 8KB
	f, err := os.Open(args.Path)
	if err != nil {
		return jsonError(fmt.Sprintf("cannot open %q: %v", args.Path, err))
	}
	defer f.Close()

	sample := make([]byte, 8192)
	n, _ := f.Read(sample)
	sample = sample[:n]
	if isBinary(sample) {
		return jsonError(fmt.Sprintf("%q appears to be a binary file — use shell to read it", args.Path))
	}
	f.Close() // close to re-read from start

	// Count total lines first
	totalLines := countLines(args.Path)

	// Read the requested range
	content, err := readLines(args.Path, args.Offset, args.Limit)
	if err != nil {
		return jsonError(fmt.Sprintf("cannot read %q: %v", args.Path, err))
	}

	result := readFileResult{
		Content:    content,
		TotalLines: totalLines,
	}
	return jsonResult(result)
}

// ── WriteFile Tool ─────────────────────────────────────────────────────

type writeFileTool struct {
	dangerousConfig danger.DangerousConfig
	trustedClasses map[danger.RiskClass]bool
}

func (t *writeFileTool) Name() string { return "write_file" }

func (t *writeFileTool) Description() string {
	return `Write content to a file, completely replacing existing content.
Creates parent directories automatically. OVERWRITES the entire file.
Use patch for targeted edits.`
}

func (t *writeFileTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the file to write (will be created/overwritten).",
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

	// Security: classify and check write operation
	risk := danger.ClassifyPath(args.Path)
	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "write_file", Resource: args.Path, Risk: risk,
	}, t.trustedClasses); err != nil {
		return jsonError(err.Error())
	}

	// Create parent directories
	dir := filepath.Dir(args.Path)
	if dir != "." && dir != "/" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return jsonError(fmt.Sprintf("cannot create directory %q: %v", dir, err))
		}
	}

	if err := os.WriteFile(args.Path, []byte(args.Content), 0644); err != nil {
		return jsonError(fmt.Sprintf("cannot write %q: %v", args.Path, err))
	}

	return jsonResult(writeFileResult{
		Success: true,
		Path:    args.Path,
	})
}

// ── SearchFiles Tool ───────────────────────────────────────────────────

const maxMatches = 50

type searchFilesTool struct {
	dangerousConfig danger.DangerousConfig
}

func (t *searchFilesTool) Name() string { return "search_files" }

func (t *searchFilesTool) Description() string {
	return `Search file contents or find files by name.
Two modes: target="content" searches inside files for a regex pattern,
target="files" finds files by glob pattern.
Results are sorted by modification time (newest first).`
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
				"description": "Directory or file to search in (default: current directory '.').",
			},
			"file_glob": map[string]any{
				"type":        "string",
				"description": "Filter files by pattern in content mode (e.g. '*.go' to only search Go files).",
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

func (t *searchFilesTool) searchContent(args searchFilesArgs) (string, error) {
	re, err := regexp.Compile(args.Pattern)
	if err != nil {
		return jsonError(fmt.Sprintf("invalid regex pattern %q: %v", args.Pattern, err))
	}

	var matches []searchMatch
	limit := args.Limit

	err = filepath.Walk(args.Path, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip inaccessible files
		}
		if info.IsDir() {
			// Skip hidden directories
			if strings.HasPrefix(info.Name(), ".") && info.Name() != "." {
				return filepath.SkipDir
			}
			return nil
		}

		// Apply file_glob filter
		if args.FileGlob != "" {
			match, _ := filepath.Match(args.FileGlob, info.Name())
			if !match {
				return nil
			}
		}

		// Skip binary files
		sample := make([]byte, 512)
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		n, _ := f.Read(sample)
		f.Close()
		if isBinary(sample[:n]) {
			return nil
		}

		// Search line by line
		f, err = os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if re.MatchString(line) {
				matches = append(matches, searchMatch{
					Path:    path,
					Line:    lineNum,
					Content: strings.TrimSpace(line),
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

	return jsonResult(searchFilesResult{Matches: matches})
}

func (t *searchFilesTool) searchFiles(args searchFilesArgs) (string, error) {
	// Use the pattern as a glob
	pattern := args.Pattern
	searchDir := args.Path

	var matches []searchMatch
	limit := args.Limit

	// If pattern has path separators, use filepath.Glob instead
	if strings.Contains(pattern, "/") || strings.Contains(pattern, string(filepath.Separator)) {
		globMatches, err := filepath.Glob(filepath.Join(searchDir, pattern))
		if err != nil {
			return jsonError(fmt.Sprintf("invalid glob %q: %v", pattern, err))
		}
		for _, p := range globMatches {
			info, err := os.Stat(p)
			if err == nil && !info.IsDir() {
				matches = append(matches, searchMatch{Path: p})
				if len(matches) >= limit {
					break
				}
			}
		}
	} else {
		// Simple name match — walk the directory once
		filepath.Walk(searchDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil {
				return nil
			}
			if info.IsDir() {
				if strings.HasPrefix(info.Name(), ".") && info.Name() != "." {
					return filepath.SkipDir
				}
				return nil
			}
			match, _ := filepath.Match(pattern, info.Name())
			if match {
				matches = append(matches, searchMatch{Path: path})
				if len(matches) >= limit {
					return filepath.SkipAll
				}
			}
			return nil
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

	return jsonResult(searchFilesResult{Matches: matches})
}

// ── Patch Tool ─────────────────────────────────────────────────────────

type patchTool struct {
	dangerousConfig danger.DangerousConfig
	trustedClasses  map[danger.RiskClass]bool
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

	// Security: classify and check patch operation
	risk := danger.ClassifyPath(args.Path)
	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "patch", Resource: args.Path, Risk: risk,
	}, t.trustedClasses); err != nil {
		return jsonError(err.Error())
	}

	// Read the file
	data, err := os.ReadFile(args.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return jsonError(fmt.Sprintf("file not found: %s", args.Path))
		}
		return jsonError(fmt.Sprintf("cannot read %q: %v", args.Path, err))
	}

	original := string(data)

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

	// Generate a simple diff
	diff := fmt.Sprintf("--- a/%s\n+++ b/%s\n@@ -1 +1 @@\n-%s\n+%s\n",
		args.Path, args.Path,
		truncateDiff(original, 100),
		truncateDiff(modified, 100),
	)

	if err := os.WriteFile(args.Path, []byte(modified), 0644); err != nil {
		return jsonError(fmt.Sprintf("cannot write %q: %v", args.Path, err))
	}

	return jsonResult(patchResult{
		Success: true,
		Diff:    diff,
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

func countLines(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	count := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		count++
	}
	return count
}

func readLines(path string, offset, limit int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var out strings.Builder
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	lineNum := 0
	start := offset
	end := offset + limit - 1

	for scanner.Scan() {
		lineNum++
		if lineNum < start {
			continue
		}
		if lineNum > end {
			break
		}
		out.WriteString(fmt.Sprintf("%d|%s\n", lineNum, scanner.Text()))
	}

	return strings.TrimSuffix(out.String(), "\n"), scanner.Err()
}

func truncateDiff(s string, maxLen int) string {
	// Take first line for diff display
	firstLine := strings.SplitN(s, "\n", 2)[0]
	if len(firstLine) > maxLen {
		return firstLine[:maxLen] + "..."
	}
	return firstLine
}
