package loop

import (
	"context"

	"github.com/BackendStack21/odek/internal/session"
)

// ThirdPartyCatalogueTool is implemented by tools whose name, description or
// parameter schema were supplied by a third party (MCP servers). That text
// reaches the model through the tool catalogue rather than the message
// history, so no ingest is ever recorded for it.
type ThirdPartyCatalogueTool interface {
	ThirdPartyCatalogue() bool
}

// bindRunTaint applies catalogue taint to the run's ingest state and records
// that state so callers can persist it (Engine.UntrustedIngested).
func (e *Engine) bindRunTaint(ctx context.Context) {
	e.markCatalogueTaint(ctx)
	// The runtime prompt is rebuilt every run and never scanned as history:
	// project instructions (AGENTS.md) wrapped into it are repository
	// content, so they taint from the first run, not only after a resume.
	if state, _ := ctx.Value(ingestTaintKey{}).(*ingestTaint); state != nil && session.ContentCarriesUntrusted(e.system) {
		state.seen.Store(true)
	}
	state, _ := ctx.Value(ingestTaintKey{}).(*ingestTaint)
	e.runTaint.Store(state)
}

// UntrustedIngested reports whether the current run (or, between runs, the
// most recent one) is tainted: it ingested untrusted content, resumed tainted
// history or a tainted session, or ran with a third-party tool catalogue.
// Surfaces OR it into session.Session.UntrustedIngested before saving, so
// catalogue- or recorder-only taint survives a resume without those tools.
func (e *Engine) UntrustedIngested() bool {
	state := e.runTaint.Load()
	return state != nil && state.seen.Load()
}

// markCatalogueTaint taints the run's ingest state when any registered tool
// carries third-party catalogue metadata. A poisoned description or schema can
// steer tool calls exactly like ingested content can, so delegation trust is
// derived as if the run had ingested untrusted content.
func (e *Engine) markCatalogueTaint(ctx context.Context) {
	state, _ := ctx.Value(ingestTaintKey{}).(*ingestTaint)
	if state == nil || state.seen.Load() || e.registry == nil {
		return
	}
	for _, t := range e.registry.Tools() {
		if tp, ok := t.(ThirdPartyCatalogueTool); ok && tp.ThirdPartyCatalogue() {
			state.seen.Store(true)
			return
		}
	}
}
