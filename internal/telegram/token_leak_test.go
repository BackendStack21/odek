package telegram

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The bot token is embedded in API URLs; transport errors wrap the URL and
// must not carry it into logs or error strings.
func TestRED_TransportErrorLeaksBotToken(t *testing.T) {
	const tok = "123456789:AAEhBP0av28FoU1hGqZ1xk5VxgjZ0aBcDeF"
	b := NewBot(tok)
	b.BaseURL = "http://127.0.0.1:1/bot" + tok
	b.FileBaseURL = "http://127.0.0.1:1/file/bot" + tok
	b.Client = &http.Client{Timeout: 2 * time.Second}
	if _, err := b.DownloadFile("photos/x.jpg"); err == nil || strings.Contains(err.Error(), tok) {
		if err == nil {
			t.Skip("unexpected success")
		}
		t.Fatalf("DownloadFile error exposes the bot token: %v", err)
	}
}

// Every request path scrubs the token from its transport error.
func TestTransportErrorsScrubToken(t *testing.T) {
	const tok = "123456789:AAEhBP0av28FoU1hGqZ1xk5VxgjZ0aBcDeF"
	b := NewBot(tok)
	b.BaseURL = "http://127.0.0.1:1/bot" + tok
	b.FileBaseURL = "http://127.0.0.1:1/file/bot" + tok
	b.Client = &http.Client{Timeout: 2 * time.Second}

	file := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	errs := map[string]error{
		"doJSON":  b.doJSON("getMe", map[string]any{}, nil),
		"upload":  b.doUpload("sendDocument", "document", file, nil, nil),
		"getFile": func() error { _, e := b.GetFile("id"); return e }(),
	}
	for name, err := range errs {
		if err == nil {
			t.Errorf("%s: expected an error", name)
			continue
		}
		if strings.Contains(err.Error(), tok) {
			t.Errorf("%s: error exposes the bot token: %v", name, err)
		}
	}
}

func TestScrubErr(t *testing.T) {
	const tok = "123456789:AAEhBP0av28FoU1hGqZ1xk5VxgjZ0aBcDeF"
	b := NewBot(tok)
	if b.scrubErr(nil) != nil {
		t.Fatal("nil must stay nil")
	}
	ue := &url.Error{Op: "Get", URL: "http://h/bot" + tok + "/x", Err: errors.New("refused")}
	got := b.scrubErr(ue)
	if strings.Contains(got.Error(), tok) {
		t.Fatalf("url.Error not scrubbed: %v", got)
	}
	var back *url.Error
	if !errors.As(got, &back) {
		t.Fatal("scrubbed error must remain a *url.Error")
	}
	plain := b.scrubErr(errors.New("dial " + tok))
	if strings.Contains(plain.Error(), tok) {
		t.Fatalf("plain error not scrubbed: %v", plain)
	}
	other := errors.New("unrelated")
	if b.scrubErr(other) != other {
		t.Fatal("unrelated errors must pass through unchanged")
	}
}
