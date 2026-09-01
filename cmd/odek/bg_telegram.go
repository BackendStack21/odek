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

// bgChatNotifier pushes a human-readable line to the chat the moment a job
// exits (observer callback — the agent's own notice queue is untouched).
// BGStarted is deliberately silent: the chat already saw the request.
type bgChatNotifier struct {
	chatID int64
	bot    *telegram.Bot
}

func (n *bgChatNotifier) BGStarted(j bgproc.Job) {}

func (n *bgChatNotifier) BGExited(ex bgproc.Notice) {
	if n.bot == nil {
		return
	}
	if text := formatOneNotice(ex); text != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = n.bot.SendMessageContext(ctx, n.chatID, "📋 "+text, nil)
	}
}

// bgChatRuntimes tracks one background runtime per Telegram chat. Chats run
// one agent per message; jobs must survive across messages, so the runtime
// lives for the bot's lifetime (shutdownAllBGRuntimes at bot shutdown).
var bgChatRuntimes sync.Map // chatID int64 -> *bgRuntime

var bgWatchers sync.Map // chatID int64 -> bool (watcher running)

// bgRuntimeForChat returns the chat's long-lived background runtime, creating
// it (and the exit-notification watcher) on first use. Returns nil when the
// background section is disabled.
func bgRuntimeForChat(chatID int64, resolved config.ResolvedConfig, sessID string, bot *telegram.Bot) *bgRuntime {
	if cached, ok := bgChatRuntimes.Load(chatID); ok {
		rt := cached.(*bgRuntime)
		ensureBGWatcher(chatID, rt, bot)
		return rt
	}
	rt := newBackgroundRuntime(backgroundSettingsFromResolved(resolved), sessID, "", nil, nil,
		&bgChatNotifier{chatID: chatID, bot: bot})
	if rt == nil {
		return nil
	}
	bgChatRuntimes.Store(chatID, rt)
	ensureBGWatcher(chatID, rt, bot)
	return rt
}

// shutdownAllBGRuntimes kills every chat's running jobs at bot shutdown.
func shutdownAllBGRuntimes() {
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
