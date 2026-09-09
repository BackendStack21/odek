package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/BackendStack21/odek/internal/bgproc"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/telegram"
)

// bgChatNotifier handles background-job exit events for a chat. With wake
// enabled (wake != nil), an exit on an idle chat routes to a system-initiated
// wake turn (see bg_telegram_wake.go) and the raw push is suppressed; busy
// chats and wake-disabled setups keep the legacy push. BGStarted is
// deliberately silent: the chat already saw the request.
type bgChatNotifier struct {
	chatID int64
	bot    *telegram.Bot
	wake   *tgWakeController
}

func (n *bgChatNotifier) BGStarted(j bgproc.Job) {}

func (n *bgChatNotifier) BGExited(ex bgproc.Notice) {
	if n.bot == nil {
		return
	}
	text := formatOneNotice(ex)
	if text == "" {
		return
	}
	// Wake path: idle chat + wake enabled + under the spend cap → dispatch
	// one coalesced system-initiated turn; the model reads the completion
	// notice from the loop's drain during that turn. A raw push would
	// duplicate the notice in the chat without ever reaching the model.
	if n.wake != nil && n.wake.reserve() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, _ = n.bot.SendMessageContext(ctx, n.chatID, "📋 "+text, nil)
}

// bgChatRuntimes tracks one background runtime per Telegram chat. Chats run
// one agent per message; jobs must survive across messages, so the runtime
// lives for the bot's lifetime (shutdownAllBGRuntimes at bot shutdown).
var bgChatRuntimes sync.Map // chatID int64 -> *bgRuntime

var bgWatchers sync.Map // chatID int64 -> bool (watcher running)

// wakeControllers tracks the per-chat wake controller so /new and shutdown
// can stop pending coalesce timers.
var wakeControllers sync.Map // chatID int64 -> *tgWakeController

func stopWakeControllerForChat(chatID int64) {
	if ctl, ok := wakeControllers.LoadAndDelete(chatID); ok {
		ctl.(*tgWakeController).stop()
	}
}

// bgRuntimeForChat returns the chat's long-lived background runtime, creating
// it (and the exit-notification watcher) on first use. Returns nil when the
// background section is disabled. wakeDispatch (may be nil) starts the wake
// turn for the chat when background.wake_on_complete routes an exit to a
// system-initiated turn.
func bgRuntimeForChat(chatID int64, resolved config.ResolvedConfig, sessID string, bot *telegram.Bot,
	wakeDispatch func(chatID int64, text string)) *bgRuntime {
	if cached, ok := bgChatRuntimes.Load(chatID); ok {
		rt := cached.(*bgRuntime)
		ensureBGWatcher(chatID, rt, bot)
		return rt
	}
	var wake *tgWakeController
	if wakeDispatch != nil && telegramWakeAllowed(resolved) {
		wake = newTGWakeController(chatID,
			time.Duration(resolved.Background.WakeCoalesceMS)*time.Millisecond,
			resolved.Background.MaxWakesPerHour, 2*time.Second, wakeDispatch)
		wakeControllers.Store(chatID, wake)
	}
	rt := newBackgroundRuntime(backgroundSettingsFromResolved(resolved), sessID, "", nil, nil,
		&bgChatNotifier{chatID: chatID, bot: bot, wake: wake})
	if rt == nil {
		return nil
	}
	bgChatRuntimes.Store(chatID, rt)
	ensureBGWatcher(chatID, rt, bot)
	return rt
}

// shutdownAllBGRuntimes kills every chat's running jobs at bot shutdown.
func shutdownAllBGRuntimes() {
	wakeControllers.Range(func(k, v any) bool {
		v.(*tgWakeController).stop()
		wakeControllers.Delete(k)
		return true
	})
	bgChatRuntimes.Range(func(_, v any) bool {
		v.(*bgRuntime).Shutdown()
		return true
	})
}

// dropBGRuntimeForChat kills the chat's running jobs and forgets the runtime
// (used by /new: the fresh session must not see the old session's jobs).
func dropBGRuntimeForChat(chatID int64) {
	if cached, ok := bgChatRuntimes.LoadAndDelete(chatID); ok {
		cached.(*bgRuntime).Shutdown()
	}
	bgWatchers.Delete(chatID)
	stopWakeControllerForChat(chatID)
}

// ensureBGWatcher starts the single per-chat exit-pusher goroutine if none
// is running. The watcher exits on its own after a quiet period.
func ensureBGWatcher(chatID int64, rt *bgRuntime, bot *telegram.Bot) {
	if rt == nil || bot == nil {
		return
	}
	if _, loaded := bgWatchers.LoadOrStore(chatID, true); loaded {
		return
	}
	go func() {
		defer bgWatchers.Delete(chatID)
		watchBGNotices(chatID, rt, bot)
	}()
}

// watchBGNotices pushes a human-readable line to the chat when a job exits,
// so the user hears about completions between messages. The agent still gets
// the full observe-phase notice from its own drain — this watcher never
// touches the agent's notice queue. It stops after ~30s with nothing running.
func watchBGNotices(chatID int64, rt *bgRuntime, bot *telegram.Bot) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	// Snapshot the exit states the watcher has already announced so a job is
	// pushed exactly once (the queue belongs to the agent; this polls state).
	announced := map[string]bool{}
	idle := 0
	for range ticker.C {
		pushed := false
		for _, j := range rt.mgr.List(rt.session) {
			if j.Status == bgproc.StatusRunning || announced[j.ID] {
				continue
			}
			announced[j.ID] = true
			end := j.EndedAt
			if end.IsZero() {
				end = time.Now()
			}
			if text := formatOneNotice(bgproc.Notice{
				JobID:    j.ID,
				Status:   j.Status,
				ExitCode: j.ExitCode,
				Command:  j.Command,
				Duration: end.Sub(j.StartedAt),
			}); text != "" {
				pushed = true
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				_, _ = bot.SendMessageContext(ctx, chatID, "📋 "+text, nil)
				cancel()
			}
		}
		if pushed {
			idle = 0
			continue
		}
		running := false
		for _, j := range rt.mgr.List(rt.session) {
			if j.Status == bgproc.StatusRunning {
				running = true
				break
			}
		}
		if running {
			idle = 0
			continue
		}
		idle++
		if idle >= 3 {
			return
		}
	}
}

// formatBGJobsForChat renders the chat's job list for the /jobs command.
func formatBGJobsForChat(rt *bgRuntime) string {
	jobs := rt.mgr.List(rt.session)
	if len(jobs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("📋 Background jobs:\n")
	for _, j := range jobs {
		if j.Status == bgproc.StatusRunning {
			fmt.Fprintf(&b, "• %s — %s — running (%.0fs)\n", j.ID, headString(j.Command, 48), jobRuntimeSeconds(j))
		} else {
			fmt.Fprintf(&b, "• %s — %s — %s (exit %d)\n", j.ID, headString(j.Command, 48), j.Status, j.ExitCode)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
