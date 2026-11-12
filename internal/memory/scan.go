package memory

import (
	"context"
	"fmt"
	"regexp"

	"github.com/BackendStack21/odek/internal/guard"
)

// ScanContent checks memory content for security threats. Returns an error if
// the content contains patterns that could compromise the agent.
//
// Checks:
//   - Invisible Unicode characters (zero-width spaces, direction overrides, BOM)
//   - Mixed confusable scripts (Cyrillic/Greek homoglyphs mixed with Latin)
//   - Prompt injection markers ("ignore previous instructions", etc.)
//   - Credential exfiltration patterns (API keys, private keys, bearer tokens)
func ScanContent(content string) error {
	if err := guard.ScanContent(context.Background(), content, nil, nil); err != nil {
		return fmt.Errorf("memory: %v", err)
	}
	return nil
}

// remoteExecRe / evalFetchRe / shellFetchRe match the download-and-execute /
// pipe-to-shell class of instruction — the shape a poisoned "fact" would take
// to turn the always-injected fact files into a persistent backdoor. They are
// deliberately NARROW: legitimate command facts a session should remember
// (e.g. "go test ./...", "make build", "uses Postgres on :5432", "prefers curl
// over wget", "restarts with sudo systemctl") do not match — only a remote
// fetch piped into a shell, or a shell/eval/source run on the output of a
// fetch, do.
//
//   - remoteExecRe: a fetcher whose output is piped into a shell, optionally
//     through wrapper words (sudo -E, doas, env VAR=x, nice, …) and with the
//     shell spelled by path (/bin/sh, /usr/bin/env bash).
//   - evalFetchRe: eval/exec/source applied to a command substitution of a
//     fetcher.
//   - shellFetchRe: a shell (with options such as -c) whose script is a
//     command or process substitution of a fetcher: bash -c "$(curl …)",
//     bash <(curl …), sh < <(wget …).
const (
	fetcherAlt = `(?:curl|wget|fetch|iwr|irm|invoke-webrequest|invoke-restmethod)`
	shellWord  = `(?:[\w./-]*/)?\w*sh`
	wrapperOpt = `(?:\s+-\S+(?:\s+[^-\s|]\S*)?|\s+\w+=\S+)*`
	wrapperPre = `(?:(?:[\w./-]*/)?(?:sudo|doas|env|nice|nohup|time|exec|command)` + wrapperOpt + `\s+)*`
)

var (
	remoteExecRe = regexp.MustCompile(`(?i)\b` + fetcherAlt + `\b[^\n|]*\|\s*` + wrapperPre + shellWord + `\b`)
	evalFetchRe  = regexp.MustCompile(`(?i)\b(eval|exec|source)\b[^\n]*\$\(\s*(curl|wget|fetch)\b`)
	shellFetchRe = regexp.MustCompile("(?i)(?:^|[^\\w])" + shellWord + `\b(?:\s+-\S+)*\s*(?:<\s*)?["']?(?:\$\(|<\(|` + "`" + `)\s*` + fetcherAlt + `\b`)
)

// FactLooksUnsafe reports whether a fact embeds a download-and-execute /
// pipe-to-shell instruction. It is applied to every path that persists a
// fact — auto-extraction at session end and agent-driven memory add/replace
// (AddFact/ReplaceFact) — because facts are injected into every system
// prompt. It does not catch every malicious fact — turning conversation
// into durable memory has an irreducible residual risk — but it closes the
// concrete download-and-run class.
func FactLooksUnsafe(fact string) bool {
	return remoteExecRe.MatchString(fact) || evalFetchRe.MatchString(fact) || shellFetchRe.MatchString(fact)
}
