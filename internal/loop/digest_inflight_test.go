package loop

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// A drop that arrives while a digest side call is in flight must still be
// summarized by a later side call; it must not be marked "covered" by a
// call that never saw it.
func TestRED_DigestSkipsDropsQueuedDuringInFlightCall(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		n := len(bodies)
		mu.Unlock()
		if n == 1 {
			started <- struct{}{}
			<-release
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"digest text"}}]}`)
	}))
	defer server.Close()

	e := New(testChatClient(t, server.URL), tool.NewRegistry(nil), 10, "sys", nil, 0)
	e.SetCompaction(true)
	e.sideCallTimeout = 15 * time.Second
	ctx := context.Background()
	msgs := []session.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "task"}}
	mk := func(s string) []session.Message {
		return []session.Message{{Role: "tool", Content: s}}
	}
	msgs = e.refreshDigest(ctx, msgs, mk("AAAAmarker"))
	<-started
	msgs = e.refreshDigest(ctx, msgs, mk("BBBBmarker"))
	close(release)
	for i := 0; i < 400; i++ {
		e.compactMu.Lock()
		ready := e.pendingDigestReady
		e.compactMu.Unlock()
		if ready {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	msgs = e.refreshDigest(ctx, msgs, mk("CCCCmarker"))
	_ = msgs
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(bodies)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.waitDigestSideCall(ctx)
	mu.Lock()
	defer mu.Unlock()
	all := strings.Join(bodies[1:], "\n")
	if !strings.Contains(all, "BBBBmarker") {
		t.Fatalf("drop B queued during in-flight call was never sent to any later summarizer (bodies after first: %d)", len(bodies)-1)
	}
}
