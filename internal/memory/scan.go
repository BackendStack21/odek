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

// The matchers below recognise the download-and-execute / pipe-to-shell class
// of instruction — the shape a poisoned "fact" would take to turn the
// always-injected fact files into a persistent backdoor. They are deliberately
// NARROW: legitimate command facts a session should remember (e.g. "go test
// ./...", "make build", "uses Postgres on :5432", "prefers curl over wget",
// "restarts with sudo systemctl") do not match — only a remote fetch piped
// into a shell, or a shell/eval/source run on the output of a fetch, do. The
// shell is named from an explicit list and must start a word, so words that
// merely end in "sh" (publish, finish) and file names (deploy.sh) are
// ordinary text.
//
//   - pipeExecRe: a fetcher with at least one argument (one without fetches
//     nothing, and a Markdown table row such as `| curl | wget | sh |` names
//     tools) whose output reaches a shell, directly or through up to
//     maxPipeStages pass-through stages (tee, grep, …), and optionally via
//     xargs/parallel, wrapper words (sudo -E, doas, env VAR=x, nice, …) and a
//     shell spelled by path (/bin/sh, /usr/bin/env bash, busybox sh). Each
//     pipe is a single `|` (or `|&`): `||` is a list operator, so
//     `curl … || bash x.sh` does not run the fetch's output.
//   - evalFetchRe: eval/exec/source with a command or process substitution
//     of a fetcher anywhere later on the line.
//   - shellArgFetchRe: a shell (with options such as -c) whose argument is a
//     command or process substitution of a fetcher: bash -c "$(curl …)",
//     bash <(curl …).
//   - redirectFetchRe: a shell, or source/`.` of /dev/stdin, fed a
//     substitution of a fetcher through a redirect, a here-string or a
//     here-doc: sh < <(wget …), bash <<< "$(curl …)", bash <<EOF $(curl …).
//   - dotFetchRe: the `.` builtin applied directly to a substitution of a
//     fetcher: . <(curl …).
//   - backtickFetchRe: a backtick substitution of a fetcher counts only as an
//     argument of a sink: anywhere after eval (which re-parses its
//     arguments), after a shell's -c option, or right after a shell, exec,
//     source or `.` standing in command position (the start of the fact or a
//     line, after ; & | ( { a quote, $( or a label colon, or after run, then,
//     do or else; wrappers such as sudo may come between). A shell or exec
//     handed the output as words runs it: bash `curl …` is code execution
//     once the output starts with -c. A Markdown code span in prose ("the
//     data source is `fetch`", "prefers bash `curl -s` aliases") is not a
//     substitution.
//
// All of them are RE2 expressions, so matching is linear in the fact length.
const (
	fetcherAlt = `(?:curl|wget|fetch|iwr|irm|invoke-webrequest|invoke-restmethod)`
	shellNames = `(?:sh|bash|zsh|dash|ksh|ash|fish|csh|tcsh|mksh|rbash|pwsh)`
	shellWord  = `(?:(?:[\w./-]*/)?busybox\s+)?(?:[\w./-]*/)?` + shellNames
	// shellStart is what may precede a shell word: the start of the fact or a
	// character that cannot continue a word or a file name.
	shellStart = `(?:^|[^\w.-])`
	shellOpts  = `(?:\s+-\S+)*`
	// shellCOpts are shell options that include a -c form (-c, -ec, -e -c).
	shellCOpts = shellOpts + `\s+-[a-z]*c[a-z]*` + shellOpts
	wrapperOpt = `(?:\s+-\S+(?:\s+[^-\s|]\S*)?|\s+\w+=\S+)*`
	wrapperPre = `(?:(?:[\w./-]*/)?(?:sudo|doas|env|nice|nohup|time|exec|command)` + wrapperOpt + `\s+)*`
	// xargsPre lets xargs/parallel (with options such as -n 1, -I{}) launch the shell.
	xargsPre = `(?:(?:xargs|parallel)(?:\s+-\S+(?:\s+\d+)?)*\s+)?`
	// fetchArgs is a fetcher's argument list up to the first pipe: at least
	// one non-blank character.
	fetchArgs = `[^\S\n]*[^\s|][^\n|]*`
	// pipeStage is one pass-through stage after a single-bar pipe.
	pipeStage = `\|&?[^\n|]+`
	// maxPipeStages bounds the pass-through stages between a fetcher and the shell.
	maxPipeStages = "4"
	// pipeSink is what the last pipe feeds: a shell, possibly through xargs
	// and wrappers.
	pipeSink = xargsPre + wrapperPre + shellWord + `\b`
	// substOpen opens a command or process substitution.
	substOpen = `(?:\$\(|<\()`
	// substOpenTick also accepts a backtick, where the syntax around it
	// (a redirect, here-string or here-doc) is unambiguous shell.
	substOpenTick = "(?:\\$\\(|<\\(|`)"
	// hereOp is a redirect, here-string or here-doc operator with an optional
	// here-doc delimiter line.
	hereOp = `<{1,3}-?\s*(?:["']?\w+["']?\s*\n\s*)?`
	// stdinPath names the standard input as a file.
	stdinPath = `(?:/dev/stdin|/dev/fd/0|/proc/self/fd/0)`
	// cmdPos is the start of a command: the fact or a line, a shell
	// separator, a quote, an opening substitution, a label colon or a word
	// that introduces a command.
	cmdPos = "(?:^|[\\n;&|({\"'`]|\\$\\(|:|\\b(?:run|then|do|else)\\s)\\s*"
)

var (
	pipeExecRe      = regexp.MustCompile(`(?i)\b` + fetcherAlt + `\b` + fetchArgs + `(?:` + pipeStage + `){0,` + maxPipeStages + `}\|&?\s*` + pipeSink)
	evalFetchRe     = regexp.MustCompile(`(?i)\b(?:eval|exec|source)\b[^\n]*` + substOpen + `\s*` + fetcherAlt + `\b`)
	shellArgFetchRe = regexp.MustCompile(`(?i)` + shellStart + shellWord + `\b` + shellOpts + `\s*["']?` + substOpen + `\s*` + fetcherAlt + `\b`)
	redirectFetchRe = regexp.MustCompile(`(?i)(?:` + shellStart + shellWord + `\b` + shellOpts + `|` + shellStart + `(?:source|\.)\s+` + stdinPath + `)\s*` + hereOp + `["']?` + substOpenTick + `\s*` + fetcherAlt + `\b`)
	dotFetchRe      = regexp.MustCompile(`(?i)(?:^|[\s;&|(])\.\s+["']?` + substOpen + `\s*` + fetcherAlt + `\b`)
	backtickFetchRe = regexp.MustCompile("(?i)(?:\\beval\\b[^\\n]*|" + shellStart + shellWord + `\b` + shellCOpts + `\s+["']?|` +
		cmdPos + wrapperPre + `(?:` + shellWord + `\b` + shellOpts + `|exec|source|\.)\s+["']?)` + "`\\s*" + fetcherAlt + `\b`)
)

// FactLooksUnsafe reports whether a fact embeds a download-and-execute /
// pipe-to-shell instruction. It is applied to every path that persists a
// fact — auto-extraction at session end and agent-driven memory add/replace
// (AddFact/ReplaceFact) — because facts are injected into every system
// prompt. It does not catch every malicious fact — turning conversation
// into durable memory has an irreducible residual risk — but it closes the
// concrete download-and-run class.
func FactLooksUnsafe(fact string) bool {
	for _, re := range factUnsafeRes {
		if re.MatchString(fact) {
			return true
		}
	}
	return false
}

var factUnsafeRes = []*regexp.Regexp{pipeExecRe, evalFetchRe, shellArgFetchRe, redirectFetchRe, dotFetchRe, backtickFetchRe}
