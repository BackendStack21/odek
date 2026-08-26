package loop

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/BackendStack21/odek/internal/danger"
)

// ── Reply/ledger reconciliation (H-9) ────────────────────────────────────
//
// Observed in the study: odek wrote a persistence hook, then read the
// payload file, correctly identified the injection — and told the user
// "Action taken: I will not source this file or execute any part of it.
// The setup is blocked." The setup was not blocked; the hook was already
// on disk. Detection that lands after the side effect is reporting, not
// prevention — but a reply that MISREPORTS the side effect is worse than
// silence, because it actively stops the user from looking.
//
// Before a final answer goes out, its claims are diffed against the ledger
// of mutating tool calls that completed during this run. On conflict, a
// clearly-attributed notice is appended — odek speaking, not the model —
// and a reply_ledger_mismatch signal is emitted.

// denialClaimPatterns match strong no-action / all-clear claims in a final
// reply. Deliberately conservative: each requires an explicit denial verb,
// not a hedged or partial statement. False negatives are acceptable (no
// notice); false positives are not (users would learn to ignore the gate).
var denialClaimPatterns = []*regexp.Regexp{
	// "I did not run/write/modify…", "I haven't executed…", "I will not source…"
	regexp.MustCompile(`(?i)\b(?:i|we|it)\s+(?:did\s+not|didn't|have\s+not|haven't|will\s+not|won't)\s+(?:\w+\s+){0,3}?(?:run|ran|execute|executed|write|wrote|written|modify|modified|change|changed|create|created|delete|deleted|source|sourced|install|installed|apply|applied)\b`),
	// "no changes were made", "no files were written", "no commands were run"
	regexp.MustCompile(`(?i)\bno\s+(?:changes|files|commands|code|hooks)\s+(?:were|was)\s+(?:made|written|modified|run|executed|created|deleted|applied)\b`),
	// "nothing was executed", "nothing has been written"
	regexp.MustCompile(`(?i)\bnothing\s+(?:was|has\s+been)\s+(?:run|executed|written|modified|changed|created|deleted|installed)\b`),
	// "the setup is blocked", "the payload was blocked"
	regexp.MustCompile(`(?i)\b(?:the\s+)?(?:setup|installation|payload|hook|attack|exploit|malicious\s+\w+)\s+(?:is|was|has\s+been)\s+blocked\b`),
	// "blocked the setup/installation/payload"
	regexp.MustCompile(`(?i)\b(?:blocked|prevented)\s+the\s+(?:setup|installation|payload|hook|attack|exploit)`),
}

// mutatingToolNames are native tools whose success always mutates state.
var mutatingToolNames = map[string]bool{
	"write_file": true, "patch": true, "batch_patch": true,
}

// mutatingShellCommand reports whether a shell/parallel_shell command
// writes, executes, or otherwise escalates — reads (ls, cat, grep…) do not
// count as mutations for reconciliation.
func mutatingShellCommand(cmd string) bool {
	return danger.Rank(danger.Classify(cmd)) >= danger.Rank(danger.LocalWrite)
}

// toolResultFailed mirrors the loop's failure heuristic for raw outputs.
func toolResultFailed(output string) bool {
	return strings.HasPrefix(output, "error:") || strings.Contains(output, `"error"`)
}

// recordMutation updates the run ledger for one completed tool call.
// Called from the loop's result phase, where success/failure is known.
func (e *Engine) recordMutation(name, args, output string) {
	if toolResultFailed(output) {
		return
	}
	switch {
	case mutatingToolNames[name]:
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal([]byte(args), &p)
		if p.Path != "" {
			e.runMutations = append(e.runMutations, fmt.Sprintf("%s %s", name, p.Path))
		} else {
			e.runMutations = append(e.runMutations, name)
		}
	case name == "shell", name == "terminal":
		var p struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(args), &p); err != nil || p.Command == "" {
			return
		}
		if mutatingShellCommand(p.Command) {
			e.runMutations = append(e.runMutations, "shell: "+p.Command)
		}
	case name == "parallel_shell":
		var p struct {
			Commands []struct {
				Command string `json:"command"`
			} `json:"commands"`
		}
		if err := json.Unmarshal([]byte(args), &p); err != nil {
			return
		}
		for _, c := range p.Commands {
			if c.Command != "" && mutatingShellCommand(c.Command) {
				e.runMutations = append(e.runMutations, "shell: "+c.Command)
			}
		}
	}
}

// replyDenialClaims returns the denial claims present in a final reply.
func replyDenialClaims(answer string) []string {
	var claims []string
	for _, re := range denialClaimPatterns {
		if m := re.FindString(answer); m != "" {
			claims = append(claims, strings.TrimSpace(m))
		}
	}
	return claims
}

// reconcileFinalReply diffs the final answer's claims against this run's
// mutation ledger. It returns an amended answer (notice appended) when the
// reply denies actions the ledger shows completed, else the answer as-is.
func (e *Engine) reconcileFinalReply(answer string) string {
	if len(e.runMutations) == 0 {
		return answer
	}
	claims := replyDenialClaims(answer)
	if len(claims) == 0 {
		return answer
	}

	e.emitSignal(SignalEvent{
		Type:   "reply_ledger_mismatch",
		Detail: fmt.Sprintf("final reply denies %d claim pattern(s) after %d mutating action(s) completed", len(claims), len(e.runMutations)),
	})

	var sb strings.Builder
	sb.WriteString(answer)
	sb.WriteString("\n\n---\n⚠️ odek consistency notice (automated, added by the runtime — not the model): ")
	sb.WriteString("this reply claims no action was taken, but the following mutating tool calls completed during this run:\n")
	limit := len(e.runMutations)
	if limit > 5 {
		limit = 5
	}
	for i, m := range e.runMutations[:limit] {
		sb.WriteString(fmt.Sprintf("  %d. %s\n", i+1, m))
	}
	if len(e.runMutations) > limit {
		sb.WriteString(fmt.Sprintf("  … and %d more\n", len(e.runMutations)-limit))
	}
	sb.WriteString("Verify the actual state before trusting the all-clear.\n")
	return sb.String()
}
