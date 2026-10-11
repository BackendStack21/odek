package main

import (
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/config"
)

// A background job that exits in a chat without wake-on-complete must be
// announced to the chat exactly once. Both the runtime's exit observer
// (bgChatNotifier.BGExited raw push) and the per-chat poller
// (watchBGNotices) push a "📋 … exited" line for the same job, because the
// watcher only suppresses jobs routed to a wake turn. Every completion is
// therefore posted twice.
func TestRED_BGExitPushedTwiceToTelegramChat(t *testing.T) {
	bot, calls := newWakeTestBot(t)
	const chatID int64 = 920_011
	resolved := config.ResolvedConfig{Background: config.BackgroundConfig{
		Enabled: true, Notify: "observe", WakeOnComplete: false,
	}}
	rt := bgRuntimeForChat(chatID, resolved, "tg-920011", bot, nil)
	if rt == nil {
		t.Fatal("background runtime not created")
	}
	t.Cleanup(func() { dropBGRuntimeForChat(chatID) })

	if _, err := rt.mgr.Start(rt.session, "true", "", 0); err != nil {
		t.Fatalf("start job: %v", err)
	}

	// The watcher polls every 10s; give it one full tick after the exit.
	time.Sleep(11500 * time.Millisecond)

	if n := sentCount(calls); n != 1 {
		t.Fatalf("job exit announced %d times to the chat, want exactly 1", n)
	}
}
