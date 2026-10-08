package loop

import (
	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/llmclient"
	"github.com/BackendStack21/odek/internal/session"
)

// Reasons carried by an answer_superseded AnswerEvent.
const (
	SupersededCompletionNudge = "completion_nudge"
	SupersededVerifyRetry     = "verify_retry"
)

// Verification outcomes reported by VerifyOutcome.
const (
	VerifyOutcomePass      = "pass"
	VerifyOutcomeFail      = "fail"
	VerifyOutcomeUncertain = "uncertain"
	VerifyOutcomeSkipped   = "skipped"
)

// AnswerEvent is a synchronous final-answer lifecycle notification. Unlike
// the odek.event/v1 stream (dispatched asynchronously), it fires on the loop
// goroutine, so a live client sees it in order with the streamed deltas:
// an answer_superseded arrives after the draft's text and before the next
// call's first fragment, and verification frames arrive before the run
// returns.
//
// Not every field is set for every Type; the zero value means "not
// applicable". Verifier prose is never carried.
type AnswerEvent struct {
	// Type is one of:
	//   "answer_superseded"      — the reply text produced since the last
	//                              tool call was a draft; the loop is about
	//                              to ask the model again and the next reply
	//                              replaces it (Reason, Cycle, Streamed)
	//   "verification_started"   — the verification side call is starting
	//   "verification_completed" — the verification side call settled
	//                              (Verdict, CyclesUsed, SkippedReason)
	Type string
	// Reason is SupersededCompletionNudge or SupersededVerifyRetry.
	Reason string
	// Cycle counts re-asks of this kind within the run, starting at 1.
	Cycle int
	// Streamed reports that the draft's text reached the delta handler.
	Streamed bool
	// Verdict is pass | fail | uncertain | skipped.
	Verdict       string
	CyclesUsed    int
	SkippedReason string
}

// AnswerEventHandler receives AnswerEvents. It runs on the loop goroutine
// and must not block.
type AnswerEventHandler func(AnswerEvent)

// SetAnswerEventHandler installs the synchronous final-answer lifecycle
// handler. Nil disables it.
func (e *Engine) SetAnswerEventHandler(h AnswerEventHandler) { e.answerHandler = h }

// VerifyOutcome reports how the last run's final answer fared in the
// verification stage: pass, fail, uncertain or skipped. Empty when the stage
// never ran (verification disabled, or the run ended before a final answer).
// fail also covers an answer that ships with VerifyFailedMarker after its
// corrective cycles were exhausted.
func (e *Engine) VerifyOutcome() string { return e.verifyOutcome }

func (e *Engine) emitAnswerEvent(ev AnswerEvent) {
	if e.answerHandler != nil {
		e.answerHandler(ev)
	}
}

// supersedeDraft marks the draft final answer as replaced and notifies the
// live client before the loop re-asks the model. The returned message is
// the draft record to append to the history.
func (e *Engine) supersedeDraft(result *llmclient.CallResult, reason string, cycle int) session.Message {
	e.emitAnswerEvent(AnswerEvent{
		Type:     "answer_superseded",
		Reason:   reason,
		Cycle:    cycle,
		Streamed: e.streamedContent,
	})
	return session.Message{
		Role:             "assistant",
		Content:          result.Content,
		ReasoningContent: result.ReasoningContent,
		Superseded:       true,
		SupersededReason: reason,
	}
}

// emitVerificationStarted reports the start of a verification side call on
// both the runtime event stream and the synchronous answer handler.
func (e *Engine) emitVerificationStarted() {
	e.emitEvent(events.Event{Type: events.TypeVerificationStarted})
	e.emitAnswerEvent(AnswerEvent{Type: events.TypeVerificationStarted})
}

// emitVerificationCompleted reports a settled verification stage and records
// the outcome for VerifyOutcome.
func (e *Engine) emitVerificationCompleted(verdict, skippedReason string) {
	data := map[string]any{"verdict": verdict}
	if skippedReason != "" {
		data["skipped_reason"] = skippedReason
	} else {
		data["cycles_used"] = e.verifyCyclesUsed
	}
	e.verifyOutcome = verdict
	e.emitEvent(events.Event{Type: events.TypeVerificationCompleted, Data: data})
	e.emitAnswerEvent(AnswerEvent{
		Type:          events.TypeVerificationCompleted,
		Verdict:       verdict,
		CyclesUsed:    e.verifyCyclesUsed,
		SkippedReason: skippedReason,
	})
}
