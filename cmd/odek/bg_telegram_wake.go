package main

// Telegram wake-on-complete (docs/CONFIG.md `background.wake_on_complete`).
//
// A background job exiting while its chat is idle is a dead letter for the
// model: the legacy bgChatNotifier pushes a raw 📋 exit line to the chat,
// but no agent turn ever runs, so the results are never summarized or acted
// on. This dispatcher closes that gap on the Telegram surface, mirroring the
// WebUI wake design in bg_wake.go:
//
//	job exit (bgChatNotifier.BGExited)
//	  ├─ wake disabled (wake_on_complete=false, notify=off, cap=0) → legacy raw push
//	  ├─ chat busy (slot held by a running turn)                   → legacy raw push
//	  │   (the in-loop notice drain feeds the model on the running turn)
//	  ├─ spend cap reached (max_wakes_per_hour, per chat)          → legacy raw push
//	  └─ chat idle → reserve → coalesce window → ONE system-initiated
//	      wake turn via handleChatMessage; the raw push is suppressed
//	      because the wake turn's own notice drain delivers the facts to
//	      the model, and a push would duplicate them in the chat.
//
// Deliberate limits:
//   - The wake preamble is generic; job details arrive via the loop's
//     notice drain, same as the WebUI path.
//   - Wake turns run with userID 0: the TelegramApprover already treats a
//     zero originating user as "no user binding", so approvals stay
//     available without hijacking a user.
//   - Idle detection uses the same per-chat slot the turn pipeline holds
//     (pinChat), checked with a bounded wait (idleWait) so a wake never
//     queues behind a long turn.

import (
	"strconv"
	"sync"
	"time"

	"github.com/BackendStack21/odek/internal/config"
)

// tgWakePreamble is the system-attributed wake turn text. Generic by design:
// the factual completion notice is injected by the loop's per-iteration
// drain at the wake turn's first iteration.
const tgWakePreamble = "[background-jobs] One or more background jobs finished while this chat was idle. Their completion notice is attached to this turn — read bg_output for the relevant job id(s) and report the results to the chat."

// tgWakeAllowed reports whether Telegram wake turns are permitted under the
// resolved config. The same rules as the WebUI surface apply: the background
// section must be enabled, wake_on_complete on, notices injected (a wake
// would point the model at notices that are never delivered otherwise), and
// the per-hour cap positive.
func telegramWakeAllowed(resolved config.ResolvedConfig) bool {
	bg := resolved.Background
	return bg.Enabled && bg.WakeOnComplete && bg.Notify != "off" && bg.MaxWakesPerHour > 0
}

// tgWakeController coalesces background-job exits per chat and dispatches
// system-initiated wake turns. One controller lives per chat notifier. The
// reserve decision (busy chat, spend cap) is synchronous — the raw-push
// choice in BGExited depends on it; only the dispatch itself is deferred to
// the coalesce timer.
type tgWakeController struct {
	chatID     int64
	coalesce   time.Duration
	maxPerHour int
	idleWait   time.Duration
	dispatch   func(chatID int64, text string)

	mu      sync.Mutex
	timer   *time.Timer
	pending int
	wakes   []time.Time // wake timestamps inside the spend window
	done    chan struct{}
}

func newTGWakeController(chatID int64, coalesce time.Duration, maxPerHour int,
	idleWait time.Duration, dispatch func(int64, string)) *tgWakeController {
	if coalesce <= 0 {
		coalesce = 2 * time.Second
	}
	if idleWait <= 0 {
		idleWait = 2 * time.Second
	}
	return &tgWakeController{
		chatID:     chatID,
		coalesce:   coalesce,
		maxPerHour: maxPerHour,
		idleWait:   idleWait,
		dispatch:   dispatch,
		done:       make(chan struct{}),
	}
}

// stop tears the controller down, cancelling any pending coalesce timer.
func (c *tgWakeController) stop() {
	select {
	case <-c.done:
		return
	default:
	}
	close(c.done)
	c.mu.Lock()
	if c.timer != nil {
		c.timer.Stop()
	}
	c.mu.Unlock()
}

// reserve attempts to route this exit to a wake turn. It returns true when
// a wake turn is (or will be) dispatched for it — the caller must suppress
// the legacy raw push — and false when the exit falls back to the raw push
// (controller stopped, spend cap reached, or the chat is busy).
func (c *tgWakeController) reserve() bool {
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return false
	default:
	}
	now := time.Now()
	kept := c.wakes[:0]
	for _, ts := range c.wakes {
		if now.Sub(ts) < time.Hour {
			kept = append(kept, ts)
		}
	}
	c.wakes = kept
	if len(c.wakes) >= c.maxPerHour {
		c.mu.Unlock()
		return false
	}
	c.mu.Unlock()

	// Busy check: a chat running a turn keeps the legacy push (the
	// running turn's notice drain reaches the model already). Bounded
	// wait so the observer goroutine never queues behind a long turn.
	if !chatIsIdle(c.chatID, c.idleWait) {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.done:
		return false
	default:
	}
	c.pending++
	if c.timer == nil {
		// New coalesce window: spend one wake and start the timer.
		c.wakes = append(c.wakes, time.Now())
		c.timer = time.AfterFunc(c.coalesce, c.fire)
	}
	return true
}

// fire runs after the coalesce window and dispatches one wake turn for all
// reserved exits.
func (c *tgWakeController) fire() {
	c.mu.Lock()
	c.timer = nil
	n := c.pending
	c.pending = 0
	stopped := c.done
	c.mu.Unlock()
	if n == 0 {
		return
	}
	select {
	case <-stopped:
		return
	default:
	}
	c.dispatch(c.chatID, tgWakePreamble)
}

// wakeSpend reports how many wake turns the chat has spent in the last hour
// (diagnostics/tests).
func (c *tgWakeController) wakeSpend() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.wakes)
}

// chatIsIdle reports whether the chat's turn slot can be taken within wait
// (i.e. no agent turn is running). The probe releases the slot immediately.
func chatIsIdle(chatID int64, wait time.Duration) bool {
	acquired := make(chan struct{}, 1)
	go func() {
		slot := pinChat(chatID)
		slot.mu.Lock()
		acquired <- struct{}{}
		unpinChat(chatID, slot)
	}()
	select {
	case <-acquired:
		return true
	case <-time.After(wait):
		return false
	}
}

// chatIDString formats a chat id for logs.
func chatIDString(id int64) string { return strconv.FormatInt(id, 10) }
