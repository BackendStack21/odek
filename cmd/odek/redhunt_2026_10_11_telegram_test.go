package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/BackendStack21/odek/internal/bgproc"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/telegram"
)

// telegramFixCaptureBot records every sendMessage request body.
func telegramFixCaptureBot(t *testing.T) (*telegram.Bot, func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var sent []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			body, _ := io.ReadAll(r.Body)
			var p map[string]any
			_ = json.Unmarshal(body, &p)
			mu.Lock()
			sent = append(sent, p)
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})
	}))
	t.Cleanup(srv.Close)
	bot := telegram.NewBot("test:token")
	bot.BaseURL = srv.URL
	return bot, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), sent...)
	}
}

// The overshoot warning must actually reach the chat: it was sent as
// MarkdownV2 with an unescaped error string ('.', '+', '=') and a literal
// "\n", which Telegram rejects, so the warning never arrived.
func TestRED_BudgetOvershootWarningDelivered(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bot, sent := telegramFixCaptureBot(t)
	bot.SetDailyTokenBudget(100)

	billTelegramChatTurn(bot, telegram.NewNopLogger(), 42, 7, 60) // under the budget: billed, no warning
	if n := len(sent()); n != 0 {
		t.Fatalf("warning sent under the budget (%d messages)", n)
	}
	billTelegramChatTurn(bot, telegram.NewNopLogger(), 42, 7, 60) // 120 > 100: billed and warned
	msgs := sent()
	if len(msgs) != 1 {
		t.Fatalf("overshoot warnings sent = %d, want 1", len(msgs))
	}
	if pm, _ := msgs[0]["parse_mode"].(string); pm != "" {
		t.Errorf("warning sent with parse_mode %q; the raw error text is not MarkdownV2-safe", pm)
	}
	text, _ := msgs[0]["text"].(string)
	if strings.Contains(text, `\n`) || !strings.Contains(text, "\n") {
		t.Errorf("warning must use real newlines, got %q", text)
	}
	if !strings.Contains(text, "120 total") {
		t.Errorf("warning lacks the budget error: %q", text)
	}
	if used, _ := bot.DailyTokenUsage(); used != 120 {
		t.Errorf("recorded usage = %d, want 120", used)
	}
}

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

// Verbose tool-progress lines truncate tool arguments at a raw byte offset;
// multi-byte arguments (non-ASCII file content, CJK prompts) are cut
// mid-rune and Telegram receives U+FFFD mojibake.
func TestRED_TruncateToolArgsSplitsRune(t *testing.T) {
	data := `{"content": "` + strings.Repeat("é", 1500) + `"}`
	got := truncateToolArgs(data, 2000)
	if !utf8.ValidString(got) {
		t.Fatalf("truncateToolArgs produced invalid UTF-8 at the cut: %q", got[1990:2010])
	}
	kept, _, _ := strings.Cut(got, "…")
	if len(kept) > 2000 {
		t.Errorf("kept %d bytes, want at most 2000", len(kept))
	}
	if want := fmt.Sprintf("… [%d more bytes]", len(data)-len(kept)); !strings.HasSuffix(got, want) {
		t.Errorf("suffix = %q, want %q", got[len(kept):], want)
	}
}

// The /schedule subcommand must be separated from its arguments by any
// whitespace, not only a space ("/schedule view\n<id>" typed on a phone).
func TestRED_ScheduleSubcommandNewlineSeparator(t *testing.T) {
	st := newTGStore(t)
	for _, args := range []string{"help\nmore", "list\tall"} {
		reply, _ := telegramScheduleReply(1, 0, args, st, nil, false, nil, nil)
		if strings.Contains(reply, "Unknown subcommand") {
			t.Errorf("telegramScheduleReply(%q) = %q, want the subcommand recognised", args, reply)
		}
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
