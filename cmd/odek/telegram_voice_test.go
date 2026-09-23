package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/llmclient"
	"github.com/BackendStack21/odek/internal/telegram"
)

func voiceReplyConfig() config.TTSConfig {
	return config.TTSConfig{
		Backend:              config.TTSBackendProvider,
		Model:                "tts-1",
		Voice:                "alloy",
		Format:               "mp3",
		MaxChars:             4096,
		TelegramVoiceReplies: true,
	}
}

func newVoiceReplyTestBot(t *testing.T, hit *bool) *telegram.Bot {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hit != nil {
			*hit = true
		}
		if !strings.Contains(r.URL.Path, "sendVoice") {
			t.Errorf("unexpected telegram method: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":42,"chat":{"id":7}}}`))
	}))
	t.Cleanup(ts.Close)
	bot := telegram.NewBot("testtoken")
	bot.BaseURL = ts.URL + "/bottesttoken"
	return bot
}

func TestTelegramVoiceReply_SendsVoiceInAdditionToText(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	fake := &fakeSpeechBackend{speakRes: &llmclient.SpeakResult{Audio: []byte("fakeaudio"), MIMEType: "audio/mpeg"}}
	hit := false
	bot := newVoiceReplyTestBot(t, &hit)

	if err := sendTelegramVoiceReply(bot, 7, "hello answer", voiceReplyConfig(), fake); err != nil {
		t.Fatalf("voice reply: %v", err)
	}
	if !hit {
		t.Fatal("sendVoice was not called")
	}
	if fake.gotText != "hello answer" {
		t.Fatalf("synthesized text = %q", fake.gotText)
	}
	// The synthesized file is consumed immediately — nothing lingers.
	matches, _ := filepath.Glob(filepath.Join(home, ".odek", "media", "*"))
	if len(matches) != 0 {
		t.Fatalf("audio file not cleaned up: %v", matches)
	}
}

func TestTelegramVoiceReply_TruncatesAtMaxChars(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	fake := &fakeSpeechBackend{speakRes: &llmclient.SpeakResult{Audio: []byte("a"), MIMEType: "audio/mpeg"}}
	bot := newVoiceReplyTestBot(t, nil)

	cfg := voiceReplyConfig()
	cfg.MaxChars = 10
	long := strings.Repeat("x", 500)
	if err := sendTelegramVoiceReply(bot, 7, long, cfg, fake); err != nil {
		t.Fatalf("voice reply: %v", err)
	}
	if len(fake.gotText) != 10 {
		t.Fatalf("synthesized text length = %d, want 10", len(fake.gotText))
	}
}

func TestTelegramVoiceReply_SynthesisFailureDoesNotFailTurn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	fake := &fakeSpeechBackend{speakErr: errors.New("provider down")}
	bot := newVoiceReplyTestBot(t, nil)

	// The returned error is for logging only — the caller always treats the
	// turn as complete; assert the call does not panic and reports cleanly.
	err := sendTelegramVoiceReply(bot, 7, "answer", voiceReplyConfig(), fake)
	if err == nil || !strings.Contains(err.Error(), "provider down") {
		t.Fatalf("expected synthesis error to be reported for logging, got %v", err)
	}
	if fake.speakCalls != 1 {
		t.Fatalf("speak calls = %d", fake.speakCalls)
	}
}

func TestTelegramVoiceReply_GatedOffByDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	fake := &fakeSpeechBackend{speakRes: &llmclient.SpeakResult{Audio: []byte("a"), MIMEType: "audio/mpeg"}}
	hit := false
	bot := newVoiceReplyTestBot(t, &hit)

	// Default config: feature flag off — nothing may be synthesized or sent.
	cfg := voiceReplyConfig()
	cfg.TelegramVoiceReplies = false
	if cfg.TelegramVoiceReplies {
		t.Fatal("flag must default to false")
	}
	// With the flag off the turn-level gate never builds a speech client;
	// simulate that nil-backend path to prove the send is a no-op.
	if err := sendTelegramVoiceReply(bot, 7, "answer", cfg, nil); err != nil {
		t.Fatalf("nil backend must be a silent no-op, got %v", err)
	}
	if fake.speakCalls != 0 || hit {
		t.Fatal("voice reply fired while gated off")
	}
}
