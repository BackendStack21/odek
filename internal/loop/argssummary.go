package loop

import (
	"encoding/json"
	"net/url"
	"strings"

	"github.com/BackendStack21/odek/internal/danger"
)

// ── Event argument summaries (P0-4) ────────────────────────────────────
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
	summaryMaxStr  = 512
	summaryMaxList = 16
)

func clampSummaryStr(s string) string {
	if len(s) > summaryMaxStr {
		return s[:summaryMaxStr]
	}
	return s
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
func argSummary(name, argsJSON string) map[string]any {
	switch name {
	case "shell", "terminal":
		var p struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &p); err != nil || p.Command == "" {
			return nil
		}
		return map[string]any{
			"argv0": argv0(p.Command),
			"class": string(danger.Classify(p.Command)),
		}
	case "parallel_shell":
		var p struct {
			Commands []struct {
				Command string `json:"command"`
			} `json:"commands"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &p); err != nil || len(p.Commands) == 0 {
			return nil
		}
		maxRank := 0
		var argv0s []string
		for _, c := range p.Commands {
			if c.Command == "" {
				continue
			}
			argv0s = append(argv0s, argv0(c.Command))
			if r := danger.Rank(danger.Classify(c.Command)); r > maxRank {
				maxRank = r
			}
			if len(argv0s) >= summaryMaxList {
				break
			}
		}
		if len(argv0s) == 0 {
			return nil
		}
		return map[string]any{
			"argv0": argv0s,
			"class": string(riskClassFromRank(maxRank)),
		}
	case "read_file", "write_file", "patch", "search_files", "batch_read", "file_info",
		"glob", "diff", "multi_grep", "json_query", "tree", "count_lines", "checksum",
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
	case "batch_patch":
		var p struct {
			Patches []struct {
				Path string `json:"path"`
			} `json:"patches"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &p); err != nil || len(p.Patches) == 0 {
			return nil
		}
		maxRank := 0
		var paths []string
		for _, patch := range p.Patches {
			if patch.Path == "" {
				continue
			}
			paths = append(paths, clampSummaryStr(patch.Path))
			if r := danger.Rank(danger.ClassifyPath(patch.Path)); r > maxRank {
				maxRank = r
			}
			if len(paths) >= summaryMaxList {
				break
			}
		}
		if len(paths) == 0 {
			return nil
		}
		return map[string]any{
			"path":  paths,
			"class": string(riskClassFromRank(maxRank)),
		}
	case "browser", "http_batch", "web_search":
		// Host only: full URLs can embed credentials or unguessable tokens.
		var p struct {
			URL     string `json:"url"`
			Action  string `json:"action"`
			Queries []struct {
				URL string `json:"url"`
			} `json:"requests"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &p); err != nil {
			return nil
		}
		target := p.URL
		if target == "" && len(p.Queries) > 0 {
			target = p.Queries[0].URL
		}
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
