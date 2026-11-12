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

// remoteExecRe / evalFetchRe / dotFetchRe / shellFetchRe match the
// download-and-execute / pipe-to-shell class of instruction — the shape a
// poisoned "fact" would take to turn the always-injected fact files into a
// persistent backdoor. They are deliberately NARROW: legitimate command facts
// a session should remember (e.g. "go test ./...", "make build", "uses
// Postgres on :5432", "prefers curl over wget", "restarts with sudo
// systemctl") do not match — only a remote fetch piped into a shell, or a
// shell/eval/source run on the output of a fetch, do. The shell is named from
// an explicit list, so words that merely end in "sh" (publish, finish) are
// ordinary text.
//
//   - remoteExecRe: a fetcher whose output reaches a shell, directly or
//     through up to maxPipeStages pass-through stages (tee, grep, …), and
//     optionally via xargs/parallel, wrapper words (sudo -E, doas, env VAR=x,
//     nice, …) and a shell spelled by path (/bin/sh, /usr/bin/env bash,
//     busybox sh).
//   - evalFetchRe / dotFetchRe: eval/exec/source or the `.` builtin applied to
//     a command or process substitution of a fetcher.
//   - shellFetchRe: a shell (with options such as -c) whose script is a
//     command or process substitution of a fetcher, handed over as an
//     argument, a redirect, a here-string or a here-doc: bash -c "$(curl …)",
//     bash <(curl …), sh < <(wget …), bash <<< "$(curl …)".
//
// All of them are RE2 expressions, so matching is linear in the fact length.
const (
	fetcherAlt = `(?:curl|wget|fetch|iwr|irm|invoke-webrequest|invoke-restmethod)`
	shellNames = `(?:sh|bash|zsh|dash|ksh|ash|fish|csh|tcsh|mksh|rbash|pwsh)`
	shellWord  = `(?:(?:[\w./-]*/)?busybox\s+)?(?:[\w./-]*/)?` + shellNames
	wrapperOpt = `(?:\s+-\S+(?:\s+[^-\s|]\S*)?|\s+\w+=\S+)*`
	wrapperPre = `(?:(?:[\w./-]*/)?(?:sudo|doas|env|nice|nohup|time|exec|command)` + wrapperOpt + `\s+)*`
	// xargsPre lets xargs/parallel (with options such as -n 1, -I{}) launch the shell.
	xargsPre = `(?:(?:xargs|parallel)(?:\s+-\S+(?:\s+\d+)?)*\s+)?`
	// maxPipeStages bounds the pass-through stages between a fetcher and the shell.
	maxPipeStages = "4"
	// substOpen opens a command substitution, process substitution or backtick.
	substOpen = "(?:\\$\\(|<\\(|`)"
)

var (
	remoteExecRe = regexp.MustCompile(`(?i)\b` + fetcherAlt + `\b[^\n|]*(?:\|[^\n|]*){0,` + maxPipeStages + `}\|\s*` + xargsPre + wrapperPre + shellWord + `\b`)
	evalFetchRe  = regexp.MustCompile(`(?i)\b(?:eval|exec|source)\b[^\n]*` + substOpen + `\s*` + fetcherAlt + `\b`)
	dotFetchRe   = regexp.MustCompile(`(?i)(?:^|[\s;&|(])\.\s+[^\n]*` + substOpen + `\s*` + fetcherAlt + `\b`)
	shellFetchRe = regexp.MustCompile("(?i)(?:^|[^\\w])" + shellWord + `\b(?:\s+-\S+)*\s*(?:<{1,3}-?\s*(?:["']?\w+["']?\s*\n\s*)?)?["']?` + substOpen + `\s*` + fetcherAlt + `\b`)
)

// FactLooksUnsafe reports whether a fact embeds a download-and-execute /
// pipe-to-shell instruction. It is applied to every path that persists a
// fact — auto-extraction at session end and agent-driven memory add/replace
// (AddFact/ReplaceFact) — because facts are injected into every system
// prompt. It does not catch every malicious fact — turning conversation
// into durable memory has an irreducible residual risk — but it closes the
// concrete download-and-run class.
func FactLooksUnsafe(fact string) bool {
	return remoteExecRe.MatchString(fact) || evalFetchRe.MatchString(fact) ||
		dotFetchRe.MatchString(fact) || shellFetchRe.MatchString(fact)
}
