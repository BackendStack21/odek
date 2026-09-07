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

// SECURITY.md (untrusted-content boundary): the rolling compaction digest
// is DERIVED from potentially untrusted tool output and "passes through
// the same untrusted wrapper" before entering the system context. The
// wrapper call and its source tag were unpinned. If this regresses, a
// poisoned tool result can ride into the protected system head as
// authoritative-looking summary text with no boundary marker.
func TestRefreshDigest_WrapsDigestAsUntrusted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"summary of dropped turns"}}]}`)
	}))
	defer server.Close()

	engine := New(testChatClient(t, server.URL),
		tool.NewRegistry(nil), 10, "sys", nil, 0)
	engine.SetCompaction(true)
	engine.SetUntrustedWrapper(func(source, content string) string {
		return "<UNTRUSTED-" + source + ">" + content + "</UNTRUSTED>"
	})

	msgs := []session.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
	}
	dropped := []session.Message{{Role: "tool", Content: "possibly poisoned tool output"}}

	out := engine.refreshDigest(context.Background(), msgs, dropped)

	var digest string
	for _, m := range out {
		if isDigestMessage(m) {
			digest = m.Content
		}
	}
	if digest == "" {
		t.Fatal("digest message not inserted")
	}
	if !strings.Contains(digest, "<UNTRUSTED-compaction>") {
		t.Fatalf("extractive digest must carry the untrusted wrapper with source 'compaction', got: %.200s", digest)
	}
	if !strings.Contains(digest, "possibly poisoned tool output") {
		t.Fatalf("extractive digest must include dropped content, got: %.200s", digest)
	}

	engine.waitDigestSideCall(context.Background())
	out = engine.applyPendingDigest(context.Background(), out)
	digest = ""
	for _, m := range out {
		if isDigestMessage(m) {
			digest = m.Content
		}
	}
	if !strings.Contains(digest, "<UNTRUSTED-compaction>summary of dropped turns</UNTRUSTED>") {
		t.Fatalf("LLM digest body must carry the untrusted wrapper with source 'compaction', got: %.200s", digest)
	}
}

func TestRefreshDigest_DoesNotWaitForSideCall(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		fmt.Fprint(w, `{"choices":[{"message":{"content":"llm digest"}}]}`)
	}))
	defer server.Close()

	engine := New(testChatClient(t, server.URL),
		tool.NewRegistry(nil), 10, "sys", nil, 0)
	engine.SetUntrustedWrapper(func(source, content string) string {
		return "<UNTRUSTED-" + source + ">" + content + "</UNTRUSTED>"
	})

	msgs := []session.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
	}
	dropped := []session.Message{{Role: "assistant", Content: "old work that was dropped"}}

	done := make(chan []session.Message, 1)
	go func() {
		done <- engine.refreshDigest(context.Background(), msgs, dropped)
	}()
	var out []session.Message
	select {
	case out = <-done:
	case <-time.After(time.Second):
		t.Fatal("refreshDigest blocked on the compaction side call")
	}

	var digest string
	for _, m := range out {
		if isDigestMessage(m) {
			digest = m.Content
		}
	}
	if digest == "" {
		t.Fatal("extractive digest must be installed before the side call returns")
	}
	if strings.Contains(digest, "llm digest") {
		t.Fatal("blocked side call must not have replaced the extractive digest yet")
	}
	if !strings.Contains(digest, "old work that was dropped") {
		t.Fatalf("extractive digest missing dropped content: %.200s", digest)
	}

	unblock()
	engine.waitDigestSideCall(context.Background())
	out = engine.applyPendingDigest(context.Background(), out)
	digest = ""
	for _, m := range out {
		if isDigestMessage(m) {
			digest = m.Content
		}
	}
	if !strings.Contains(digest, "<UNTRUSTED-compaction>llm digest</UNTRUSTED>") {
		t.Fatalf("applied LLM digest missing or unwrapped: %.200s", digest)
	}
}
