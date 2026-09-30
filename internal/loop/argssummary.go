package loop

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/BackendStack21/odek/internal/danger"
)

// ── Event argument summaries ────────────────────────────────────
//
// The event stream redacts tool arguments to args_bytes + args_sha256 by
// default — right for secret hygiene, but it means the stream alone cannot
// answer the first question any incident review asks: what actually ran?
//
// argSummary extracts low-cardinality, auditable metadata instead: the
// program name / subcommand, the target paths, and the risk classification.
// Values stay redacted by the emitter; raw argument content never appears
// unless the operator explicitly opts in via EventsIncludeArgs.

const (
	summaryMaxStr = 512
)

func clampSummaryStr(s string) string {
	// Truncate on a rune boundary: byte slicing mid-rune would emit U+FFFD
	// into the JSONL stream.
	if len(s) <= summaryMaxStr {
		return s
	}
	cut := s[:summaryMaxStr]
	for i := len(cut) - 1; i >= 0 && i >= len(cut)-4; i-- {
		if (cut[i] & 0xC0) != 0x80 { // not a continuation byte → boundary at i+1
			return cut[:i+1]
		}
	}
	return cut[:len(cut)-4] // pathological: give up on the last few bytes
}

// argv0 returns the program name a shell command would execute: leading
// VAR=value assignments are skipped, quotes are stripped, and the token is
// reduced to its basename. Best-effort by design — this is audit metadata,
// not a parser.
func argv0(cmd string) string {
	for _, field := range strings.Fields(cmd) {
		if isEnvAssignment(field) {
			continue
		}
		field = strings.Trim(field, `"'`)
		if i := strings.LastIndexByte(field, '/'); i >= 0 {
			field = field[i+1:]
		}
		return clampSummaryStr(field)
	}
	return ""
}

func isEnvAssignment(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}
	head := tok[:eq]
	if head[0] == '_' {
		return true
	}
	for _, r := range head {
		isAlpha := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		isIdent := r == '_' || isAlpha || (r >= '0' && r <= '9')
		if !isIdent {
			return false
		}
	}
	return true
}

// argSummary builds the args_summary payload for a tool_call_started event.
// It returns nil when nothing structured could be extracted.
func argSummary(ctx context.Context, name, argsJSON string) map[string]any {
	switch name {
	case "shell", "terminal":
		var p struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &p); err != nil || p.Command == "" {
			return nil
		}
		cls, _ := danger.ClassifyScriptGateCtx(ctx, p.Command)
		return map[string]any{
			"argv0": argv0(p.Command),
			"class": string(cls),
		}
	case "write_file", "patch":
		// Write tools: the class the write gate actually uses.
		var p struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &p); err != nil || p.Path == "" {
			return nil
		}
		return map[string]any{
			"path":  clampSummaryStr(p.Path),
			"class": string(danger.ClassifyPathWrite(p.Path)),
		}
	case "read_file", "search_files", "file_info",
		"glob", "diff", "json_query", "tree", "count_lines", "checksum",
		"sort", "head_tail", "base64", "tr", "word_count", "transcribe":
		var p struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &p); err != nil || p.Path == "" {
			return nil
		}
		return map[string]any{
			"path":  clampSummaryStr(p.Path),
			"class": string(danger.ClassifyPath(p.Path)),
		}
	case "browser", "http_request", "web_search":
		// Host only: full URLs can embed credentials or unguessable tokens.
		var p struct {
			URL    string `json:"url"`
			Action string `json:"action"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &p); err != nil {
			return nil
		}
		target := p.URL

		host := urlHost(target)
		if host == "" && p.Action != "" {
			return map[string]any{"action": clampSummaryStr(p.Action)}
		}
		if host == "" {
			return nil
		}
		return map[string]any{"host": host}
	case "delegate_tasks":
		var p struct {
			Tasks []json.RawMessage `json:"tasks"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &p); err != nil || len(p.Tasks) == 0 {
			return nil
		}
		return map[string]any{"task_count": len(p.Tasks)}
	default:
		return nil
	}
}

func urlHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return clampSummaryStr(u.Hostname())
}
