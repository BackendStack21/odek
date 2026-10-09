package loop

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// A provider that keeps rejecting the request as over-context must end the
// run after a bounded number of retries. The survival retry re-adds a
// warning each pass and trimToSurvival drops/re-adds its own warning, so the
// "strictly shrinks" invariant does not hold and the loop never terminates.
func TestRED_SurvivalRetryTerminatesOnPersistentContextError(t *testing.T) {
	var calls int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"This model's maximum context length is 4096 tokens."}}`)
	}))
	defer server.Close()

	engine := New(testChatClient(t, server.URL), tool.NewRegistry(nil), 3, "sys", nil, 0)
	msgs := []session.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task one"},
		{Role: "assistant", Content: "did step one"},
		{Role: "user", Content: "task two"},
		{Role: "assistant", Content: "did step two"},
		{Role: "user", Content: "final question"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := engine.RunWithMessages(ctx, msgs)
		done <- err
	}()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatalf("run did not terminate; %d provider calls made against a persistent context-length error", atomic.LoadInt64(&calls))
	}
	if n := atomic.LoadInt64(&calls); n > 10 {
		t.Fatalf("%d provider calls for a persistent context-length error; retries must be bounded", n)
	}
}
