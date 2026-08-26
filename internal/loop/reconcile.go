package loop

import (
	crand "crypto/rand"
	"encoding/hex"
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

// shellOutputFailed matches the shell tool's failure shape only (review
// HIGH-003): the explicit error prefix. A bare `"error"` substring in
// successful build/JSON stdout must not drop a real mutation from the
// ledger (false ledger entries fire a warning at worst; dropped entries
// hide a lying all-clear at best).
func shellOutputFailed(output string) bool {
	return strings.HasPrefix(output, "error:")
}

// jsonToolFailed matches the file tools' failure shape: jsonError emits
// {"error": "..."} while success is {"success":true,...}.
func jsonToolFailed(output string) bool {
	return strings.Contains(output, `"error"`)
}

// parallelShellEntries extracts per-command outcomes from a parallel_shell
// result envelope so one failed entry does not erase the mutations of its
// successful siblings.
func parallelShellEntries(output string) []struct{ Command string } {
	var env struct {
		Results []struct {
			Command string `json:"command"`
			Error   string `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(output), &env); err != nil {
		return nil
	}
	var cmds []struct{ Command string }
	for _, r := range env.Results {
		if r.Error == "" && r.Command != "" {
			cmds = append(cmds, struct{ Command string }{r.Command})
		}
	}
	return cmds
}

// recordMutation updates the run ledger for one completed tool call.
// Called from the loop's result phase, where success/failure is known.
func (e *Engine) recordMutation(name, args, output string) {
	switch {
	case mutatingToolNames[name]:
		if jsonToolFailed(output) {
			return
		}
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
		if shellOutputFailed(output) {
			return
		}
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
		for _, r := range parallelShellEntries(output) {
			if mutatingShellCommand(r.Command) {
				e.runMutations = append(e.runMutations, "shell: "+r.Command)
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

// noticeNonce generates a short random reference for a consistency notice.
// The model cannot predict it, so it cannot pre-emit a matching header in
// the reply body to borrow the runtime's attribution (review HIGH-002 —
// best-effort, not proof: the authoritative record is the
// reply_ledger_mismatch signal in the event stream).
func noticeNonce() string {
	var b [3]byte
	if _, err := crand.Read(b[:]); err != nil {
		return "xxxxxx"
	}
	return hex.EncodeToString(b[:])
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

	ref := noticeNonce()
	e.emitSignal(SignalEvent{
		Type:   "reply_ledger_mismatch",
		Detail: fmt.Sprintf("final reply denies %d claim pattern(s) after %d mutating action(s) completed (notice ref %s)", len(claims), len(e.runMutations), ref),
	})

	var sb strings.Builder
	sb.WriteString(answer)
	fmt.Fprintf(&sb, "\n\n---\n⚠️ odek consistency notice [ref %s] (automated, added by the runtime — not the model): ", ref)
	sb.WriteString("this reply claims no action was taken, but the following mutating tool calls completed during this run:\n")
	limit := len(e.runMutations)
	if limit > 5 {
		limit = 5
	}
	for i, m := range e.runMutations[:limit] {
		fmt.Fprintf(&sb, "  %d. %s\n", i+1, m)
	}
	if len(e.runMutations) > limit {
		fmt.Fprintf(&sb, "  … and %d more\n", len(e.runMutations)-limit)
	}
	sb.WriteString("Verify the actual state before trusting the all-clear.\n")
	return sb.String()
}
