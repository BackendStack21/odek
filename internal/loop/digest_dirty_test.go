package loop

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// startDigestSideCall reports whether it started a call: a debounced request
// (one already in flight) and an engine without a client both report false,
// and the debounce leaves the dirty flag set for the next refresh.
func TestStartDigestSideCallReportsStarted(t *testing.T) {
	var none Engine
	if none.startDigestSideCall(context.Background(), 0, nil) {
		t.Fatal("engine without a client must not start a side call")
	}

	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		fmt.Fprint(w, `{"choices":[{"message":{"content":"digest"}}]}`)
	}))
	defer server.Close()
	e := New(testChatClient(t, server.URL), tool.NewRegistry(nil), 10, "sys", nil, 0)
	e.sideCallTimeout = 10 * time.Second
	dropped := []session.Message{{Role: "tool", Content: "x"}}
	if !e.startDigestSideCall(context.Background(), 0, dropped) {
		t.Fatal("first call must start")
	}
	if e.startDigestSideCall(context.Background(), 0, dropped) {
		t.Fatal("call during flight must be debounced")
	}
	e.compactMu.Lock()
	dirty := e.digestDirty
	e.compactMu.Unlock()
	if !dirty {
		t.Fatal("debounced call must leave digestDirty set")
	}
	close(release)
	e.waitDigestSideCall(context.Background())
}
