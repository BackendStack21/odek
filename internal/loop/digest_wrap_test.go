package loop

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	if !strings.Contains(digest, "<UNTRUSTED-compaction>summary of dropped turns</UNTRUSTED>") {
		t.Fatalf("digest body must carry the untrusted wrapper with source 'compaction', got: %.200s", digest)
	}
}
