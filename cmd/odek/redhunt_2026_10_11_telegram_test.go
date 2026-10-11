package main

import (
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/bgproc"
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

// An exit that arrives while a coalesce window is already open is covered by
// the wake turn that window will dispatch (the wake turn's notice drain
// delivers every queued completion). The spend-cap check runs before the
// "window already open" check, so at the cap the second exit is refused,
// pushed raw AND reported again by the wake turn.
func TestRED_WakeCapRejectsExitInsideOpenWindow(t *testing.T) {
	bot, calls := newWakeTestBot(t)
	rec := &dispatchedRecorder{}
	ctl := newTGWakeController(920_012, 300*time.Millisecond, 1, 100*time.Millisecond, rec.add)
	t.Cleanup(ctl.stop)
	n := &bgChatNotifier{chatID: 920_012, bot: bot, wake: ctl}

	n.BGExited(bgproc.Notice{JobID: "a", ExitCode: 0}) // opens the window, spends the 1 wake
	n.BGExited(bgproc.Notice{JobID: "b", ExitCode: 0}) // same window, no extra spend needed

	waitDispatched(t, rec, 1, 2*time.Second)
	if got := sentCount(calls); got != 0 {
		t.Fatalf("exit inside an open coalesce window was pushed raw %d time(s) although the pending wake turn covers it", got)
	}
	if !ctl.wakeRouted("b") {
		t.Error("exit joining the open window must be marked routed so the watcher suppresses it")
	}
	if got := ctl.wakeSpend(); got != 1 {
		t.Errorf("wake spend = %d, want 1 (joining an open window is free)", got)
	}

	// Once the window has fired, the cap applies again: a new exit is pushed raw.
	time.Sleep(50 * time.Millisecond)
	n.BGExited(bgproc.Notice{JobID: "c", ExitCode: 0})
	if got := sentCount(calls); got != 1 {
		t.Errorf("exit after the window closed at the cap: raw pushes = %d, want 1", got)
	}
}
