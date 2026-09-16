package loop

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// ── Stable digest nonce (prompt-cache preservation) ─────────────────────
//
// installDigest re-wraps the summary through protectDerivedContext on every
// content change. A fresh nonce per install means the digest system message
// bytes churn even when the summary is unchanged — each install invalidates
// the provider prefix cache from the digest position onward. The wrapper must
// be reused for identical summaries, mirroring the plan-message and memory
// slot caches.

func TestInstallDigest_StableWrapperForUnchangedSummary(t *testing.T) {
	engine := New(testChatClient(t, newIdleServer()), tool.NewRegistry(nil), 10, "sys", nil, 0)
	engine.SetUntrustedWrapper(func(source, content string) string {
		return "<UNTRUSTED-" + source + ">" + content + "</UNTRUSTED>"
	})
	// Note: SetUntrustedWrapper bypasses the internal nonce path entirely.
	// The cache must live on the raw→wrapped mapping inside installDigest,
	// so force the internal path by NOT setting the wrapper here. Rebuild.
	engine = New(testChatClient(t, newIdleServer()), tool.NewRegistry(nil), 10, "sys", nil, 0)

	msgs := []session.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
	}
	summary := "dropped: 3 turns about config refactoring"

	out1 := engine.installDigest(context.Background(), append([]session.Message(nil), msgs...), summary)
	out2 := engine.installDigest(context.Background(), append([]session.Message(nil), msgs...), summary)

	d1, d2 := digestContent(out1), digestContent(out2)
	if d1 == "" || d2 == "" {
		t.Fatal("digest message missing after install")
	}
	if d1 != d2 {
		t.Fatalf("identical summaries must produce byte-identical digest messages (nonce churn defeats prompt cache):\nfirst:  %.200s\nsecond: %.200s", d1, d2)
	}

	// A changed summary must still get a fresh wrapper (no over-caching).
	out3 := engine.installDigest(context.Background(), append([]session.Message(nil), msgs...), summary+" plus deployment notes")
	d3 := digestContent(out3)
	if d3 == d1 {
		t.Fatal("changed summary must produce a different digest message")
	}
}

// ── Digest side-call debounce (no cancel-restart churn) ─────────────────
//
// startDigestSideCall used to cancel the in-flight summarizer on every new
// trim, restarting the whole HTTP round-trip and wasting full LLM calls
// under trim bursts. A new trim while a side call is in flight must leave
// the in-flight call alone: pendingDropped accumulates, and the NEXT natural
// side call (after the current one lands) picks up the delta.

func TestStartDigestSideCall_DebouncesInFlightCall(t *testing.T) {
	var mu sync.Mutex
	canceled := false
	firstStarted := make(chan struct{})
	release := make(chan struct{})
	reqs := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs++
		first := reqs == 1
		mu.Unlock()
		if first {
			close(firstStarted)
		}
		watchCtx := r.Context()
		go func() {
			<-watchCtx.Done()
			if watchCtx.Err() != nil && watchCtx.Err() != context.Canceled {
				return
			}
			// Distinguish normal completion (client read returns) from
			// server-side cancellation by checking Done timing: canceled
			// requests end without the handler returning a body.
			select {
			case <-release:
				// released normally after body write — not a cancel
			default:
				mu.Lock()
				canceled = true
				mu.Unlock()
			}
		}()
		<-release
		fmt.Fprint(w, `{"choices":[{"message":{"content":"digest v"}}]}`)
	}))
	defer server.Close()

	engine := New(testChatClient(t, server.URL), tool.NewRegistry(nil), 10, "sys", nil, 0)
	engine.SetCompaction(true)
	engine.sideCallTimeout = 15 * time.Second

	dropped := []session.Message{{Role: "tool", Content: "dropped output"}}
	engine.startDigestSideCall(context.Background(), 0, dropped)
	<-firstStarted

	// A second trim while the first call is in flight must NOT cancel it.
	engine.startDigestSideCall(context.Background(), 0, dropped)

	mu.Lock()
	wasCanceled := canceled
	mu.Unlock()
	if wasCanceled {
		t.Fatal("second startDigestSideCall canceled the in-flight summarizer — trim bursts restart full LLM calls (debounce missing)")
	}

	// The in-flight call must still complete and be applied.
	close(release)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		engine.compactMu.Lock()
		ready := engine.pendingDigestReady
		engine.compactMu.Unlock()
		if ready {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("debounced engine never applied the digest side-call result")
}

// digestContent returns the content of the digest message in msgs ("" if absent).
func digestContent(msgs []session.Message) string {
	for _, m := range msgs {
		if isDigestMessage(m) {
			return m.Content
		}
	}
	return ""
}

func newIdleServer() string {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"idle"}}]}`)
	})).URL
}

var _ = strings.TrimSpace // keep import if assertions evolve
