package loop

import (
	"context"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

func TestDigestBodyCapBytesBounds(t *testing.T) {
	none := New(nil, tool.NewRegistry(nil), 10, "sys", nil, 0)
	if got := none.digestBodyCapBytes(); got != 0 {
		t.Fatalf("no context limit: cap = %d, want 0", got)
	}
	small := New(nil, tool.NewRegistry(nil), 10, "sys", nil, 1000)
	if got := small.digestBodyCapBytes(); got < 256 {
		t.Fatalf("small context: cap = %d, want >= 256", got)
	}
	huge := New(nil, tool.NewRegistry(nil), 10, "sys", nil, 10_000_000)
	if got, max := huge.digestBodyCapBytes(), digestMaxTokens*4; got > max {
		t.Fatalf("huge context: cap = %d exceeds %d", got, max)
	}
}

// An oversized digest body is cut to the cap when installed, and the
// reserve shrinks once a digest and warning are already in the history.
func TestInstallDigestCapsBodyAndReserveAccountsForExisting(t *testing.T) {
	e := New(nil, tool.NewRegistry(nil), 10, "sys", nil, 6000)
	e.SetCompaction(true)
	msgs := []session.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "task"}}
	fresh := e.trimInstallReserve(msgs)

	msgs = e.installDigest(context.Background(), msgs, strings.Repeat("y", 50_000))
	for _, m := range msgs {
		if isDigestMessage(m) && len(m.Content) > e.digestBodyCapBytes()+len(digestMsgHeader)+digestWrapperBytes {
			t.Fatalf("digest message is %d bytes, over the cap", len(m.Content))
		}
	}
	msgs = upsertTrimWarning(msgs, e.buildTrimWarning())
	if again := e.trimInstallReserve(msgs); again >= fresh {
		t.Fatalf("reserve with installed digest/warning = %d, want < %d", again, fresh)
	}
}
