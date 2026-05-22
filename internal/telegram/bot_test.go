package telegram

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// okResponse writes a standard Telegram API success response wrapping result.
func okResponse(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{"ok": true, "result": result}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		panic(err)
	}
}

// failResponse writes a standard Telegram API error response.
func failResponse(w http.ResponseWriter, code int, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK) // Telegram always returns 200 even on errors
	resp := map[string]any{
		"ok":          false,
		"error_code":  code,
		"description": description,
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		panic(err)
	}
}

// decodeJSONBody decodes the request body into dest and returns an error if
// the body cannot be read or decoded. The body is read completely first so
// that it can also be checked for raw content if needed.
func decodeJSONBody(r *http.Request, dest any) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	return json.Unmarshal(body, dest)
}

// requireMethod fails the test if r.Method != method.
func requireMethod(t *testing.T, r *http.Request, method string) {
	t.Helper()
	if r.Method != method {
		t.Errorf("expected method %q, got %q", method, r.Method)
	}
}

// requireHeader fails the test if the header is missing or wrong.
func requireHeader(t *testing.T, r *http.Request, key, want string) {
	t.Helper()
	if got := r.Header.Get(key); got != want {
		t.Errorf("header %q = %q, want %q", key, got, want)
	}
}

// requireJSONBody decodes the request body as JSON into dest.  It also
// verifies the Content-Type is application/json when contentType is set.
func requireJSONBody(t *testing.T, r *http.Request, contentType string, dest any) {
	t.Helper()
	if contentType != "" {
		requireHeader(t, r, "Content-Type", contentType)
	}
	requireMethod(t, r, http.MethodPost)
	if err := decodeJSONBody(r, dest); err != nil {
		t.Fatalf("failed to decode JSON body: %v", err)
	}
}

// requireMultipartBody reads the multipart body and verifies it contains the
// expected file field and the expected extra params.
func requireMultipartBody(t *testing.T, r *http.Request, fieldName, fileName string, params map[string]any) {
	t.Helper()
	requireMethod(t, r, http.MethodPost)
	ct := r.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "multipart/form-data") {
		t.Fatalf("Content-Type = %q, want multipart/form-data", ct)
	}

	// Read the entire body for inspection.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read multipart body: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("multipart body is empty")
	}

	// Parse the multipart form.
	bReader := bytes.NewReader(body)
	boundary := strings.TrimPrefix(ct, "multipart/form-data; boundary=")
	reader := NewReader(bReader, boundary)

	var foundFile bool
	var foundParams = make(map[string]bool)
	for k := range params {
		foundParams[k] = false
	}

	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read multipart part: %v", err)
		}

		// check if it's the file part
		if part.FormName() == fieldName && part.FileName() == fileName {
			foundFile = true
			content, _ := io.ReadAll(part)
			if len(content) == 0 {
				t.Errorf("file part %q is empty", fieldName)
			}
		}

		// check if it's a regular field
		if v := part.FormName(); v != "" {
			if _, ok := params[v]; ok {
				foundParams[v] = true
				val, _ := io.ReadAll(part)
				var decoded any
				if err := json.Unmarshal(val, &decoded); err != nil {
					t.Errorf("param %q value %q is not valid JSON: %v", v, string(val), err)
				}
			}
		}

		part.Close()
	}

	if !foundFile {
		t.Errorf("multipart body missing file field %q with filename %q", fieldName, fileName)
	}
	for k, found := range foundParams {
		if !found {
			t.Errorf("multipart body missing param %q", k)
		}
	}
}

// ---------------------------------------------------------------------------
// NewBot
// ---------------------------------------------------------------------------

func TestNewBot(t *testing.T) {
	token := "123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11"
	bot := NewBot(token)

	if bot == nil {
		t.Fatal("NewBot returned nil")
	}
	if bot.Token != token {
		t.Errorf("Token = %q, want %q", bot.Token, token)
	}
	wantBase := fmt.Sprintf("https://api.telegram.org/bot%s", token)
	if bot.BaseURL != wantBase {
		t.Errorf("BaseURL = %q, want %q", bot.BaseURL, wantBase)
	}
	if bot.Client == nil {
		t.Fatal("Client is nil")
	}
	if bot.Client.Timeout == 0 {
		t.Error("Client.Timeout is zero, expected 30s default")
	}
}

// ---------------------------------------------------------------------------
// SendMessage — success
// ---------------------------------------------------------------------------

func TestSendMessage_Success(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimSuffix(r.URL.Path, "/")
		if path != "/sendMessage" {
			t.Errorf("unexpected path: %s", path)
		}
		requireJSONBody(t, r, "application/json", &gotBody)
		okResponse(w, map[string]any{
			"message_id": 42,
			"text":       "Hello, world!",
			"chat":       map[string]any{"id": 123, "type": "private"},
			"from":       map[string]any{"id": 1, "is_bot": true, "first_name": "Bot"},
			"date":       1_700_000_000,
		})
	}))
	defer ts.Close()

	bot := NewBot("testtoken")
	bot.BaseURL = ts.URL

	msg, err := bot.SendMessage(123, "Hello, world!", nil)
	if err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}
	if msg == nil {
		t.Fatal("SendMessage returned nil message")
	}
	if msg.ID != 42 {
		t.Errorf("msg.ID = %d, want 42", msg.ID)
	}
	if msg.Text != "Hello, world!" {
		t.Errorf("msg.Text = %q, want %q", msg.Text, "Hello, world!")
	}
	if msg.Chat == nil || msg.Chat.ID != 123 {
		t.Errorf("msg.Chat.ID = %d, want 123", msg.Chat.ID)
	}

	// Verify request body.
	if gotBody["chat_id"] != float64(123) {
		t.Errorf("chat_id = %v, want 123", gotBody["chat_id"])
	}
	if gotBody["text"] != "Hello, world!" {
		t.Errorf("text = %v, want %q", gotBody["text"], "Hello, world!")
	}
}

// ---------------------------------------------------------------------------
// SendMessage — with opts
// ---------------------------------------------------------------------------

func TestSendMessage_WithOpts(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJSONBody(t, r, "application/json", &gotBody)
		okResponse(w, map[string]any{"message_id": 1, "text": "hey"})
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	opts := &SendOpts{
		ParseMode:             "MarkdownV2",
		DisableWebPagePreview: true,
		ReplyToMessageID:      7,
		ReplyMarkup: &InlineKeyboardMarkup{
			InlineKeyboard: [][]InlineKeyboardButton{
				{{Text: "Go", CallbackData: "go"}},
			},
		},
	}
	_, err := bot.SendMessage(456, "hey", opts)
	if err != nil {
		t.Fatalf("SendMessage with opts: %v", err)
	}

	if gotBody["parse_mode"] != "MarkdownV2" {
		t.Errorf("parse_mode = %v, want MarkdownV2", gotBody["parse_mode"])
	}
	if gotBody["disable_web_page_preview"] != true {
		t.Errorf("disable_web_page_preview = %v, want true", gotBody["disable_web_page_preview"])
	}
	if gotBody["reply_to_message_id"] != float64(7) {
		t.Errorf("reply_to_message_id = %v, want 7", gotBody["reply_to_message_id"])
	}
	rm, ok := gotBody["reply_markup"].(map[string]any)
	if !ok {
		t.Fatal("reply_markup missing or wrong type")
	}
	kb, ok := rm["inline_keyboard"].([]any)
	if !ok || len(kb) != 1 {
		t.Fatalf("inline_keyboard malformed: %+v", rm)
	}
}

// ---------------------------------------------------------------------------
// SendMessage — API error
// ---------------------------------------------------------------------------

func TestSendMessage_APIError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failResponse(w, 400, "Bad Request: chat not found")
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	msg, err := bot.SendMessage(999, "hi", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if msg != nil {
		t.Errorf("expected nil message, got %+v", msg)
	}
	if !strings.Contains(err.Error(), "chat not found") {
		t.Errorf("error = %q, want substring %q", err, "chat not found")
	}
}

// ---------------------------------------------------------------------------
// GetUpdates
// ---------------------------------------------------------------------------

func TestGetUpdates_Success(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimSuffix(r.URL.Path, "/")
		if path != "/getUpdates" {
			t.Errorf("unexpected path: %s", path)
		}
		requireJSONBody(t, r, "application/json", &gotBody)
		okResponse(w, []map[string]any{
			{
				"update_id": 100,
				"message": map[string]any{
					"message_id": 1,
					"text":       "first",
					"chat":       map[string]any{"id": 1, "type": "private"},
					"date":       1000,
				},
			},
			{
				"update_id": 101,
				"callback_query": map[string]any{
					"id":   "cq_1",
					"data": "btn_click",
					"from": map[string]any{"id": 2, "first_name": "User"},
				},
			},
		})
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	updates, err := bot.GetUpdates(5, 30)
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	if len(updates) != 2 {
		t.Fatalf("got %d updates, want 2", len(updates))
	}

	// First update
	if updates[0].ID != 100 {
		t.Errorf("updates[0].ID = %d, want 100", updates[0].ID)
	}
	if updates[0].Message == nil {
		t.Fatal("updates[0].Message is nil")
	}
	if updates[0].Message.Text != "first" {
		t.Errorf("updates[0].Message.Text = %q, want %q", updates[0].Message.Text, "first")
	}

	// Second update — callback query
	if updates[1].ID != 101 {
		t.Errorf("updates[1].ID = %d, want 101", updates[1].ID)
	}
	if updates[1].CallbackQuery == nil {
		t.Fatal("updates[1].CallbackQuery is nil")
	}
	if updates[1].CallbackQuery.Data != "btn_click" {
		t.Errorf("CallbackQuery.Data = %q, want %q", updates[1].CallbackQuery.Data, "btn_click")
	}

	// Verify request body contains offset and timeout.
	if gotBody["offset"] != float64(5) {
		t.Errorf("offset = %v, want 5", gotBody["offset"])
	}
	if gotBody["timeout"] != float64(30) {
		t.Errorf("timeout = %v, want 30", gotBody["timeout"])
	}
}

func TestGetUpdates_Empty(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		okResponse(w, []map[string]any{})
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	updates, err := bot.GetUpdates(0, 10)
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	if len(updates) != 0 {
		t.Errorf("expected 0 updates, got %d", len(updates))
	}
}

// ---------------------------------------------------------------------------
// GetFile
// ---------------------------------------------------------------------------

func TestGetFile_Success(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimSuffix(r.URL.Path, "/")
		if path != "/getFile" {
			t.Errorf("unexpected path: %s", path)
		}
		requireJSONBody(t, r, "application/json", &gotBody)
		okResponse(w, map[string]any{
			"file_id":   "abc123",
			"file_path": "documents/file.txt",
			"file_size": 1024,
		})
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	f, err := bot.GetFile("abc123")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if f == nil {
		t.Fatal("GetFile returned nil")
	}
	if f.FileID != "abc123" {
		t.Errorf("FileID = %q, want %q", f.FileID, "abc123")
	}
	if f.FilePath != "documents/file.txt" {
		t.Errorf("FilePath = %q, want %q", f.FilePath, "documents/file.txt")
	}
	if f.FileSize != 1024 {
		t.Errorf("FileSize = %d, want 1024", f.FileSize)
	}

	if gotBody["file_id"] != "abc123" {
		t.Errorf("file_id = %v, want abc123", gotBody["file_id"])
	}
}

// ---------------------------------------------------------------------------
// DownloadFile
// ---------------------------------------------------------------------------

// roundTripperFunc is an http.RoundTripper that delegates to a function.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// apiRewriter is an http.RoundTripper that rewrites requests to
// api.telegram.org to point at testServerURL.  It wraps inner so that
// real network calls are avoided entirely.
type apiRewriter struct {
	inner         http.RoundTripper
	testServerURL string
}

func (a *apiRewriter) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Host, "api.telegram.org") {
		u := *req.URL
		u.Scheme = "http"
		u.Host = strings.TrimPrefix(a.testServerURL, "http://")
		u.Host = strings.TrimPrefix(u.Host, "https://")
		newReq := req.Clone(req.Context())
		newReq.URL = &u
		return a.inner.RoundTrip(newReq)
	}
	return a.inner.RoundTrip(req)
}

func TestDownloadFile_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// We expect: GET /file/botTOKEN/documents%2Ffile.txt
		// Only check that the path contains the file path.
		if !strings.Contains(r.URL.Path, "/file/bot") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if !strings.Contains(r.URL.Path, "documents/file.txt") {
			// path may be URL-encoded, check both forms
			if !strings.Contains(r.URL.RawPath, "documents%2Ffile.txt") &&
				!strings.Contains(r.URL.Path, "documents/file.txt") {
				t.Errorf("path does not contain file path: %s", r.URL.Path)
			}
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("file content here"))
	}))
	defer ts.Close()

	bot := NewBot("testtoken")
	// DownloadFile uses a hardcoded URL, so we need a custom transport.
	bot.Client = &http.Client{
		Transport: &apiRewriter{
			inner:         http.DefaultTransport,
			testServerURL: ts.URL,
		},
	}

	data, err := bot.DownloadFile("documents/file.txt")
	if err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}
	if string(data) != "file content here" {
		t.Errorf("got body %q, want %q", string(data), "file content here")
	}
}

func TestDownloadFile_NotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Not Found"))
	}))
	defer ts.Close()

	bot := NewBot("testtoken")
	bot.Client = &http.Client{
		Transport: &apiRewriter{
			inner:         http.DefaultTransport,
			testServerURL: ts.URL,
		},
	}

	_, err := bot.DownloadFile("missing.txt")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "status 404") {
		t.Errorf("error = %q, want substring %q", err, "status 404")
	}
}

// ---------------------------------------------------------------------------
// SendPhoto — multipart upload
// ---------------------------------------------------------------------------

func TestSendPhoto_Success(t *testing.T) {
	// Create a temporary file to "upload".
	tmpDir := t.TempDir()
	photoPath := filepath.Join(tmpDir, "test_photo.jpg")
	if err := os.WriteFile(photoPath, []byte("fake-jpeg-data"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimSuffix(r.URL.Path, "/")
		if path != "/sendPhoto" {
			t.Errorf("unexpected path: %s", path)
		}
		requireMultipartBody(t, r, "photo", "test_photo.jpg", map[string]any{
			"chat_id": float64(123),
			"caption": "nice photo",
		})
		okResponse(w, map[string]any{
			"message_id": 10,
			"text":       "",
			"chat":       map[string]any{"id": 123, "type": "private"},
			"date":       2000,
		})
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	msg, err := bot.SendPhoto(123, photoPath, "nice photo", nil)
	if err != nil {
		t.Fatalf("SendPhoto: %v", err)
	}
	if msg == nil {
		t.Fatal("SendPhoto returned nil message")
	}
	if msg.ID != 10 {
		t.Errorf("msg.ID = %d, want 10", msg.ID)
	}
}

func TestSendPhoto_NoCaption(t *testing.T) {
	tmpDir := t.TempDir()
	photoPath := filepath.Join(tmpDir, "img.png")
	if err := os.WriteFile(photoPath, []byte("png-data"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireMultipartBody(t, r, "photo", "img.png", map[string]any{
			"chat_id": float64(456),
		})
		okResponse(w, map[string]any{
			"message_id": 11,
			"chat":       map[string]any{"id": 456},
		})
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	msg, err := bot.SendPhoto(456, photoPath, "", nil)
	if err != nil {
		t.Fatalf("SendPhoto(no caption): %v", err)
	}
	if msg.ID != 11 {
		t.Errorf("msg.ID = %d, want 11", msg.ID)
	}
}

func TestSendPhoto_FileNotFound(t *testing.T) {
	bot := NewBot("x")
	// No server needed — the file doesn't exist locally.
	_, err := bot.SendPhoto(1, "/nonexistent/path.jpg", "", nil)
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), "open") {
		t.Errorf("error = %q, want substring %q", err, "open")
	}
}

// ---------------------------------------------------------------------------
// SendDocument — multipart upload
// ---------------------------------------------------------------------------

func TestSendDocument_Success(t *testing.T) {
	tmpDir := t.TempDir()
	docPath := filepath.Join(tmpDir, "test_doc.pdf")
	if err := os.WriteFile(docPath, []byte("fake-pdf-data"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimSuffix(r.URL.Path, "/")
		if path != "/sendDocument" {
			t.Errorf("unexpected path: %s", path)
		}
		requireMultipartBody(t, r, "document", "test_doc.pdf", map[string]any{
			"chat_id": float64(123),
			"caption": "here is the doc",
		})
		okResponse(w, map[string]any{
			"message_id": 20,
			"text":       "",
			"chat":       map[string]any{"id": 123, "type": "private"},
			"date":       2000,
		})
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	msg, err := bot.SendDocument(123, docPath, "here is the doc", nil)
	if err != nil {
		t.Fatalf("SendDocument: %v", err)
	}
	if msg == nil {
		t.Fatal("SendDocument returned nil message")
	}
	if msg.ID != 20 {
		t.Errorf("msg.ID = %d, want 20", msg.ID)
	}
}

func TestSendDocument_NoCaption(t *testing.T) {
	tmpDir := t.TempDir()
	docPath := filepath.Join(tmpDir, "report.csv")
	if err := os.WriteFile(docPath, []byte("a,b,c\n1,2,3"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireMultipartBody(t, r, "document", "report.csv", map[string]any{
			"chat_id": float64(456),
		})
		okResponse(w, map[string]any{
			"message_id": 21,
			"chat":       map[string]any{"id": 456},
		})
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	msg, err := bot.SendDocument(456, docPath, "", nil)
	if err != nil {
		t.Fatalf("SendDocument(no caption): %v", err)
	}
	if msg.ID != 21 {
		t.Errorf("msg.ID = %d, want 21", msg.ID)
	}
}

func TestSendDocument_FileNotFound(t *testing.T) {
	bot := NewBot("x")
	_, err := bot.SendDocument(1, "/nonexistent/path.pdf", "", nil)
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), "open") {
		t.Errorf("error = %q, want substring %q", err, "open")
	}
}

// ---------------------------------------------------------------------------
// AnswerCallbackQuery
// ---------------------------------------------------------------------------

func TestAnswerCallbackQuery_Success(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimSuffix(r.URL.Path, "/")
		if path != "/answerCallbackQuery" {
			t.Errorf("unexpected path: %s", path)
		}
		requireJSONBody(t, r, "application/json", &gotBody)
		okResponse(w, true)
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	err := bot.AnswerCallbackQuery("cq_123", "Done!", false)
	if err != nil {
		t.Fatalf("AnswerCallbackQuery: %v", err)
	}

	if gotBody["callback_query_id"] != "cq_123" {
		t.Errorf("callback_query_id = %v, want cq_123", gotBody["callback_query_id"])
	}
	if gotBody["text"] != "Done!" {
		t.Errorf("text = %v, want Done!", gotBody["text"])
	}
	if gotBody["show_alert"] != false {
		t.Errorf("show_alert = %v, want false", gotBody["show_alert"])
	}
}

func TestAnswerCallbackQuery_WithAlert(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJSONBody(t, r, "application/json", &gotBody)
		okResponse(w, true)
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	err := bot.AnswerCallbackQuery("cq_999", "Alert!", true)
	if err != nil {
		t.Fatalf("AnswerCallbackQuery: %v", err)
	}
	if gotBody["show_alert"] != true {
		t.Errorf("show_alert = %v, want true", gotBody["show_alert"])
	}
}

func TestAnswerCallbackQuery_Error(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failResponse(w, 400, "Bad Request: invalid query ID")
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	err := bot.AnswerCallbackQuery("bad_id", "text", false)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid query ID") {
		t.Errorf("error = %q, want substring %q", err, "invalid query ID")
	}
}

// ---------------------------------------------------------------------------
// GetMe
// ---------------------------------------------------------------------------

func TestGetMe_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimSuffix(r.URL.Path, "/")
		if path != "/getMe" {
			t.Errorf("unexpected path: %s", path)
		}
		// getMe sends nil body
		body, _ := io.ReadAll(r.Body)
		if len(body) != 0 {
			t.Errorf("expected empty body, got %q", string(body))
		}
		okResponse(w, map[string]any{
			"id":         123456,
			"is_bot":     true,
			"first_name": "TestBot",
			"username":   "test_bot",
		})
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	user, err := bot.GetMe()
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	if user == nil {
		t.Fatal("GetMe returned nil")
	}
	if user.ID != 123456 {
		t.Errorf("user.ID = %d, want 123456", user.ID)
	}
	if !user.IsBot {
		t.Error("user.IsBot = false, want true")
	}
	if user.FirstName != "TestBot" {
		t.Errorf("user.FirstName = %q, want %q", user.FirstName, "TestBot")
	}
	if user.Username != "test_bot" {
		t.Errorf("user.Username = %q, want %q", user.Username, "test_bot")
	}
}

// ---------------------------------------------------------------------------
// SetMyCommands
// ---------------------------------------------------------------------------

func TestSetMyCommands_Success(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimSuffix(r.URL.Path, "/")
		if path != "/setMyCommands" {
			t.Errorf("unexpected path: %s", path)
		}
		requireJSONBody(t, r, "application/json", &gotBody)
		okResponse(w, true)
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	cmds := []BotCommand{
		{Command: "start", Description: "Start the bot"},
		{Command: "help", Description: "Show help"},
	}
	err := bot.SetMyCommands(cmds)
	if err != nil {
		t.Fatalf("SetMyCommands: %v", err)
	}

	cmdsRaw, ok := gotBody["commands"].([]any)
	if !ok {
		t.Fatalf("commands field missing or wrong type: %+v", gotBody)
	}
	if len(cmdsRaw) != 2 {
		t.Fatalf("len(commands) = %d, want 2", len(cmdsRaw))
	}
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

func TestSendMessage_ZeroChatID(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJSONBody(t, r, "application/json", &gotBody)
		okResponse(w, map[string]any{"message_id": 0, "text": ""})
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	// chatID = 0 is valid in some contexts (e.g. channel testing).
	msg, err := bot.SendMessage(0, "zero", nil)
	if err != nil {
		t.Fatalf("SendMessage with chatID=0: %v", err)
	}
	if msg == nil {
		t.Fatal("SendMessage returned nil")
	}
}

func TestBot_ServerUnreachable(t *testing.T) {
	// Point to a closed server to trigger connection error.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	_, err := bot.GetMe()
	if err == nil {
		t.Fatal("expected error for closed server, got nil")
	}
}

func TestBot_MalformedJSONResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{invalid json`))
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	_, err := bot.GetMe()
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

func TestBot_EmptyResult(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		okResponse(w, nil) // result is null
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	// SendMessage should not fail when result is null — it should return empty Message.
	msg, err := bot.SendMessage(1, "text", nil)
	if err != nil {
		t.Fatalf("SendMessage with null result: %v", err)
	}
	if msg == nil {
		t.Error("SendMessage returned nil message on null result")
	}
}

// ---------------------------------------------------------------------------
// Multipart helper — mime/multipart.Reader does not export a constructor that
// takes a boundary string directly in older Go versions, so we provide a thin
// wrapper.
// ---------------------------------------------------------------------------

// NewReader creates a new multipart.Reader that reads from r using the given boundary.
var NewReader = func(r io.Reader, boundary string) interface {
	NextPart() (interface {
		FormName() string
		FileName() string
		io.ReadCloser
	}, error)
} {
	// Use the standard library's multipart.NewReader.
	type partish interface {
		FormName() string
		FileName() string
		io.ReadCloser
	}
	mp := multipart.NewReader(r, boundary)
	return &mpReader{Reader: mp}
}

type mpReader struct {
	*multipart.Reader
}

func (m *mpReader) NextPart() (interface {
	FormName() string
	FileName() string
	io.ReadCloser
}, error) {
	return m.Reader.NextPart()
}

// ---------------------------------------------------------------------------
// SetLogger
// ---------------------------------------------------------------------------

func TestBot_SetLogger(t *testing.T) {
	t.Run("nil uses NopLogger", func(t *testing.T) {
		bot := NewBot("testtoken")
		bot.SetLogger(nil)
		if _, ok := bot.log.(nopLogger); !ok {
			t.Errorf("expected nopLogger after SetLogger(nil), got %T", bot.log)
		}
	})

	t.Run("valid logger is set", func(t *testing.T) {
		bot := NewBot("testtoken")
		fl := NewFileLogger(LogDebug, "")
		bot.SetLogger(fl)
		if bot.log != fl {
			t.Errorf("logger not set correctly")
		}
	})
}

// ---------------------------------------------------------------------------
// EditMessageText
// ---------------------------------------------------------------------------

func TestBot_EditMessageText(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimSuffix(r.URL.Path, "/")
		if path != "/editMessageText" {
			t.Errorf("unexpected path: %s", path)
		}
		requireJSONBody(t, r, "application/json", &gotBody)
		okResponse(w, map[string]any{
			"message_id": 42,
			"text":       "edited",
			"chat":       map[string]any{"id": 123},
		})
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	err := bot.EditMessageText(123, 42, "edited", nil)
	if err != nil {
		t.Fatalf("EditMessageText: %v", err)
	}

	if gotBody["chat_id"] != float64(123) {
		t.Errorf("chat_id = %v, want 123", gotBody["chat_id"])
	}
	if gotBody["message_id"] != float64(42) {
		t.Errorf("message_id = %v, want 42", gotBody["message_id"])
	}
	if gotBody["text"] != "edited" {
		t.Errorf("text = %v, want %q", gotBody["text"], "edited")
	}
}

func TestBot_EditMessageText_WithParseMode(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJSONBody(t, r, "application/json", &gotBody)
		okResponse(w, map[string]any{"message_id": 1, "text": "hello"})
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	opts := &SendOpts{ParseMode: ParseModeMarkdownV2}
	err := bot.EditMessageText(456, 7, "hello", opts)
	if err != nil {
		t.Fatalf("EditMessageText with opts: %v", err)
	}

	if gotBody["parse_mode"] != ParseModeMarkdownV2 {
		t.Errorf("parse_mode = %v, want %q", gotBody["parse_mode"], ParseModeMarkdownV2)
	}
	if gotBody["chat_id"] != float64(456) {
		t.Errorf("chat_id = %v, want 456", gotBody["chat_id"])
	}
	if gotBody["message_id"] != float64(7) {
		t.Errorf("message_id = %v, want 7", gotBody["message_id"])
	}
	if gotBody["text"] != "hello" {
		t.Errorf("text = %v, want %q", gotBody["text"], "hello")
	}
}

func TestBot_EditMessageText_Error(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failResponse(w, 400, "Bad Request: message is not modified")
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	err := bot.EditMessageText(999, 1, "same text", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "message is not modified") {
		t.Errorf("error = %q, want substring %q", err, "message is not modified")
	}
}

// ---------------------------------------------------------------------------
// DeleteMessage
// ---------------------------------------------------------------------------

func TestBot_DeleteMessage_Success(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimSuffix(r.URL.Path, "/")
		if path != "/deleteMessage" {
			t.Errorf("unexpected path: %s", path)
		}
		requireJSONBody(t, r, "application/json", &gotBody)
		okResponse(w, true)
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	err := bot.DeleteMessage(123, 456)
	if err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}

	if gotBody["chat_id"] != float64(123) {
		t.Errorf("chat_id = %v, want 123", gotBody["chat_id"])
	}
	if gotBody["message_id"] != float64(456) {
		t.Errorf("message_id = %v, want 456", gotBody["message_id"])
	}
}

func TestBot_DeleteMessage_Error(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failResponse(w, 400, "Bad Request: message to delete not found")
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	err := bot.DeleteMessage(1, 999)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "message to delete not found") {
		t.Errorf("error = %q, want substring %q", err, "message to delete not found")
	}
}

// ---------------------------------------------------------------------------
// SetFallbackURLs
// ---------------------------------------------------------------------------

func TestBot_SetFallbackURLs(t *testing.T) {
	bot := NewBot("testtoken")
	originalClient := bot.Client

	fallbacks := []string{"https://api.telegram2.org", "https://api.telegram3.org"}
	bot.SetFallbackURLs(fallbacks)

	// The bot's client should have been replaced.
	if bot.Client == originalClient {
		t.Error("bot.Client was not replaced after SetFallbackURLs")
	}

	// The transport should be a *FallbackTransport.
	ft, ok := bot.Client.Transport.(*FallbackTransport)
	if !ok {
		t.Fatalf("bot.Client.Transport = %T, want *FallbackTransport", bot.Client.Transport)
	}

	if len(ft.FallbackURLs) != 2 {
		t.Errorf("FallbackURLs length = %d, want 2", len(ft.FallbackURLs))
	}
	if ft.FallbackURLs[0] != "https://api.telegram2.org" {
		t.Errorf("FallbackURLs[0] = %q, want %q", ft.FallbackURLs[0], "https://api.telegram2.org")
	}
	if ft.FallbackURLs[1] != "https://api.telegram3.org" {
		t.Errorf("FallbackURLs[1] = %q, want %q", ft.FallbackURLs[1], "https://api.telegram3.org")
	}
}

func TestBot_SetFallbackURLs_Empty(t *testing.T) {
	bot := NewBot("testtoken")
	originalClient := bot.Client

	// Empty slice should be a no-op.
	bot.SetFallbackURLs([]string{})

	if bot.Client != originalClient {
		t.Error("bot.Client was replaced despite empty fallback list")
	}
}

// ---------------------------------------------------------------------------
// SetDailyTokenBudget / CheckDailyBudget
// ---------------------------------------------------------------------------

func TestBot_SetDailyTokenBudget(t *testing.T) {
	bot := NewBot("testtoken")
	if bot.DailyTokenBudget != 0 {
		t.Errorf("initial DailyTokenBudget = %d, want 0", bot.DailyTokenBudget)
	}

	bot.SetDailyTokenBudget(100_000)
	if bot.DailyTokenBudget != 100_000 {
		t.Errorf("DailyTokenBudget = %d, want 100000", bot.DailyTokenBudget)
	}
}

func TestBot_CheckDailyBudget_Unset(t *testing.T) {
	bot := NewBot("testtoken")
	// Budget is 0 (unset).
	err := bot.CheckDailyBudget(5000)
	if err != nil {
		t.Errorf("expected nil for unset budget, got %v", err)
	}
}

func TestBot_CheckDailyBudget_UnderLimit(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	bot := NewBot("testtoken")
	bot.SetDailyTokenBudget(10_000)

	err := bot.CheckDailyBudget(3000)
	if err != nil {
		t.Fatalf("CheckDailyBudget under limit: %v", err)
	}

	// Verify the budget file was created and contains the correct value.
	date := time.Now().Format("2006-01-02")
	budgetPath := filepath.Join(tmpDir, ".odek", "telegram_token_usage_"+date)
	data, err := os.ReadFile(budgetPath)
	if err != nil {
		t.Fatalf("read budget file: %v", err)
	}
	if string(data) != "3000" {
		t.Errorf("budget file content = %q, want %q", string(data), "3000")
	}
}

func TestBot_CheckDailyBudget_Exceeded(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	bot := NewBot("testtoken")
	bot.SetDailyTokenBudget(10_000)

	// Use 8,000 tokens first — under limit.
	if err := bot.CheckDailyBudget(8000); err != nil {
		t.Fatalf("first CheckDailyBudget (8000): %v", err)
	}

	// Try to use another 3,000 — should exceed the 10,000 limit.
	err := bot.CheckDailyBudget(3000)
	if err == nil {
		t.Fatal("expected error for exceeded budget, got nil")
	}
	if !strings.Contains(err.Error(), "daily token budget exceeded") {
		t.Errorf("error = %q, want substring %q", err, "daily token budget exceeded")
	}
	if !strings.Contains(err.Error(), "10000") {
		t.Errorf("error should mention limit 10000, got %q", err)
	}
}

func TestBot_CheckDailyBudget_PreflightPattern(t *testing.T) {
	// This test verifies the exact pattern used by handleChatMessage:
	// call CheckDailyBudget(1) to detect if the budget is already exhausted
	// before running the agent (avoids wasting an API call).
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	bot := NewBot("testtoken")
	bot.SetDailyTokenBudget(10_000)

	// Pre-flight check with 1 token — should succeed (no usage yet).
	if err := bot.CheckDailyBudget(1); err != nil {
		t.Fatalf("pre-flight CheckDailyBudget(1) should succeed: %v", err)
	}

	// Simulate an agent run that used 9,999 tokens.
	if err := bot.CheckDailyBudget(9999); err != nil {
		t.Fatalf("CheckDailyBudget(9999) under 10000 limit: %v", err)
	}

	// Next pre-flight check with 1 token — should fail (budget exhausted).
	if err := bot.CheckDailyBudget(1); err == nil {
		t.Fatal("pre-flight CheckDailyBudget(1) should fail when budget is exhausted")
	}
}

func TestBot_CheckDailyBudget_SequentialBillings(t *testing.T) {
	// Simulates the production pattern: multiple agent runs, each billing
	// actual input+output tokens against the daily budget.
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	bot := NewBot("testtoken")
	bot.SetDailyTokenBudget(50_000)

	// Agent run 1: used 12,000 tokens
	if err := bot.CheckDailyBudget(12000); err != nil {
		t.Fatalf("run 1: CheckDailyBudget(12000): %v", err)
	}

	// Agent run 2: used 8,000 tokens (running total: 20,000)
	if err := bot.CheckDailyBudget(8000); err != nil {
		t.Fatalf("run 2: CheckDailyBudget(8000): %v", err)
	}

	// Agent run 3: used 30,000 tokens (running total: 50,000 — exactly at limit)
	if err := bot.CheckDailyBudget(30000); err != nil {
		t.Fatalf("run 3: CheckDailyBudget(30000) at exact limit: %v", err)
	}

	// Agent run 4: used 1 token (running total: 50,001 — exceeds limit)
	if err := bot.CheckDailyBudget(1); err == nil {
		t.Fatal("run 4: expected error for exceeded budget")
	}
}

// ---------------------------------------------------------------------------
// DailyTokenUsage
// ---------------------------------------------------------------------------

// TestBot_DailyTokenUsage verifies the read-only usage query.
func TestBot_DailyTokenUsage(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	bot := NewBot("testtoken")
	bot.SetDailyTokenBudget(100_000)

	// Before any usage — should be 0.
	used, limit := bot.DailyTokenUsage()
	if used != 0 {
		t.Errorf("initial usage = %d, want 0", used)
	}
	if limit != 100_000 {
		t.Errorf("limit = %d, want 100000", limit)
	}

	// Bill some tokens.
	bot.CheckDailyBudget(25000)
	used, limit = bot.DailyTokenUsage()
	if used != 25000 {
		t.Errorf("usage after 25k = %d, want 25000", used)
	}

	// Bill more.
	bot.CheckDailyBudget(15000)
	used, limit = bot.DailyTokenUsage()
	if used != 40000 {
		t.Errorf("usage after 40k = %d, want 40000", used)
	}

	// Zero budget — returns (0,0).
	bot2 := NewBot("testtoken")
	used, limit = bot2.DailyTokenUsage()
	if used != 0 || limit != 0 {
		t.Errorf("unconfigured usage = (%d, %d), want (0,0)", used, limit)
	}
}

// ---------------------------------------------------------------------------
// SendChatAction
// ---------------------------------------------------------------------------

func TestBot_SendChatAction(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimSuffix(r.URL.Path, "/")
		if path != "/sendChatAction" {
			t.Errorf("unexpected path: %s", path)
		}
		requireJSONBody(t, r, "application/json", &gotBody)
		okResponse(w, true)
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	err := bot.SendChatAction(123, "typing")
	if err != nil {
		t.Fatalf("SendChatAction: %v", err)
	}

	if gotBody["chat_id"] != float64(123) {
		t.Errorf("chat_id = %v, want 123", gotBody["chat_id"])
	}
	if gotBody["action"] != "typing" {
		t.Errorf("action = %v, want %q", gotBody["action"], "typing")
	}
}

// ---------------------------------------------------------------------------
// SendVoice — no caption
// ---------------------------------------------------------------------------

func TestBot_SendVoice_NoCaption(t *testing.T) {
	tmpDir := t.TempDir()
	voicePath := filepath.Join(tmpDir, "test_voice.ogg")
	if err := os.WriteFile(voicePath, []byte("fake-ogg-data"), 0o644); err != nil {
		t.Fatalf("write temp voice file: %v", err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimSuffix(r.URL.Path, "/")
		if path != "/sendVoice" {
			t.Errorf("unexpected path: %s", path)
		}
		requireMultipartBody(t, r, "voice", "test_voice.ogg", map[string]any{
			"chat_id": float64(789),
		})
		okResponse(w, map[string]any{
			"message_id": 20,
			"chat":       map[string]any{"id": 789},
		})
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	msg, err := bot.SendVoice(789, voicePath, "", nil)
	if err != nil {
		t.Fatalf("SendVoice(no caption): %v", err)
	}
	if msg == nil {
		t.Fatal("SendVoice returned nil message")
	}
	if msg.ID != 20 {
		t.Errorf("msg.ID = %d, want 20", msg.ID)
	}
}

// ---------------------------------------------------------------------------
// DownloadFile — non-ok status and server error
// ---------------------------------------------------------------------------

func TestBot_DownloadFile_NonOKStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("Forbidden"))
	}))
	defer ts.Close()

	bot := NewBot("testtoken")
	bot.Client = &http.Client{
		Transport: &apiRewriter{
			inner:         http.DefaultTransport,
			testServerURL: ts.URL,
		},
	}

	_, err := bot.DownloadFile("secret/file.txt")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "status 403") {
		t.Errorf("error = %q, want substring %q", err, "status 403")
	}
}

func TestBot_DownloadFile_Error(t *testing.T) {
	// Point to a closed server to trigger a connection error.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	ts.Close()

	bot := NewBot("testtoken")
	bot.Client = &http.Client{
		Transport: &apiRewriter{
			inner:         http.DefaultTransport,
			testServerURL: ts.URL,
		},
	}

	_, err := bot.DownloadFile("some/file.txt")
	if err == nil {
		t.Fatal("expected error for closed server, got nil")
	}
	if !strings.Contains(err.Error(), "download file") {
		t.Errorf("error = %q, want substring %q", err, "download file")
	}
}

// ---------------------------------------------------------------------------
// Retry (doJSON with exponential backoff)
// ---------------------------------------------------------------------------

func TestBot_DoJSON_RetryOn429(t *testing.T) {
	attempts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			json.NewEncoder(w).Encode(map[string]any{
				"ok":          false,
				"error_code":  429,
				"description": "Too Many Requests",
			})
			return
		}
		okResponse(w, map[string]any{
			"message_id": 99,
			"text":       "retry works",
			"chat":       map[string]any{"id": 1, "type": "private"},
			"date":       2000,
		})
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	var msg Message
	err := bot.doJSON("sendMessage", map[string]any{
		"chat_id": 1,
		"text":    "hello",
	}, &msg)
	if err != nil {
		t.Fatalf("doJSON with retry: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
	if msg.ID != 99 {
		t.Errorf("msg.ID = %d, want 99", msg.ID)
	}
}

func TestBot_DoJSON_RetryOn5xx(t *testing.T) {
	attempts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(502)
			json.NewEncoder(w).Encode(map[string]any{
				"ok":          false,
				"error_code":  502,
				"description": "Bad Gateway",
			})
			return
		}
		okResponse(w, map[string]any{"text": "ok"})
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	err := bot.doJSON("sendMessage", map[string]any{"chat_id": 1, "text": "hi"}, nil)
	if err != nil {
		t.Fatalf("doJSON with 5xx retry: %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

func TestBot_DoJSON_NoRetryOn4xx(t *testing.T) {
	attempts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]any{
			"ok":          false,
			"error_code":  400,
			"description": "Bad Request",
		})
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	err := bot.doJSON("sendMessage", map[string]any{"chat_id": 1, "text": "hi"}, nil)
	if err == nil {
		t.Fatal("expected error for 400, got nil")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (no retry on 4xx)", attempts)
	}
}

func TestBot_DoJSON_RetryExhausted(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		json.NewEncoder(w).Encode(map[string]any{
			"ok":          false,
			"error_code":  503,
			"description": "Service Unavailable",
		})
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	err := bot.doJSON("sendMessage", map[string]any{"chat_id": 1, "text": "hi"}, nil)
	if err == nil {
		t.Fatal("expected error after retries exhausted, got nil")
	}
	if !strings.Contains(err.Error(), "Service Unavailable") {
		t.Errorf("error = %q, want substring %q", err, "Service Unavailable")
	}
}

// ---------------------------------------------------------------------------
// Retry (doUpload with exponential backoff)
// ---------------------------------------------------------------------------

func TestBot_DoUpload_RetryOn429(t *testing.T) {
	tmpDir := t.TempDir()
	photoPath := filepath.Join(tmpDir, "test.jpg")
	if err := os.WriteFile(photoPath, []byte("fake-jpeg"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	attempts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(429)
			json.NewEncoder(w).Encode(map[string]any{
				"ok":          false,
				"error_code":  429,
				"description": "Too Many Requests",
			})
			return
		}
		okResponse(w, map[string]any{
			"message_id": 30,
			"chat":       map[string]any{"id": 1},
		})
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	var msg Message
	err := bot.doUpload("sendPhoto", "photo", photoPath, map[string]any{"chat_id": float64(1)}, &msg)
	if err != nil {
		t.Fatalf("doUpload with retry: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
	if msg.ID != 30 {
		t.Errorf("msg.ID = %d, want 30", msg.ID)
	}
}

func TestBot_DoUpload_RetryOn5xx(t *testing.T) {
	tmpDir := t.TempDir()
	docPath := filepath.Join(tmpDir, "test.pdf")
	if err := os.WriteFile(docPath, []byte("pdf-data"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	attempts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(503)
			json.NewEncoder(w).Encode(map[string]any{
				"ok":          false,
				"error_code":  503,
				"description": "Service Unavailable",
			})
			return
		}
		okResponse(w, map[string]any{"message_id": 31})
	}))
	defer ts.Close()

	bot := NewBot("x")
	bot.BaseURL = ts.URL

	err := bot.doUpload("sendDocument", "document", docPath, map[string]any{"chat_id": float64(1)}, nil)
	if err != nil {
		t.Fatalf("doUpload with 5xx retry: %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

// ---------------------------------------------------------------------------
// IsFatalAPIError
// ---------------------------------------------------------------------------

func TestIsFatalAPIError(t *testing.T) {
	tests := []struct {
		err   error
		fatal bool
	}{
		{&TelegramError{Method: "getUpdates", Description: "Unauthorized", Code: 401}, true},
		{&TelegramError{Method: "getUpdates", Description: "Forbidden: bot was blocked by the user", Code: 403}, true},
		{&TelegramError{Method: "getUpdates", Description: "Conflict: terminated by other getUpdates request", Code: 409}, true},
		{&TelegramError{Method: "sendMessage", Description: "Too Many Requests", Code: 429}, false},
		{&TelegramError{Method: "getUpdates", Description: "Bad Gateway", Code: 502}, false},
		{fmt.Errorf("network error"), false},
		{nil, false},
	}

	for _, tt := range tests {
		got := IsFatalAPIError(tt.err)
		if got != tt.fatal {
			t.Errorf("IsFatalAPIError(%v) = %v, want %v", tt.err, got, tt.fatal)
		}
	}
}
