package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/llmclient"
	"github.com/BackendStack21/odek/internal/session"
)

// Verification disposition modes for the final-answer verify stage.
const (
	VerifyModeHint   = "hint"   // corrective re-try, bounded (default)
	VerifyModeStrict = "strict" // marker header on the returned answer
	VerifyModeOff    = "off"
)

// VerifyConfig is the engine-level verification-pass configuration. It is
// resolved from the operator-level "verify" config section and disabled by
// default: the zero value never runs a verification side call.
type VerifyConfig struct {
	Enabled bool
	// Mode is hint | strict | off. An unrecognized value resolves to hint.
	Mode string
	// MaxCycles bounds corrective re-tries after a failing verdict.
	MaxCycles int
}

// verifyCyclesMax is the absolute ceiling on corrective cycles regardless of
// configuration; verification must stay a bounded post-check, not a loop.
const verifyCyclesMax = 3

// VerifyFailedMarker prefixes the returned answer in strict mode when the
// verifier returns fail. It is a fixed string: verifier prose is never
// concatenated into the returned answer.
const VerifyFailedMarker = "[Verification failed — answer returned unverified]"

// verifyVerdictMaxBytes caps the side-call output eligible for verdict
// parsing; anything larger is treated as malformed (uncertain).
const verifyVerdictMaxBytes = 8 * 1024

// verifyVerdict is the structured verdict returned by the verifier side
// call. Strictly parsed: a malformed payload resolves to uncertain, never to
// executable content.
type verifyVerdict struct {
	Verdict string   `json:"verdict"`
	Reasons []string `json:"reasons"`
	Missing []string `json:"missing"`
}

// parseVerifyVerdict extracts the verdict JSON object from verifier output.
// Leading/trailing prose around the JSON object is tolerated (models wrap
// JSON in text), but the object itself must be well-formed and contain a
// recognized verdict value; anything else is uncertain.
func parseVerifyVerdict(content string) verifyVerdict {
	if len(content) > verifyVerdictMaxBytes {
		return verifyVerdict{Verdict: "uncertain"}
	}
	obj, ok := extractJSONObject(content)
	if !ok {
		return verifyVerdict{Verdict: "uncertain"}
	}
	var v verifyVerdict
	if err := json.Unmarshal([]byte(obj), &v); err != nil {
		return verifyVerdict{Verdict: "uncertain"}
	}
	switch strings.ToLower(strings.TrimSpace(v.Verdict)) {
	case "pass", "fail":
		v.Verdict = strings.ToLower(strings.TrimSpace(v.Verdict))
	default:
		return verifyVerdict{Verdict: "uncertain"}
	}
	// Reasons/missing are advisory only; clamp their combined rendering.
	for i, s := range v.Reasons {
		if len(s) > 200 {
			v.Reasons[i] = s[:200] + "…"
		}
	}
	for i, s := range v.Missing {
		if len(s) > 200 {
			v.Missing[i] = s[:200] + "…"
		}
	}
	return v
}

// extractJSONObject finds the first balanced top-level JSON object in s.
func extractJSONObject(s string) (string, bool) {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return "", false
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
}

// verifyEnabled reports whether the final-answer verification stage runs.
// mode "off" disables the stage even when Enabled is true, mirroring the
// config surface where "off" is an explicit opt-out.
func (e *Engine) verifyEnabled() bool {
	return e.verifyCfg.Enabled && e.verifyCfg.Mode != VerifyModeOff && e.verifyCyclesLeft()
}

func (e *Engine) verifyCyclesLeft() bool {
	max := e.verifyCfg.MaxCycles
	if max <= 0 {
		max = 1
	}
	if max > verifyCyclesMax {
		max = verifyCyclesMax
	}
	return e.verifyCyclesUsed < max
}

// SetVerify installs the verification-pass configuration.
func (e *Engine) SetVerify(cfg VerifyConfig) { e.verifyCfg = cfg }

// SetVerifyClient overrides the LLM client used for verification side calls
// (a cheaper model). Nil keeps the engine's main client.
func (e *Engine) SetVerifyClient(c *llmclient.Client) { e.verifyClient = c }

// verifyPrompt builds the tool-less verifier prompt. The verifier evaluates
// only — it has no tools, and its output is parsed as a verdict, never
// executed. Untrusted tool-result content inside `messages` is summarized
// name-only (tool call trace), keeping the verifier prompt free of raw
// external content beyond the final answer itself.
func verifyPrompt(task string, toolTrace string, answer string) string {
	var b strings.Builder
	b.WriteString("You are a strict verifier for an AI agent runtime. Evaluate whether the agent's final answer satisfies the original task.\n\n")
	b.WriteString("Respond with ONLY a JSON object:\n")
	b.WriteString(`{"verdict":"pass|fail","reasons":["..."],"missing":["..."]}` + "\n")
	b.WriteString("Rules: verdict \"pass\" only if the answer addresses the task and its claims are supported by the tool trace. ")
	b.WriteString("Flag unsupported claims, skipped verifications, or unanswered parts in reasons/missing. ")
	b.WriteString("When in doubt, prefer \"fail\" with an explanation over guessing.\n\n")
	b.WriteString("## Original task\n")
	b.WriteString(task + "\n\n")
	b.WriteString("## Tool call trace (names and truncated results)\n")
	b.WriteString(toolTrace + "\n\n")
	b.WriteString("## Final answer to verify\n")
	b.WriteString(answer + "\n")
	return b.String()
}

// verifyToolTrace renders the executed tool calls from the message history:
// names plus truncated results. Bounded to keep the side-call prompt small.
func verifyToolTrace(messages []session.Message) string {
	var b strings.Builder
	n := 0
	for _, m := range messages {
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		for _, tc := range m.ToolCalls {
			if n > 0 {
				b.WriteString("\n")
			}
			n++
			fmt.Fprintf(&b, "%d. %s", n, tc.Function.Name)
			if args := strings.TrimSpace(tc.Function.Arguments); args != "" {
				if len(args) > 160 {
					args = args[:160] + "…"
				}
				fmt.Fprintf(&b, "(%s)", args)
			}
		}
	}
	if n == 0 {
		return "(no tool calls were executed)"
	}
	return b.String()
}

// verifyOriginalTask extracts the first user message as the task under
// verification.
func verifyOriginalTask(messages []session.Message) string {
	for _, m := range messages {
		if m.Role == "user" {
			t := strings.TrimSpace(m.Content)
			if len(t) > 2000 {
				return t[:2000] + "…"
			}
			return t
		}
	}
	return "(no user task found)"
}

// runVerifyStage runs one verification side call against the candidate final
// answer and returns the verdict. The boolean is false when the stage was
// skipped (disabled, out of cycles, or budget), in which case no corrective
// action may be taken.
func (e *Engine) runVerifyStage(ctx context.Context, messages []session.Message, answer string) (verifyVerdict, bool) {
	if !e.verifyEnabled() {
		return verifyVerdict{}, false
	}
	if e.client == nil || !e.budgetAllowsSideCall() {
		e.emitEvent(events.Event{
			Type: events.TypeVerificationCompleted,
			Data: map[string]any{"verdict": "skipped", "skipped_reason": "budget"},
		})
		return verifyVerdict{}, false
	}
	client := e.client
	if e.verifyClient != nil {
		client = e.verifyClient
	}
	e.emitEvent(events.Event{Type: events.TypeVerificationStarted})
	callCtx, cancel := context.WithTimeout(ctx, e.sideTimeout())
	defer cancel()
	res, err := client.SideCall(callCtx, []session.Message{
		{Role: "user", Content: verifyPrompt(verifyOriginalTask(messages), verifyToolTrace(messages), answer)},
	})
	if err != nil || res == nil {
		if res != nil {
			e.recordSideCallUsage("verify", res)
		}
		e.emitEvent(events.Event{
			Type: events.TypeVerificationCompleted,
			Data: map[string]any{"verdict": "skipped", "skipped_reason": "error"},
		})
		// A failed verification is never a fail verdict — the answer ships.
		return verifyVerdict{}, false
	}
	e.recordSideCallUsage("verify", res)
	v := parseVerifyVerdict(res.Content)
	e.emitEvent(events.Event{
		Type: events.TypeVerificationCompleted,
		Data: map[string]any{
			"verdict":     v.Verdict,
			"cycles_used": e.verifyCyclesUsed,
		},
	})
	return v, true
}

// verifyCorrectiveText renders the system message injected on a failing hint
// cycle. Verdict prose is wrapped as untrusted content — it came out of an
// LLM that saw untrusted data.
func (e *Engine) verifyCorrectiveText(v verifyVerdict) string {
	var b strings.Builder
	b.WriteString("Your previous final answer failed verification. ")
	b.WriteString("Address the issues below and produce a corrected final answer.\n")
	reasons := strings.Join(v.Reasons, "; ")
	if reasons == "" {
		reasons = "(no reasons provided)"
	}
	missing := strings.Join(v.Missing, "; ")
	if missing == "" {
		missing = "(none listed)"
	}
	body := fmt.Sprintf("reasons: %s\nmissing: %s", reasons, missing)
	if e.wrapUntrusted != nil {
		body = e.wrapUntrusted("verify_verdict", body)
	}
	b.WriteString(body)
	return b.String()
}
