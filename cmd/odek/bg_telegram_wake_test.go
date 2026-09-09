package main

// Telegram wake-on-complete (docs/CONFIG.md `background.wake_on_complete`,
// docs/TELEGRAM.md). A background job exiting while its chat is idle is a
// dead letter today: the raw 📋 exit line reaches the chat, but the model
// never sees it — no turn runs, so the results are never summarized or
// acted on. These tests pin the wake dispatcher:
//
//	idle chat     → system-initiated wake turn (raw push suppressed)
//	busy chat     → raw push only (the in-loop notice drain covers the model)
//	wake disabled → raw push only (WakeOnComplete=false / Notify=off / cap=0)
//	spend control → max_wakes_per_hour enforced per chat
//	coalescing    → exits inside the window share one wake turn

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/bgproc"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/telegram"
)

// dispatchedRecorder is a goroutine-safe collector for wake dispatch
// callbacks and raw-push counts.
type dispatchedRecorder struct {
	mu  sync.Mutex
	dst []string
}

func (r *dispatchedRecorder) add(_ int64, text string) {
	r.mu.Lock()
	r.dst = append(r.dst, text)
	r.mu.Unlock()
}

func (r *dispatchedRecorder) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.dst)
}

func (r *dispatchedRecorder) first() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.dst) == 0 {
		return ""
	}
	return r.dst[0]
}

func waitDispatched(t *testing.T, r *dispatchedRecorder, want int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for r.len() < want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if r.len() < want {
		t.Fatalf("dispatched = %d, want %d within %v", r.len(), want, within)
	}
}

// newWakeTestBot points a Bot at an httptest server that counts sendMessage
// calls, mirroring the fake-server pattern used by the bot's own tests.
func newWakeTestBot(t *testing.T) (*telegram.Bot, *dispatchedRecorder) {
	t.Helper()
	calls := &dispatchedRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			calls.add(0, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})
	}))
	t.Cleanup(srv.Close)
	bot := telegram.NewBot("test:token")
	bot.BaseURL = srv.URL
	return bot, calls
}

func sentCount(calls *dispatchedRecorder) int { return calls.len() }

func wakeResolved(t *testing.T) config.ResolvedConfig {
	t.Helper()
	return config.ResolvedConfig{
		Background: config.BackgroundConfig{
			Enabled:         true,
			Notify:          "observe",
			WakeOnComplete:  true,
			MaxWakesPerHour: 60,
		},
	}
}

func TestTelegramWakeAllowed_DefaultOn(t *testing.T) {
	resolved := wakeResolved(t)
	if !resolved.Background.WakeOnComplete {
		t.Fatal("background.wake_on_complete should default to true")
	}
	if !telegramWakeAllowed(resolved) {
		t.Error("telegramWakeAllowed = false with default config, want true")
	}
}

func TestTelegramWakeAllowed_DisabledPaths(t *testing.T) {
	resolved := wakeResolved(t)

	off := resolved
	off.Background.WakeOnComplete = false
	if telegramWakeAllowed(off) {
		t.Error("wake should be off when wake_on_complete=false")
	}

	notifyOff := resolved
	notifyOff.Background.Notify = "off"
	if telegramWakeAllowed(notifyOff) {
		t.Error("wake should be off when notify=off (notices never reach the model)")
	}

	capped := resolved
	capped.Background.MaxWakesPerHour = 0
	if telegramWakeAllowed(capped) {
		t.Error("wake should be off when max_wakes_per_hour=0")
	}
}

func TestBGChatNotifier_IdleChatWakeTurn(t *testing.T) {
	bot, calls := newWakeTestBot(t)
	rec := &dispatchedRecorder{}
	ctl := newTGWakeController(910001, 10*time.Millisecond, 10, 100*time.Millisecond, rec.add)
	t.Cleanup(ctl.stop)
	n := &bgChatNotifier{chatID: 910001, bot: bot, wake: ctl}

	n.BGExited(bgproc.Notice{JobID: "j1", ExitCode: 0})

	waitDispatched(t, rec, 1, 2*time.Second)
	if !strings.Contains(rec.first(), "background") {
		t.Errorf("wake text missing system marker: %q", rec.first())
	}
	if sentCount(calls) != 0 {
		t.Errorf("raw push sent %d messages on wake path, want 0", sentCount(calls))
	}
}

func TestBGChatNotifier_BusyChatRawPush(t *testing.T) {
	bot, calls := newWakeTestBot(t)
	rec := &dispatchedRecorder{}
	ctl := newTGWakeController(910002, 10*time.Millisecond, 10, 100*time.Millisecond, rec.add)
	t.Cleanup(ctl.stop)

	// Occupy the chat slot: a turn is running.
	slot := getChatMutex(910002)
	slot.Lock()
	t.Cleanup(slot.Unlock)

	n := &bgChatNotifier{chatID: 910002, bot: bot, wake: ctl}
	n.BGExited(bgproc.Notice{JobID: "j2", ExitCode: 0})

	deadline := time.Now().Add(300 * time.Millisecond)
	for sentCount(calls) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if rec.len() != 0 {
		t.Errorf("wake dispatched on busy chat, want 0")
	}
	if sentCount(calls) != 1 {
		t.Errorf("raw push count = %d, want 1 (busy chats keep the legacy push)", sentCount(calls))
	}
}

func TestBGChatNotifier_WakeDisabledRawPush(t *testing.T) {
	bot, calls := newWakeTestBot(t)
	n := &bgChatNotifier{chatID: 910003, bot: bot} // wake nil = disabled

	n.BGExited(bgproc.Notice{JobID: "j3", ExitCode: 0})

	deadline := time.Now().Add(300 * time.Millisecond)
	for sentCount(calls) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if sentCount(calls) != 1 {
		t.Errorf("raw push count = %d, want 1 when wake disabled", sentCount(calls))
	}
}

func TestTGWakeController_MaxWakesPerHour(t *testing.T) {
	bot, calls := newWakeTestBot(t)
	rec := &dispatchedRecorder{}
	ctl := newTGWakeController(910004, 5*time.Millisecond, 2, 100*time.Millisecond, rec.add)
	t.Cleanup(ctl.stop)
	n := &bgChatNotifier{chatID: 910004, bot: bot, wake: ctl}

	// First two exits coalesce into wakes; the third must hit the cap and
	// fall back to the raw push.
	n.BGExited(bgproc.Notice{JobID: "a", ExitCode: 0})
	waitDispatched(t, rec, 1, 2*time.Second)
	time.Sleep(20 * time.Millisecond) // out of the coalesce window

	n.BGExited(bgproc.Notice{JobID: "b", ExitCode: 0})
	waitDispatched(t, rec, 2, 2*time.Second)
	time.Sleep(20 * time.Millisecond)

	n.BGExited(bgproc.Notice{JobID: "c", ExitCode: 0})
	deadline := time.Now().Add(300 * time.Millisecond)
	for sentCount(calls) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if rec.len() != 2 {
		t.Errorf("wake dispatches = %d, want 2 (cap is 2/h)", rec.len())
	}
	if sentCount(calls) != 1 {
		t.Errorf("raw push after cap = %d, want 1", sentCount(calls))
	}
}

func TestTGWakeController_CoalesceWindow(t *testing.T) {
	rec := &dispatchedRecorder{}
	ctl := newTGWakeController(910005, 80*time.Millisecond, 10, 100*time.Millisecond, rec.add)
	t.Cleanup(ctl.stop)

	ctl.reserve("x")
	ctl.reserve("y")

	waitDispatched(t, rec, 1, 2*time.Second)
	time.Sleep(150 * time.Millisecond) // let any second timer fire
	if rec.len() != 1 {
		t.Errorf("coalesced wake dispatches = %d, want 1", rec.len())
	}
}

func TestBGChatNotifier_NilBotNoop(t *testing.T) {
	n := &bgChatNotifier{chatID: 910006, bot: nil}
	n.BGExited(bgproc.Notice{JobID: "z", ExitCode: 0}) // must not panic
}

// F1 regression: jobs routed to a wake turn must be invisible to the
// exit-watcher, or the watcher re-pushes the raw line the wake turn already
// covered (the duplication the suppression exists to prevent).
func TestTGWakeController_WakeRoutedSuppressesWatcher(t *testing.T) {
	ctl := newTGWakeController(910007, 5*time.Millisecond, 10, 100*time.Millisecond,
		func(int64, string) {})
	t.Cleanup(ctl.stop)
	if ctl.wakeRouted("jw") {
		t.Fatal("wakeRouted = true before any reserve")
	}
	if !ctl.reserve("jw") {
		t.Fatal("reserve = false, want true (idle chat under cap)")
	}
	if !ctl.wakeRouted("jw") {
		t.Error("wakeRouted(jw) = false after reserve, want true")
	}
	if ctl.wakeRouted("other") {
		t.Error("wakeRouted(other) = true for an unrouted job")
	}
}

// F2 regression: when the chat turns busy between reserve and fire, the wake
// must be dropped (not queued behind the user's turn) and the spend refunded.
func TestTGWakeController_BusyAtFireDropsAndRefunds(t *testing.T) {
	rec := &dispatchedRecorder{}
	ctl := newTGWakeController(910008, 10*time.Millisecond, 1, 100*time.Millisecond, rec.add)
	t.Cleanup(ctl.stop)
	if !ctl.reserve("jb") {
		t.Fatal("reserve = false, want true")
	}
	// Occupy the slot before the coalesce timer fires.
	slot := getChatMutex(910008)
	slot.Lock()
	t.Cleanup(slot.Unlock)

	waitDispatched(t, rec, 0, 0)
	deadline := time.Now().Add(500 * time.Millisecond)
	for ctl.wakeSpend() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if rec.len() != 0 {
		t.Errorf("wake dispatched on busy-at-fire chat, want 0")
	}
	if ctl.wakeSpend() != 0 {
		t.Errorf("wakeSpend = %d after refund, want 0", ctl.wakeSpend())
	}
}
