package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/BackendStack21/odek/internal/telegram"
)

// previewBot returns a bot built the way production builds it (from the
// operator config) and the recorded bodies of every text request. The first
// MarkdownV2 send is rejected so the plain-text fallback runs too.
func previewBot(t *testing.T, linkPreview bool) (*telegram.Bot, func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var bodies []map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		bodies = append(bodies, body)
		first := len(bodies) == 1
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if first && body["parse_mode"] == telegram.ParseModeMarkdownV2 {
			_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"chat":{"id":1}}}`))
	}))
	t.Cleanup(ts.Close)
	cfg := telegram.DefaultConfig()
	cfg.Token = "t:k"
	cfg.LinkPreview = linkPreview
	bot := telegram.NewBotFromConfig(cfg)
	bot.BaseURL = ts.URL
	return bot, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), bodies...)
	}
}

// Scheduled-result delivery (including its plain-text retry) and plain
// notices carry the link-preview policy of the configured bot.
func TestTelegramScheduleAndNoticeLinkPreview(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		bot, bodies := previewBot(t, enabled)
		if err := sendTelegramResult(context.Background(), bot, 7, "**see** https://evil.example/?s=secret"); err != nil {
			t.Fatalf("sendTelegramResult: %v", err)
		}
		if _, err := bot.SendMessageContext(context.Background(), 7, "📋 job done https://evil.example/", nil); err != nil {
			t.Fatalf("notice: %v", err)
		}
		got := bodies()
		if len(got) < 3 {
			t.Fatalf("enabled=%v: got %d requests, want >= 3", enabled, len(got))
		}
		for i, b := range got {
			lpo, has := b["link_preview_options"].(map[string]any)
			text, _ := b["text"].(string)
			if !enabled && (!has || lpo["is_disabled"] != true) {
				t.Errorf("previews disabled: request %d (%.30q) lacks link_preview_options.is_disabled", i, text)
			}
			if enabled && has {
				t.Errorf("previews enabled: request %d (%.30q) carries link_preview_options", i, text)
			}
			if strings.TrimSpace(text) == "" {
				t.Errorf("request %d has empty text", i)
			}
		}
	}
}
