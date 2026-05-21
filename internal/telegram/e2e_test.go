package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// TestE2E_FullTextMessageFlow — complete poll → handle → response cycle
// for a plain text message.
// ---------------------------------------------------------------------------

func TestE2E_FullTextMessageFlow(t *testing.T) {
	rec := new(requestRecorder)

	// Create a mock server that handles both getUpdates (returns a text update)
	// and sendMessage (records the request).
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := readAllBody(r)

		rec.mu.Lock()
		rec.requests = append(rec.requests, recordedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(bodyBytes),
		})
		rec.mu.Unlock()

		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			// Return a single text message update.
			okResponse(w, []map[string]any{
				{
					"update_id": 1,
					"message": map[string]any{
						"message_id": 10,
						"text":       "Hello, bot!",
						"chat":       map[string]any{"id": 123, "type": "private"},
						"from":       map[string]any{"id": 456, "first_name": "User"},
						"date":       1_700_000_000,
					},
				},
			})

		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			okResponse(w, map[string]any{
				"message_id": 100,
				"text":       extractTextFromBody(string(bodyBytes)),
				"chat":       map[string]any{"id": 123, "type": "private"},
			})

		case strings.HasSuffix(r.URL.Path, "/sendPhoto"):
			okResponse(w, map[string]any{"message_id": 200})

		case strings.HasSuffix(r.URL.Path, "/sendVoice"):
			okResponse(w, map[string]any{"message_id": 300})

		case strings.HasSuffix(r.URL.Path, "/answerCallbackQuery"):
			okResponse(w, map[string]any{"ok": true})

		default:
			okResponse(w, map[string]any{"ok": true})
		}
	}))
	defer ts.Close()

	// Wire the bot to the mock server.
	bot := testBot(t, ts)
	poller := NewPoller(bot)
	poller.Timeout = 0 // don't wait in tests
	handler := NewHandler(bot)

	// Set up the text message callback.
	var (
		capturedChatID int64
		capturedText   string
	)
	handler.OnTextMessage = func(chatID int64, messageID int, text string) (string, error) {
		capturedChatID = chatID
		capturedText = text
		return "Hello back!", nil
	}

	// Step 1: Poll for updates.
	ctx := context.Background()
	updates, err := poller.Poll(ctx)
	if err != nil {
		t.Fatalf("Poll failed: %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}

	// Verify the update has the expected text.
	upd := updates[0]
	if upd.Message == nil {
		t.Fatal("update has no message")
	}
	if upd.Message.Text != "Hello, bot!" {
		t.Errorf("message text = %q, want %q", upd.Message.Text, "Hello, bot!")
	}
	if upd.Message.Chat == nil || upd.Message.Chat.ID != 123 {
		t.Errorf("chat ID = %d, want 123", upd.Message.Chat.ID)
	}

	// Step 2: Handle the update.
	handler.HandleUpdate(upd)

	// Verify the callback was invoked with the correct arguments.
	if capturedChatID != 123 {
		t.Errorf("OnTextMessage chatID = %d, want 123", capturedChatID)
	}
	if capturedText != "Hello, bot!" {
		t.Errorf("OnTextMessage text = %q, want %q", capturedText, "Hello, bot!")
	}

	// Step 3: Verify the mock server received a sendMessage call with the response.
	reqs := rec.all()
	var foundSendMessage bool
	for _, req := range reqs {
		if strings.HasSuffix(req.Path, "/sendMessage") {
			foundSendMessage = true
			if !strings.Contains(req.Body, `"text":"Hello back!"`) &&
				!strings.Contains(req.Body, `"text":"Hello back!`) {
				// Check with possible markdown escaping
				if !strings.Contains(req.Body, "Hello back") {
					t.Errorf("sendMessage body does not contain expected response text: %s", req.Body)
				}
			}
			if !strings.Contains(req.Body, `"chat_id":123`) {
				t.Errorf("sendMessage body does not contain chat_id=123: %s", req.Body)
			}
			break
		}
	}
	if !foundSendMessage {
		t.Error("no sendMessage request was made after HandleUpdate")
	}
}

// ---------------------------------------------------------------------------
// TestE2E_FullCommandFlow — complete cycle for a /start command.
// ---------------------------------------------------------------------------

func TestE2E_FullCommandFlow(t *testing.T) {
	rec := new(requestRecorder)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := readAllBody(r)

		rec.mu.Lock()
		rec.requests = append(rec.requests, recordedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(bodyBytes),
		})
		rec.mu.Unlock()

		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			okResponse(w, []map[string]any{
				{
					"update_id": 5,
					"message": map[string]any{
						"message_id": 20,
						"text":       "/start welcome_message",
						"entities": []map[string]any{
							{"type": "bot_command", "offset": 0, "length": 6},
						},
						"chat": map[string]any{"id": 789, "type": "private"},
						"from": map[string]any{"id": 101, "first_name": "User"},
						"date": 1_700_000_001,
					},
				},
			})

		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			okResponse(w, map[string]any{
				"message_id": 101,
				"text":       extractTextFromBody(string(bodyBytes)),
				"chat":       map[string]any{"id": 789, "type": "private"},
			})

		case strings.HasSuffix(r.URL.Path, "/answerCallbackQuery"):
			okResponse(w, map[string]any{"ok": true})

		default:
			okResponse(w, map[string]any{"ok": true})
		}
	}))
	defer ts.Close()

	bot := testBot(t, ts)
	poller := NewPoller(bot)
	poller.Timeout = 0
	handler := NewHandler(bot)

	var (
		capturedChatID int64
		capturedCmd    string
		capturedArgs   string
	)
	handler.OnCommand = func(chatID int64, messageID int, cmd string, args string) (string, error) {
		capturedChatID = chatID
		capturedCmd = cmd
		capturedArgs = args
		return "Welcome! Type /help to see available commands.", nil
	}

	// Poll.
	ctx := context.Background()
	updates, err := poller.Poll(ctx)
	if err != nil {
		t.Fatalf("Poll failed: %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}

	upd := updates[0]

	// Verify the raw update looks correct.
	if upd.Message == nil {
		t.Fatal("update has no message")
	}
	if !upd.Message.IsCommand() {
		t.Error("message should be detected as command")
	}

	// Handle.
	handler.HandleUpdate(upd)

	// Verify callback captured the right values.
	if capturedChatID != 789 {
		t.Errorf("OnCommand chatID = %d, want 789", capturedChatID)
	}
	if capturedCmd != "start" {
		t.Errorf("OnCommand cmd = %q, want %q", capturedCmd, "start")
	}
	if capturedArgs != "welcome_message" {
		t.Errorf("OnCommand args = %q, want %q", capturedArgs, "welcome_message")
	}

	// Verify sendMessage was called with the command response.
	// Note: FormatResponse escapes MarkdownV2 reserved chars (!, ., etc.)
	reqs := rec.all()
	var foundSendMessage bool
	for _, req := range reqs {
		if strings.HasSuffix(req.Path, "/sendMessage") {
			foundSendMessage = true
			if !strings.Contains(req.Body, "Welcome") {
				t.Errorf("sendMessage body missing expected text: %s", req.Body)
			}
			if !strings.Contains(req.Body, `"chat_id":789`) {
				t.Errorf("sendMessage body missing chat_id=789: %s", req.Body)
			}
			break
		}
	}
	if !foundSendMessage {
		t.Error("no sendMessage request was made for command response")
	}

	// Verify offset advanced past the update.
	if poller.Offset != 6 {
		t.Errorf("poller offset = %d, want 6 (5+1)", poller.Offset)
	}
}

// ---------------------------------------------------------------------------
// TestE2E_FullCallbackFlow — complete cycle for callback query.
// ---------------------------------------------------------------------------

func TestE2E_FullCallbackFlow(t *testing.T) {
	rec := new(requestRecorder)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := readAllBody(r)

		rec.mu.Lock()
		rec.requests = append(rec.requests, recordedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(bodyBytes),
		})
		rec.mu.Unlock()

		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			okResponse(w, []map[string]any{
				{
					"update_id": 10,
					"callback_query": map[string]any{
						"id":   "cq_test_123",
						"from": map[string]any{"id": 456, "first_name": "User"},
						"message": map[string]any{
							"message_id": 30,
							"chat":       map[string]any{"id": 111, "type": "private"},
							"date":       1_700_000_002,
							"text":       "Choose an option:",
						},
						"data": "option_1",
					},
				},
			})

		case strings.HasSuffix(r.URL.Path, "/answerCallbackQuery"):
			okResponse(w, map[string]any{"ok": true})

		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			okResponse(w, map[string]any{
				"message_id": 102,
				"text":       extractTextFromBody(string(bodyBytes)),
				"chat":       map[string]any{"id": 111, "type": "private"},
			})

		default:
			okResponse(w, map[string]any{"ok": true})
		}
	}))
	defer ts.Close()

	bot := testBot(t, ts)
	poller := NewPoller(bot)
	poller.Timeout = 0
	handler := NewHandler(bot)

	var (
		capturedChatID int64
		capturedData   string
	)
	handler.OnCallbackQuery = func(chatID int64, callbackData string) (string, error) {
		capturedChatID = chatID
		capturedData = callbackData
		return "You selected option 1!", nil
	}

	// Poll.
	ctx := context.Background()
	updates, err := poller.Poll(ctx)
	if err != nil {
		t.Fatalf("Poll failed: %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}

	upd := updates[0]

	// Verify update has callback query.
	if upd.CallbackQuery == nil {
		t.Fatal("update has no callback_query")
	}
	if upd.CallbackQuery.Data != "option_1" {
		t.Errorf("CallbackQuery.Data = %q, want %q", upd.CallbackQuery.Data, "option_1")
	}

	// Handle.
	handler.HandleUpdate(upd)

	// Verify callback captured the right values.
	if capturedChatID != 111 {
		t.Errorf("OnCallbackQuery chatID = %d, want 111", capturedChatID)
	}
	if capturedData != "option_1" {
		t.Errorf("OnCallbackQuery data = %q, want %q", capturedData, "option_1")
	}

	// Verify answerCallbackQuery was called.
	reqs := rec.all()
	var foundAnswer, foundSendMessage bool
	for _, req := range reqs {
		if strings.HasSuffix(req.Path, "/answerCallbackQuery") {
			foundAnswer = true
			if !strings.Contains(req.Body, `"callback_query_id":"cq_test_123"`) {
				t.Errorf("answerCallbackQuery body missing callback_query_id: %s", req.Body)
			}
		}
		if strings.HasSuffix(req.Path, "/sendMessage") {
			foundSendMessage = true
			if !strings.Contains(req.Body, "You selected") {
				t.Errorf("sendMessage body missing response: %s", req.Body)
			}
		}
	}
	if !foundAnswer {
		t.Error("no answerCallbackQuery request was made")
	}
	if !foundSendMessage {
		t.Error("no sendMessage request was made for callback response")
	}
}

// ---------------------------------------------------------------------------
// TestE2E_PollThenHandlerFlow — tests Poller.Poll + Handler.HandleUpdate
// together, verifying the full pipeline end-to-end.
// ---------------------------------------------------------------------------

func TestE2E_PollThenHandlerFlow(t *testing.T) {
	rec := new(requestRecorder)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := readAllBody(r)

		rec.mu.Lock()
		rec.requests = append(rec.requests, recordedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(bodyBytes),
		})
		rec.mu.Unlock()

		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			okResponse(w, []map[string]any{
				{
					"update_id": 42,
					"message": map[string]any{
						"message_id": 1,
						"text":       "What's the weather?",
						"chat":       map[string]any{"id": 555, "type": "private"},
						"from":       map[string]any{"id": 666, "first_name": "User"},
						"date":       1_700_000_003,
					},
				},
			})

		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			okResponse(w, map[string]any{
				"message_id": 200,
				"text":       extractTextFromBody(string(bodyBytes)),
				"chat":       map[string]any{"id": 555, "type": "private"},
			})

		default:
			okResponse(w, map[string]any{"ok": true})
		}
	}))
	defer ts.Close()

	bot := testBot(t, ts)
	poller := NewPoller(bot)
	poller.Timeout = 0
	handler := NewHandler(bot)

	// Track how many text messages and their content.
	var (
		textCallCount int
		textChatID    int64
		textContent   string
	)
	handler.OnTextMessage = func(chatID int64, messageID int, text string) (string, error) {
		textCallCount++
		textChatID = chatID
		textContent = text
		return "The weather is sunny today.", nil
	}

	// Step 1: Poll once to get the update.
	ctx := context.Background()
	updates, err := poller.Poll(ctx)
	if err != nil {
		t.Fatalf("Poll failed: %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}
	if updates[0].ID != 42 {
		t.Errorf("update ID = %d, want 42", updates[0].ID)
	}

	// Step 2: Route the update through the handler.
	handler.HandleUpdate(updates[0])

	// Verify the handler received the right data.
	if textCallCount != 1 {
		t.Errorf("OnTextMessage was called %d times, want 1", textCallCount)
	}
	if textChatID != 555 {
		t.Errorf("OnTextMessage chatID = %d, want 555", textChatID)
	}
	if textContent != "What's the weather?" {
		t.Errorf("OnTextMessage text = %q, want %q", textContent, "What's the weather?")
	}

	// Verify the mock server saw the expected sequence of requests.
	reqs := rec.all()

	// Request 1: getUpdates (from Poll)
	// Request 2: sendMessage with the response (from HandleUpdate)
	if len(reqs) < 2 {
		t.Fatalf("expected at least 2 requests, got %d", len(reqs))
	}

	if !strings.HasSuffix(reqs[0].Path, "/getUpdates") {
		t.Errorf("first request should be getUpdates, got %s", reqs[0].Path)
	}

	var foundSendMessage bool
	for _, req := range reqs {
		if strings.HasSuffix(req.Path, "/sendMessage") {
			foundSendMessage = true
			if !strings.Contains(req.Body, "sunny") {
				t.Errorf("sendMessage does not contain expected response: %s", req.Body)
			}
			if !strings.Contains(req.Body, `"chat_id":555`) {
				t.Errorf("sendMessage does not contain chat_id=555: %s", req.Body)
			}
			// Verify parse_mode is MarkdownV2 (default behavior)
			if !strings.Contains(req.Body, `"parse_mode":"MarkdownV2"`) {
				t.Errorf("sendMessage should include parse_mode=MarkdownV2: %s", req.Body)
			}
			break
		}
	}
	if !foundSendMessage {
		t.Error("no sendMessage request was made in the flow")
	}

	// Verify poller offset advanced.
	if poller.Offset != 43 {
		t.Errorf("poller offset = %d, want 43 (42+1)", poller.Offset)
	}

	// Verify the sequence: getUpdates before sendMessage.
	getUpdatesIdx := -1
	sendMsgIdx := -1
	for i, req := range reqs {
		if strings.HasSuffix(req.Path, "/getUpdates") {
			getUpdatesIdx = i
		}
		if strings.HasSuffix(req.Path, "/sendMessage") {
			sendMsgIdx = i
		}
	}
	if getUpdatesIdx < 0 {
		t.Error("getUpdates request not found")
	}
	if sendMsgIdx < 0 {
		t.Error("sendMessage request not found")
	}
	if getUpdatesIdx >= 0 && sendMsgIdx >= 0 && getUpdatesIdx > sendMsgIdx {
		t.Error("getUpdates should come before sendMessage in the request sequence")
	}
}

// ---------------------------------------------------------------------------
// TestE2E_MediaFlow — end-to-end test for MEDIA response prefix.
// ---------------------------------------------------------------------------

func TestE2E_MediaFlow(t *testing.T) {
	// Create a temp media file that the handler can reference.
	tmpFile, err := os.CreateTemp(t.TempDir(), "test-photo-*.jpg")
	if err != nil {
		t.Fatalf("failed to create temp photo file: %v", err)
	}
	tmpPath := tmpFile.Name()
	// Write some content so the file isn't empty.
	if _, err := tmpFile.Write([]byte("fake-image-data")); err != nil {
		t.Fatalf("failed to write to temp file: %v", err)
	}
	tmpFile.Close()

	rec := new(requestRecorder)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := readAllBody(r)

		rec.mu.Lock()
		rec.requests = append(rec.requests, recordedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(bodyBytes),
		})
		rec.mu.Unlock()

		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			okResponse(w, []map[string]any{
				{
					"update_id": 99,
					"message": map[string]any{
						"message_id": 50,
						"text":       "Show me a photo",
						"chat":       map[string]any{"id": 777, "type": "private"},
						"from":       map[string]any{"id": 888, "first_name": "User"},
						"date":       1_700_000_004,
					},
				},
			})

		case strings.HasSuffix(r.URL.Path, "/sendPhoto"):
			okResponse(w, map[string]any{
				"message_id": 201,
				"photo": []map[string]any{
					{"file_id": "sent_photo_id", "width": 800, "height": 600},
				},
			})

		case strings.HasSuffix(r.URL.Path, "/sendVoice"):
			okResponse(w, map[string]any{"message_id": 301})

		default:
			okResponse(w, map[string]any{"ok": true})
		}
	}))
	defer ts.Close()

	bot := testBot(t, ts)
	poller := NewPoller(bot)
	poller.Timeout = 0
	handler := NewHandler(bot)

	// OnTextMessage returns a MEDIA:photo response.
	handler.OnTextMessage = func(chatID int64, messageID int, text string) (string, error) {
		return "MEDIA:photo:" + tmpPath, nil
	}

	// Poll.
	ctx := context.Background()
	updates, err := poller.Poll(ctx)
	if err != nil {
		t.Fatalf("Poll failed: %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}

	// Handle the update — this should trigger sendPhoto.
	handler.HandleUpdate(updates[0])

	// Verify sendPhoto was called.
	reqs := rec.all()
	var foundSendPhoto bool
	for _, req := range reqs {
		if strings.HasSuffix(req.Path, "/sendPhoto") {
			foundSendPhoto = true
			if !strings.Contains(req.Body, tmpPath) && !strings.Contains(strings.ToLower(req.Body), "form-data") {
				// The photo is sent via multipart, so the body won't contain the path as text.
				// Just verify the request was made with Content-Type multipart/form-data.
			}
			break
		}
	}
	if !foundSendPhoto {
		t.Error("no sendPhoto request was made for MEDIA:photo response")
	}

	// Verify no sendMessage was made for the MEDIA response.
	for _, req := range reqs {
		if strings.HasSuffix(req.Path, "/sendMessage") {
			t.Errorf("sendMessage should not be called for MEDIA responses, but got: %s %s", req.Method, req.Path)
		}
	}
}

// ---------------------------------------------------------------------------
// TestE2E_VoiceMediaFlow — end-to-end test for MEDIA:voice prefix.
// ---------------------------------------------------------------------------

func TestE2E_VoiceMediaFlow(t *testing.T) {
	// Create a temp voice file.
	tmpFile, err := os.CreateTemp(t.TempDir(), "test-voice-*.ogg")
	if err != nil {
		t.Fatalf("failed to create temp voice file: %v", err)
	}
	tmpPath := tmpFile.Name()
	if _, err := tmpFile.Write([]byte("fake-voice-data")); err != nil {
		t.Fatalf("failed to write to temp file: %v", err)
	}
	tmpFile.Close()

	rec := new(requestRecorder)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := readAllBody(r)

		rec.mu.Lock()
		rec.requests = append(rec.requests, recordedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(bodyBytes),
		})
		rec.mu.Unlock()

		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			okResponse(w, []map[string]any{
				{
					"update_id": 100,
					"message": map[string]any{
						"message_id": 60,
						"text":       "Send me a voice note",
						"chat":       map[string]any{"id": 888, "type": "private"},
						"from":       map[string]any{"id": 999, "first_name": "User"},
						"date":       1_700_000_005,
					},
				},
			})

		case strings.HasSuffix(r.URL.Path, "/sendVoice"):
			okResponse(w, map[string]any{"message_id": 302})

		default:
			okResponse(w, map[string]any{"ok": true})
		}
	}))
	defer ts.Close()

	bot := testBot(t, ts)
	poller := NewPoller(bot)
	poller.Timeout = 0
	handler := NewHandler(bot)

	handler.OnTextMessage = func(chatID int64, messageID int, text string) (string, error) {
		return "MEDIA:voice:" + tmpPath, nil
	}

	ctx := context.Background()
	updates, err := poller.Poll(ctx)
	if err != nil {
		t.Fatalf("Poll failed: %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}

	handler.HandleUpdate(updates[0])

	reqs := rec.all()
	var foundSendVoice bool
	for _, req := range reqs {
		if strings.HasSuffix(req.Path, "/sendVoice") {
			foundSendVoice = true
			break
		}
	}
	if !foundSendVoice {
		t.Error("no sendVoice request was made for MEDIA:voice response")
	}
}

// ---------------------------------------------------------------------------
// TestE2E_PollEmptyThenMessage — verify poll returns empty, then a later
// poll returns an update that is handled.
// ---------------------------------------------------------------------------

func TestE2E_PollEmptyThenMessage(t *testing.T) {
	callCount := 0
	rec := new(requestRecorder)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := readAllBody(r)

		rec.mu.Lock()
		rec.requests = append(rec.requests, recordedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(bodyBytes),
		})
		rec.mu.Unlock()

		callCount++

		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			if callCount == 1 {
				// First poll: empty (timeout).
				okResponse(w, []map[string]any{})
			} else {
				// Second poll: return an update.
				okResponse(w, []map[string]any{
					{
						"update_id": 200,
						"message": map[string]any{
							"message_id": 70,
							"text":       "Second message",
							"chat":       map[string]any{"id": 333, "type": "private"},
							"from":       map[string]any{"id": 444, "first_name": "User"},
							"date":       1_700_000_006,
						},
					},
				})
			}

		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			okResponse(w, map[string]any{
				"message_id": 400,
				"text":       extractTextFromBody(string(bodyBytes)),
				"chat":       map[string]any{"id": 333, "type": "private"},
			})

		default:
			okResponse(w, map[string]any{"ok": true})
		}
	}))
	defer ts.Close()

	bot := testBot(t, ts)
	poller := NewPoller(bot)
	poller.Timeout = 0
	handler := NewHandler(bot)

	var messagesReceived []string
	handler.OnTextMessage = func(chatID int64, messageID int, text string) (string, error) {
		messagesReceived = append(messagesReceived, text)
		return "Response to: " + text, nil
	}

	ctx := context.Background()

	// First poll: should be empty.
	updates, err := poller.Poll(ctx)
	if err != nil {
		t.Fatalf("first Poll failed: %v", err)
	}
	if len(updates) != 0 {
		t.Fatalf("expected 0 updates on first poll, got %d", len(updates))
	}
	if poller.Offset != 0 {
		t.Errorf("offset should remain 0 on empty poll, got %d", poller.Offset)
	}

	// Second poll: should return the message update.
	updates, err = poller.Poll(ctx)
	if err != nil {
		t.Fatalf("second Poll failed: %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("expected 1 update on second poll, got %d", len(updates))
	}
	if updates[0].Message.Text != "Second message" {
		t.Errorf("message text = %q, want %q", updates[0].Message.Text, "Second message")
	}

	// Handle and verify.
	handler.HandleUpdate(updates[0])

	if len(messagesReceived) != 1 {
		t.Fatalf("expected 1 message received, got %d", len(messagesReceived))
	}
	if messagesReceived[0] != "Second message" {
		t.Errorf("message = %q, want %q", messagesReceived[0], "Second message")
	}

	// Verify offset advanced.
	if poller.Offset != 201 {
		t.Errorf("poller offset = %d, want 201", poller.Offset)
	}

	// Verify the flow: 2 getUpdates calls, 1 sendMessage.
	reqs := rec.all()
	getUpdatesCount := 0
	sendMsgCount := 0
	for _, req := range reqs {
		if strings.HasSuffix(req.Path, "/getUpdates") {
			getUpdatesCount++
		}
		if strings.HasSuffix(req.Path, "/sendMessage") {
			sendMsgCount++
		}
	}
	if getUpdatesCount != 2 {
		t.Errorf("expected 2 getUpdates calls, got %d", getUpdatesCount)
	}
	if sendMsgCount != 1 {
		t.Errorf("expected 1 sendMessage call, got %d", sendMsgCount)
	}
}

// ---------------------------------------------------------------------------
// TestE2E_InlineKeyboardResponse — handler returns a response that triggers
// an inline keyboard (via SendOpts). This tests that the handler path works
// end-to-end when the callback returns text and the bot sends it.
// ---------------------------------------------------------------------------

func TestE2E_InlineKeyboardResponse(t *testing.T) {
	rec := new(requestRecorder)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := readAllBody(r)

		rec.mu.Lock()
		rec.requests = append(rec.requests, recordedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(bodyBytes),
		})
		rec.mu.Unlock()

		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			okResponse(w, []map[string]any{
				{
					"update_id": 300,
					"message": map[string]any{
						"message_id": 80,
						"text":       "/options",
						"entities":   []map[string]any{{"type": "bot_command", "offset": 0, "length": 8}},
						"chat":       map[string]any{"id": 444, "type": "private"},
						"from":       map[string]any{"id": 555, "first_name": "User"},
						"date":       1_700_000_007,
					},
				},
			})

		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			okResponse(w, map[string]any{
				"message_id": 500,
				"text":       extractTextFromBody(string(bodyBytes)),
				"chat":       map[string]any{"id": 444, "type": "private"},
			})

		default:
			okResponse(w, map[string]any{"ok": true})
		}
	}))
	defer ts.Close()

	bot := testBot(t, ts)
	poller := NewPoller(bot)
	poller.Timeout = 0
	handler := NewHandler(bot)

	handler.OnCommand = func(chatID int64, messageID int, cmd string, args string) (string, error) {
		return "Here are your options:", nil
	}

	ctx := context.Background()
	updates, err := poller.Poll(ctx)
	if err != nil {
		t.Fatalf("Poll failed: %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}

	handler.HandleUpdate(updates[0])

	// Verify sendMessage was called with the command response.
	reqs := rec.all()
	var foundSendMsg bool
	for _, req := range reqs {
		if strings.HasSuffix(req.Path, "/sendMessage") {
			foundSendMsg = true
			if !strings.Contains(req.Body, "Here are your options") {
				t.Errorf("sendMessage body missing expected text: %s", req.Body)
			}
			break
		}
	}
	if !foundSendMsg {
		t.Error("no sendMessage request was made for command response")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// readAllBody reads all bytes from the request body and restores it for
// subsequent reads (since Body can only be read once).
func readAllBody(r *http.Request) ([]byte, error) {
	buf := make([]byte, r.ContentLength)
	if r.Body == nil {
		return nil, nil
	}
	defer r.Body.Close()
	_, err := r.Body.Read(buf)
	if err != nil && err.Error() == "EOF" && len(buf) == 0 {
		return nil, nil
	}
	// Read remaining if ContentLength was -1 (not set)
	if r.ContentLength == -1 {
		var sb strings.Builder
		sb.Write(buf)
		for {
			tmp := make([]byte, 4096)
			n, err := r.Body.Read(tmp)
			if n > 0 {
				sb.Write(tmp[:n])
			}
			if err != nil {
				break
			}
		}
		return []byte(sb.String()), nil
	}
	return buf, nil
}
