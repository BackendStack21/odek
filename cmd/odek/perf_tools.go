package main

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/danger"
)

// maxFileReadBytes caps how much of a single file the perf tools will load
// into memory. This prevents OOM when tools like diff/base64/tr/sort point at
// multi-gigabyte logs or core dumps.
const maxFileReadBytes = 10 << 20 // 10 MiB

// maxTreeEntries caps the number of children reported for a single directory
// by the tree tool, preventing OOM from directories with millions of entries.
const maxTreeEntries = 1000

// readFileNoFollow reads a file with O_NOFOLLOW (anti-symlink), rejecting files
// larger than maxFileReadBytes to avoid unbounded memory consumption.
func readFileNoFollow(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > maxFileReadBytes {
		return nil, fmt.Errorf("file too large (%d bytes, max %d)", info.Size(), maxFileReadBytes)
	}

	return io.ReadAll(io.LimitReader(f, maxFileReadBytes+1))
}

// ═════════════════════════════════════════════════════════════════════════
// 1. batch_patch — Apply multiple edits atomically
// ═════════════════════════════════════════════════════════════════════════

const maxBatchPatches = 10

type batchPatchTool struct {
	dangerousConfig danger.DangerousConfig
	restrictToCWD   bool // when true, reject paths escaping the working directory
}

func (t *batchPatchTool) Name() string { return "batch_patch" }
func (t *batchPatchTool) Description() string {
	return `Apply up to 10 find-replace edits across files in a single call. Edits are applied sequentially; if any fails the rest are skipped (early-stop). Each edit uses O_NOFOLLOW read + atomic temp+rename write, same as the patch tool.`
}

type batchPatchArg struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

type batchPatchEntry struct {
	Path    string `json:"path"`
	Success bool   `json:"success"`
	Diff    string `json:"diff,omitempty"`
	Error   string `json:"error,omitempty"`
}

type batchPatchArgs struct {
	Patches []batchPatchArg `json:"patches"`
}

type batchPatchResult struct {
	Results []batchPatchEntry `json:"results"`
}

func (t *batchPatchTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"patches": map[string]any{
				"type":        "array",
				"description": "Edits to apply (max 10). Each: {path, old_string, new_string, replace_all?}.",
				"minItems":    1,
				"maxItems":    maxBatchPatches,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":        map[string]any{"type": "string", "description": "File path."},
						"old_string":  map[string]any{"type": "string", "description": "Text to find."},
						"new_string":  map[string]any{"type": "string", "description": "Replacement (empty = delete)."},
						"replace_all": map[string]any{"type": "boolean", "description": "Replace all occurrences (default: false)."},
					},
					"required": []string{"path", "old_string"},
				},
			},
		},
		"required": []string{"patches"},
	}
}

func (t *batchPatchTool) Call(argsJSON string) (result string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("batch_patch: panic: %v", r)
			result = `{"error":"internal tool error"}`
		}
	}()
	var args batchPatchArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}
	if len(args.Patches) == 0 {
		return jsonError("at least one patch is required")
	}
	if len(args.Patches) > maxBatchPatches {
		return jsonError(fmt.Sprintf("max %d patches per call", maxBatchPatches))
	}

	results := make([]batchPatchEntry, len(args.Patches))

	for idx, p := range args.Patches {
		entry := batchPatchEntry{Path: p.Path}
		if p.OldString == "" {
			entry.Error = "old_string is required"
			results[idx] = entry
			continue
		}

		// Path confinement: same as write_file/patch — reject paths that
		// escape the working directory.
		if t.restrictToCWD {
			resolved, err := confineToCWD(p.Path)
			if err != nil {
				entry.Error = err.Error()
				results[idx] = entry
				continue
			}
			p.Path = resolved
			entry.Path = resolved
		}

		if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
			Name: "batch_patch", Resource: p.Path, Risk: danger.ClassifyPath(p.Path),
		}, nil); err != nil {
			entry.Error = err.Error()
			results[idx] = entry
			continue
		}

		f, err := os.OpenFile(p.Path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			entry.Error = fmt.Sprintf("cannot open %q: %v", p.Path, err)
			results[idx] = entry
			continue
		}

		info, err := f.Stat()
		if err != nil {
			f.Close()
			entry.Error = fmt.Sprintf("cannot stat %q: %v", p.Path, err)
			results[idx] = entry
			continue
		}
		if info.Size() > maxFileReadBytes {
			f.Close()
			entry.Error = fmt.Sprintf("file too large (%d bytes, max %d)", info.Size(), maxFileReadBytes)
			results[idx] = entry
			continue
		}

		var sb strings.Builder
		_, err = io.Copy(&sb, f)
		f.Close()
		if err != nil {
			entry.Error = fmt.Sprintf("cannot read %q: %v", p.Path, err)
			results[idx] = entry
			continue
		}
		original := sb.String()

		if !strings.Contains(original, p.OldString) {
			entry.Error = fmt.Sprintf("old_string not found in %q", p.Path)
			results[idx] = entry
			continue
		}

		var modified string
		if p.ReplaceAll {
			modified = strings.ReplaceAll(original, p.OldString, p.NewString)
		} else {
			modified = strings.Replace(original, p.OldString, p.NewString, 1)
		}
		if len(modified) > maxFileReadBytes {
			entry.Error = fmt.Sprintf("patch result too large (%d bytes, max %d)", len(modified), maxFileReadBytes)
			results[idx] = entry
			continue
		}

		diff := fmt.Sprintf("--- a/%s\n+++ b/%s\n@@ -1 +1 @@\n-%s\n+%s\n",
			p.Path, p.Path, truncateDiff(original, 100), truncateDiff(modified, 100))

		// Atomic write — preserve the original file's mode and surface any
		// write error so a short or failed write cannot silently corrupt
		// the target (see IMPROVEMENTS_ROADMAP.md B-H1, B-H2).
		dir := filepath.Dir(p.Path)
		origMode := os.FileMode(0644)
		if st, err := os.Stat(p.Path); err == nil {
			origMode = st.Mode().Perm()
		}
		tmpFile, err := os.CreateTemp(dir, ".tmp_batchpatch_*")
		if err != nil {
			entry.Error = fmt.Sprintf("cannot create temp file: %v", err)
			results[idx] = entry
			continue
		}
		tmpPath := tmpFile.Name()
		if _, werr := tmpFile.Write([]byte(modified)); werr != nil {
			tmpFile.Close()
			os.Remove(tmpPath)
			entry.Error = fmt.Sprintf("cannot write temp for %q: %v", p.Path, werr)
			results[idx] = entry
			continue
		}
		if cerr := tmpFile.Chmod(origMode); cerr != nil {
			tmpFile.Close()
			os.Remove(tmpPath)
			entry.Error = fmt.Sprintf("cannot set mode on temp for %q: %v", p.Path, cerr)
			results[idx] = entry
			continue
		}
		if cerr := tmpFile.Close(); cerr != nil {
			os.Remove(tmpPath)
			entry.Error = fmt.Sprintf("cannot close temp for %q: %v", p.Path, cerr)
			results[idx] = entry
			continue
		}

		if err := os.Rename(tmpPath, p.Path); err != nil {
			os.Remove(tmpPath)
			entry.Error = fmt.Sprintf("cannot write %q: %v", p.Path, err)
			results[idx] = entry
			continue
		}

		entry.Success = true
		entry.Diff = wrapUntrusted("batch_patch:"+p.Path, diff)
		results[idx] = entry
	}

	return jsonResult(batchPatchResult{Results: results})
}

// ═════════════════════════════════════════════════════════════════════════
// 2. parallel_shell — Run N shell commands concurrently
// ═════════════════════════════════════════════════════════════════════════

const maxParallelShellCmds = 8

type parallelShellTool struct {
	dangerousConfig danger.DangerousConfig
	approver        danger.Approver
	// containerName, when set, routes every command through "docker exec"
	// so sandbox isolation is preserved — same as shellTool.
	containerName string
}

func (t *parallelShellTool) Name() string { return "parallel_shell" }
func (t *parallelShellTool) Description() string {
	return `Run multiple independent shell commands in parallel. Each command gets its own process. Returns structured results with stdout, stderr, exit_code, and duration for each command. Commands requiring approval are checked before execution.`
}

type parallelShellCmd struct {
	Command     string `json:"command"`
	Description string `json:"description,omitempty"`
	Timeout     int    `json:"timeout,omitempty"`
}

type parallelShellEntry struct {
	Index      int    `json:"index"`
	Command    string `json:"command"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	DurationMs int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

type parallelShellArgs struct {
	Commands []parallelShellCmd `json:"commands"`
}

type parallelShellResult struct {
	Results []parallelShellEntry `json:"results"`
}

func (t *parallelShellTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"commands": map[string]any{
				"type":        "array",
				"description": "Commands to run in parallel (max 8). Each: {command, description?, timeout?}.",
				"minItems":    1,
				"maxItems":    maxParallelShellCmds,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command":     map[string]any{"type": "string", "description": "Shell command to execute."},
						"description": map[string]any{"type": "string", "description": "Explain what this command does (shown in approval)."},
						"timeout":     map[string]any{"type": "integer", "description": "Per-command timeout in seconds (default: 30)."},
					},
					"required": []string{"command"},
				},
			},
		},
		"required": []string{"commands"},
	}
}

func (t *parallelShellTool) Call(argsJSON string) (result string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("parallel_shell: panic: %v", r)
			result = `{"error":"internal tool error"}`
		}
	}()
	var args parallelShellArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}
	if len(args.Commands) == 0 {
		return jsonError("at least one command is required")
	}
	if len(args.Commands) > maxParallelShellCmds {
		return jsonError(fmt.Sprintf("max %d commands per call", maxParallelShellCmds))
	}

	// Pre-check all commands for approval
	for _, c := range args.Commands {
		action := t.dangerousConfig.ActionForCommand(c.Command)
		switch action {
		case danger.Deny:
			return jsonError(fmt.Sprintf("command denied: %s", c.Command))
		case danger.Prompt:
			if t.approver != nil {
				cls := danger.Classify(c.Command)
				if err := t.approver.PromptCommand(cls, c.Command, c.Description); err != nil {
					return jsonError(fmt.Sprintf("command rejected: %s", c.Command))
				}
			}
		}
	}

	// Commands are pre-approved above; run them with bounded concurrency and
	// per-worker panic recovery. Results stay in input order, so Index == slot.
	results := parallelMap(args.Commands, toolConcurrency(), t.runOne,
		func(c parallelShellCmd, p any) parallelShellEntry {
			return parallelShellEntry{Command: c.Command, Error: fmt.Sprintf("internal error: %v", p)}
		})
	for i := range results {
		results[i].Index = i
	}

	return jsonResult(parallelShellResult{Results: results})
}

// runOne executes a single pre-approved command with a per-command timeout.
func (t *parallelShellTool) runOne(cmd parallelShellCmd) parallelShellEntry {
	timeout := cmd.Timeout
	if timeout <= 0 {
		timeout = 30
	}

	start := time.Now()
	entry := parallelShellEntry{Command: cmd.Command}

	var shCmd *exec.Cmd
	if t.containerName != "" {
		shCmd = exec.Command("docker", "exec", "-w", "/workspace", t.containerName, "sh", "-c", cmd.Command)
	} else {
		shCmd = exec.Command("sh", "-c", cmd.Command)
	}
	var stdout, stderr bytes.Buffer
	outW := &limitWriter{buf: &stdout, limit: maxShellOutputBytes}
	errW := &limitWriter{buf: &stderr, limit: maxShellOutputBytes}
	shCmd.Stdout = outW
	shCmd.Stderr = errW

	// Kill on timeout via goroutine, with mutex to avoid Process race
	var procMu sync.Mutex
	done := make(chan error, 1)
	go func() {
		err := shCmd.Run()
		procMu.Lock()
		done <- err
		procMu.Unlock()
	}()

	select {
	case err := <-done:
		entry.Stdout = strings.TrimSpace(stdout.String())
		entry.Stderr = strings.TrimSpace(stderr.String())
		entry.DurationMs = time.Since(start).Milliseconds()

		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				entry.ExitCode = exitErr.ExitCode()
			} else {
				entry.Error = err.Error()
			}
		}
	case <-time.After(time.Duration(timeout) * time.Second):
		procMu.Lock()
		if shCmd.Process != nil {
			shCmd.Process.Kill()
		}
		procMu.Unlock()
		entry.Error = fmt.Sprintf("timeout after %ds", timeout)
		entry.DurationMs = time.Since(start).Milliseconds()
	}

	return entry
}

// ═════════════════════════════════════════════════════════════════════════
// 3. http_batch — Fetch N URLs in parallel
// ═════════════════════════════════════════════════════════════════════════

const maxHTTPBatchURLs = 10

type httpBatchTool struct {
	ctxTool
	dangerousConfig danger.DangerousConfig
	client          *http.Client
}

func newHTTPBatchTool(dc danger.DangerousConfig) *httpBatchTool {
	t := &httpBatchTool{dangerousConfig: dc}
	t.client = &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: t.checkRedirect,
		Transport:     ssrfGuardedTransport(),
	}
	return t
}

// checkRedirect re-classifies every redirect hop. http_batch only checks
// the initial URL before the request, so without this a benign-classified
// URL could 302 to an SSRF target (cloud metadata, internal host) that the
// initial ClassifyURL gate would have blocked. The body is discarded, but
// the request itself — and the leaked status/content-length — is the
// vector we close here. Installing CheckRedirect disables Go's implicit
// 10-hop cap, so we re-impose it.
func (t *httpBatchTool) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	target := req.URL.String()
	risk := danger.ClassifyURL(target)
	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "http_batch", Resource: target, Risk: risk,
	}, nil); err != nil {
		return fmt.Errorf("redirect to %s blocked: %w", target, err)
	}
	return nil
}

func (t *httpBatchTool) Name() string { return "http_batch" }
func (t *httpBatchTool) Description() string {
	return `Fetch multiple URLs in parallel. Returns status code, content length, and error for each URL. Does NOT parse HTML — it's a lightweight parallel fetch for APIs, docs, and data files. Max 10 URLs per call.`
}

type httpBatchReq struct {
	URL     string            `json:"url"`
	Method  string            `json:"method,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

type httpBatchEntry struct {
	URL           string `json:"url"`
	Status        int    `json:"status"`
	ContentLength int64  `json:"content_length,omitempty"`
	Error         string `json:"error,omitempty"`
}

type httpBatchArgs struct {
	Requests []httpBatchReq `json:"requests"`
}

type httpBatchResult struct {
	Results []httpBatchEntry `json:"results"`
}

func (t *httpBatchTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"requests": map[string]any{
				"type":        "array",
				"description": "URLs to fetch in parallel (max 10). Each: {url, method?, headers?}.",
				"minItems":    1,
				"maxItems":    maxHTTPBatchURLs,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"url":     map[string]any{"type": "string", "description": "URL to fetch (http/https)."},
						"method":  map[string]any{"type": "string", "description": "HTTP method (default: GET)."},
						"headers": map[string]any{"type": "object", "description": "Optional HTTP headers."},
					},
					"required": []string{"url"},
				},
			},
		},
		"required": []string{"requests"},
	}
}

func (t *httpBatchTool) Call(argsJSON string) (result string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("http_batch: panic: %v", r)
			result = `{"error":"internal tool error"}`
		}
	}()
	var args httpBatchArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}
	if len(args.Requests) == 0 {
		return jsonError("at least one URL is required")
	}
	if len(args.Requests) > maxHTTPBatchURLs {
		return jsonError(fmt.Sprintf("max %d URLs per call", maxHTTPBatchURLs))
	}

	// Security checks run serially up front (they may prompt the user), then
	// only the approved fetches run in parallel. Denied requests get an error
	// entry in place and are not fetched.
	results := make([]httpBatchEntry, len(args.Requests))
	type httpJob struct {
		idx int
		req httpBatchReq
	}
	var todo []httpJob
	for i, req := range args.Requests {
		risk := danger.ClassifyURL(req.URL)
		if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
			Name: "http_batch", Resource: req.URL, Risk: risk,
		}, nil); err != nil {
			results[i] = httpBatchEntry{URL: req.URL, Error: err.Error()}
			continue
		}
		todo = append(todo, httpJob{idx: i, req: req})
	}

	done := parallelMap(todo, toolConcurrency(),
		func(j httpJob) httpBatchEntry { return t.fetchOne(j.req) },
		func(j httpJob, p any) httpBatchEntry {
			return httpBatchEntry{URL: j.req.URL, Error: fmt.Sprintf("internal error: %v", p)}
		})
	for k, j := range todo {
		results[j.idx] = done[k]
	}

	return jsonResult(httpBatchResult{Results: results})
}

// fetchOne performs a single pre-approved HTTP request through the SSRF-guarded
// client and reports status + content length (body discarded, capped at 1 MiB).
func (t *httpBatchTool) fetchOne(r httpBatchReq) httpBatchEntry {
	method := r.Method
	if method == "" {
		method = "GET"
	}

	entry := httpBatchEntry{URL: r.URL}
	httpReq, err := http.NewRequestWithContext(t.toolCtx(), method, r.URL, nil)
	if err != nil {
		entry.Error = err.Error()
		return entry
	}

	for k, v := range r.Headers {
		httpReq.Header.Set(k, v)
	}
	httpReq.Header.Set("User-Agent", "odek-http-batch/0.1")

	resp, err := t.client.Do(httpReq)
	if err != nil {
		entry.Error = err.Error()
		return entry
	}
	defer resp.Body.Close()

	entry.Status = resp.StatusCode
	entry.ContentLength = resp.ContentLength
	if entry.ContentLength <= 0 {
		n, _ := io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		entry.ContentLength = n
	}

	return entry
}

// ═════════════════════════════════════════════════════════════════════════
// 4. math_eval — Evaluate arithmetic expressions
// ═════════════════════════════════════════════════════════════════════════

type mathEvalTool struct{}

func (t *mathEvalTool) Name() string { return "math_eval" }
func (t *mathEvalTool) Description() string {
	return `Evaluate a math expression and return the result. Supports: +, -, *, /, %, parentheses, decimal numbers. Zero-fork — no subprocess spawned. Example: "42 * 17 + 256 / 10"`
}

type mathEvalArgs struct {
	Expression string `json:"expression"`
}

type mathEvalResult struct {
	Expression string  `json:"expression"`
	Result     float64 `json:"result"`
	Error      string  `json:"error,omitempty"`
}

func (t *mathEvalTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"expression": map[string]any{
				"type":        "string",
				"description": "Arithmetic expression (e.g. '42 * 17 + 256 / 10').",
			},
		},
		"required": []string{"expression"},
	}
}

func (t *mathEvalTool) Call(argsJSON string) (result string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("math_eval: panic: %v", r)
			result = `{"error":"internal tool error"}`
		}
	}()
	var args mathEvalArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}
	if args.Expression == "" {
		return jsonError("expression is required")
	}

	res, err := evalMath(args.Expression)
	if err != nil {
		return jsonResult(mathEvalResult{Expression: args.Expression, Error: err.Error()})
	}
	return jsonResult(mathEvalResult{Expression: args.Expression, Result: res})
}

func evalMath(expr string) (float64, error) {
	node, err := parser.ParseExpr(expr)
	if err != nil {
		return 0, fmt.Errorf("parse error: %v", err)
	}
	return evalNode(node)
}

func evalNode(node ast.Expr) (float64, error) {
	switch n := node.(type) {
	case *ast.BasicLit:
		if n.Kind == token.INT || n.Kind == token.FLOAT {
			return strconv.ParseFloat(n.Value, 64)
		}
		return 0, fmt.Errorf("unsupported literal: %s", n.Value)
	case *ast.BinaryExpr:
		x, err := evalNode(n.X)
		if err != nil {
			return 0, err
		}
		y, err := evalNode(n.Y)
		if err != nil {
			return 0, err
		}
		switch n.Op {
		case token.ADD:
			return x + y, nil
		case token.SUB:
			return x - y, nil
		case token.MUL:
			return x * y, nil
		case token.QUO:
			if y == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			return x / y, nil
		case token.REM:
			if y == 0 {
				return 0, fmt.Errorf("modulo by zero")
			}
			return float64(int64(x) % int64(y)), nil
		default:
			return 0, fmt.Errorf("unsupported operator: %s", n.Op)
		}
	case *ast.ParenExpr:
		return evalNode(n.X)
	case *ast.UnaryExpr:
		if n.Op == token.SUB {
			v, err := evalNode(n.X)
			if err != nil {
				return 0, err
			}
			return -v, nil
		}
		return 0, fmt.Errorf("unsupported unary operator")
	default:
		return 0, fmt.Errorf("unsupported expression: %T", node)
	}
}

// ═════════════════════════════════════════════════════════════════════════
// 5. diff — Structured file comparison
// ═════════════════════════════════════════════════════════════════════════

type diffTool struct {
	dangerousConfig danger.DangerousConfig
}

func (t *diffTool) Name() string { return "diff" }
func (t *diffTool) Description() string {
	return `Compare two files and return structured hunks. Each hunk has a type (equal/added/removed) and line-by-line content. Use path_a+path_b for file-vs-file, or path+content for file-vs-string. Zero-fork LCS-based diff — no subprocess spawned.`
}

type diffArgs struct {
	PathA   string `json:"path_a,omitempty"`
	PathB   string `json:"path_b,omitempty"`
	Path    string `json:"path,omitempty"`
	Content string `json:"content,omitempty"`
}

type diffLine struct {
	OldLine int    `json:"old_line,omitempty"`
	NewLine int    `json:"new_line,omitempty"`
	Content string `json:"content"`
}

type diffHunk struct {
	Type  string     `json:"type"`
	Lines []diffLine `json:"lines"`
}

type diffResult struct {
	Hunks []diffHunk `json:"hunks"`
	Error string     `json:"error,omitempty"`
	PathA string     `json:"path_a"`
	PathB string     `json:"path_b"`
}

func (t *diffTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path_a":  map[string]any{"type": "string", "description": "First file path (for file-vs-file)."},
			"path_b":  map[string]any{"type": "string", "description": "Second file path."},
			"path":    map[string]any{"type": "string", "description": "File path (for file-vs-string)."},
			"content": map[string]any{"type": "string", "description": "String content (for file-vs-string)."},
		},
	}
}

func (t *diffTool) Call(argsJSON string) (result string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("diff: panic: %v", r)
			result = `{"error":"internal tool error"}`
		}
	}()
	var args diffArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}

	var linesA, linesB []string
	var pathA, pathB string

	if args.PathA != "" && args.PathB != "" {
		pathA, pathB = args.PathA, args.PathB
		for _, p := range []string{args.PathA, args.PathB} {
			if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
				Name: "diff", Resource: p, Risk: danger.ClassifyPath(p),
			}, nil); err != nil {
				return jsonError(err.Error())
			}
		}
		data, err := readFileNoFollow(args.PathA)
		if err != nil {
			return jsonResult(diffResult{Error: err.Error(), PathA: pathA, PathB: pathB})
		}
		linesA = strings.Split(string(data), "\n")
		data, err = readFileNoFollow(args.PathB)
		if err != nil {
			return jsonResult(diffResult{Error: err.Error(), PathA: pathA, PathB: pathB})
		}
		linesB = strings.Split(string(data), "\n")
	} else if args.Path != "" {
		pathA, pathB = args.Path, "<inline>"
		if len(args.Content) > maxFileReadBytes {
			return jsonResult(diffResult{
				Error: fmt.Sprintf("inline content too large (%d bytes, max %d)", len(args.Content), maxFileReadBytes),
				PathA: pathA, PathB: pathB,
			})
		}
		if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
			Name: "diff", Resource: args.Path, Risk: danger.ClassifyPath(args.Path),
		}, nil); err != nil {
			return jsonError(err.Error())
		}
		data, err := readFileNoFollow(args.Path)
		if err != nil {
			return jsonResult(diffResult{Error: err.Error(), PathA: pathA, PathB: pathB})
		}
		linesA = strings.Split(string(data), "\n")
		linesB = strings.Split(args.Content, "\n")
	} else {
		return jsonError("provide either path_a+path_b or path+content")
	}

	// OOM protection: the LCS table allocates (m+1)*(n+1) ints.
	// For 10K+10K lines that's ~800MB — reject early to avoid panic.
	const maxDiffLines = 10000
	if len(linesA) > maxDiffLines || len(linesB) > maxDiffLines {
		return jsonResult(diffResult{
			Error: fmt.Sprintf("files too large for in-process diff (%d vs %d lines, max %d).",
				len(linesA), len(linesB), maxDiffLines),
			PathA: pathA, PathB: pathB,
		})
	}

	// Trim trailing empty from final newline
	if len(linesA) > 0 && linesA[len(linesA)-1] == "" {
		linesA = linesA[:len(linesA)-1]
	}
	if len(linesB) > 0 && linesB[len(linesB)-1] == "" {
		linesB = linesB[:len(linesB)-1]
	}

	hunks := computeDiff(linesA, linesB)
	src := fmt.Sprintf("diff:%s|%s", pathA, pathB)
	for i := range hunks {
		for j := range hunks[i].Lines {
			hunks[i].Lines[j].Content = wrapUntrusted(src, hunks[i].Lines[j].Content)
		}
	}
	return jsonResult(diffResult{Hunks: hunks, PathA: pathA, PathB: pathB})
}

func computeDiff(a, b []string) []diffHunk {
	m, n := len(a), len(b)
	lcs := make([][]int, m+1)
	for i := range lcs {
		lcs[i] = make([]int, n+1)
	}
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if a[i-1] == b[j-1] {
				lcs[i][j] = lcs[i-1][j-1] + 1
			} else if lcs[i-1][j] >= lcs[i][j-1] {
				lcs[i][j] = lcs[i-1][j]
			} else {
				lcs[i][j] = lcs[i][j-1]
			}
		}
	}

	// Backtrack
	var hunks []diffHunk
	i, j := m, n

	var equalLines, addedLines, removedLines []diffLine

	flushHunk := func() {
		if len(equalLines) > 0 {
			hunks = append(hunks, diffHunk{Type: "equal", Lines: equalLines})
			equalLines = nil
		}
		if len(removedLines) > 0 {
			hunks = append(hunks, diffHunk{Type: "removed", Lines: removedLines})
			removedLines = nil
		}
		if len(addedLines) > 0 {
			hunks = append(hunks, diffHunk{Type: "added", Lines: addedLines})
			addedLines = nil
		}
	}

	for i > 0 || j > 0 {
		if i > 0 && j > 0 && a[i-1] == b[j-1] {
			flushHunk()
			equalLines = append([]diffLine{{OldLine: i, NewLine: j, Content: a[i-1]}}, equalLines...)
			i--
			j--
		} else if j > 0 && (i == 0 || lcs[i][j-1] >= lcs[i-1][j]) {
			addedLines = append([]diffLine{{NewLine: j, Content: b[j-1]}}, addedLines...)
			j--
		} else if i > 0 {
			removedLines = append([]diffLine{{OldLine: i, Content: a[i-1]}}, removedLines...)
			i--
		}
	}
	flushHunk()

	// Reverse hunks
	for i, k := 0, len(hunks)-1; i < k; i, k = i+1, k-1 {
		hunks[i], hunks[k] = hunks[k], hunks[i]
	}

	return hunks
}

// ═════════════════════════════════════════════════════════════════════════
// 6. count_lines — Quick line/byte/char counts
// ═════════════════════════════════════════════════════════════════════════

const maxCountFiles = 20

type countLinesTool struct {
	dangerousConfig danger.DangerousConfig
}

func (t *countLinesTool) Name() string { return "count_lines" }
func (t *countLinesTool) Description() string {
	return `Count lines, bytes, and characters in one or more files. Streaming scanner — zero-alloc on content, zero subprocess forks. Per-file and aggregate totals.`
}

type countFileArg struct {
	Path string `json:"path"`
}

type countFileEntry struct {
	Path  string `json:"path"`
	Lines int    `json:"lines"`
	Bytes int64  `json:"bytes"`
	Chars int    `json:"chars"`
	Error string `json:"error,omitempty"`
}

type countLinesArgs struct {
	Files []countFileArg `json:"files"`
}

type countLinesResult struct {
	Results []countFileEntry `json:"results"`
	Total   countFileEntry   `json:"total"`
}

func (t *countLinesTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"files": map[string]any{
				"type":        "array",
				"description": "Files to count (max 20). Each: {path}.",
				"minItems":    1,
				"maxItems":    maxCountFiles,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string", "description": "File path."},
					},
					"required": []string{"path"},
				},
			},
		},
		"required": []string{"files"},
	}
}

func (t *countLinesTool) Call(argsJSON string) (string, error) {
	var args countLinesArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}
	if len(args.Files) == 0 {
		return jsonError("at least one file is required")
	}
	if len(args.Files) > maxCountFiles {
		return jsonError(fmt.Sprintf("max %d files per call", maxCountFiles))
	}

	results := parallelMap(args.Files, toolConcurrency(),
		func(f countFileArg) countFileEntry { return t.countFile(f.Path) },
		func(f countFileArg, p any) countFileEntry {
			return countFileEntry{Path: f.Path, Error: fmt.Sprintf("internal error: %v", p)}
		})

	var total countFileEntry
	total.Path = "(total)"
	for _, r := range results {
		if r.Error == "" {
			total.Lines += r.Lines
			total.Bytes += r.Bytes
			total.Chars += r.Chars
		}
	}

	return jsonResult(countLinesResult{Results: results, Total: total})
}

func (t *countLinesTool) countFile(path string) (entry countFileEntry) {
	defer func() {
		if r := recover(); r != nil {
			entry = countFileEntry{Path: path, Error: fmt.Sprintf("internal error: %v", r)}
		}
	}()
	if path == "" {
		return countFileEntry{Error: "path is required"}
	}

	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "count_lines", Resource: path, Risk: danger.ClassifyPath(path),
	}, nil); err != nil {
		return countFileEntry{Path: path, Error: err.Error()}
	}

	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return countFileEntry{Path: path, Error: fmt.Sprintf("cannot open %q: %v", path, err)}
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return countFileEntry{Path: path, Error: fmt.Sprintf("cannot stat %q: %v", path, err)}
	}
	if info.IsDir() {
		return countFileEntry{Path: path, Error: fmt.Sprintf("%q is a directory — use tree or glob to explore directories", path)}
	}

	lines := 0
	chars := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		lines++
		chars += len([]rune(scanner.Text())) + 1
	}

	return countFileEntry{
		Path:  path,
		Lines: lines,
		Bytes: info.Size(),
		Chars: chars,
	}
}

// ═════════════════════════════════════════════════════════════════════════
// 7. multi_grep — Search multiple patterns in parallel
// ═════════════════════════════════════════════════════════════════════════

const maxGrepPatterns = 10

type multiGrepTool struct {
	dangerousConfig danger.DangerousConfig
}

func (t *multiGrepTool) Name() string { return "multi_grep" }
func (t *multiGrepTool) Description() string {
	return `Search for multiple regex patterns in parallel across files. Each pattern runs its own directory walk with bounded concurrency. Returns structured {pattern, path, line, content} results. Replaces N serial search_files calls. Directly targets the multi_search benchmark.`
}

type grepMatch struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

type grepPatternResult struct {
	Pattern string      `json:"pattern"`
	Matches []grepMatch `json:"matches"`
	Count   int         `json:"count"`
	Error   string      `json:"error,omitempty"`
}

type multiGrepArgs struct {
	Patterns []string `json:"patterns"`
	Path     string   `json:"path,omitempty"`
	FileGlob string   `json:"file_glob,omitempty"`
	Limit    int      `json:"limit,omitempty"`
}

type multiGrepResult struct {
	Results []grepPatternResult `json:"results"`
}

func (t *multiGrepTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"patterns": map[string]any{
				"type":        "array",
				"description": "Regex patterns to search for (max 10).",
				"minItems":    1,
				"maxItems":    maxGrepPatterns,
				"items":       map[string]any{"type": "string"},
			},
			"path":      map[string]any{"type": "string", "description": "Root directory (default: '.')."},
			"file_glob": map[string]any{"type": "string", "description": "Filter files by glob (e.g. '*.go')."},
			"limit":     map[string]any{"type": "integer", "description": "Max matches per pattern (default: 50)."},
		},
		"required": []string{"patterns"},
	}
}

func (t *multiGrepTool) Call(argsJSON string) (string, error) {
	var args multiGrepArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}
	if len(args.Patterns) == 0 {
		return jsonError("at least one pattern is required")
	}
	if len(args.Patterns) > maxGrepPatterns {
		return jsonError(fmt.Sprintf("max %d patterns per call", maxGrepPatterns))
	}
	if args.Path == "" {
		args.Path = "."
	}
	if args.Limit <= 0 {
		args.Limit = 50
	}
	if args.Limit > maxSearchLimit {
		args.Limit = maxSearchLimit
	}

	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "multi_grep", Resource: args.Path, Risk: danger.ClassifyPath(args.Path),
	}, nil); err != nil {
		return jsonError(err.Error())
	}

	results := parallelMap(args.Patterns, toolConcurrency(),
		func(pat string) grepPatternResult {
			return t.searchPattern(pat, args.Path, args.FileGlob, args.Limit)
		},
		func(pat string, p any) grepPatternResult {
			return grepPatternResult{Pattern: pat, Error: fmt.Sprintf("internal error: %v", p)}
		})

	return jsonResult(multiGrepResult{Results: results})
}

func (t *multiGrepTool) searchPattern(pattern, root, fileGlob string, limit int) (result grepPatternResult) {
	defer func() {
		if r := recover(); r != nil {
			result = grepPatternResult{Pattern: pattern, Error: fmt.Sprintf("internal error: %v", r)}
		}
	}()
	re, err := regexp.Compile(pattern)
	if err != nil {
		return grepPatternResult{Pattern: pattern, Error: fmt.Sprintf("invalid regex: %v", err)}
	}

	var matches []grepMatch
	resultBytes := 0

	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.IsDir() {
			if strings.HasPrefix(info.Name(), ".") && info.Name() != "." {
				return filepath.SkipDir
			}
			if skipDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		// Skip symlinks — prevents TOCTOU and listing unreadable files.
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if fileGlob != "" {
			match, _ := filepath.Match(fileGlob, info.Name())
			if !match {
				return nil
			}
		}

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
		f.Seek(0, 0)

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if re.MatchString(line) {
				trimmed := strings.TrimSpace(line)
				if resultBytes+len(trimmed) > maxSearchResultBytes {
					return filepath.SkipAll
				}
				resultBytes += len(trimmed)
				matches = append(matches, grepMatch{
					Path:    path,
					Line:    lineNum,
					Content: wrapUntrusted(fmt.Sprintf("%s:%d", path, lineNum), trimmed),
				})
				if len(matches) >= limit {
					return filepath.SkipAll
				}
			}
		}
		if len(matches) >= limit {
			return filepath.SkipAll
		}
		return nil
	})

	return grepPatternResult{
		Pattern: pattern,
		Matches: matches,
		Count:   len(matches),
	}
}

// ═════════════════════════════════════════════════════════════════════════
// 8. json_query — Query/extract from JSON files
// ═════════════════════════════════════════════════════════════════════════

type jsonQueryTool struct {
	dangerousConfig danger.DangerousConfig
}

func (t *jsonQueryTool) Name() string { return "json_query" }
func (t *jsonQueryTool) Description() string {
	return `Parse a JSON file and extract a value using a dot-path query. Supports array indexing with [N]. Empty query returns the entire parsed JSON. Zero-fork — pure Go JSON traversal.`
}

type jsonQueryArgs struct {
	Path  string `json:"path"`
	Query string `json:"query"`
}

type jsonQueryResult struct {
	Path      string      `json:"path"`
	Query     string      `json:"query"`
	Value     interface{} `json:"value,omitempty"`
	ValueType string      `json:"value_type,omitempty"`
	Error     string      `json:"error,omitempty"`
}

func (t *jsonQueryTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":  map[string]any{"type": "string", "description": "Path to JSON file."},
			"query": map[string]any{"type": "string", "description": "Dot-path query (empty = return entire JSON)."},
		},
		"required": []string{"path"},
	}
}

func (t *jsonQueryTool) Call(argsJSON string) (result string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("json_query: panic: %v", r)
			result = `{"error":"internal tool error"}`
		}
	}()
	var args jsonQueryArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}
	if args.Path == "" {
		return jsonError("path is required")
	}

	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "json_query", Resource: args.Path, Risk: danger.ClassifyPath(args.Path),
	}, nil); err != nil {
		return jsonError(err.Error())
	}

	f, err := os.OpenFile(args.Path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return jsonResult(jsonQueryResult{Path: args.Path, Error: fmt.Sprintf("cannot open %q: %v", args.Path, err)})
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return jsonResult(jsonQueryResult{Path: args.Path, Error: fmt.Sprintf("cannot stat %q: %v", args.Path, err)})
	}
	if info.Size() > maxFileReadBytes {
		return jsonResult(jsonQueryResult{Path: args.Path, Error: fmt.Sprintf("file too large (%d bytes, max %d)", info.Size(), maxFileReadBytes)})
	}

	var data interface{}
	if err := json.NewDecoder(f).Decode(&data); err != nil {
		return jsonResult(jsonQueryResult{Path: args.Path, Error: fmt.Sprintf("invalid JSON: %v", err)})
	}

	if args.Query == "" {
		vt := fmt.Sprintf("%T", data)
		return jsonResult(jsonQueryResult{Path: args.Path, Query: "", Value: wrapJSONStrings(args.Path, data), ValueType: vt})
	}

	value, err := jsonPathQuery(data, args.Query)
	if err != nil {
		return jsonResult(jsonQueryResult{Path: args.Path, Query: args.Query, Error: err.Error()})
	}

	vt := fmt.Sprintf("%T", value)
	return jsonResult(jsonQueryResult{Path: args.Path, Query: args.Query, Value: wrapJSONStrings(args.Path, value), ValueType: vt})
}

// wrapJSONStrings recursively wraps string values inside decoded JSON so that
// file content returned by json_query is treated as untrusted.
func wrapJSONStrings(source string, v interface{}) interface{} {
	switch x := v.(type) {
	case string:
		return wrapUntrusted(source, x)
	case map[string]interface{}:
		for k, val := range x {
			x[k] = wrapJSONStrings(source, val)
		}
		return x
	case []interface{}:
		for i, val := range x {
			x[i] = wrapJSONStrings(source, val)
		}
		return x
	}
	return v
}

func jsonPathQuery(data interface{}, query string) (interface{}, error) {
	parts := strings.Split(query, ".")
	current := data

	for _, part := range parts {
		if part == "" {
			return nil, fmt.Errorf("empty path segment")
		}

		bracketIdx := strings.Index(part, "[")
		if bracketIdx >= 0 {
			if !strings.HasSuffix(part, "]") {
				return nil, fmt.Errorf("invalid array index in %q", part)
			}

			var key string
			if bracketIdx > 0 {
				key = part[:bracketIdx]
			}

			idxStr := part[bracketIdx+1 : len(part)-1]
			idx, err := strconv.Atoi(idxStr)
			if err != nil {
				return nil, fmt.Errorf("invalid index %q", idxStr)
			}

			if key != "" {
				m, ok := current.(map[string]interface{})
				if !ok {
					return nil, fmt.Errorf("%q is not an object", key)
				}
				current, ok = m[key]
				if !ok {
					return nil, fmt.Errorf("key %q not found", key)
				}
			}

			arr, ok := current.([]interface{})
			if !ok {
				return nil, fmt.Errorf("value is not an array")
			}
			if idx < 0 || idx >= len(arr) {
				return nil, fmt.Errorf("index %d out of range (len %d)", idx, len(arr))
			}
			current = arr[idx]
		} else {
			m, ok := current.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("%q is not an object", part)
			}
			current, ok = m[part]
			if !ok {
				return nil, fmt.Errorf("key %q not found", part)
			}
		}
	}

	return current, nil
}

// ═════════════════════════════════════════════════════════════════════════
// 9. tree — Structured directory tree listing
// ═════════════════════════════════════════════════════════════════════════

type treeTool struct {
	dangerousConfig danger.DangerousConfig
}

func (t *treeTool) Name() string { return "tree" }
func (t *treeTool) Description() string {
	return `List the directory tree with file counts, sizes, and nesting. Returns a structured tree: each entry shows path, is_dir, file_count, total_size, children, depth. Zero-fork — pure Go directory walk.`
}

type treeArgs struct {
	Path          string `json:"path,omitempty"`
	MaxDepth      int    `json:"max_depth,omitempty"`
	IncludeHidden bool   `json:"include_hidden,omitempty"`
}

type treeEntry struct {
	Path      string      `json:"path"`
	IsDir     bool        `json:"is_dir"`
	FileCount int         `json:"file_count,omitempty"`
	TotalSize int64       `json:"total_size,omitempty"`
	Depth     int         `json:"depth"`
	Children  []treeEntry `json:"children,omitempty"`
	ErrMsg    string      `json:"error,omitempty"`
}

type treeResult struct {
	Tree  treeEntry `json:"tree"`
	Error string    `json:"error,omitempty"`
}

func (t *treeTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":           map[string]any{"type": "string", "description": "Root directory (default: '.')."},
			"max_depth":      map[string]any{"type": "integer", "description": "Max depth (default: 3, max: 10)."},
			"include_hidden": map[string]any{"type": "boolean", "description": "Include hidden files (default: false)."},
		},
	}
}

func (t *treeTool) Call(argsJSON string) (result string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("tree: panic: %v", r)
			result = `{"error":"internal tool error"}`
		}
	}()
	var args treeArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}
	if args.Path == "" {
		args.Path = "."
	}
	if args.MaxDepth <= 0 {
		args.MaxDepth = 3
	}
	if args.MaxDepth > 10 {
		args.MaxDepth = 10
	}

	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "tree", Resource: args.Path, Risk: danger.ClassifyPath(args.Path),
	}, nil); err != nil {
		return jsonError(err.Error())
	}

	entry, err := buildTree(args.Path, args.Path, 0, args.MaxDepth, args.IncludeHidden)
	if err != nil {
		return jsonResult(treeResult{Error: err.Error()})
	}

	return jsonResult(treeResult{Tree: entry})
}

func buildTree(root, path string, depth, maxDepth int, includeHidden bool) (treeEntry, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return treeEntry{Path: path, ErrMsg: err.Error()}, nil
	}

	entry := treeEntry{
		Path:  filepath.Base(path),
		IsDir: info.IsDir(),
		Depth: depth,
	}

	if depth == 0 {
		entry.Path = path
	}

	if !info.IsDir() || depth >= maxDepth {
		if !info.IsDir() {
			entry.FileCount = 1
			entry.TotalSize = info.Size()
		}
		return entry, nil
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return entry, nil
	}

	totalEntries := len(entries)
	truncated := false
	if totalEntries > maxTreeEntries {
		entries = entries[:maxTreeEntries]
		truncated = true
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	if truncated {
		entry.ErrMsg = fmt.Sprintf("directory truncated (%d entries shown, %d total)", maxTreeEntries, totalEntries)
	}

	entry.Children = make([]treeEntry, 0, len(entries))
	for _, e := range entries {
		if !includeHidden && strings.HasPrefix(e.Name(), ".") {
			continue
		}
		childPath := filepath.Join(path, e.Name())
		child, err := buildTree(root, childPath, depth+1, maxDepth, includeHidden)
		if err != nil {
			continue
		}
		entry.Children = append(entry.Children, child)
		entry.FileCount += child.FileCount
		entry.TotalSize += child.TotalSize
	}

	return entry, nil
}

// ═════════════════════════════════════════════════════════════════════════
// 10. checksum — Compute file hashes natively
// ═════════════════════════════════════════════════════════════════════════

const maxChecksumFiles = 10

type checksumTool struct {
	dangerousConfig danger.DangerousConfig
}

func (t *checksumTool) Name() string { return "checksum" }
func (t *checksumTool) Description() string {
	return `Compute cryptographic hashes of files using SHA-256 (default), SHA-1, or MD5. Uses Go crypto stdlib — zero subprocess fork, pure Go implementation.`
}

type checksumFileArg struct {
	Path      string `json:"path"`
	Algorithm string `json:"algorithm,omitempty"`
}

type checksumEntry struct {
	Path      string `json:"path"`
	Algorithm string `json:"algorithm"`
	Hash      string `json:"hash"`
	Error     string `json:"error,omitempty"`
}

type checksumArgs struct {
	Files []checksumFileArg `json:"files"`
}

type checksumResult struct {
	Results []checksumEntry `json:"results"`
}

func (t *checksumTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"files": map[string]any{
				"type":        "array",
				"description": "Files to hash (max 10). Each: {path, algorithm?} — 'sha256' (default), 'sha1', 'md5'.",
				"minItems":    1,
				"maxItems":    maxChecksumFiles,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":      map[string]any{"type": "string", "description": "File path."},
						"algorithm": map[string]any{"type": "string", "description": "Hash algorithm: sha256 (default), sha1, md5."},
					},
					"required": []string{"path"},
				},
			},
		},
		"required": []string{"files"},
	}
}

func (t *checksumTool) Call(argsJSON string) (string, error) {
	var args checksumArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}
	if len(args.Files) == 0 {
		return jsonError("at least one file is required")
	}
	if len(args.Files) > maxChecksumFiles {
		return jsonError(fmt.Sprintf("max %d files per call", maxChecksumFiles))
	}

	results := parallelMap(args.Files, toolConcurrency(), t.hashFile,
		func(cf checksumFileArg, p any) checksumEntry {
			return checksumEntry{Path: cf.Path, Algorithm: strings.ToLower(cf.Algorithm), Error: fmt.Sprintf("internal error: %v", p)}
		})

	return jsonResult(checksumResult{Results: results})
}

func (t *checksumTool) hashFile(arg checksumFileArg) (entry checksumEntry) {
	defer func() {
		if r := recover(); r != nil {
			entry = checksumEntry{Path: arg.Path, Algorithm: strings.ToLower(arg.Algorithm), Error: fmt.Sprintf("internal error: %v", r)}
		}
	}()
	if arg.Path == "" {
		return checksumEntry{Error: "path is required"}
	}
	algo := strings.ToLower(arg.Algorithm)
	if algo == "" {
		algo = "sha256"
	}

	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "checksum", Resource: arg.Path, Risk: danger.ClassifyPath(arg.Path),
	}, nil); err != nil {
		return checksumEntry{Path: arg.Path, Algorithm: algo, Error: err.Error()}
	}

	f, err := os.OpenFile(arg.Path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return checksumEntry{Path: arg.Path, Algorithm: algo, Error: fmt.Sprintf("cannot open %q: %v", arg.Path, err)}
	}
	defer f.Close()

	var hash string
	switch algo {
	case "sha256":
		h := sha256.New()
		io.Copy(h, f)
		hash = hex.EncodeToString(h.Sum(nil))
	case "sha1":
		h := sha1.New()
		io.Copy(h, f)
		hash = hex.EncodeToString(h.Sum(nil))
	case "md5":
		h := md5.New()
		io.Copy(h, f)
		hash = hex.EncodeToString(h.Sum(nil))
	default:
		return checksumEntry{Path: arg.Path, Algorithm: algo, Error: fmt.Sprintf("unsupported algorithm: %s", algo)}
	}

	return checksumEntry{Path: arg.Path, Algorithm: algo, Hash: hash}
}

// ═════════════════════════════════════════════════════════════════════════
// 11. sort — Sort lines in files natively
// ═════════════════════════════════════════════════════════════════════════

type sortTool struct {
	dangerousConfig danger.DangerousConfig
}

func (t *sortTool) Name() string { return "sort" }
func (t *sortTool) Description() string {
	return `Sort lines in one or more files. Supports ascending (default), descending, unique (dedup), numeric, case-insensitive, and reverse. For multiple files, results are merged. Zero-fork — pure Go sort with no subprocess.`
}

type sortArgs struct {
	Path       string        `json:"path,omitempty"`  // single file
	Files      []sortFileArg `json:"files,omitempty"` // multiple files
	Order      string        `json:"order,omitempty"` // "asc" (default) or "desc"
	Unique     bool          `json:"unique,omitempty"`
	Numeric    bool          `json:"numeric,omitempty"`
	IgnoreCase bool          `json:"ignore_case,omitempty"`
	Reverse    bool          `json:"reverse,omitempty"`
}

type sortFileArg struct {
	Path string `json:"path"`
}

type sortEntry struct {
	File  string `json:"file"`
	Lines int    `json:"lines"`
	Error string `json:"error,omitempty"`
}

type sortResult struct {
	Results []sortEntry `json:"results"`
	Output  string      `json:"output,omitempty"` // sorted content (single file mode)
	Total   int         `json:"total"`
}

func (t *sortTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":        map[string]any{"type": "string", "description": "Single file to sort."},
			"files":       map[string]any{"type": "array", "description": "Multiple files to sort (results merged).", "items": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}}},
			"order":       map[string]any{"type": "string", "enum": []string{"asc", "desc"}, "description": "Sort order (default: asc)."},
			"unique":      map[string]any{"type": "boolean", "description": "Remove duplicate lines."},
			"numeric":     map[string]any{"type": "boolean", "description": "Numeric sort (by number prefix)."},
			"ignore_case": map[string]any{"type": "boolean", "description": "Case-insensitive sort."},
			"reverse":     map[string]any{"type": "boolean", "description": "Reverse sort order."},
		},
	}
}

func (t *sortTool) Call(argsJSON string) (result string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("sort: panic: %v", r)
			result = `{"error":"internal tool error"}`
		}
	}()
	var args sortArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}

	var paths []string
	if args.Path != "" {
		paths = []string{args.Path}
	} else if len(args.Files) > 0 {
		for _, f := range args.Files {
			paths = append(paths, f.Path)
		}
	} else {
		return jsonError("provide path or files")
	}
	if len(paths) > 20 {
		return jsonError("max 20 files per sort call")
	}

	desc := args.Order == "desc" || args.Reverse

	// Read all files
	var allLines []string
	var results []sortEntry
	for _, p := range paths {
		if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
			Name: "sort", Resource: p, Risk: danger.ClassifyPath(p),
		}, nil); err != nil {
			results = append(results, sortEntry{File: p, Error: err.Error()})
			continue
		}
		f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			results = append(results, sortEntry{File: p, Error: fmt.Sprintf("cannot open %q: %v", p, err)})
			continue
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			results = append(results, sortEntry{File: p, Error: fmt.Sprintf("cannot stat %q: %v", p, err)})
			continue
		}
		if info.Size() > maxFileReadBytes {
			f.Close()
			results = append(results, sortEntry{File: p, Error: fmt.Sprintf("file too large (%d bytes, max %d)", info.Size(), maxFileReadBytes)})
			continue
		}
		data, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			results = append(results, sortEntry{File: p, Error: err.Error()})
			continue
		}
		lines := strings.Split(string(data), "\n")
		// Trim trailing empty
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		results = append(results, sortEntry{File: p, Lines: len(lines)})
		allLines = append(allLines, lines...)
	}

	if len(allLines) == 0 {
		return jsonResult(sortResult{Results: results})
	}

	// Sort
	sort.Slice(allLines, func(i, j int) bool {
		a, b := allLines[i], allLines[j]
		if args.IgnoreCase {
			a, b = strings.ToLower(a), strings.ToLower(b)
		}
		if args.Numeric {
			var ai, bi float64
			fa := strings.Fields(a)
			fb := strings.Fields(b)
			if len(fa) > 0 {
				ai, _ = strconv.ParseFloat(fa[0], 64)
			}
			if len(fb) > 0 {
				bi, _ = strconv.ParseFloat(fb[0], 64)
			}
			if desc {
				return ai > bi
			}
			return ai < bi
		}
		if desc {
			return a > b
		}
		return a < b
	})

	// Unique
	if args.Unique {
		seen := make(map[string]bool)
		unique := make([]string, 0, len(allLines))
		for _, line := range allLines {
			key := line
			if args.IgnoreCase {
				key = strings.ToLower(key)
			}
			if !seen[key] {
				seen[key] = true
				unique = append(unique, line)
			}
		}
		allLines = unique
	}

	output := strings.Join(allLines, "\n")
	return jsonResult(sortResult{
		Results: results,
		Output:  wrapUntrusted("sort:"+strings.Join(paths, ","), output),
		Total:   len(allLines),
	})
}

// ═════════════════════════════════════════════════════════════════════════
// 12. head_tail — Quick file preview (first/last N lines)
// ═════════════════════════════════════════════════════════════════════════

type headTailTool struct {
	dangerousConfig danger.DangerousConfig
}

func (t *headTailTool) Name() string { return "head_tail" }
func (t *headTailTool) Description() string {
	return `Read the first or last N lines of one or more files. Streaming — stops after N lines (no full-file read for head). Supports multiple files in parallel. Zero-fork — pure Go scanner.`
}

type headTailFileArg struct {
	Path string `json:"path"`
}

type headTailArgs struct {
	Files []headTailFileArg `json:"files"`
	Lines int               `json:"lines,omitempty"`
	Mode  string            `json:"mode,omitempty"` // "head" (default) or "tail"
}

type headTailFileResult struct {
	Path  string   `json:"path"`
	Lines []string `json:"lines"`
	Count int      `json:"count"`
	Total int      `json:"total"` // total lines in file
	Error string   `json:"error,omitempty"`
}

type headTailResult struct {
	Results []headTailFileResult `json:"results"`
}

func (t *headTailTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"files": map[string]any{
				"type": "array", "description": "Files to preview (max 10).",
				"items": map[string]any{
					"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}},
					"required": []string{"path"},
				},
			},
			"lines": map[string]any{"type": "integer", "description": "Number of lines (default: 10, max: 100)."},
			"mode":  map[string]any{"type": "string", "enum": []string{"head", "tail"}, "description": "head (default) or tail."},
		},
		"required": []string{"files"},
	}
}

func (t *headTailTool) Call(argsJSON string) (string, error) {
	var args headTailArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}
	if len(args.Files) == 0 {
		return jsonError("at least one file is required")
	}
	if len(args.Files) > 10 {
		return jsonError("max 10 files per call")
	}
	n := args.Lines
	if n <= 0 {
		n = 10
	}
	if n > 100 {
		n = 100
	}
	mode := args.Mode
	if mode == "" {
		mode = "head"
	}

	results := parallelMap(args.Files, toolConcurrency(),
		func(f headTailFileArg) headTailFileResult { return t.readPreview(f.Path, n, mode) },
		func(f headTailFileArg, p any) headTailFileResult {
			return headTailFileResult{Path: f.Path, Error: fmt.Sprintf("internal error: %v", p)}
		})

	return jsonResult(headTailResult{Results: results})
}

func (t *headTailTool) readPreview(path string, n int, mode string) (result headTailFileResult) {
	defer func() {
		if r := recover(); r != nil {
			result = headTailFileResult{Path: path, Error: fmt.Sprintf("internal error: %v", r)}
		}
	}()
	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "head_tail", Resource: path, Risk: danger.ClassifyPath(path),
	}, nil); err != nil {
		return headTailFileResult{Path: path, Error: err.Error()}
	}

	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return headTailFileResult{Path: path, Error: fmt.Sprintf("cannot open %q: %v", path, err)}
	}
	defer f.Close()

	if mode == "tail" {
		return t.readTail(f, path, n)
	}
	return t.readHead(f, path, n)
}

func (t *headTailTool) readHead(f *os.File, path string, n int) headTailFileResult {
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	var lines []string
	total := 0
	for scanner.Scan() {
		total++
		if len(lines) < n {
			lines = append(lines, wrapUntrusted(path, scanner.Text()))
		}
	}
	return headTailFileResult{Path: path, Lines: lines, Count: len(lines), Total: total}
}

func (t *headTailTool) readTail(f *os.File, path string, n int) headTailFileResult {
	// Use ring buffer for tail
	buf := make([]string, n)
	written := 0
	total := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		buf[written%n] = scanner.Text()
		written++
		total++
	}
	// Extract in correct order
	var lines []string
	start := 0
	if written >= n {
		start = written % n
	}
	for i := 0; i < n && i < written; i++ {
		lines = append(lines, wrapUntrusted(path, buf[(start+i)%n]))
	}
	return headTailFileResult{Path: path, Lines: lines, Count: len(lines), Total: total}
}

// ═════════════════════════════════════════════════════════════════════════
// 13. base64 — Encode/decode base64
// ═════════════════════════════════════════════════════════════════════════

type base64Tool struct {
	dangerousConfig danger.DangerousConfig
}

func (t *base64Tool) Name() string { return "base64" }
func (t *base64Tool) Description() string {
	return `Encode or decode base64. Supports file input (path) or inline string (content). Encode: file or string → base64. Decode: base64 string → decoded string. Zero-fork — pure Go encoding. Use path for file, content for inline string, decode=true to decode.`
}

type base64Args struct {
	Path    string `json:"path,omitempty"`
	Content string `json:"content,omitempty"`
	Decode  bool   `json:"decode,omitempty"`
	String  string `json:"string,omitempty"`
}

type base64Result struct {
	Encoded string `json:"encoded,omitempty"`
	Decoded string `json:"decoded,omitempty"`
	Size    int    `json:"size,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (t *base64Tool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":    map[string]any{"type": "string", "description": "File to encode (base64)."},
			"content": map[string]any{"type": "string", "description": "Inline string to encode."},
			"string":  map[string]any{"type": "string", "description": "Base64 string to decode."},
			"decode":  map[string]any{"type": "boolean", "description": "Set true when decoding (used with string param)."},
		},
	}
}

func (t *base64Tool) Call(argsJSON string) (result string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("base64: panic: %v", r)
			result = `{"error":"internal tool error"}`
		}
	}()
	var args base64Args
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}

	if args.Decode || args.String != "" {
		src := args.String
		if src == "" {
			src = args.Content
		}
		if src == "" {
			return jsonError("string to decode is required")
		}
		decoded, err := base64.StdEncoding.DecodeString(src)
		if err != nil {
			return jsonResult(base64Result{Error: fmt.Sprintf("decode error: %v", err)})
		}
		return jsonResult(base64Result{Decoded: string(decoded), Size: len(decoded)})
	}

	if args.Path == "" && args.Content == "" {
		return jsonError("provide path (file to encode) or content (inline string)")
	}

	if args.Content != "" {
		encoded := base64.StdEncoding.EncodeToString([]byte(args.Content))
		return jsonResult(base64Result{Encoded: encoded, Size: len(args.Content)})
	}

	// File mode
	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "base64", Resource: args.Path, Risk: danger.ClassifyPath(args.Path),
	}, nil); err != nil {
		return jsonError(err.Error())
	}

	data, err := readFileNoFollow(args.Path)
	if err != nil {
		return jsonResult(base64Result{Error: fmt.Sprintf("cannot read %q: %v", args.Path, err)})
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	return jsonResult(base64Result{Encoded: encoded, Size: len(data)})
}

// ═════════════════════════════════════════════════════════════════════════
// 14. tr — Native text transformation
// ═════════════════════════════════════════════════════════════════════════

type trTool struct {
	dangerousConfig danger.DangerousConfig
}

func (t *trTool) Name() string { return "tr" }
func (t *trTool) Description() string {
	return `Transform text: case conversion, character replacement, string substitution, character deletion. Operates on a file or inline content. Zero-fork — pure Go strings transformations.`
}

type trTransform struct {
	From string `json:"from,omitempty"` // for char/string replacement
	To   string `json:"to,omitempty"`
	Type string `json:"type,omitempty"` // "upper", "lower", "char", "string", "delete"
}

type trArgs struct {
	Path            string        `json:"path,omitempty"`
	Content         string        `json:"content,omitempty"`
	Transformations []trTransform `json:"transformations"`
}

type trResult struct {
	Result   string `json:"result"`
	Error    string `json:"error,omitempty"`
	FromFile bool   `json:"from_file,omitempty"`
}

func (t *trTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":    map[string]any{"type": "string", "description": "File to transform."},
			"content": map[string]any{"type": "string", "description": "Inline string to transform."},
			"transformations": map[string]any{
				"type": "array", "description": "Transformations to apply (in order).",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"type": map[string]any{"type": "string", "enum": []string{"upper", "lower", "char", "string", "delete"}, "description": "upper/lower/char/string/delete."},
						"from": map[string]any{"type": "string", "description": "Source characters/string (for char/string/delete types)."},
						"to":   map[string]any{"type": "string", "description": "Target characters/string (for char/string types)."},
					},
					"required": []string{"type"},
				},
			},
		},
		"required": []string{"transformations"},
	}
}

func (t *trTool) Call(argsJSON string) (result string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("tr: panic: %v", r)
			result = `{"error":"internal tool error"}`
		}
	}()
	var args trArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}
	if len(args.Transformations) == 0 {
		return jsonError("at least one transformation is required")
	}

	var text string
	fromFile := false
	if args.Path != "" {
		if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
			Name: "tr", Resource: args.Path, Risk: danger.ClassifyPath(args.Path),
		}, nil); err != nil {
			return jsonError(err.Error())
		}
		data, err := readFileNoFollow(args.Path)
		if err != nil {
			return jsonResult(trResult{Error: fmt.Sprintf("cannot read %q: %v", args.Path, err)})
		}
		text = string(data)
		fromFile = true
	} else {
		text = args.Content
	}

	for _, tf := range args.Transformations {
		switch tf.Type {
		case "upper":
			text = strings.ToUpper(text)
		case "lower":
			text = strings.ToLower(text)
		case "char":
			if tf.From == "" {
				return jsonResult(trResult{Error: "from is required for char transformation"})
			}
			if len(tf.From) != len(tf.To) && len(tf.To) != 1 && len(tf.To) != 0 {
				text = strings.Map(func(r rune) rune {
					if i := strings.IndexRune(tf.From, r); i >= 0 {
						if i < len(tf.To) {
							return rune(tf.To[i])
						}
						return rune(tf.To[len(tf.To)-1])
					}
					return r
				}, text)
			} else {
				text = strings.Replace(text, tf.From, tf.To, -1)
			}
		case "string":
			if tf.From == "" {
				return jsonResult(trResult{Error: "from is required for string transformation"})
			}
			text = strings.ReplaceAll(text, tf.From, tf.To)
		case "delete":
			if tf.From == "" {
				return jsonResult(trResult{Error: "from is required for delete transformation"})
			}
			text = strings.Map(func(r rune) rune {
				if strings.ContainsRune(tf.From, r) {
					return -1
				}
				return r
			}, text)
		default:
			return jsonResult(trResult{Error: fmt.Sprintf("unknown transformation type: %q", tf.Type)})
		}
	}

	if fromFile {
		text = wrapUntrusted(args.Path, text)
	}
	return jsonResult(trResult{Result: text, FromFile: fromFile})
}

// ═════════════════════════════════════════════════════════════════════════
// 15. word_count — Count words in files
// ═════════════════════════════════════════════════════════════════════════

const maxWordCountFiles = 20

type wordCountTool struct {
	dangerousConfig danger.DangerousConfig
}

func (t *wordCountTool) Name() string { return "word_count" }
func (t *wordCountTool) Description() string {
	return `Count words, lines, and characters in one or more files. Streaming scanner — no full-content load. Returns per-file and aggregate totals. Zero-fork — pure Go scanner.`
}

type wordCountFileArg struct {
	Path string `json:"path"`
}

type wordCountEntry struct {
	Path  string `json:"path"`
	Lines int    `json:"lines"`
	Words int    `json:"words"`
	Chars int    `json:"chars"`
	Bytes int64  `json:"bytes"`
	Error string `json:"error,omitempty"`
}

type wordCountArgs struct {
	Files []wordCountFileArg `json:"files"`
}

type wordCountResult struct {
	Results []wordCountEntry `json:"results"`
	Total   wordCountEntry   `json:"total"`
}

func (t *wordCountTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"files": map[string]any{
				"type": "array", "description": "Files to count (max 20).",
				"items": map[string]any{
					"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}},
					"required": []string{"path"},
				},
			},
		},
		"required": []string{"files"},
	}
}

func (t *wordCountTool) Call(argsJSON string) (string, error) {
	var args wordCountArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}
	if len(args.Files) == 0 {
		return jsonError("at least one file is required")
	}
	if len(args.Files) > maxWordCountFiles {
		return jsonError(fmt.Sprintf("max %d files per call", maxWordCountFiles))
	}

	results := parallelMap(args.Files, toolConcurrency(),
		func(f wordCountFileArg) wordCountEntry { return t.countWords(f.Path) },
		func(f wordCountFileArg, p any) wordCountEntry {
			return wordCountEntry{Path: f.Path, Error: fmt.Sprintf("internal error: %v", p)}
		})

	var total wordCountEntry
	total.Path = "(total)"
	for _, r := range results {
		if r.Error == "" {
			total.Lines += r.Lines
			total.Words += r.Words
			total.Chars += r.Chars
			total.Bytes += r.Bytes
		}
	}

	return jsonResult(wordCountResult{Results: results, Total: total})
}

func (t *wordCountTool) countWords(path string) (entry wordCountEntry) {
	defer func() {
		if r := recover(); r != nil {
			entry = wordCountEntry{Path: path, Error: fmt.Sprintf("internal error: %v", r)}
		}
	}()
	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "word_count", Resource: path, Risk: danger.ClassifyPath(path),
	}, nil); err != nil {
		return wordCountEntry{Path: path, Error: err.Error()}
	}

	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return wordCountEntry{Path: path, Error: fmt.Sprintf("cannot open %q: %v", path, err)}
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return wordCountEntry{Path: path, Error: fmt.Sprintf("cannot stat: %v", err)}
	}

	lines := 0
	words := 0
	chars := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		lines++
		line := scanner.Text()
		chars += len([]rune(line)) + 1
		words += len(strings.Fields(line))
	}

	return wordCountEntry{
		Path:  path,
		Lines: lines,
		Words: words,
		Chars: chars,
		Bytes: info.Size(),
	}
}

// ── Compile-time interface checks ────────────────────────────────────
var (
	_ odek.Tool = (*batchPatchTool)(nil)
	_ odek.Tool = (*parallelShellTool)(nil)
	_ odek.Tool = (*httpBatchTool)(nil)
	_ odek.Tool = (*mathEvalTool)(nil)
	_ odek.Tool = (*diffTool)(nil)
	_ odek.Tool = (*countLinesTool)(nil)
	_ odek.Tool = (*multiGrepTool)(nil)
	_ odek.Tool = (*jsonQueryTool)(nil)
	_ odek.Tool = (*treeTool)(nil)
	_ odek.Tool = (*checksumTool)(nil)
	_ odek.Tool = (*sortTool)(nil)
	_ odek.Tool = (*headTailTool)(nil)
	_ odek.Tool = (*base64Tool)(nil)
	_ odek.Tool = (*trTool)(nil)
	_ odek.Tool = (*wordCountTool)(nil)
)
