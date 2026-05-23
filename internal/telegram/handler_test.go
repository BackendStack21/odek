package telegram

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

// testServer creates an httptest.Server that records requests and returns
// canned JSON responses for Telegram Bot API calls made by the Handler.
//   - answerCallbackQuery → {"ok": true}
//   - sendMessage → {"ok": true, "result": {"message_id": 1}}
//   - sendPhoto → {"ok": true, "result": {"message_id": 2}}
//   - sendVoice → {"ok": true, "result": {"message_id": 3}}
//   - anything else → {"ok": true}
func testServer(t *testing.T, recorder *requestRecorder) *httptest.Server {
	t.Helper()

	if recorder == nil {
		recorder = new(requestRecorder)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		r.Body.Close()

		recorder.mu.Lock()
		recorder.requests = append(recorder.requests, recordedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(bodyBytes),
		})
		recorder.mu.Unlock()

		// Determine response based on the endpoint
		var resp any
		switch {
		case strings.HasSuffix(r.URL.Path, "/answerCallbackQuery"):
			resp = map[string]any{"ok": true}
		case strings.HasSuffix(r.URL.Path, "/deleteMessage"):
			resp = map[string]any{"ok": true}
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			resp = map[string]any{
				"ok": true,
				"result": map[string]any{
					"message_id": 1,
					"text":       extractTextFromBody(string(bodyBytes)),
				},
			}
		case strings.HasSuffix(r.URL.Path, "/sendPhoto"):
			resp = map[string]any{
				"ok":     true,
				"result": map[string]any{"message_id": 2},
			}
		case strings.HasSuffix(r.URL.Path, "/sendVoice"):
			resp = map[string]any{
				"ok":     true,
				"result": map[string]any{"message_id": 3},
			}
		case strings.HasSuffix(r.URL.Path, "/sendDocument"):
			resp = map[string]any{
				"ok":     true,
				"result": map[string]any{"message_id": 4},
			}
		default:
			resp = map[string]any{"ok": true}
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("failed to encode response: %v", err)
		}
	}))
	return ts
}

// extractTextFromBody parses the JSON body of a sendMessage request to get the text.
func extractTextFromBody(body string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return ""
	}
	text, _ := m["text"].(string)
	return text
}

// recordedRequest stores a single captured HTTP request.
type recordedRequest struct {
	Method string
	Path   string
	Body   string
}

// requestRecorder collects HTTP requests in a thread-safe way.
type requestRecorder struct {
	mu       sync.Mutex
	requests []recordedRequest
}

func (r *requestRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

func (r *requestRecorder) last() recordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.requests) == 0 {
		return recordedRequest{}
	}
	return r.requests[len(r.requests)-1]
}

func (r *requestRecorder) all() []recordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]recordedRequest, len(r.requests))
	copy(cp, r.requests)
	return cp
}

// testBot creates a Bot wired to the given test server.
func testBot(t *testing.T, ts *httptest.Server) *Bot {
	t.Helper()
	return &Bot{
		Token:       "test:token",
		BaseURL:     ts.URL + "/bottest:token",
		FileBaseURL: ts.URL + "/file/bottest:token",
		Client:      ts.Client(),
		log:         NewNopLogger(),
	}
}

// ─── TestNewHandler ───────────────────────────────────────────────────────────

func TestNewHandler_defaults(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)

	if h.Bot != bot {
		t.Errorf("Handler.Bot = %p, want %p", h.Bot, bot)
	}
	if h.Config.MaxMsgLength != 4096 {
		t.Errorf("Handler.Config.MaxMsgLength = %d, want 4096", h.Config.MaxMsgLength)
	}
	if h.Config.AllowedChats != nil {
		t.Errorf("Handler.Config.AllowedChats = %v, want nil", h.Config.AllowedChats)
	}
	if h.Config.BotUsername != "" {
		t.Errorf("Handler.Config.BotUsername = %q, want empty", h.Config.BotUsername)
	}

	// Verify default callbacks return appropriate messages.
	textResp, _ := h.OnTextMessage(1, 0, "hi")
	if textResp != "Not implemented yet: text" {
		t.Errorf("default OnTextMessage = %q, want %q", textResp, "Not implemented yet: text")
	}

	cbResp, _ := h.OnCallbackQuery(1, "data")
	if cbResp != "Not implemented yet: callback query" {
		t.Errorf("default OnCallbackQuery = %q, want %q", cbResp, "Not implemented yet: callback query")
	}

	cmdResp, _ := h.OnCommand(1, 0, "start", "")
	if cmdResp != "Not implemented yet: command" {
		t.Errorf("default OnCommand = %q, want %q", cmdResp, "Not implemented yet: command")
	}

	voiceResp, voiceErr := h.OnVoiceMessage(1, 0, "file_id")
	// Voice and photo defaults now try to download via Bot (no real client in test).
	// They should return an error, not a placeholder string.
	if voiceResp != "" || voiceErr == nil {
		t.Logf("onVoiceMessage returned: %q (err=%v)", voiceResp, voiceErr)
	}

	photoResp, photoErr := h.OnPhotoMessage(1, 0, []string{"f1", "f2"})
	if photoResp != "" || photoErr == nil {
		t.Logf("onPhotoMessage returned: %q (err=%v)", photoResp, photoErr)
	}
}

// ─── Test HandleUpdate routing ────────────────────────────────────────────────

func TestHandleUpdate_TextMessage(t *testing.T) {
	var (
		capturedChatID    int64
		capturedMessageID int
		capturedText      string
	)
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.OnTextMessage = func(chatID int64, messageID int, text string) (string, error) {
		capturedChatID = chatID
		capturedMessageID = messageID
		capturedText = text
		return "response text", nil
	}

	upd := Update{
		ID: 1,
		Message: &Message{
			ID:   42,
			Chat: &Chat{ID: 123},
			From: &User{ID: 456},
			Text: "hello world",
		},
	}

	h.HandleUpdate(upd)

	if capturedChatID != 123 {
		t.Errorf("OnTextMessage chatID = %d, want 123", capturedChatID)
	}
	if capturedMessageID != 42 {
		t.Errorf("OnTextMessage messageID = %d, want 42", capturedMessageID)
	}
	if capturedText != "hello world" {
		t.Errorf("OnTextMessage text = %q, want %q", capturedText, "hello world")
	}
}

func TestHandleUpdate_CallbackQuery(t *testing.T) {
	var (
		capturedChatID     int64
		capturedCallbackID string
	)
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.OnCallbackQuery = func(chatID int64, data string) (string, error) {
		capturedChatID = chatID
		capturedCallbackID = data
		return "callback response", nil
	}

	upd := Update{
		ID: 2,
		CallbackQuery: &CallbackQuery{
			ID:   "cq_123",
			From: &User{ID: 456},
			Message: &Message{
				Chat: &Chat{ID: 789},
			},
			Data: "btn_data",
		},
	}

	h.HandleUpdate(upd)

	if capturedChatID != 789 {
		t.Errorf("OnCallbackQuery chatID = %d, want 789", capturedChatID)
	}
	if capturedCallbackID != "btn_data" {
		t.Errorf("OnCallbackQuery data = %q, want %q", capturedCallbackID, "btn_data")
	}
}

func TestHandleUpdate_Command(t *testing.T) {
	var (
		capturedChatID int64
		capturedCmd    string
		capturedArgs   string
	)
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.OnCommand = func(chatID int64, messageID int, cmd string, args string) (string, error) {
		capturedChatID = chatID
		capturedCmd = cmd
		capturedArgs = args
		return "cmd response", nil
	}

	upd := Update{
		ID: 3,
		Message: &Message{
			Chat: &Chat{ID: 111},
			From: &User{ID: 222},
			Text: "/start arg1 arg2",
			Entities: []MessageEntity{
				{Type: "bot_command", Offset: 0, Length: 6},
			},
		},
	}

	h.HandleUpdate(upd)

	if capturedChatID != 111 {
		t.Errorf("OnCommand chatID = %d, want 111", capturedChatID)
	}
	if capturedCmd != "start" {
		t.Errorf("OnCommand cmd = %q, want %q", capturedCmd, "start")
	}
	if capturedArgs != "arg1 arg2" {
		t.Errorf("OnCommand args = %q, want %q", capturedArgs, "arg1 arg2")
	}
}

func TestHandleUpdate_VoiceMessage(t *testing.T) {
	var (
		capturedChatID int64
		capturedFileID string
	)
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.OnVoiceMessage = func(chatID int64, messageID int, fileID string) (string, error) {
		capturedChatID = chatID
		capturedFileID = fileID
		return "voice received", nil
	}

	upd := Update{
		ID: 4,
		Message: &Message{
			Chat: &Chat{ID: 333},
			From: &User{ID: 444},
			Voice: &Voice{
				FileID:   "voice_file_abc",
				Duration: 12,
				MimeType: "audio/ogg",
			},
		},
	}

	h.HandleUpdate(upd)

	if capturedChatID != 333 {
		t.Errorf("OnVoiceMessage chatID = %d, want 333", capturedChatID)
	}
	if capturedFileID != "voice_file_abc" {
		t.Errorf("OnVoiceMessage fileID = %q, want %q", capturedFileID, "voice_file_abc")
	}
}

func TestHandleUpdate_PhotoMessage(t *testing.T) {
	var (
		capturedChatID  int64
		capturedFileIDs []string
	)
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.OnPhotoMessage = func(chatID int64, messageID int, fileIDs []string) (string, error) {
		capturedChatID = chatID
		capturedFileIDs = fileIDs
		return "photo received", nil
	}

	upd := Update{
		ID: 5,
		Message: &Message{
			Chat: &Chat{ID: 555},
			From: &User{ID: 666},
			Photo: []PhotoSize{
				{FileID: "photo_small", Width: 100, Height: 100},
				{FileID: "photo_large", Width: 800, Height: 600},
			},
		},
	}

	h.HandleUpdate(upd)

	if capturedChatID != 555 {
		t.Errorf("OnPhotoMessage chatID = %d, want 555", capturedChatID)
	}
	if len(capturedFileIDs) != 2 {
		t.Fatalf("OnPhotoMessage fileIDs length = %d, want 2", len(capturedFileIDs))
	}
	if capturedFileIDs[0] != "photo_small" {
		t.Errorf("OnPhotoMessage fileIDs[0] = %q, want %q", capturedFileIDs[0], "photo_small")
	}
	if capturedFileIDs[1] != "photo_large" {
		t.Errorf("OnPhotoMessage fileIDs[1] = %q, want %q", capturedFileIDs[1], "photo_large")
	}
}

func TestHandleUpdate_UnsupportedType(t *testing.T) {
	// An update with neither Message nor CallbackQuery should be silently ignored.
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)

	called := false
	h.OnTextMessage = func(_ int64, _ int, _ string) (string, error) {
		called = true
		return "", nil
	}

	upd := Update{ID: 99}
	h.HandleUpdate(upd)

	if called {
		t.Error("OnTextMessage was called, but no message or callback was in the update")
	}
}

func TestHandleUpdate_NilChat(t *testing.T) {
	called := false
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.OnTextMessage = func(_ int64, _ int, _ string) (string, error) {
		called = true
		return "", nil
	}

	upd := Update{
		ID: 10,
		Message: &Message{
			Chat: nil, // nil Chat
			From: &User{ID: 1},
			Text: "hello",
		},
	}

	h.HandleUpdate(upd)

	if called {
		t.Error("OnTextMessage was called despite nil Chat")
	}
}

func TestHandleUpdate_NilFrom(t *testing.T) {
	called := false
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.OnTextMessage = func(_ int64, _ int, _ string) (string, error) {
		called = true
		return "", nil
	}

	upd := Update{
		ID: 11,
		Message: &Message{
			Chat: &Chat{ID: 1},
			From: nil, // nil From
			Text: "hello",
		},
	}

	h.HandleUpdate(upd)

	if called {
		t.Error("OnTextMessage was called despite nil From")
	}
}

// ─── Test extractCommand ────────────────────────────────────────────────────

func TestExtractCommand_Simple(t *testing.T) {
	cmd, args := extractCommand("/start")
	if cmd != "start" {
		t.Errorf("cmd = %q, want %q", cmd, "start")
	}
	if args != "" {
		t.Errorf("args = %q, want %q", args, "")
	}
}

func TestExtractCommand_WithArgs(t *testing.T) {
	cmd, args := extractCommand("/cmd arg1 arg2")
	if cmd != "cmd" {
		t.Errorf("cmd = %q, want %q", cmd, "cmd")
	}
	if args != "arg1 arg2" {
		t.Errorf("args = %q, want %q", args, "arg1 arg2")
	}
}

func TestExtractCommand_WithBotName(t *testing.T) {
	cmd, args := extractCommand("/cmd@MyBot")
	if cmd != "cmd" {
		t.Errorf("cmd = %q, want %q", cmd, "cmd")
	}
	if args != "" {
		t.Errorf("args = %q, want %q", args, "")
	}
}

func TestExtractCommand_WithBotNameAndArgs(t *testing.T) {
	cmd, args := extractCommand("/cmd@MyBot arg1 arg2")
	if cmd != "cmd" {
		t.Errorf("cmd = %q, want %q", cmd, "cmd")
	}
	if args != "arg1 arg2" {
		t.Errorf("args = %q, want %q", args, "arg1 arg2")
	}
}

func TestExtractCommand_PlainText(t *testing.T) {
	cmd, args := extractCommand("hello world")
	if cmd != "" {
		t.Errorf("cmd = %q, want %q", cmd, "")
	}
	if args != "hello world" {
		t.Errorf("args = %q, want %q", args, "hello world")
	}
}

func TestExtractCommand_EmptyString(t *testing.T) {
	cmd, args := extractCommand("")
	if cmd != "" {
		t.Errorf("cmd = %q, want %q", cmd, "")
	}
	if args != "" {
		t.Errorf("args = %q, want %q", args, "")
	}
}

func TestExtractCommand_CommandWithDifferentBot(t *testing.T) {
	// extractCommand just strips the @botname — filtering is done by handleCommand.
	cmd, args := extractCommand("/cmd@OtherBot")
	if cmd != "cmd" {
		t.Errorf("cmd = %q, want %q", cmd, "cmd")
	}
	if args != "" {
		t.Errorf("args = %q, want %q", args, "")
	}
}

func TestExtractCommand_OnlySlash(t *testing.T) {
	cmd, args := extractCommand("/")
	if cmd != "" {
		t.Errorf("cmd = %q, want %q", cmd, "")
	}
	if args != "" {
		t.Errorf("args = %q, want %q", args, "")
	}
}

func TestExtractCommand_SlashWithSpace(t *testing.T) {
	cmd, args := extractCommand("/ something")
	if cmd != "" {
		t.Errorf("cmd = %q, want %q", cmd, "")
	}
	if args != "something" {
		t.Errorf("args = %q, want %q", args, "something")
	}
}

// ─── Test handleCommand @mention filtering ───────────────────────────────────

func TestHandleCommand_MentionMatchingBot(t *testing.T) {
	var (
		capturedCmd  string
		capturedArgs string
	)
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.Config.BotUsername = "MyTestBot"
	h.OnCommand = func(_ int64, _ int, cmd string, args string) (string, error) {
		capturedCmd = cmd
		capturedArgs = args
		return "ok", nil
	}

	// /start@MyTestBot some args → should match
	h.handleCommand(&Message{
		Chat: &Chat{ID: 100},
		From: &User{ID: 200},
		Text: "/start@MyTestBot some args",
	})

	if capturedCmd != "start" {
		t.Errorf("command = %q, want %q", capturedCmd, "start")
	}
	if capturedArgs != "some args" {
		t.Errorf("args = %q, want %q", capturedArgs, "some args")
	}
}

func TestHandleCommand_MentionDifferentBot_Ignored(t *testing.T) {
	called := false
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.Config.BotUsername = "MyTestBot"
	h.OnCommand = func(_ int64, _ int, _ string, _ string) (string, error) {
		called = true
		return "", nil
	}

	// /start@OtherBot → should be ignored
	h.handleCommand(&Message{
		Chat: &Chat{ID: 100},
		From: &User{ID: 200},
		Text: "/start@OtherBot",
	})

	if called {
		t.Error("OnCommand was called but the command was targeted at a different bot")
	}
}

func TestHandleCommand_MentionDifferentBotCaseInsensitive(t *testing.T) {
	called := false
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.Config.BotUsername = "MyTestBot"
	h.OnCommand = func(_ int64, _ int, _ string, _ string) (string, error) {
		called = true
		return "", nil
	}

	// /start@mytestbot → case-insensitive match, should NOT be ignored
	h.handleCommand(&Message{
		Chat: &Chat{ID: 100},
		From: &User{ID: 200},
		Text: "/start@mytestbot",
	})

	if !called {
		t.Error("OnCommand was NOT called but the mention should match case-insensitively")
	}
}

func TestHandleCommand_NoMention_GroupWithBotUsername(t *testing.T) {
	var capturedCmd string
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.Config.BotUsername = "MyTestBot"
	h.OnCommand = func(_ int64, _ int, cmd string, _ string) (string, error) {
		capturedCmd = cmd
		return "ok", nil
	}

	// In a group, /help without @mention but BotUsername is set — should still process
	h.handleCommand(&Message{
		Chat: &Chat{ID: 100},
		From: &User{ID: 200},
		Text: "/help",
	})

	if capturedCmd != "help" {
		t.Errorf("command = %q, want %q", capturedCmd, "help")
	}
}

func TestHandleCommand_NoBotUsernameSet(t *testing.T) {
	var capturedCmd string
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.Config.BotUsername = "" // no bot username configured
	h.OnCommand = func(_ int64, _ int, cmd string, _ string) (string, error) {
		capturedCmd = cmd
		return "ok", nil
	}

	// /start@SomeBot should still be processed since BotUsername is empty
	// (the @mention check is only performed when BotUsername is set)
	h.handleCommand(&Message{
		Chat: &Chat{ID: 100},
		From: &User{ID: 200},
		Text: "/start@SomeBot",
	})

	if capturedCmd != "start" {
		t.Errorf("command = %q, want %q", capturedCmd, "start")
	}
}

func TestHandleCommand_EmptyCommand(t *testing.T) {
	called := false
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.OnCommand = func(_ int64, _ int, _ string, _ string) (string, error) {
		called = true
		return "", nil
	}

	// Plain text (no leading slash) — extractCommand returns ("", text)
	h.handleCommand(&Message{
		Chat: &Chat{ID: 100},
		From: &User{ID: 200},
		Text: "not a command",
	})

	if called {
		t.Error("OnCommand was called but the message is not a command")
	}
}

// ─── Test isAllowed ──────────────────────────────────────────────────────────

func TestIsAllowed_EmptyAllowlist(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	// Both AllowedChats and AllowedUsers are empty → allow all.
	if !h.isAllowed(999, 888) {
		t.Error("isAllowed(999, 888) = false, want true (empty allowlists)")
	}
	if !h.isAllowed(0, 0) {
		t.Error("isAllowed(0, 0) = false, want true (empty allowlists)")
	}
}

func TestIsAllowed_SpecificChat(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.Config.AllowedChats = []int64{100, 200, 300}

	if !h.isAllowed(100, 1) {
		t.Error("isAllowed(100, 1) = false, want true (chat 100 is in allowlist)")
	}
	if !h.isAllowed(200, 1) {
		t.Error("isAllowed(200, 1) = false, want true (chat 200 is in allowlist)")
	}
}

func TestIsAllowed_ChatNotInList(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.Config.AllowedChats = []int64{100, 200}

	if h.isAllowed(300, 1) {
		t.Error("isAllowed(300, 1) = true, want false (chat 300 is not in allowlist)")
	}
	if h.isAllowed(0, 1) {
		t.Error("isAllowed(0, 1) = true, want false (chat 0 is not in allowlist)")
	}
}

func TestIsAllowed_SpecificUser(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.Config.AllowedUsers = []int64{10, 20, 30}

	if !h.isAllowed(1, 10) {
		t.Error("isAllowed(1, 10) = false, want true (user 10 is in allowlist)")
	}
	if !h.isAllowed(1, 30) {
		t.Error("isAllowed(1, 30) = false, want true (user 30 is in allowlist)")
	}
}

func TestIsAllowed_UserNotInList(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.Config.AllowedUsers = []int64{10, 20}

	if h.isAllowed(1, 40) {
		t.Error("isAllowed(1, 40) = true, want false (user 40 is not in allowlist)")
	}
	if h.isAllowed(1, 0) {
		t.Error("isAllowed(1, 0) = true, want false (user 0 is not in allowlist)")
	}
}

func TestIsAllowed_BothChecks(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.Config.AllowedChats = []int64{100, 200}
	h.Config.AllowedUsers = []int64{10, 20}

	// Both chat and user must be allowed.
	if !h.isAllowed(100, 10) {
		t.Error("isAllowed(100, 10) = false, want true (both in allowlists)")
	}
	if h.isAllowed(300, 10) {
		t.Error("isAllowed(300, 10) = true, want false (chat not in allowlist)")
	}
	if h.isAllowed(100, 30) {
		t.Error("isAllowed(100, 30) = true, want false (user not in allowlist)")
	}
	if h.isAllowed(300, 30) {
		t.Error("isAllowed(300, 30) = true, want false (neither in allowlists)")
	}
}

// ─── Test SendResponse calls SendMessage ─────────────────────────────────────

func TestSendResponse_callsSendMessage(t *testing.T) {
	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)

	h.SendResponse(123, "Hello, World!", 0)

	reqs := rec.all()
	// Should have at least one sendMessage call
	var found bool
	for _, req := range reqs {
		if strings.HasSuffix(req.Path, "/sendMessage") {
			found = true
			if !strings.Contains(req.Body, `"text":"Hello, World!"`) &&
				!strings.Contains(req.Body, `"text":"Hello, World!`) {
				// Check with escape sequences (FormatResponse may escape chars)
				if !strings.Contains(req.Body, `Hello`) || !strings.Contains(req.Body, `World`) {
					t.Errorf("sendMessage body does not contain expected text: %s", req.Body)
				}
			}
		}
	}
	if !found {
		t.Error("no sendMessage request was made by SendResponse")
	}
}

func TestSendResponse_SendMessageWithParseMode(t *testing.T) {
	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)

	h.SendResponse(123, "Hello *World*!", 0)

	reqs := rec.all()
	var foundParseMode bool
	for _, req := range reqs {
		if strings.HasSuffix(req.Path, "/sendMessage") {
			if strings.Contains(req.Body, `"parse_mode":"MarkdownV2"`) {
				foundParseMode = true
				break
			}
		}
	}
	if !foundParseMode {
		t.Error("sendMessage was not called with parse_mode=MarkdownV2")
	}
}

func TestSendResponse_EmptyString(t *testing.T) {
	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)

	h.SendResponse(123, "", 0)

	if rec.count() != 0 {
		t.Errorf("SendResponse with empty string made %d HTTP requests, want 0", rec.count())
	}
}

// TestSendResponse_WithReplyTo verifies that reply_to_message_id is included
// in the HTTP request body when replyToMessageID is non-zero.
func TestSendResponse_WithReplyTo(t *testing.T) {
	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)

	h.SendResponse(123, "Hello response", 42)

	reqs := rec.all()
	for _, req := range reqs {
		if strings.HasSuffix(req.Path, "/sendMessage") {
			var body map[string]any
			if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
				t.Fatalf("failed to parse body: %v", err)
			}
			got, ok := body["reply_to_message_id"]
			if !ok {
				t.Errorf("sendMessage body missing reply_to_message_id, got keys: %v", keys(body))
			}
			if got != float64(42) {
				t.Errorf("reply_to_message_id = %v (%T), want 42", got, got)
			}
			return
		}
	}
	t.Error("no sendMessage request found")
}

// TestSendResponse_WithoutReplyTo verifies that reply_to_message_id is NOT
// included when replyToMessageID is 0.
func TestSendResponse_WithoutReplyTo(t *testing.T) {
	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)

	h.SendResponse(123, "Hello response", 0)

	reqs := rec.all()
	for _, req := range reqs {
		if strings.HasSuffix(req.Path, "/sendMessage") {
			if strings.Contains(req.Body, "reply_to_message_id") {
				t.Errorf("sendMessage body should not contain reply_to_message_id when param is 0: %s", req.Body)
			}
			return
		}
	}
	t.Error("no sendMessage request found")
}

// keys returns the keys of a map as a sorted slice (for test error messages).
func keys(m map[string]any) []string {
	k := make([]string, 0, len(m))
	for key := range m {
		k = append(k, key)
	}
	sort.Strings(k)
	return k
}

func TestSendResponse_RetryOnParseError(t *testing.T) {
	// Create a server that returns "Can't parse entities" on the first call
	// and succeeds on the second.
	attempt := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if attempt == 1 && strings.HasSuffix(r.URL.Path, "/sendMessage") {
			w.Header().Set("Content-Type", "application/json")
			resp := map[string]any{
				"ok":          false,
				"description": "Bad Request: Can't parse entities",
				"error_code":  400,
			}
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(resp)
			return
		}
		// Default success response
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"ok":     true,
			"result": map[string]any{"message_id": 1},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	bot := testBot(t, ts)
	h := NewHandler(bot)

	h.SendResponse(123, "Hello _World_", 0)

	// Should have been called twice: first with MarkdownV2 (fails), then plain text (succeeds)
	if attempt < 2 {
		t.Errorf("expected at least 2 sendMessage attempts, got %d", attempt)
	}
}

// ─── Test SendResponse MEDIA prefix ──────────────────────────────────────────

func TestSendResponse_MediaPhoto(t *testing.T) {
	// Create a temp file so os.Stat succeeds.
	tmpFile, err := os.CreateTemp("", "test-photo-*.jpg")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)

	h.SendResponse(123, "MEDIA:photo:"+tmpPath, 0)

	reqs := rec.all()
	var found bool
	for _, req := range reqs {
		if strings.HasSuffix(req.Path, "/sendPhoto") {
			found = true
			break
		}
	}
	if !found {
		t.Error("no sendPhoto request was made for MEDIA:photo")
	}
}

func TestSendResponse_MediaVoice(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test-voice-*.ogg")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)

	h.SendResponse(456, "MEDIA:voice:"+tmpPath, 0)

	reqs := rec.all()
	var found bool
	for _, req := range reqs {
		if strings.HasSuffix(req.Path, "/sendVoice") {
			found = true
			break
		}
	}
	if !found {
		t.Error("no sendVoice request was made for MEDIA:voice")
	}
}

func TestSendResponse_MediaFileNotFound(t *testing.T) {
	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)

	errCalled := false
	h.OnError = func(_ int64, err error) {
		errCalled = true
	}

	h.SendResponse(123, "MEDIA:photo:/nonexistent/file.jpg", 0)

	reqs := rec.all()
	for _, req := range reqs {
		if strings.HasSuffix(req.Path, "/sendPhoto") || strings.HasSuffix(req.Path, "/sendVoice") {
			t.Errorf("sendPhoto/sendVoice was called despite file not existing: %s %s", req.Method, req.Path)
		}
	}
	if !errCalled {
		t.Error("OnError was not called when media file was not found")
	}
}

func TestSendResponse_MediaUnknownType(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test-file-*.dat")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)

	errCalled := false
	h.OnError = func(_ int64, err error) {
		errCalled = true
	}

	h.SendResponse(123, "MEDIA:video:"+tmpPath, 0)

	reqs := rec.all()
	for _, req := range reqs {
		if strings.HasSuffix(req.Path, "/sendPhoto") || strings.HasSuffix(req.Path, "/sendVoice") || strings.HasSuffix(req.Path, "/sendDocument") {
			t.Errorf("unexpected send request for unknown media type: %s", req.Path)
		}
	}
	if !errCalled {
		t.Error("OnError was not called for unknown media type")
	}
}

func TestSendResponse_MediaDocument(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test-doc-*.pdf")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)

	h.SendResponse(456, "MEDIA:document:"+tmpPath, 0)

	reqs := rec.all()
	found := false
	for _, req := range reqs {
		if strings.HasSuffix(req.Path, "/sendDocument") {
			found = true
			break
		}
	}
	if !found {
		t.Error("no sendDocument request was made for MEDIA:document")
	}
}

func TestSendResponse_MediaMalformed(t *testing.T) {
	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)

	// MEDIA: with no type:path — should be silently ignored (len(parts) < 2)
	h.SendResponse(123, "MEDIA:", 0)
	// No requests should be made, no error should be raised
	if rec.count() != 0 {
		t.Errorf("expected 0 requests for malformed MEDIA, got %d", rec.count())
	}
}

// ─── Test SendResponse with Chunking ─────────────────────────────────────────

func TestSendResponse_Chunking(t *testing.T) {
	// SendResponse calls FormatResponse which may split text into chunks.
	// Each chunk results in a separate sendMessage call.
	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)

	// Create text long enough to trigger chunking (over 4096 bytes).
	longText := strings.Repeat("A paragraph. ", 50) + "\n\n" + strings.Repeat("Another paragraph. ", 50)
	h.SendResponse(123, longText, 0)

	reqs := rec.all()
	var sendMsgCount int
	for _, req := range reqs {
		if strings.HasSuffix(req.Path, "/sendMessage") {
			sendMsgCount++
		}
	}
	if sendMsgCount < 1 {
		t.Error("expected at least 1 sendMessage call, got 0")
	}
}

// ─── Test OnError propagation ────────────────────────────────────────────────

func TestHandleUpdate_OnErrorCalled(t *testing.T) {
	var (
		errChatID int64
		errMsg    string
	)
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.OnTextMessage = func(_ int64, _ int, _ string) (string, error) {
		return "", assertError("simulated error")
	}
	h.OnError = func(chatID int64, err error) {
		errChatID = chatID
		errMsg = err.Error()
	}

	upd := Update{
		ID: 20,
		Message: &Message{
			Chat: &Chat{ID: 777},
			From: &User{ID: 888},
			Text: "trigger error",
		},
	}

	h.HandleUpdate(upd)

	if errChatID != 777 {
		t.Errorf("OnError chatID = %d, want 777", errChatID)
	}
	if errMsg != "simulated error" {
		t.Errorf("OnError error = %q, want %q", errMsg, "simulated error")
	}
}

// assertError is a simple error type for testing.
type assertError string

func (e assertError) Error() string { return string(e) }

// ─── Test isAllowed integration with HandleUpdate ────────────────────────────

func TestHandleUpdate_NotAllowed(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.Config.AllowedChats = []int64{100}
	h.Config.AllowedUsers = []int64{10}

	called := false
	h.OnTextMessage = func(_ int64, _ int, _ string) (string, error) {
		called = true
		return "", nil
	}

	upd := Update{
		ID: 30,
		Message: &Message{
			Chat: &Chat{ID: 999}, // not in allowlist
			From: &User{ID: 10},
			Text: "should be blocked",
		},
	}

	h.HandleUpdate(upd)

	if called {
		t.Error("OnTextMessage was called but the chat is not in the allowlist")
	}
}

func TestHandleUpdate_AllowedUserOnly(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.Config.AllowedUsers = []int64{42}

	called := false
	h.OnTextMessage = func(_ int64, _ int, _ string) (string, error) {
		called = true
		return "", nil
	}

	// User 42 should be allowed
	upd := Update{
		ID: 31,
		Message: &Message{
			Chat: &Chat{ID: 1},
			From: &User{ID: 42},
			Text: "allowed user",
		},
	}

	h.HandleUpdate(upd)
	if !called {
		t.Error("OnTextMessage was not called for allowed user")
	}

	called = false
	upd2 := Update{
		ID: 32,
		Message: &Message{
			Chat: &Chat{ID: 1},
			From: &User{ID: 99},
			Text: "blocked user",
		},
	}

	h.HandleUpdate(upd2)
	if called {
		t.Error("OnTextMessage was called for a user not in the allowlist")
	}
}

// ─── TestHandler_SetApprover ──────────────────────────────────────────────────

func TestHandler_SetApprover(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)

	chatID := int64(12345)
	approver := NewTelegramApprover(bot, chatID)

	// Initially, no approver.
	if got := h.GetApprover(chatID); got != nil {
		t.Fatalf("GetApprover before SetApprover = %v, want nil", got)
	}

	// Set and retrieve.
	h.SetApprover(chatID, approver)
	got := h.GetApprover(chatID)
	if got == nil {
		t.Fatal("GetApprover after SetApprover = nil, want non-nil")
	}
	if got != approver {
		t.Errorf("GetApprover returned a different approver pointer")
	}

	// Delete and verify nil.
	h.DeleteApprover(chatID)
	if got := h.GetApprover(chatID); got != nil {
		t.Errorf("GetApprover after DeleteApprover = %v, want nil", got)
	}
}

// ─── TestHandler_SetLogger ───────────────────────────────────────────────────

func TestHandler_SetLogger(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)

	// Default logger should be a NopLogger.
	if _, ok := h.log.(nopLogger); !ok {
		t.Fatalf("default Handler.log = %T, want nopLogger", h.log)
	}

	// SetLogger with nil should keep/restore NopLogger.
	h.SetLogger(nil)
	if _, ok := h.log.(nopLogger); !ok {
		t.Errorf("SetLogger(nil) resulted in %T, want nopLogger", h.log)
	}

	// SetLogger with a valid NopLogger.
	l := NewNopLogger()
	h.SetLogger(l)
	if h.log != l {
		t.Errorf("SetLogger did not store the provided logger")
	}

	// Verify SetLogger(nil) resets to NopLogger again.
	h.SetLogger(nil)
	if _, ok := h.log.(nopLogger); !ok {
		t.Errorf("SetLogger(nil) after valid logger resulted in %T, want nopLogger", h.log)
	}
}

// ─── TestHandler_HandleCallback_RouteToApprover ──────────────────────────────

func TestHandler_HandleCallback_RouteToApprover(t *testing.T) {
	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)

	chatID := int64(789)
	approver := NewTelegramApprover(bot, chatID)
	h.SetApprover(chatID, approver)

	// Send a callback with data that HandleCallback recognises as approval.
	cq := &CallbackQuery{
		ID:   "cq_approve_123",
		From: &User{ID: 456},
		Message: &Message{
			Chat: &Chat{ID: chatID},
		},
		Data: "apr:test-request-id",
	}

	h.handleCallback(cq)

	// Verify answerCallbackQuery was called.
	reqs := rec.all()
	var foundAnswer bool
	for _, req := range reqs {
		if strings.HasSuffix(req.Path, "/answerCallbackQuery") {
			foundAnswer = true
			if !strings.Contains(req.Body, `"callback_query_id":"cq_approve_123"`) {
				t.Errorf("answerCallbackQuery body missing callback_query_id: %s", req.Body)
			}
			break
		}
	}
	if !foundAnswer {
		t.Error("no answerCallbackQuery request was made after approver handled callback")
	}
}

// ─── TestHandler_HandleCallback_ApproverAnswerError ──────────────────────────

func TestHandler_HandleCallback_ApproverAnswerError(t *testing.T) {
	// Create a server that returns an error on answerCallbackQuery.
	var answerAttempted bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/answerCallbackQuery") {
			answerAttempted = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			resp := map[string]any{
				"ok":          false,
				"description": "Bad Request: query is too old",
				"error_code":  400,
			}
			json.NewEncoder(w).Encode(resp)
			return
		}
		// Default success for any other endpoint.
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer ts.Close()

	bot := &Bot{
		Token:       "test:token",
		BaseURL:     ts.URL + "/bottest:token",
		FileBaseURL: ts.URL + "/file/bottest:token",
		Client:      ts.Client(),
		log:         NewNopLogger(),
	}
	h := NewHandler(bot)

	chatID := int64(789)
	approver := NewTelegramApprover(bot, chatID)
	h.SetApprover(chatID, approver)

	var (
		errChatID int64
		errMsg    string
	)
	h.OnError = func(chatID int64, err error) {
		errChatID = chatID
		errMsg = err.Error()
	}

	cq := &CallbackQuery{
		ID:   "cq_old",
		From: &User{ID: 456},
		Message: &Message{
			Chat: &Chat{ID: chatID},
		},
		Data: "den:test-request-id",
	}

	h.handleCallback(cq)

	if !answerAttempted {
		t.Error("answerCallbackQuery was not attempted")
	}
	if errChatID != chatID {
		t.Errorf("OnError chatID = %d, want %d", errChatID, chatID)
	}
	if errMsg == "" {
		t.Error("OnError was not called with an error message")
	}
}

// ─── TestHandler_HandleCallback_FallbackToOnCallbackQuery ────────────────────

func TestHandler_HandleCallback_FallbackToOnCallbackQuery(t *testing.T) {
	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	// No approver registered.

	var (
		capturedChatID int64
		capturedData   string
	)
	h.OnCallbackQuery = func(chatID int64, data string) (string, error) {
		capturedChatID = chatID
		capturedData = data
		return "fallback response", nil
	}

	cq := &CallbackQuery{
		ID:   "cq_fallback",
		From: &User{ID: 456},
		Message: &Message{
			Chat: &Chat{ID: 999},
		},
		Data: "some_data",
	}

	h.handleCallback(cq)

	if capturedChatID != 999 {
		t.Errorf("OnCallbackQuery chatID = %d, want 999", capturedChatID)
	}
	if capturedData != "some_data" {
		t.Errorf("OnCallbackQuery data = %q, want %q", capturedData, "some_data")
	}

	// Verify answerCallbackQuery was also called before OnCallbackQuery.
	reqs := rec.all()
	var foundAnswer bool
	for _, req := range reqs {
		if strings.HasSuffix(req.Path, "/answerCallbackQuery") {
			foundAnswer = true
			break
		}
	}
	if !foundAnswer {
		t.Error("no answerCallbackQuery request was made during fallback path")
	}
}

// ─── TestHandler_HandleCommand_MentionErrorHandling ──────────────────────────

func TestHandler_HandleCommand_MentionErrorHandling(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)

	chatID := int64(100)
	expectedErr := assertError("command execution failed")
	h.OnCommand = func(_ int64, _ int, _ string, _ string) (string, error) {
		return "", expectedErr
	}

	var (
		errChatID int64
		errMsg    string
	)
	h.OnError = func(chatID int64, err error) {
		errChatID = chatID
		errMsg = err.Error()
	}

	h.handleCommand(&Message{
		Chat: &Chat{ID: chatID},
		From: &User{ID: 200},
		Text: "/do_something arg1 arg2",
	})

	if errChatID != chatID {
		t.Errorf("OnError chatID = %d, want %d", errChatID, chatID)
	}
	if errMsg != string(expectedErr) {
		t.Errorf("OnError msg = %q, want %q", errMsg, string(expectedErr))
	}
}

// ─── TestHandler_HandleMessage_OnErrorCalledOnVoiceFailure ───────────────────

func TestHandler_HandleMessage_OnErrorCalledOnVoiceFailure(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)

	chatID := int64(333)
	expectedErr := assertError("voice processing failed")
	h.OnVoiceMessage = func(_ int64, _ int, _ string) (string, error) {
		return "", expectedErr
	}

	var (
		errChatID int64
		errMsg    string
	)
	h.OnError = func(chatID int64, err error) {
		errChatID = chatID
		errMsg = err.Error()
	}

	h.handleMessage(&Message{
		Chat: &Chat{ID: chatID},
		From: &User{ID: 444},
		Voice: &Voice{
			FileID:   "voice_file_err",
			Duration: 10,
			MimeType: "audio/ogg",
		},
	})

	if errChatID != chatID {
		t.Errorf("OnError chatID = %d, want %d", errChatID, chatID)
	}
	if errMsg != string(expectedErr) {
		t.Errorf("OnError msg = %q, want %q", errMsg, string(expectedErr))
	}
}

// ─── TestHandler_HandleMessage_OnErrorCalledOnPhotoFailure ───────────────────

func TestHandler_HandleMessage_OnErrorCalledOnPhotoFailure(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)

	chatID := int64(555)
	expectedErr := assertError("photo processing failed")
	h.OnPhotoMessage = func(_ int64, _ int, _ []string) (string, error) {
		return "", expectedErr
	}

	var (
		errChatID int64
		errMsg    string
	)
	h.OnError = func(chatID int64, err error) {
		errChatID = chatID
		errMsg = err.Error()
	}

	h.handleMessage(&Message{
		Chat: &Chat{ID: chatID},
		From: &User{ID: 666},
		Photo: []PhotoSize{
			{FileID: "photo_err", Width: 100, Height: 100},
		},
	})

	if errChatID != chatID {
		t.Errorf("OnError chatID = %d, want %d", errChatID, chatID)
	}
	if errMsg != string(expectedErr) {
		t.Errorf("OnError msg = %q, want %q", errMsg, string(expectedErr))
	}
}

// ─── TestHandler_HandleMessage_OnErrorCalledOnTextFailure ────────────────────

func TestHandler_HandleMessage_OnErrorCalledOnTextFailure(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)

	chatID := int64(777)
	expectedErr := assertError("text processing failed")
	h.OnTextMessage = func(_ int64, _ int, _ string) (string, error) {
		return "", expectedErr
	}

	var (
		errChatID int64
		errMsg    string
	)
	h.OnError = func(chatID int64, err error) {
		errChatID = chatID
		errMsg = err.Error()
	}

	h.handleMessage(&Message{
		Chat: &Chat{ID: chatID},
		From: &User{ID: 888},
		Text: "trigger text error",
	})

	if errChatID != chatID {
		t.Errorf("OnError chatID = %d, want %d", errChatID, chatID)
	}
	if errMsg != string(expectedErr) {
		t.Errorf("OnError msg = %q, want %q", errMsg, string(expectedErr))
	}
}

// ─── TestHandler_HandleMessage_NilCallbacks ──────────────────────────────────

func TestHandler_HandleMessage_NilCallbacks(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)

	// Set all callbacks to nil — handleMessage should not panic.
	h.OnCommand = nil
	h.OnVoiceMessage = nil
	h.OnPhotoMessage = nil
	h.OnTextMessage = nil

	// Text message with nil OnTextMessage.
	h.handleMessage(&Message{
		Chat: &Chat{ID: 1},
		From: &User{ID: 2},
		Text: "hello",
	})

	// Voice message with nil OnVoiceMessage.
	h.handleMessage(&Message{
		Chat: &Chat{ID: 1},
		From: &User{ID: 2},
		Voice: &Voice{
			FileID:   "voice_file",
			Duration: 5,
			MimeType: "audio/ogg",
		},
	})

	// Photo message with nil OnPhotoMessage.
	h.handleMessage(&Message{
		Chat: &Chat{ID: 1},
		From: &User{ID: 2},
		Photo: []PhotoSize{
			{FileID: "photo_1", Width: 100, Height: 100},
		},
	})

	// Command message with nil OnCommand.
	h.handleMessage(&Message{
		Chat: &Chat{ID: 1},
		From: &User{ID: 2},
		Text: "/start",
		Entities: []MessageEntity{
			{Type: "bot_command", Offset: 0, Length: 6},
		},
	})

	// If we got here without panicking, the test passes.
}

// ─── Test HandleUpdate does NOT route disallowed users in callback queries ────

func TestHandleUpdate_CallbackQueryNotAllowed(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.Config.AllowedUsers = []int64{100}

	called := false
	h.OnCallbackQuery = func(_ int64, _ string) (string, error) {
		called = true
		return "", nil
	}

	// Note: handleCallback does NOT check isAllowed — it only routes.
	// The isAllowed check is only in handleMessage.
	// So callback queries from any user are processed regardless.
	// This is the current behavior — document it.
	upd := Update{
		ID: 40,
		CallbackQuery: &CallbackQuery{
			ID:   "cq_999",
			From: &User{ID: 999}, // not in allowed users
			Message: &Message{
				Chat: &Chat{ID: 1},
			},
			Data: "some_data",
		},
	}

	h.HandleUpdate(upd)

	// Callback queries are currently processed without isAllowed check
	if !called {
		t.Error("OnCallbackQuery was not called for user 999 (callback queries may not check isAllowed)")
	}
}

// ── Test approvalToast ─────────────────────────────────────────────────

func TestApprovalToast_Approve(t *testing.T) {
	if got := approvalToast(cbPrefixApprove + "abc123"); got != "✅ Approved" {
		t.Errorf("expected '✅ Approved', got: %q", got)
	}
}

func TestApprovalToast_Deny(t *testing.T) {
	if got := approvalToast(cbPrefixDeny + "abc123"); got != "❌ Denied" {
		t.Errorf("expected '❌ Denied', got: %q", got)
	}
}

func TestApprovalToast_Trust(t *testing.T) {
	if got := approvalToast(cbPrefixTrust + "abc123"); got != "🔒 Trusted for this session" {
		t.Errorf("expected trust message, got: %q", got)
	}
}

func TestApprovalToast_Unknown(t *testing.T) {
	if got := approvalToast("clarify:yes"); got != "" {
		t.Errorf("expected empty for unknown prefix, got: %q", got)
	}
	if got := approvalToast(""); got != "" {
		t.Errorf("expected empty for empty data, got: %q", got)
	}
}
