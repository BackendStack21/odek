package main

import (
	"context"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/loop"
	"github.com/BackendStack21/odek/internal/session"
)

// withSessionTaint seeds a resumed run's ingest taint from the session's
// persisted flag. The loop also scans the history it is handed, but the
// untrusted content itself may already be gone from it (context trimming,
// write-time size trimming, compaction); the flag never is.
func withSessionTaint(ctx context.Context, sess *session.Session) context.Context {
	if sess != nil && sess.UntrustedIngested && !loop.UntrustedIngested(ctx) {
		return loop.WithUntrustedIngest(ctx)
	}
	return ctx
}

// markRunTaint records the agent's in-run taint on the session about to be
// saved. History-derived taint is already picked up by the store; this adds
// taint that never reaches the history (a third-party tool catalogue, an
// ingest recorded without a persisted wrapper). The store keeps it sticky.
func markRunTaint(a *odek.Agent, sess *session.Session) {
	if a != nil && sess != nil && a.UntrustedIngested() {
		sess.UntrustedIngested = true
	}
}
