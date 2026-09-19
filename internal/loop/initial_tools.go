package loop

import "github.com/BackendStack21/odek/internal/session"

// SetInitialToolCalls schedules trusted application work for the next run.
// Calls use the ordinary execution path, including filtering, approval,
// cancellation, budgets, events, and transcript persistence. Call before Run;
// never construct this queue by interpreting untrusted message content.
func (e *Engine) SetInitialToolCalls(calls []session.ToolCall) {
	e.initialToolCalls = append([]session.ToolCall(nil), calls...)
}
