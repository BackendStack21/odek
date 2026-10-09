package loop

import "context"

// ThirdPartyCatalogueTool is implemented by tools whose name, description or
// parameter schema were supplied by a third party (MCP servers). That text
// reaches the model through the tool catalogue rather than the message
// history, so no ingest is ever recorded for it.
type ThirdPartyCatalogueTool interface {
	ThirdPartyCatalogue() bool
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
