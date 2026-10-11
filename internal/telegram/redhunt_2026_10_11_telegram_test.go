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

// Backslashes in the model's answer are never escaped. MarkdownV2 treats
// "\" as an escape for the next character everywhere (and requires "\\"
// inside code spans), so:
//   - in prose, `\.` becomes `\\.`: an escaped backslash followed by a bare
//     reserved "." — Telegram rejects the MarkdownV2 send and the chunk is
//     re-sent as plain text littered with escape backslashes;
//   - inside an inline code span, `\d+` is sent as-is and Telegram renders
//     it as "d+", silently corrupting regexes/paths in code.
func TestRED_BackslashNeverEscaped(t *testing.T) {
	// Prose: every reserved character must end up escaped.
	chunks, err := FormatResponse(`Versions match \d+\.\d+ here`)
	if err != nil {
		t.Fatal(err)
	}
	out := strings.Join(chunks, "")
	for i := 0; i < len(out); i++ {
		if out[i] == '\\' {
			i++ // escape consumes the next byte
			continue
		}
		if isReserved(rune(out[i])) {
			t.Errorf("prose: bare reserved %q at %d in %q (Telegram rejects the message)", out[i], i, out)
			break
		}
	}

	// Inline code: a literal backslash must be sent as "\\".
	chunks, err = FormatResponse("Use `\\d+` to match digits")
	if err != nil {
		t.Fatal(err)
	}
	out = strings.Join(chunks, "")
	if !strings.Contains(out, "`\\\\d+`") {
		t.Errorf("inline code: backslash not escaped, Telegram renders `d+`: %q", out)
	}

	// Single-line ``` span inside a prose line.
	if got := convertItalicAndEscape("see ```C:\\dir``` now"); got != "see ```C:\\\\dir``` now" {
		t.Errorf("one-line fence: got %q", got)
	}

	// EscapeMarkdown (send_message, verbose progress) follows the same rules.
	for in, want := range map[string]string{
		`a\.b`:                `a\\\.b`,
		"code `C:\\x` end":    "code `C:\\\\x` end",
		"```\nC:\\x `y`\n```": "```\nC:\\\\x \\`y\\`\n```",
	} {
		if got := EscapeMarkdown(in); got != want {
			t.Errorf("EscapeMarkdown(%q) = %q, want %q", in, got, want)
		}
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

// telegramFixUnescapedCount counts unescaped occurrences of c in a MarkdownV2
// string (a backslash consumes the byte after it).
func telegramFixUnescapedCount(out string, c byte) int {
	n := 0
	for i := 0; i < len(out); i++ {
		if out[i] == '\\' {
			i++
			continue
		}
		if out[i] == c {
			n++
		}
	}
	return n
}

// A lone "*" (arithmetic, globs) must be escaped, not turned into an
// unclosed italic marker that makes Telegram reject the whole message.
func TestRED_LoneAsteriskBecomesUnclosedItalic(t *testing.T) {
	chunks, err := FormatResponse("Total: 5 * 3 = 15")
	if err != nil {
		t.Fatal(err)
	}
	out := strings.Join(chunks, "")
	if telegramFixUnescapedCount(out, '_')%2 == 1 {
		t.Fatalf("FormatResponse emitted an unbalanced italic marker: %q", out)
	}

	cases := []struct{ in, want string }{
		{"Total: 5 * 3 = 15", `Total: 5 \* 3 \= 15`},
		{"5 * 3 * 2", `5 \* 3 \* 2`},             // spaced operators are not emphasis
		{"ls *.go", `ls \*\.go`},                 // glob
		{"2**10", `2\*\*10`},                     // unpaired bold marker
		{"an *italic* word", "an _italic_ word"}, // real emphasis still converts
		{"a **bold** word", "a **bold** word"},
		{"*a **b** c*", "_a **b** c_"},
		{"*x* and *", `_x_ and \*`},
		{"*`a*b`*", "_`a*b`_"}, // a "*" inside a code span never pairs
		{"***", `\*\*\*`},
	}
	for _, c := range cases {
		if got := convertItalicAndEscape(c.in); got != c.want {
			t.Errorf("convertItalicAndEscape(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// telegramFixDanglingEscape reports whether s ends with an odd run of
// backslashes (an escape whose escaped character was cut off).
func telegramFixDanglingEscape(s string) bool {
	n := 0
	for j := len(s) - 1; j >= 0 && s[j] == '\\'; j-- {
		n++
	}
	return n%2 == 1
}

// When a long spaceless paragraph is split, a chunk must never end on the
// escaping backslash of a MarkdownV2 escape pair: the next chunk would begin
// with a bare reserved character and Telegram rejects the MarkdownV2 send,
// so the chunk is re-sent as plain text full of literal backslashes.
func TestRED_SplitChunksCutsEscapePair(t *testing.T) {
	for _, text := range []string{
		"a" + strings.Repeat(".", 3000),  // escapes to "a" + `\.`*3000
		"ab" + strings.Repeat(".", 3000), // other parity
		strings.Repeat(`\`, 2500) + "x",  // escaped backslashes: `\\`*2500
		"a" + strings.Repeat(`\`, 2500),
	} {
		chunks, err := FormatResponse(text)
		if err != nil {
			t.Fatal(err)
		}
		if len(chunks) < 2 {
			t.Fatalf("expected a split, got %d chunk(s)", len(chunks))
		}
		for i, c := range chunks {
			if len(c) > 4096 {
				t.Errorf("chunk %d is %d bytes", i, len(c))
			}
			if telegramFixDanglingEscape(c) {
				t.Errorf("chunk %d ends with a dangling escape backslash; chunk %d starts with %q", i, i+1, chunks[min(i+1, len(chunks)-1)][:1])
			}
		}
	}
	// The fenced-paragraph splitter keeps escaped backslash pairs together too.
	for _, c := range splitPlainSegment(strings.Repeat(`\\`, 10)+"x", 4) {
		if telegramFixDanglingEscape(c) {
			t.Errorf("splitPlainSegment chunk %q ends with a dangling escape", c)
		}
	}
}
