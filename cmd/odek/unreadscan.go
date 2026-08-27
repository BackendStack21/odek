package main

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/BackendStack21/odek/internal/danger"
)

// unreadScriptScanMaxBytes caps how much of an unread script the pre-exec
// audit inspects. The audit is enrichment for the H-6 approval prompt, not
// an enforcement boundary — visibility (the gate) stays cheap and total,
// while content auditing is capped to keep classification latency flat on
// pathological inputs. A payload beyond the window is unaudited but still
// gated.
const unreadScriptScanMaxBytes = 256 << 10

// Decoding-pass budgets, so hostile inputs cannot turn the audit into a
// CPU/memory sink.
const (
	maxEncodedTokens     = 64
	maxEncodedTokenBytes = 128 << 10
	maxDecodedTotalBytes = 256 << 10
)

// encodedTokenRe matches candidate single-layer encoded blobs inside a
// script: long base64-alphabet runs (with optional padding) and long hex
// runs. Candidates that fail to decode cleanly are ignored; decoded
// garbage cannot match injection patterns, so a finding requires the
// decoded bytes to actually carry an injection phrase.
var encodedTokenRe = regexp.MustCompile(`[A-Za-z0-9+/=]{24,}|[A-Fa-f0-9]{40,}`)

// scanUnreadScripts is the audit-then-exec companion to the H-6
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
	f, err := os.Open(path)
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

// decodeEncodedTokens extracts candidate encoded blobs and returns every
// successful single-layer decode, concatenated with separators and bounded
// by the decoding-pass budgets.
func decodeEncodedTokens(data []byte) string {
	var (
		b      strings.Builder
		budget = maxDecodedTotalBytes
	)
	for _, tok := range encodedTokenRe.FindAllString(string(data), maxEncodedTokens) {
		if len(tok) > maxEncodedTokenBytes {
			continue
		}
		for _, dec := range tryDecodeToken(tok) {
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
