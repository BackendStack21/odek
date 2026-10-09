package main

import (
	"bufio"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"hash"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/danger"
)

// maxFileReadBytes caps how much of a single file the perf tools will load
// into memory. This prevents OOM when tools like diff/base64/tr/sort point at
// multi-gigabyte logs or core dumps.
const maxFileReadBytes = 10 << 20 // 10 MiB

// maxInlineContentBytes caps inline string/content arguments for tools that
// operate on data supplied directly in the tool call. This prevents a
// prompt-injected call from passing a 100 MB base64 string and OOMing the
// process.
const maxInlineContentBytes = 10 << 20 // 10 MiB

// maxTreeEntries caps the number of children reported for a single directory
// by the tree tool, preventing OOM from directories with millions of entries.
const maxTreeEntries = 1000

// openRegularNoFollow opens path read-only without following a final symlink
// and without ever blocking: O_NONBLOCK keeps open(2) on a FIFO with no
// writer from hanging the agent turn, and anything that is neither a regular
// file nor a directory (FIFO, socket, device) is refused after the open so
// no reader can block on it later. Directories are returned so callers can
// keep their own directory messages.
func openRegularNoFollow(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if m := info.Mode(); !m.IsRegular() && !m.IsDir() {
		f.Close()
		return nil, fmt.Errorf("not a regular file")
	}
	return f, nil
}

// readFileNoFollow reads a file with O_NOFOLLOW (anti-symlink), rejecting files
// larger than maxFileReadBytes to avoid unbounded memory consumption.
// Directory symlinks in the path are resolved first so risk classification
// cannot be bypassed by a symlinked directory.
func readFileNoFollow(path string) ([]byte, error) {
	resolvedPath, err := resolveReadPath(path)
	if err != nil {
		return nil, err
	}
	f, err := openRegularNoFollow(resolvedPath)
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

	return readCapped(f, maxFileReadBytes)
}

// ═════════════════════════════════════════════════════════════════════════
// math_eval — Evaluate arithmetic expressions
// ═════════════════════════════════════════════════════════════════════════

type mathEvalTool struct{}

func (t *mathEvalTool) Name() string { return "math_eval" }
func (t *mathEvalTool) Description() string {
	return `Evaluate a math expression: +, -, *, /, %, parentheses, decimals. Example: "42 * 17 + 256 / 10"`
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
	// Recursion bound (2026-08 audit): go/parser and evalNode recurse once
	// per ParenExpr with no depth limit; a deeply nested expression is a
	// fatal, uncatchable stack overflow that kills the whole agent process.
	// 128 is far beyond any sane arithmetic; also bound raw length.
	const (
		maxMathExprLen   = 64 << 10
		maxMathNestDepth = 128
	)
	if len(expr) > maxMathExprLen {
		return 0, fmt.Errorf("expression too large (%d bytes, max %d)", len(expr), maxMathExprLen)
	}
	depth, maxDepth := 0, 0
	for _, r := range expr {
		switch r {
		case '(':
			depth++
			if depth > maxDepth {
				maxDepth = depth
			}
		case ')':
			depth--
		}
		if maxDepth > maxMathNestDepth {
			return 0, fmt.Errorf("expression too deeply nested (max %d levels)", maxMathNestDepth)
		}
	}
	node, err := parser.ParseExpr(expr)
	if err != nil {
		return 0, fmt.Errorf("parse error: %v", err)
	}
	return evalNode(node)
}

// evalNode evaluates one node and rejects any non-finite intermediate or
// final value (overflow to Inf, NaN) with an in-band error: a non-finite
// float cannot be encoded as a JSON result.
func evalNode(node ast.Expr) (float64, error) {
	v, err := evalNodeRaw(node)
	if err != nil {
		return 0, err
	}
	if math.IsInf(v, 0) || math.IsNaN(v) {
		return 0, fmt.Errorf("result is not finite (overflow or undefined)")
	}
	return v, nil
}

func evalNodeRaw(node ast.Expr) (float64, error) {
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
			// Modulo is an integer operation: fractional operands either panic
			// (int64(0.5) == 0 → divide by zero) or silently truncate to a
			// wrong answer (0.5 % 2 → 0). Reject them cleanly instead.
			if x != math.Trunc(x) || y != math.Trunc(y) {
				return 0, fmt.Errorf("modulo requires integer operands (got %v %% %v)", x, y)
			}
			// Outside int64 the conversion is implementation-defined and
			// would return a silently wrong remainder.
			const twoPow63 = 1 << 63
			if x >= twoPow63 || x < -twoPow63 || y >= twoPow63 || y < -twoPow63 {
				return 0, fmt.Errorf("modulo operands must fit in int64 (got %v %% %v)", x, y)
			}
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
// diff — Structured file comparison
// ═════════════════════════════════════════════════════════════════════════

type diffTool struct {
	ctxTool
	dangerousConfig danger.DangerousConfig
	restrictToCWD   bool // sandbox: reject paths that escape the workspace
}

func (t *diffTool) Name() string { return "diff" }
func (t *diffTool) Description() string {
	return `Compare two files, or a file against inline content (path + content — no temp files needed). Returns structured hunks with type (equal/added/removed) and line-by-line content.`
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

	var textA, textB string
	var pathA, pathB string

	if args.PathA != "" && args.PathB != "" {
		pathA, pathB = args.PathA, args.PathB
		for _, p := range []string{args.PathA, args.PathB} {
			if err := confineIfRestricted(t.restrictToCWD, p); err != nil {
				return jsonError(err.Error())
			}
			if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
				Name: "diff", Resource: p, Risk: classifyResolvedPath(p),
			}, nil); err != nil {
				return jsonError(err.Error())
			}
		}
		data, err := readFileNoFollow(args.PathA)
		if err != nil {
			return jsonResult(diffResult{Error: err.Error(), PathA: pathA, PathB: pathB})
		}
		textA = string(data)
		data, err = readFileNoFollow(args.PathB)
		if err != nil {
			return jsonResult(diffResult{Error: err.Error(), PathA: pathA, PathB: pathB})
		}
		textB = string(data)
	} else if args.Path != "" {
		pathA, pathB = args.Path, "<inline>"
		if err := confineIfRestricted(t.restrictToCWD, args.Path); err != nil {
			return jsonError(err.Error())
		}
		if len(args.Content) > maxFileReadBytes {
			return jsonResult(diffResult{
				Error: fmt.Sprintf("inline content too large (%d bytes, max %d)", len(args.Content), maxFileReadBytes),
				PathA: pathA, PathB: pathB,
			})
		}
		if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
			Name: "diff", Resource: args.Path, Risk: classifyResolvedPath(args.Path),
		}, nil); err != nil {
			return jsonError(err.Error())
		}
		data, err := readFileNoFollow(args.Path)
		if err != nil {
			return jsonResult(diffResult{Error: err.Error(), PathA: pathA, PathB: pathB})
		}
		textA = string(data)
		textB = args.Content
	} else {
		return jsonError("provide either path_a+path_b or path+content")
	}

	// OOM protection: the LCS table allocates (m+1)*(n+1) ints. The
	// per-side cap alone still allowed 10K×10K ≈ 800 MB — bound the cell
	// product too (2026-08 audit): 4M cells ≈ 32 MiB on 64-bit.
	const (
		maxDiffLines = 10000
		maxDiffCells = 4_000_000
	)
	// Count lines before splitting: strings.Split on a newline-only 10 MiB
	// file would allocate ~10M strings just to be rejected.
	countA, countB := strings.Count(textA, "\n")+1, strings.Count(textB, "\n")+1
	if countA > maxDiffLines || countB > maxDiffLines ||
		(countA+1)*(countB+1) > maxDiffCells {
		return jsonResult(diffResult{
			Error: fmt.Sprintf("files too large for in-process diff (%d vs %d lines; max %d lines per side and %d LCS cells).",
				countA, countB, maxDiffLines, maxDiffCells),
			PathA: pathA, PathB: pathB,
		})
	}

	linesA, linesB := strings.Split(textA, "\n"), strings.Split(textB, "\n")

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
			hunks[i].Lines[j].Content = wrapUntrusted(t.toolCtx(), src, hunks[i].Lines[j].Content)
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
// json_query — Query/extract from JSON files
// ═════════════════════════════════════════════════════════════════════════

type jsonQueryTool struct {
	ctxTool
	dangerousConfig danger.DangerousConfig
	restrictToCWD   bool // sandbox: reject paths that escape the workspace
}

func (t *jsonQueryTool) Name() string { return "json_query" }
func (t *jsonQueryTool) Description() string {
	return `Parse a JSON file and extract a value using a dot-path query. Supports array indexing with [N]. Empty query returns the entire parsed JSON.`
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
	if err := confineIfRestricted(t.restrictToCWD, args.Path); err != nil {
		return jsonError(err.Error())
	}

	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "json_query", Resource: args.Path, Risk: classifyResolvedPath(args.Path),
	}, nil); err != nil {
		return jsonError(err.Error())
	}

	f, err := os.OpenFile(args.Path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return jsonResult(jsonQueryResult{Path: args.Path, Error: fmt.Sprintf("cannot open %q: %v", args.Path, err)})
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return jsonResult(jsonQueryResult{Path: args.Path, Error: fmt.Sprintf("cannot stat %q: %v", args.Path, err)})
	}
	if !info.Mode().IsRegular() {
		return jsonResult(jsonQueryResult{Path: args.Path, Error: fmt.Sprintf("cannot query %q: not a regular file", args.Path)})
	}
	if info.Size() > maxFileReadBytes {
		return jsonResult(jsonQueryResult{Path: args.Path, Error: fmt.Sprintf("file too large (%d bytes, max %d)", info.Size(), maxFileReadBytes)})
	}

	dataBytes, err := io.ReadAll(io.LimitReader(f, maxFileReadBytes+1))
	if err != nil {
		return jsonResult(jsonQueryResult{Path: args.Path, Error: fmt.Sprintf("cannot read %q: %v", args.Path, err)})
	}
	if len(dataBytes) > maxFileReadBytes {
		return jsonResult(jsonQueryResult{Path: args.Path, Error: fmt.Sprintf("file too large (%d bytes, max %d)", len(dataBytes), maxFileReadBytes)})
	}
	var data interface{}
	if err := json.Unmarshal(dataBytes, &data); err != nil {
		return jsonResult(jsonQueryResult{Path: args.Path, Error: fmt.Sprintf("invalid JSON: %v", err)})
	}

	if args.Query == "" {
		return t.valueResult(args.Path, "", data)
	}

	value, err := jsonPathQuery(data, args.Query)
	if err != nil {
		return jsonResult(jsonQueryResult{Path: args.Path, Query: args.Query, Error: err.Error()})
	}

	return t.valueResult(args.Path, args.Query, value)
}

// errJSONQueryTooLarge reports a result whose untrusted-wrapped form would
// exceed the tool output bound.
var errJSONQueryTooLarge = errors.New("result too large once wrapped as untrusted content; narrow the query to a smaller subtree")

// jsonWrapBaseOverhead is the budgeted per-string cost of the untrusted
// wrapper beyond the source attribute: the two tags with their nonce, as
// they measure once JSON-encoded (angle brackets and quotes escape to
// several bytes each). The source path is charged on top, per string. The
// estimate is a pre-check so a tiny file cannot explode into gigabytes of
// wrapped output; the rendered result is still measured against the bound.
const jsonWrapBaseOverhead = 128

// jsonWrapper wraps every string in decoded JSON — values and object keys,
// both file content — as untrusted, charging each against a shared output
// budget so per-string wrapper overhead cannot amplify a small file past the
// tool bound.
type jsonWrapper struct {
	ctx      context.Context
	source   string
	budget   int
	overhead int
}

func (w *jsonWrapper) wrap(s string) (string, error) {
	if s == "" {
		return s, nil
	}
	w.budget -= len(s) + w.overhead
	if w.budget < 0 {
		return "", errJSONQueryTooLarge
	}
	return wrapUntrusted(w.ctx, w.source, s), nil
}

func (w *jsonWrapper) walk(v interface{}) (interface{}, error) {
	switch x := v.(type) {
	case string:
		return w.wrap(x)
	case map[string]interface{}:
		out := make(map[string]interface{}, len(x))
		for k, val := range x {
			wk, err := w.wrap(k)
			if err != nil {
				return nil, err
			}
			wv, err := w.walk(val)
			if err != nil {
				return nil, err
			}
			out[wk] = wv
		}
		return out, nil
	case []interface{}:
		for i, val := range x {
			wv, err := w.walk(val)
			if err != nil {
				return nil, err
			}
			x[i] = wv
		}
		return x, nil
	}
	return v, nil
}

// wrapJSONStrings recursively wraps string values and object keys inside
// decoded JSON so that file content returned by json_query is treated as
// untrusted. The wrapped result is bounded by maxFileReadBytes.
func wrapJSONStrings(ctx context.Context, source string, v interface{}) (interface{}, error) {
	w := &jsonWrapper{ctx: ctx, source: source, budget: maxFileReadBytes, overhead: jsonWrapBaseOverhead + len(source)}
	return w.walk(v)
}

// valueResult wraps value and renders the tool result, turning an
// over-budget wrap or an over-bound rendering into an in-band error.
func (t *jsonQueryTool) valueResult(path, query string, value interface{}) (string, error) {
	vt := fmt.Sprintf("%T", value)
	wrapped, err := wrapJSONStrings(t.toolCtx(), path, value)
	if err != nil {
		return jsonResult(jsonQueryResult{Path: path, Query: query, Error: err.Error()})
	}
	out, rerr := jsonResult(jsonQueryResult{Path: path, Query: query, Value: wrapped, ValueType: vt})
	if len(out) > maxFileReadBytes {
		return jsonResult(jsonQueryResult{Path: path, Query: query, Error: errJSONQueryTooLarge.Error()})
	}
	return out, rerr
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
// tree — Structured directory tree listing
// ═════════════════════════════════════════════════════════════════════════

type treeTool struct {
	ctxTool
	dangerousConfig danger.DangerousConfig
	restrictToCWD   bool // sandbox: reject roots that escape the workspace
}

func (t *treeTool) Name() string { return "tree" }
func (t *treeTool) Description() string {
	return `List the directory tree with file counts, sizes, and nesting. Returns a structured tree: each entry shows path, is_dir, file_count, total_size, children, depth. Entries deeper than max_depth (default 3, max 10) are silently cut — pass 10 before concluding a file is absent.`
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
	if err := confineIfRestricted(t.restrictToCWD, args.Path); err != nil {
		return jsonError(err.Error())
	}

	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "tree", Resource: args.Path, Risk: classifyResolvedPath(args.Path),
	}, nil); err != nil {
		return jsonError(err.Error())
	}

	// checkTreePath classifies a discovered path the same way the root path
	// was checked above — the same rule search_files apply via
	// checkSearchPath. A broad root (e.g. $HOME with include_hidden) must not
	// silently expose sensitive subtrees such as ~/.odek or ~/.ssh: names and
	// metadata leak structure even without file contents.
	checkTreePath := func(p string) bool {
		return t.dangerousConfig.CheckOperation(danger.ToolOperation{
			Name: "tree", Resource: p, Risk: classifyResolvedPath(p),
		}, nil) != nil
	}

	entry, err := buildTree(t.toolCtx(), args.Path, args.Path, 0, args.MaxDepth, args.IncludeHidden, checkTreePath)
	if err != nil {
		return jsonResult(treeResult{Error: err.Error()})
	}

	return jsonResult(treeResult{Tree: entry})
}

// skipPath, when non-nil, is consulted for every discovered child path;
// paths it rejects are omitted (search tools apply the identical rule via
// checkSearchPath).
func buildTree(ctx context.Context, root, path string, depth, maxDepth int, includeHidden bool, skipPath func(path string) bool) (treeEntry, error) {
	var info os.FileInfo
	var err error
	if depth == 0 {
		// The explicitly-requested root may be a symlink to a directory
		// (/tmp → /private/tmp on macOS, a user's ~/link). Follow it —
		// otherwise the root reports as a non-directory "file" whose size
		// is the length of the target path and the walk never happens.
		// Descendants keep Lstat semantics below: symlinked entries inside
		// the tree are shown as-is, never followed.
		info, err = os.Stat(path)
	} else {
		info, err = os.Lstat(path)
	}
	if err != nil {
		return treeEntry{Path: wrapUntrusted(ctx, "tree:"+root, path), ErrMsg: err.Error()}, nil
	}

	entry := treeEntry{
		Path:  filepath.Base(path),
		IsDir: info.IsDir(),
		Depth: depth,
	}

	if depth == 0 {
		entry.Path = path
	}

	// Tree paths come from the filesystem trust boundary, so mark them as
	// untrusted before returning them to the model.
	entry.Path = wrapUntrusted(ctx, "tree:"+root, entry.Path)

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

	// Sort, then apply the hidden-file filter, and only then truncate to the
	// entry cap. Truncating first made hidden entries consume the whole
	// budget: in hidden-heavy directories every visible file silently
	// vanished while the notice claimed they were "shown".
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})
	if !includeHidden {
		filtered := entries[:0]
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), ".") {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}
	totalEntries := len(entries)
	truncated := false
	if totalEntries > maxTreeEntries {
		entries = entries[:maxTreeEntries]
		truncated = true
	}

	if truncated {
		entry.ErrMsg = fmt.Sprintf("directory truncated (%d entries shown, %d total)", maxTreeEntries, totalEntries)
	}

	entry.Children = make([]treeEntry, 0, len(entries))
	for _, e := range entries {
		childPath := filepath.Join(path, e.Name())
		// Security: classify each discovered path, not just the requested
		// root. Tree output is names/metadata only, but that still leaks the
		// structure of sensitive subtrees the search tools would skip.
		if skipPath != nil && skipPath(childPath) {
			continue
		}
		child, err := buildTree(ctx, root, childPath, depth+1, maxDepth, includeHidden, skipPath)
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
// checksum — Compute file hashes natively
// ═════════════════════════════════════════════════════════════════════════

type checksumTool struct {
	dangerousConfig danger.DangerousConfig
	restrictToCWD   bool // sandbox: reject paths that escape the workspace
}

func (t *checksumTool) Name() string { return "checksum" }
func (t *checksumTool) Description() string {
	return `Compute SHA-256 (default), SHA-1, or MD5 hash of one file — works inside sandboxes where shell hash tools may be absent.`
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

type checksumResult struct {
	Results []checksumEntry `json:"results"`
}

func (t *checksumTool) Schema() any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"path":      map[string]any{"type": "string", "description": "File to hash."},
		"algorithm": map[string]any{"type": "string", "enum": []string{"sha256", "sha1", "md5"}},
	}, "required": []string{"path"}}
}

func (t *checksumTool) Call(argsJSON string) (string, error) {
	var args checksumFileArg
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}
	if args.Path == "" {
		return jsonError("path is required; use one checksum call per file")
	}
	return jsonResult(checksumResult{Results: []checksumEntry{t.hashFile(args)}})
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
	if err := confineIfRestricted(t.restrictToCWD, arg.Path); err != nil {
		return checksumEntry{Path: arg.Path, Algorithm: strings.ToLower(arg.Algorithm), Error: err.Error()}
	}
	algo := strings.ToLower(arg.Algorithm)
	if algo == "" {
		algo = "sha256"
	}

	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "checksum", Resource: arg.Path, Risk: classifyResolvedPath(arg.Path),
	}, nil); err != nil {
		return checksumEntry{Path: arg.Path, Algorithm: algo, Error: err.Error()}
	}

	f, err := openRegularNoFollow(arg.Path)
	if err != nil {
		return checksumEntry{Path: arg.Path, Algorithm: algo, Error: fmt.Sprintf("cannot open %q: %v", arg.Path, err)}
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return checksumEntry{Path: arg.Path, Algorithm: algo, Error: fmt.Sprintf("cannot stat %q: %v", arg.Path, err)}
	}
	if info.Size() > maxFileReadBytes {
		return checksumEntry{Path: arg.Path, Algorithm: algo, Error: fmt.Sprintf("file too large (%d bytes, max %d)", info.Size(), maxFileReadBytes)}
	}

	var h hash.Hash
	switch algo {
	case "sha256":
		h = sha256.New()
	case "sha1":
		h = sha1.New()
	case "md5":
		h = md5.New()
	default:
		return checksumEntry{Path: arg.Path, Algorithm: algo, Error: fmt.Sprintf("unsupported algorithm: %s", algo)}
	}
	n, err := io.Copy(h, io.LimitReader(f, maxFileReadBytes+1))
	if err != nil {
		return checksumEntry{Path: arg.Path, Algorithm: algo, Error: fmt.Sprintf("cannot hash %q: %v", arg.Path, err)}
	}
	if n > maxFileReadBytes {
		return checksumEntry{Path: arg.Path, Algorithm: algo, Error: fmt.Sprintf("file too large (%d bytes, max %d)", n, maxFileReadBytes)}
	}
	return checksumEntry{Path: arg.Path, Algorithm: algo, Hash: hex.EncodeToString(h.Sum(nil))}
}

// ═════════════════════════════════════════════════════════════════════════
// head_tail — Quick file preview (first/last N lines)
// ═════════════════════════════════════════════════════════════════════════

// maxHeadTailTotalBytes caps the content returned by head_tail for a single
// file. A preview cannot exceed 1 MiB even when individual lines are large.
const maxHeadTailTotalBytes = maxReadBytes // 1 MiB per file

type headTailTool struct {
	ctxTool
	dangerousConfig danger.DangerousConfig
	restrictToCWD   bool // sandbox: reject paths that escape the workspace
}

func (t *headTailTool) Name() string { return "head_tail" }
func (t *headTailTool) Description() string {
	return `Read the first or last N lines of one file. Reports the file's exact total line count (the head path scans the whole file, bounded by a 1 MiB line buffer). Default 10 lines, max 100 — this is a peek, not the file; for whole content use read_file, and compare the returned count to total_lines before trusting it.`
}

type headTailArgs struct {
	Path  string `json:"path"`
	Lines int    `json:"lines,omitempty"`
	Mode  string `json:"mode,omitempty"` // "head" (default) or "tail"
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
			"path":  map[string]any{"type": "string", "description": "File to preview."},
			"lines": map[string]any{"type": "integer", "description": "Number of lines (default: 10, max: 100)."},
			"mode":  map[string]any{"type": "string", "enum": []string{"head", "tail"}, "description": "head (default) or tail."},
		},
		"required": []string{"path"},
	}
}

func (t *headTailTool) Call(argsJSON string) (string, error) {
	var args headTailArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}
	if args.Path == "" {
		return jsonError("path is required; use one head_tail call per file")
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

	if mode != "head" && mode != "tail" {
		return jsonError("mode must be head or tail")
	}
	return jsonResult(headTailResult{Results: []headTailFileResult{t.readPreview(args.Path, n, mode)}})
}

func (t *headTailTool) readPreview(path string, n int, mode string) (result headTailFileResult) {
	defer func() {
		if r := recover(); r != nil {
			result = headTailFileResult{Path: path, Error: fmt.Sprintf("internal error: %v", r)}
		}
	}()
	if err := confineIfRestricted(t.restrictToCWD, path); err != nil {
		return headTailFileResult{Path: path, Error: err.Error()}
	}
	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "head_tail", Resource: path, Risk: classifyResolvedPath(path),
	}, nil); err != nil {
		return headTailFileResult{Path: path, Error: err.Error()}
	}

	f, err := openRegularNoFollow(path)
	if err != nil {
		return headTailFileResult{Path: path, Error: fmt.Sprintf("cannot open %q: %v", path, err)}
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return headTailFileResult{Path: path, Error: fmt.Sprintf("cannot stat %q: %v", path, err)}
	}
	if info.IsDir() {
		return headTailFileResult{Path: path, Error: fmt.Sprintf("%q is a directory — use tree or glob to explore directories", path)}
	}
	if info.Size() > maxFileReadBytes {
		return headTailFileResult{Path: path, Error: fmt.Sprintf("file too large (%d bytes, max %d)", info.Size(), maxFileReadBytes)}
	}

	if mode == "tail" {
		return t.readTail(f, path, n)
	}
	return t.readHead(f, path, n)
}

func (t *headTailTool) readHead(f *os.File, path string, n int) headTailFileResult {
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	var rawLines []string
	total := 0
	for scanner.Scan() {
		total++
		if len(rawLines) < n {
			rawLines = append(rawLines, scanner.Text())
		}
	}
	rawLines = truncateHeadTailLines(rawLines)
	lines := make([]string, len(rawLines))
	for i, l := range rawLines {
		lines[i] = wrapUntrusted(t.toolCtx(), path, l)
	}
	res := headTailFileResult{Path: path, Lines: lines, Count: len(lines), Total: total}
	if err := scanner.Err(); err != nil {
		res.Error = fmt.Sprintf("cannot read %q fully (line over 1 MiB or read error): %v", path, err)
	}
	return res
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
	var rawLines []string
	start := 0
	if written >= n {
		start = written % n
	}
	for i := 0; i < n && i < written; i++ {
		rawLines = append(rawLines, buf[(start+i)%n])
	}
	rawLines = truncateHeadTailLines(rawLines)
	lines := make([]string, len(rawLines))
	for i, l := range rawLines {
		lines[i] = wrapUntrusted(t.toolCtx(), path, l)
	}
	res := headTailFileResult{Path: path, Lines: lines, Count: len(lines), Total: total}
	if err := scanner.Err(); err != nil {
		res.Error = fmt.Sprintf("cannot read %q fully (line over 1 MiB or read error): %v", path, err)
	}
	return res
}

// truncateHeadTailLines truncates a slice of raw lines so the total byte
// count stays within maxHeadTailTotalBytes. It preserves leading lines and
// appends a marker when truncation occurs.
func truncateHeadTailLines(lines []string) []string {
	total := 0
	for i, l := range lines {
		if total+len(l) > maxHeadTailTotalBytes {
			if i == 0 {
				return []string{"... [truncated]"}
			}
			return append(lines[:i], "... [truncated]")
		}
		total += len(l)
	}
	return lines
}

// ═════════════════════════════════════════════════════════════════════════
// base64 — Encode/decode base64
// ═════════════════════════════════════════════════════════════════════════

type base64Tool struct {
	ctxTool
	dangerousConfig danger.DangerousConfig
	restrictToCWD   bool // sandbox: reject paths that escape the workspace
}

func (t *base64Tool) Name() string { return "base64" }
func (t *base64Tool) Description() string {
	return `Encode or decode base64. Supports file input (path) or inline string (content). Encode: file or string → base64. Decode: base64 string → decoded string. Use path for file, content for inline string, decode=true to decode.`
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
	if len(args.String) > maxInlineContentBytes || len(args.Content) > maxInlineContentBytes {
		return jsonError(fmt.Sprintf("inline content too large (max %d bytes)", maxInlineContentBytes))
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
		// Decoded input is externally-sourced data like every other tool
		// output — wrap it in the untrusted boundary (base64 is a classic
		// carrier for obfuscated injection payloads).
		return jsonResult(base64Result{Decoded: wrapUntrusted(t.toolCtx(), "base64:decode", string(decoded)), Size: len(decoded)})
	}

	if args.Path == "" && args.Content == "" {
		return jsonError("provide path (file to encode) or content (inline string)")
	}

	if args.Content != "" {
		encoded := base64.StdEncoding.EncodeToString([]byte(args.Content))
		return jsonResult(base64Result{Encoded: encoded, Size: len(args.Content)})
	}

	// File mode
	if err := confineIfRestricted(t.restrictToCWD, args.Path); err != nil {
		return jsonError(err.Error())
	}
	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "base64", Resource: args.Path, Risk: classifyResolvedPath(args.Path),
	}, nil); err != nil {
		return jsonError(err.Error())
	}

	data, err := readFileNoFollow(args.Path)
	if err != nil {
		return jsonResult(base64Result{Error: fmt.Sprintf("cannot read %q: %v", args.Path, err)})
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	return jsonResult(base64Result{Encoded: wrapUntrusted(t.toolCtx(), args.Path, encoded), Size: len(data)})
}

// ── Compile-time interface checks ────────────────────────────────────
var (
	_ odek.Tool = (*mathEvalTool)(nil)
	_ odek.Tool = (*diffTool)(nil)
	_ odek.Tool = (*jsonQueryTool)(nil)
	_ odek.Tool = (*treeTool)(nil)
	_ odek.Tool = (*checksumTool)(nil)
	_ odek.Tool = (*headTailTool)(nil)
	_ odek.Tool = (*base64Tool)(nil)
)
