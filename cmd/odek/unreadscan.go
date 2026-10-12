package main

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/BackendStack21/odek/internal/danger"
)

// unreadScriptScanMaxBytes caps how much of an unread script the pre-exec
// audit inspects. The audit is enrichment for the approval prompt, not
// an enforcement boundary — visibility (the gate) stays cheap and total,
// while content auditing is capped to keep classification latency flat on
// pathological inputs. A payload beyond the window is unaudited but still
// gated.
const unreadScriptScanMaxBytes = 256 << 10

// Decoding-pass budgets, so hostile inputs cannot turn the audit into a
// CPU/memory sink.
const (
	maxEncodedCandidateBytes = 1 << 20
	maxEncodedTokenBytes     = 128 << 10
	maxDecodedTotalBytes     = 256 << 10
)

// encodedTokenRe matches candidate single-layer encoded blobs inside a
// script: long base64-alphabet runs (with optional padding) and long hex
// runs. "=" is accepted only as trailing padding so a shell assignment
// (NAME=blob) yields the blob on its own. Candidates that fail to decode
// cleanly are ignored; decoded
// garbage cannot match injection patterns, so a finding requires the
// decoded bytes to actually carry an injection phrase.
var encodedTokenRe = regexp.MustCompile(`[A-Za-z0-9+/]{24,}={0,2}|[A-Fa-f0-9]{40,}`)

// urlSafeTokenRe matches runs in the URL-safe base64 alphabet ('-' and '_'
// in place of '+' and '/'). It is a second pass rather than a wider
// alphabet in encodedTokenRe so a standard blob glued to a hyphenated word
// is still extracted on its own by the first pass.
var urlSafeTokenRe = regexp.MustCompile(`[A-Za-z0-9_-]{24,}={0,2}`)

// scanUnreadScripts is the audit-then-exec companion to the
// unread-script gate. For each target it reads the leading
// unreadScriptScanMaxBytes bytes (READ-ONLY) and runs the local rule-based
// injection scanner over the raw bytes and over best-effort single-layer
// base64/hex decodes of embedded blobs. It returns deduplicated,
// human-readable threat labels for surfacing in the approval description,
// so the human decides with content evidence — not just a file path.
//
// Invariants:
//   - it never writes to the read ledger: the auditor is not the model,
//     and a scan that licensed execution would invert the gate;
//   - it never returns raw content, only threat labels;
//   - unreadable targets are skipped silently — the gate still enforces
//     visibility on them.
func scanUnreadScripts(targets []string) []string {
	var findings []string
	seen := make(map[string]bool)
	add := func(rs []danger.ScanResult) {
		for _, r := range rs {
			if !seen[r.Label] {
				seen[r.Label] = true
				findings = append(findings, r.Label)
			}
		}
	}
	for _, tgt := range targets {
		data, err := readScriptHead(tgt, unreadScriptScanMaxBytes)
		if err != nil || len(data) == 0 {
			continue
		}
		add(danger.ScanInjection(string(data)))
		if decoded := decodeEncodedTokens(data); decoded != "" {
			add(danger.ScanInjection(decoded))
		}
	}
	return findings
}

// readScriptHead returns up to limit bytes from the start of a regular
// file, for audit purposes only.
func readScriptHead(path string, limit int64) ([]byte, error) {
	// O_NONBLOCK: opening a FIFO must not hang the approval prompt; the
	// regular-file check below refuses it after the open.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file: %s", path)
	}
	return io.ReadAll(io.LimitReader(f, limit))
}

// wrappedBase64Runs returns the base64 payloads that were wrapped over
// several lines (the 64- and 76-column forms the base64 tools emit),
// each joined into one candidate. A run is consecutive lines of the base64
// alphabet of equal width, ending with a line no wider than the rest. A short
// final line may be a here-doc terminator (EOF), so a run is returned both
// with and without its last line.
func wrappedBase64Runs(text string) []string {
	lines := strings.Split(text, "\n")
	isB64 := func(l string) bool {
		if l == "" {
			return false
		}
		body := strings.TrimRight(l, "=")
		if len(l)-len(body) > 2 || body == "" {
			return false
		}
		for i := 0; i < len(body); i++ {
			c := body[i]
			if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/') {
				return false
			}
		}
		return true
	}
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	var out []string
	for i := 0; i < len(lines); {
		width := len(lines[i])
		if width < 16 || !isB64(lines[i]) || strings.HasSuffix(lines[i], "=") {
			i++
			continue
		}
		j := i + 1
		for j < len(lines) && isB64(lines[j]) && len(lines[j]) <= width && len(lines[j-1]) == width {
			j++
		}
		if j-i >= 2 {
			out = append(out, strings.Join(lines[i:j], ""))
			if j-i >= 3 || len(lines[j-1]) < width {
				out = append(out, strings.Join(lines[i:j-1], ""))
			}
		}
		i = j
	}
	return out
}

// decodedLooksTextual reports whether a decode could carry a text payload:
// valid UTF-8, or mostly printable ASCII. Garbage decoded from ordinary
// paths and identifiers is rejected so it cannot consume the decoded-bytes
// budget that real payloads need.
func decodedLooksTextual(dec string) bool {
	if dec == "" {
		return false
	}
	printable := 0
	for i := 0; i < len(dec); i++ {
		c := dec[i]
		if c == '\n' || c == '\r' || c == '\t' || (c >= 0x20 && c < 0x7f) {
			printable++
		}
	}
	return printable*10 >= len(dec)*9 || (utf8.ValidString(dec) && printable*2 >= len(dec))
}

// decodeEncodedTokens extracts candidate encoded blobs and returns every
// successful single-layer decode, concatenated with separators. Work is
// bounded by bytes, not by candidate count: candidates are deduplicated and
// tried longest first, only text-like decodes are kept, and the total
// decoded and candidate bytes are capped. A script full of ordinary long
// paths therefore cannot starve a real payload.
func decodeEncodedTokens(data []byte) string {
	var (
		b      strings.Builder
		budget = maxDecodedTotalBytes
	)
	text := string(data)
	seen := make(map[string]bool)
	var toks []string
	add := func(tok string) {
		if len(tok) <= maxEncodedTokenBytes && !seen[tok] {
			seen[tok] = true
			toks = append(toks, tok)
		}
	}
	for _, tok := range wrappedBase64Runs(text) {
		add(tok)
	}
	for _, tok := range encodedTokenRe.FindAllString(text, -1) {
		add(tok)
	}
	for _, tok := range urlSafeTokenRe.FindAllString(text, -1) {
		if strings.ContainsAny(tok, "-_") {
			add(tok)
		}
	}
	sort.SliceStable(toks, func(i, j int) bool { return len(toks[i]) > len(toks[j]) })
	work := maxEncodedCandidateBytes
	for _, tok := range toks {
		if work -= len(tok); work < 0 {
			break
		}
		for _, dec := range tryDecodeToken(tok) {
			if !decodedLooksTextual(dec) {
				continue
			}
			if len(dec) > budget {
				dec = dec[:budget]
			}
			b.WriteByte('\n')
			b.WriteString(dec)
			budget -= len(dec)
			if budget <= 0 {
				return b.String()
			}
		}
	}
	return b.String()
}

// tryDecodeToken attempts hex first (even-length, hex-alphabet tokens),
// then the four base64 alphabets. All successful decodes are returned — a
// token can be valid under more than one encoding, and scanning every
// plausible decode is the fail-safe direction.
func tryDecodeToken(tok string) []string {
	var out []string
	if len(tok)%2 == 0 {
		if dec, err := hex.DecodeString(tok); err == nil {
			out = append(out, string(dec))
		}
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if dec, err := enc.DecodeString(tok); err == nil {
			out = append(out, string(dec))
		}
	}
	return out
}
