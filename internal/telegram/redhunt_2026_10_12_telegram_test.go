package telegram

import (
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/session"
)

// rh12tgUnescapedReserved returns the reserved MarkdownV2 characters that
// appear unescaped outside code in s. FormatResponse never emits links, so an
// unescaped '[', ']', '(' or ')' is always either a parse error or injected
// live markup; '-', '.', '!', '#', '+', '=', '{', '}', '>' must always be
// escaped (Telegram rejects the whole message otherwise). Entity markers
// ('*', '_', '~', '|', '`') are not checked here.
func rh12tgUnescapedReserved(s string) []string {
	var bad []string
	inBlock, inSpan := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' {
			i++ // skip the escaped character
			continue
		}
		if strings.HasPrefix(s[i:], "```") {
			inBlock = !inBlock
			i += 2
			continue
		}
		if inBlock {
			continue
		}
		if c == '`' {
			inSpan = !inSpan
			continue
		}
		if inSpan {
			continue
		}
		switch c {
		case '-', '.', '!', '#', '+', '=', '{', '}', '>', '[', ']', '(', ')':
			lo := max(0, i-10)
			hi := min(len(s), i+10)
			bad = append(bad, string(c)+" in "+strings.ReplaceAll(s[lo:hi], "\n", "⏎"))
		}
	}
	return bad
}

// A "───" separator line is rewritten to a bare "---". '-' is a reserved
// MarkdownV2 character, so Telegram rejects the whole chunk ("Character '-'
// is reserved and must be escaped") and the answer falls back to plain text
// littered with escape backslashes.
func TestRED_SeparatorLineEmitsUnescapedReservedDash(t *testing.T) {
	chunks, err := FormatResponse("Summary\n────────\nAll good")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range chunks {
		if bad := rh12tgUnescapedReserved(c); len(bad) > 0 {
			t.Fatalf("chunk %q has unescaped reserved MarkdownV2 characters: %v", c, bad)
		}
	}
}

// A line starting with "MEDIA:" in the middle of an answer is copied into the
// MarkdownV2 output verbatim. Only an answer that STARTS with MEDIA: is sent
// as media; a mid-answer MEDIA line is plain text, so it must be escaped like
// any other line. Unescaped, a path ("report.pdf") breaks the chunk, and text
// echoed from untrusted content after "MEDIA:" injects live markup such as a
// hidden link.
func TestRED_MidAnswerMediaLineBypassesMarkdownEscaping(t *testing.T) {
	in := "Here is the report:\nMEDIA:document:/tmp/report.pdf\nMEDIA:[Open invoice](https://evil.example/?k=SECRET)"
	chunks, err := FormatResponse(in)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(chunks, "\n\n")
	if bad := rh12tgUnescapedReserved(joined); len(bad) > 0 {
		t.Fatalf("mid-answer MEDIA lines are emitted unescaped (%v):\n%s", bad, joined)
	}
}

// An answer that ends inside an unterminated ``` fence (a truncated model
// answer, or one with an odd number of fences) leaves the pre entity open.
// Telegram rejects the chunk ("Can't find end of Pre entity") and the entire
// answer, prose included, is re-sent as backslash-littered plain text. The
// formatter must close the fence it opened.
func TestRED_UnterminatedFenceLeavesPreEntityOpen(t *testing.T) {
	chunks, err := FormatResponse("Done. Here is the fix:\n```go\nfmt.Println(\"x\")")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range chunks {
		n := 0
		for _, line := range strings.Split(c, "\n") {
			if strings.HasPrefix(line, "```") {
				n++
			}
		}
		if n%2 != 0 {
			t.Fatalf("chunk has an unclosed code fence (%d fence lines):\n%s", n, c)
		}
	}
}

// The system prompt tells the model to put "MEDIA:document:/path" at the START
// of its answer, followed by the answer text. SendResponse treats everything
// after "MEDIA:<type>:" as the path, so the path never resolves: neither the
// file nor the answer text is delivered — the whole reply is silently lost.
func TestRED_MediaPrefixedAnswerLosesFileAndText(t *testing.T) {
	dir, err := os.MkdirTemp("", "rh12tg-media-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.4"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	a := NewTelegramApprover(bot, 123, 0)
	a.SetTrustAll(true) // approval granted; this test is about delivery
	h.SetApprover(123, a)

	h.SendResponse(123, "MEDIA:document:"+path+"\n\nHere is the quarterly report you asked for.", 0)

	var sentDoc, sentText bool
	for _, r := range rec.all() {
		if strings.HasSuffix(r.Path, "/sendDocument") {
			sentDoc = true
		}
		if strings.HasSuffix(r.Path, "/sendMessage") && strings.Contains(r.Body, "quarterly report") {
			sentText = true
		}
	}
	if !sentDoc || !sentText {
		t.Fatalf("MEDIA-prefixed answer: sendDocument=%v, answer text sent=%v; want both", sentDoc, sentText)
	}
}

// PromptMedia files outbound uploads under network_egress, a trustable class.
// Once the user taps "Trust Session" on any network_egress prompt (e.g. a
// plain `curl https://api…` GET), every later media upload — exfiltration of
// any allowlisted file — is approved silently, although the docs require an
// explicit approval before any upload.
func TestRED_EgressTrustSilentlyApprovesMediaUpload(t *testing.T) {
	old := approvalTimeout
	approvalTimeout = 150 * time.Millisecond
	defer func() { approvalTimeout = old }()

	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	bot := testBot(t, ts)
	a := NewTelegramApprover(bot, 5, 7)

	// The user trusted network_egress for this session on an earlier
	// curl prompt.
	a.mu.Lock()
	a.trusted[danger.NetworkEgress] = true
	a.mu.Unlock()

	err := a.PromptMedia("/srv/app/config/production.yaml")
	prompts := 0
	for _, r := range rec.all() {
		if strings.HasSuffix(r.Path, "/sendMessage") {
			prompts++
		}
	}
	if err == nil || prompts == 0 {
		t.Fatalf("media upload approved without a prompt (err=%v, prompts sent=%d); an upload must always be explicitly approved", err, prompts)
	}
}

// Media sends drop SendOpts.ParseMode and SendOpts.ReplyMarkup. send_message
// with a file passes a MarkdownV2-escaped caption and inline buttons; the
// buttons silently vanish and the caption is shown with literal escape
// backslashes.
func TestRED_MediaSendDropsParseModeAndButtons(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	fields := map[string]string{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			b, _ := io.ReadAll(p)
			if p.FileName() == "" {
				mu.Lock()
				fields[p.FormName()] = string(b)
				mu.Unlock()
			}
		}
		okResponse(w, map[string]any{"message_id": 1, "chat": map[string]any{"id": 1}})
	}))
	defer ts.Close()
	bot := testBot(t, ts)

	markup := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{{Text: "OK", CallbackData: "cb:ok"}}}}
	if _, err := bot.SendDocument(1, path, `Report v1\.2`, &SendOpts{ParseMode: ParseModeMarkdownV2, ReplyMarkup: markup}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if fields["parse_mode"] != ParseModeMarkdownV2 {
		t.Errorf("parse_mode = %q, want %q (escaped caption shown raw)", fields["parse_mode"], ParseModeMarkdownV2)
	}
	if !strings.Contains(fields["reply_markup"], "cb:ok") {
		t.Errorf("reply_markup = %q, want the inline keyboard", fields["reply_markup"])
	}
}

// Multipart string parameters are JSON-encoded before being written, so the
// caption field carries surrounding quotes (and \" / \n escapes) that Telegram
// displays literally: caption `nice photo` arrives as `"nice photo"`.
func TestRED_MediaCaptionSentJSONQuoted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.jpg")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var caption string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			b, _ := io.ReadAll(p)
			if p.FormName() == "caption" {
				mu.Lock()
				caption = string(b)
				mu.Unlock()
			}
		}
		okResponse(w, map[string]any{"message_id": 1, "chat": map[string]any{"id": 1}})
	}))
	defer ts.Close()
	bot := testBot(t, ts)
	if _, err := bot.SendPhoto(1, path, "nice photo", nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if caption != "nice photo" {
		t.Fatalf("caption field = %q, want %q", caption, "nice photo")
	}
}

// FallbackTransport retries a failed request against each fallback base URL
// with req.Clone, which shares the original Body. When the primary endpoint
// fails after the request body was written (connection reset by a middlebox,
// timeout waiting for the response — exactly the blocked-network case the
// fallback exists for), the body is already drained and every fallback
// attempt fails or sends an empty body. Every Bot API call is a POST with a
// body, so fallback never works when it is needed.
func TestRED_FallbackTransportResendsDrainedBody(t *testing.T) {
	// Primary: reads the whole request, then drops the connection.
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("no hijacker")
			return
		}
		c, _, _ := hj.Hijack()
		_ = c.Close()
	}))
	defer primary.Close()

	var mu sync.Mutex
	var gotBody string
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody = string(b)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer fallback.Close()

	ft, err := NewFallbackTransport([]string{fallback.URL})
	if err != nil {
		t.Fatal(err)
	}
	ft.PrimaryURL = primary.URL
	bot := ft.WrapBot(&Bot{BaseURL: "https://api.telegram.org/bottest:token", Token: "test:token", log: NewNopLogger()})

	if _, err := bot.SendMessage(42, "hello through the fallback", nil); err != nil {
		t.Fatalf("SendMessage via fallback failed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(gotBody, "hello through the fallback") {
		t.Fatalf("fallback endpoint received body %q, want the original request body", gotBody)
	}
}

// MarkEpisodeUntrusted persists on the chat's stored session and survives the
// next save; marking a missing chat is a no-op.
func TestRH12TG_MarkEpisodeUntrustedPersists(t *testing.T) {
	sm, _ := setupTestSessionManager(t)
	sm.MarkEpisodeUntrusted(42) // no cached session: no-op, no panic
	if _, err := sm.GetOrCreate(42); err != nil {
		t.Fatal(err)
	}
	if err := sm.Save(42, []session.Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	cs, _ := sm.Load(42)
	if cs.EpisodeUntrusted() {
		t.Fatal("fresh session must not be episode-untrusted")
	}
	sm.MarkEpisodeUntrusted(42)
	if err := sm.Save(42, []session.Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "yo"}}); err != nil {
		t.Fatal(err)
	}
	cs, _ = sm.Load(42)
	if !cs.EpisodeUntrusted() || cs.UntrustedIngested() {
		t.Fatalf("EpisodeUntrusted=%v UntrustedIngested=%v, want true/false", cs.EpisodeUntrusted(), cs.UntrustedIngested())
	}
	var nilCS *ChatSession
	if nilCS.EpisodeUntrusted() {
		t.Fatal("nil session is not episode-untrusted")
	}
}

// mediaParams carries the reply target, parse mode and markup, and uploads
// write non-string values as JSON.
func TestRH12TG_MediaParams(t *testing.T) {
	if p := mediaParams(7, "", nil); len(p) != 1 || p["chat_id"] != int64(7) {
		t.Fatalf("nil opts params = %v", p)
	}
	mk := &InlineKeyboardMarkup{}
	p := mediaParams(7, "cap", &SendOpts{ReplyToMessageID: 3, ParseMode: ParseModeMarkdownV2, ReplyMarkup: mk})
	if p["caption"] != "cap" || p["reply_to_message_id"] != 3 || p["parse_mode"] != ParseModeMarkdownV2 || p["reply_markup"] != mk {
		t.Fatalf("params = %v", p)
	}
}

// An open fence after a table row is closed too.
func TestRH12TG_FormatClosesOpenFenceAfterTable(t *testing.T) {
	chunks, err := FormatResponse("| a | b |\n```\ncode")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(chunks, "\n")
	if strings.Count(joined, "```")%2 != 0 {
		t.Fatalf("unbalanced fences: %q", joined)
	}
}
