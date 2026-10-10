package telegram

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
)

// linkPreviewServer records every text send/edit body. When failMarkdown is
// set, MarkdownV2 sends are rejected so the plain-text fallback runs; when
// failAll is set every text send except the lost-chunk notice is rejected.
type linkPreviewServer struct {
	mu           sync.Mutex
	bodies       []map[string]any
	paths        []string
	failMarkdown bool
	failAll      bool
}

func (s *linkPreviewServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		isText := strings.HasSuffix(r.URL.Path, "/sendMessage") || strings.HasSuffix(r.URL.Path, "/editMessageText")
		s.mu.Lock()
		if isText {
			s.bodies = append(s.bodies, body)
			s.paths = append(s.paths, r.URL.Path)
		}
		failAll, failMarkdown := s.failAll, s.failMarkdown
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		text, _ := body["text"].(string)
		reject := isText && (failAll || (failMarkdown && body["parse_mode"] == ParseModeMarkdownV2))
		if reject && !strings.Contains(text, "part of response lost") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":9,"text":"x"}}`))
	}))
	t.Cleanup(ts.Close)
	return ts
}

func (s *linkPreviewServer) set(failMarkdown, failAll bool) {
	s.mu.Lock()
	s.failMarkdown, s.failAll = failMarkdown, failAll
	s.mu.Unlock()
}

func (s *linkPreviewServer) snapshot() ([]string, []map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.paths...), append([]map[string]any(nil), s.bodies...)
}

// driveEveryTextPath exercises each outbound text path the bot package owns:
// direct sends and edits, multi-chunk replies, the plain-text fallback, the
// lost-chunk notice, and approval prompts with their decision edit.
func driveEveryTextPath(t *testing.T, linkPreview bool) ([]string, []map[string]any) {
	t.Helper()
	srv := &linkPreviewServer{}
	ts := srv.start(t)
	bot := testBot(t, ts)
	bot.LinkPreview = linkPreview

	if _, err := bot.SendMessage(1, "see https://evil.example/?s=secret", nil); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if _, err := bot.SendMessage(1, "with opts", &SendOpts{ParseMode: ParseModeMarkdownV2}); err != nil {
		t.Fatalf("SendMessage opts: %v", err)
	}
	if err := bot.EditMessageText(1, 9, "edited https://evil.example/", nil); err != nil {
		t.Fatalf("EditMessageText: %v", err)
	}
	if err := bot.EditMessageText(1, 9, "edited md", &SendOpts{ParseMode: ParseModeMarkdownV2}); err != nil {
		t.Fatalf("EditMessageText opts: %v", err)
	}

	h := NewHandler(bot)
	h.SendResponse(1, strings.Repeat("word https://evil.example/ ", 400), 5) // several chunks

	srv.set(true, false)
	h.SendResponse(1, "fallback https://evil.example/", 0) // plain-text retry
	srv.set(false, true)
	h.SendResponse(1, "lost https://evil.example/", 0) // lost-chunk notice
	srv.set(false, false)

	a := NewTelegramApprover(bot, 1, 7)
	_, before := srv.snapshot()
	done := make(chan error, 1)
	go func() { done <- a.PromptCommand(danger.NetworkEgress, "curl https://evil.example/", "why") }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		// Answer only once the prompt reached the server, so the decision
		// edit is exercised too.
		_, now := srv.snapshot()
		var id string
		if len(now) > len(before) {
			a.mu.Lock()
			for k := range a.pending {
				id = k
			}
			a.mu.Unlock()
		}
		if id != "" {
			a.HandleCallback(cbPrefixDeny+id, 7)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("approval prompt never sent")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := <-done; err == nil {
		t.Fatal("denied approval returned nil")
	}
	return srv.snapshot()
}

func TestRED_LinkPreviewDisabledOnEveryTextSend(t *testing.T) {
	paths, bodies := driveEveryTextPath(t, false)
	if len(bodies) < 10 {
		t.Fatalf("expected at least 10 text requests, got %d", len(bodies))
	}
	var sawEdit bool
	for i, b := range bodies {
		if strings.HasSuffix(paths[i], "/editMessageText") {
			sawEdit = true
		}
		lpo, ok := b["link_preview_options"].(map[string]any)
		if !ok || lpo["is_disabled"] != true {
			t.Errorf("request %d (%s, text %.40q): link_preview_options = %v, want {is_disabled:true}",
				i, paths[i], b["text"], b["link_preview_options"])
		}
	}
	if !sawEdit {
		t.Fatal("no editMessageText request was exercised")
	}
}

func TestLinkPreviewEnabledOmitsOption(t *testing.T) {
	paths, bodies := driveEveryTextPath(t, true)
	if len(bodies) < 10 {
		t.Fatalf("expected at least 10 text requests, got %d", len(bodies))
	}
	for i, b := range bodies {
		if _, ok := b["link_preview_options"]; ok {
			t.Errorf("request %d (%s): link_preview_options present with previews enabled: %v", i, paths[i], b["link_preview_options"])
		}
		if _, ok := b["disable_web_page_preview"]; ok {
			t.Errorf("request %d (%s): disable_web_page_preview present with previews enabled", i, paths[i])
		}
	}
}

// A per-call DisableWebPagePreview still disables previews when the operator
// has enabled them globally.
func TestLinkPreviewPerCallDisableWins(t *testing.T) {
	srv := &linkPreviewServer{}
	ts := srv.start(t)
	bot := testBot(t, ts)
	bot.LinkPreview = true
	if _, err := bot.SendMessage(1, "x", &SendOpts{DisableWebPagePreview: true}); err != nil {
		t.Fatal(err)
	}
	if err := bot.EditMessageText(1, 9, "x", &SendOpts{DisableWebPagePreview: true}); err != nil {
		t.Fatal(err)
	}
	_, bodies := srv.snapshot()
	if len(bodies) != 2 {
		t.Fatalf("got %d requests, want 2", len(bodies))
	}
	for i, b := range bodies {
		lpo, ok := b["link_preview_options"].(map[string]any)
		if !ok || lpo["is_disabled"] != true {
			t.Errorf("request %d: link_preview_options = %v, want disabled", i, b["link_preview_options"])
		}
	}
}

func TestNewBotFromConfigCarriesLinkPreview(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Token = "t:k"
	cfg.MaxDownloadSize = 11
	cfg.MediaQuotaPerChat = 22
	b := NewBotFromConfig(cfg)
	if b.LinkPreview {
		t.Fatal("default config enabled link previews")
	}
	if b.Token != "t:k" || b.MaxDownloadSize != 11 || b.MediaQuotaPerChat != 22 {
		t.Fatalf("config not carried: token=%q max=%d quota=%d", b.Token, b.MaxDownloadSize, b.MediaQuotaPerChat)
	}
	cfg.LinkPreview = true
	if !NewBotFromConfig(cfg).LinkPreview {
		t.Fatal("link_preview=true not carried to the bot")
	}
}

func TestConfigFromEnvLinkPreview(t *testing.T) {
	for _, tc := range []struct {
		env  string
		base bool
		want bool
	}{
		{"true", false, true},
		{"1", false, true},
		{"false", true, false},
		{"garbage", true, false}, // malformed fails closed
		{"", true, true},         // unset keeps the file value
	} {
		t.Setenv("ODEK_TELEGRAM_LINK_PREVIEW", tc.env)
		base := DefaultConfig()
		base.LinkPreview = tc.base
		got, _ := ConfigFromEnv(base)
		if got.LinkPreview != tc.want {
			t.Errorf("env %q base %v: LinkPreview = %v, want %v", tc.env, tc.base, got.LinkPreview, tc.want)
		}
	}
}

func TestRED_LinkPreviewMalformedEnvWarns(t *testing.T) {
	var buf strings.Builder
	old := warnWriter
	warnWriter = &buf
	defer func() { warnWriter = old }()
	t.Setenv("ODEK_TELEGRAM_LINK_PREVIEW", "yes")
	cfg, _ := ConfigFromEnv(TelegramConfig{LinkPreview: true})
	if cfg.LinkPreview {
		t.Fatal("malformed value enabled previews")
	}
	if out := buf.String(); !strings.Contains(out, "ODEK_TELEGRAM_LINK_PREVIEW") || !strings.Contains(out, `"yes"`) {
		t.Fatalf("no warning naming the variable and value: %q", out)
	}
	buf.Reset()
	t.Setenv("ODEK_TELEGRAM_LINK_PREVIEW", "TRUE")
	if cfg, _ := ConfigFromEnv(TelegramConfig{}); !cfg.LinkPreview || buf.Len() != 0 {
		t.Fatalf("valid value: LinkPreview=%v warn=%q", cfg.LinkPreview, buf.String())
	}
}
