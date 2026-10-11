package telegram

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// A callback rejected by the approver (pressed by a group member who did not
// start the operation, or on a request that is no longer pending) must not be
// acknowledged with an "Approved" toast.
func TestRED_RejectedCallbackShowsApprovedToast(t *testing.T) {
	var mu sync.Mutex
	var toasts []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/answerCallbackQuery") {
			body, _ := io.ReadAll(r.Body)
			var p map[string]any
			_ = json.Unmarshal(body, &p)
			s, _ := p["text"].(string)
			mu.Lock()
			toasts = append(toasts, s)
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer ts.Close()
	bot := testBot(t, ts)

	h := &Handler{Bot: bot, Config: HandlerConfig{AllowAllUsers: true}, log: NewNopLogger()}
	a := NewTelegramApprover(bot, 5, 111) // operation started by user 111
	id := a.newID()
	pr := &pendingRequest{resp: make(chan string, 1), userID: 111}
	a.pending[id] = pr
	h.SetApprover(5, a)

	press := func(from int64, data string) string {
		mu.Lock()
		n := len(toasts)
		mu.Unlock()
		h.handleCallback(&CallbackQuery{ID: "cq", From: &User{ID: from},
			Message: &Message{ID: 9, Chat: &Chat{ID: 5}}, Data: data})
		mu.Lock()
		defer mu.Unlock()
		if len(toasts) != n+1 {
			t.Fatalf("expected one callback answer, got %d", len(toasts)-n)
		}
		return toasts[n]
	}

	// User 222 taps Approve on user 111's prompt.
	if s := press(222, cbPrefixApprove+id); strings.Contains(s, "Approved") {
		t.Fatalf("rejected callback from a non-originating user acknowledged with toast %q", s)
	}
	select {
	case got := <-pr.resp:
		t.Fatalf("approval accepted from wrong user: %q", got)
	default:
	}

	// The originating user's press is delivered and acknowledged.
	if s := press(111, cbPrefixApprove+id); !strings.Contains(s, "Approved") {
		t.Fatalf("accepted press toast = %q, want Approved", s)
	}
	// A second tap before the reader consumed the first is not applied.
	if s := press(111, cbPrefixDeny+id); strings.Contains(s, "Denied") {
		t.Fatalf("duplicate press acknowledged as applied: %q", s)
	}
	// A tap on a request that is no longer pending.
	if s := press(111, cbPrefixApprove+"gone"); strings.Contains(s, "Approved") {
		t.Fatalf("stale press acknowledged with toast %q", s)
	}
}

// A command followed by a newline (multi-line /plan description) or a tab
// must still be recognised as that command, and the @BotUsername check must
// split on the same separators.
func TestRED_ExtractCommandNewlineSeparator(t *testing.T) {
	cases := []struct {
		in, cmd, args string
	}{
		{"/plan\nAdd OAuth login\nwith Google", "plan", "Add OAuth login\nwith Google"},
		{"/resume\ttg-42-20261011-120000", "resume", "tg-42-20261011-120000"},
		{"/start@mybot\nhi", "start", "hi"},
		{"/plan Add login", "plan", "Add login"},
		{"/help", "help", ""},
	}
	for _, c := range cases {
		cmd, args := extractCommand(c.in)
		if cmd != c.cmd || strings.TrimSpace(args) != c.args {
			t.Errorf("extractCommand(%q) = (%q, %q), want (%q, %q)", c.in, cmd, args, c.cmd, c.args)
		}
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer ts.Close()
	var got []string
	h := &Handler{Bot: testBot(t, ts), Config: HandlerConfig{AllowAllUsers: true, BotUsername: "mybot"}, log: NewNopLogger(),
		OnCommand: func(chatID int64, msgID int, cmd, args string, userID int64) (string, error) {
			got = append(got, cmd)
			return "", nil
		}}
	h.handleCommand(&Message{ID: 1, Chat: &Chat{ID: 5}, Text: "/start@mybot\nhi"}, 1)
	h.handleCommand(&Message{ID: 2, Chat: &Chat{ID: 5}, Text: "/start@otherbot\nhi"}, 1)
	if len(got) != 1 || got[0] != "start" {
		t.Errorf("handled commands = %q, want exactly [start] (addressed to this bot only)", got)
	}
}

// A run whose billing pushes the day's usage past daily_token_budget must
// leave the budget exhausted. CheckDailyBudget rejects the overshooting
// amount without recording it, so usage stays below the limit and every
// following pre-flight (CheckDailyBudget(1) / DailyTokenUsage) keeps letting
// runs through: the budget is never enforced once a run overshoots it.
func TestRED_DailyBudgetOvershootIsNeverRecorded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	b := &Bot{DailyTokenBudget: 10000, log: NewNopLogger()}

	if err := b.CheckDailyBudget(9000); err != nil {
		t.Fatalf("first run billing: %v", err)
	}
	// Second run actually consumed 3000 tokens (already spent at the provider).
	if err := b.CheckDailyBudget(3000); err == nil {
		t.Fatal("overshooting billing must still report the overshoot")
	}

	// The read-only pre-flight must see the real spend.
	if used, limit := b.DailyTokenUsage(); used != 12000 || limit != 10000 {
		t.Fatalf("DailyTokenUsage = %d/%d, want 12000/10000 (actual spend recorded)", used, limit)
	}
	// The day's real spend is 12000 > 10000: the next run must be refused.
	if err := b.CheckDailyBudget(1); err == nil {
		used, limit := b.DailyTokenUsage()
		t.Fatalf("pre-flight after overshoot passed: recorded usage %d/%d, real spend 12000 — budget never blocks further runs", used, limit)
	}
}
