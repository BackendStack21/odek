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
// reduced to its basename. Words are split quote-aware, so no part of a
// quoted assignment value (often a secret) can be mistaken for the program.
// Best-effort by design — this is audit metadata, not a parser.
func argv0(cmd string) string {
	for _, w := range shellWords(cmd) {
		ok, open := assignmentWord(w.raw)
		if open {
			return ""
		}
		if ok {
			continue
		}
		// A word that opens a subshell, group or substitution (or carries
		// shell metacharacters) is not a program name; its body may hold
		// assignment values, so nothing of it is reported.
		if strings.ContainsAny(w.value, "(){}$`;&|<>") {
			return ""
		}
		field := w.value
		if i := strings.LastIndexByte(field, '/'); i >= 0 {
			field = field[i+1:]
		}
		return clampSummaryStr(field)
	}
	return ""
}

// shellWord is one whitespace-separated shell word: raw keeps its quoting
// (assignment detection needs the unquoted name before '='), value has the
// quotes and backslash escapes removed.
type shellWord struct{ raw, value string }

// shellWords splits cmd into words honouring single quotes, double quotes,
// backslash escapes and $( ... ), ${ ... } and backtick substitutions (each
// is consumed whole, including quotes and parentheses nested inside it).
// A word left open by an unterminated quote or substitution is dropped, and
// so is everything after it, so no fragment of its contents surfaces as a
// word of its own.
func shellWords(cmd string) []shellWord {
	var words []shellWord
	var raw, val strings.Builder
	inWord := false
	var quote byte
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		if quote != '\'' {
			if end, ok := substitutionEnd(cmd, i); ok {
				if end < 0 {
					return words
				}
				raw.WriteString(cmd[i : end+1])
				val.WriteString(cmd[i : end+1])
				inWord = true
				i = end
				continue
			}
		}
		switch {
		case quote == '\'':
			raw.WriteByte(c)
			if c == '\'' {
				quote = 0
			} else {
				val.WriteByte(c)
			}
		case quote == '"':
			raw.WriteByte(c)
			switch {
			case c == '"':
				quote = 0
			case c == '\\' && i+1 < len(cmd):
				i++
				raw.WriteByte(cmd[i])
				val.WriteByte(cmd[i])
			default:
				val.WriteByte(c)
			}
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if inWord {
				words = append(words, shellWord{raw: raw.String(), value: val.String()})
				raw.Reset()
				val.Reset()
				inWord = false
			}
		case c == '(' && inWord && isEnvAssignment(raw.String()) && strings.HasSuffix(raw.String(), "="):
			// Array assignment NAME=(a b): the whole parenthesised value
			// belongs to the assignment word.
			end, _ := scanClosing(cmd, i+1, ')')
			if end < 0 {
				return words
			}
			raw.WriteString(cmd[i : end+1])
			val.WriteString(cmd[i : end+1])
			i = end
		default:
			inWord = true
			raw.WriteByte(c)
			switch {
			case c == '\'' || c == '"':
				quote = c
			case c == '\\' && i+1 < len(cmd):
				i++
				raw.WriteByte(cmd[i])
				val.WriteByte(cmd[i])
			default:
				val.WriteByte(c)
			}
		}
	}
	if inWord && quote == 0 {
		words = append(words, shellWord{raw: raw.String(), value: val.String()})
	}
	return words
}

// substitutionEnd reports whether a $( ... ), ${ ... } or backtick
// substitution opens at cmd[i]. When it does, end is the index of its
// closing delimiter, or -1 when it never closes. Quotes, escapes and
// nested substitutions or parentheses inside it are skipped.
func substitutionEnd(cmd string, i int) (end int, ok bool) {
	var closer byte
	j := i + 1
	switch {
	case cmd[i] == '`':
		closer = '`'
	case cmd[i] == '$' && i+1 < len(cmd) && cmd[i+1] == '(':
		closer, j = ')', i+2
	case cmd[i] == '$' && i+1 < len(cmd) && cmd[i+1] == '{':
		closer, j = '}', i+2
	default:
		return 0, false
	}
	return scanClosing(cmd, j, closer)
}

// scanClosing finds the delimiter closing a construct whose body starts at
// cmd[j]; it returns -1 when the construct never closes.
func scanClosing(cmd string, j int, closer byte) (end int, ok bool) {
	parens := 0
	for ; j < len(cmd); j++ {
		c := cmd[j]
		switch {
		case c == '\\' && j+1 < len(cmd):
			j++
		case c == '\'':
			k := strings.IndexByte(cmd[j+1:], '\'')
			if k < 0 {
				return -1, true
			}
			j += k + 1
		case c == '"':
			for j++; j < len(cmd) && cmd[j] != '"'; j++ {
				if cmd[j] == '\\' {
					j++
				} else if e, nested := substitutionEnd(cmd, j); nested {
					if e < 0 {
						return -1, true
					}
					j = e
				}
			}
			if j >= len(cmd) {
				return -1, true
			}
		case c == '`' && closer != '`':
			e, _ := substitutionEnd(cmd, j)
			if e < 0 {
				return -1, true
			}
			j = e
		case c == '$' && j+1 < len(cmd) && (cmd[j+1] == '(' || cmd[j+1] == '{'):
			e, _ := substitutionEnd(cmd, j)
			if e < 0 {
				return -1, true
			}
			j = e
		case c == '(' && closer == ')':
			parens++
		case c == closer:
			if closer == ')' && parens > 0 {
				parens--
				continue
			}
			return j, true
		}
	}
	return -1, true
}

func isEnvAssignment(tok string) bool {
	ok, _ := assignmentWord(tok)
	return ok
}

// assignmentWord reports whether tok is a variable assignment, including
// NAME+=value and subscripted NAME[expr]=value / NAME[expr]+=value forms.
// open is true when the word starts like a subscripted assignment whose
// subscript never closes: the rest of the command cannot be attributed, so
// callers report no program name.
func assignmentWord(tok string) (ok, open bool) {
	i := 0
	for i < len(tok) && (tok[i] == '_' || tok[i] >= 'a' && tok[i] <= 'z' || tok[i] >= 'A' && tok[i] <= 'Z' || tok[i] >= '0' && tok[i] <= '9') {
		i++
	}
	if i > 0 && i < len(tok) && tok[i] == '[' {
		depth := 0
		for j := i; j < len(tok); j++ {
			switch tok[j] {
			case '[':
				depth++
			case ']':
				depth--
				if depth == 0 {
					rest := strings.TrimPrefix(tok[j+1:], "+")
					return strings.HasPrefix(rest, "="), false
				}
			}
		}
		return false, true
	}
	return plainAssignment(tok), false
}

func plainAssignment(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}
	head := strings.TrimSuffix(tok[:eq], "+")
	if head == "" {
		return false
	}
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
