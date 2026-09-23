package main

// Local-only e2e tests for the speech path (Speak + Transcribe) against real
// providers using credentials from the operator's environment. These tests
// hit paid endpoints and are NEVER run in CI: they execute only when
// ODEK_E2E=true is set and the corresponding <PROVIDER>_API_KEY is present.
// Run locally with:
//
//	ODEK_E2E=true go test -v -run 'TestE2E_Speak|TestE2E_Transcribe' ./cmd/odek/
//
// Key discipline: the key is read from the environment only, never printed
// or logged; tests skip (not fail) when it is absent.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/llmclient"
)

const ttsModel = "gpt-4o-mini-tts"

func newOpenAISpeechClient(t *testing.T) *llmclient.Client {
	t.Helper()
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		t.Skip("OPENAI_API_KEY not set — skipping OpenAI speech e2e test")
	}
	c, err := llmclient.Dial("openai", ttsModel, key, "")
	if err != nil {
		t.Fatalf("Dial openai: %v", err)
	}
	return c
}

func TestE2E_Speak_OpenAI(t *testing.T) {
	if os.Getenv("ODEK_E2E") != "true" {
		t.Skip("ODEK_E2E not set — skipping local-only speech e2e test")
	}
	c := newOpenAISpeechClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := c.Speak(ctx, ttsModel, llmclient.SpeakRequest{
		Text:  "Hello from odek",
		Voice: "alloy",
	})
	if err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if len(res.Audio) == 0 {
		t.Fatal("Speak returned empty audio")
	}
	if res.MIMEType == "" || !strings.HasPrefix(res.MIMEType, "audio/") {
		t.Errorf("unexpected MIME type: %q", res.MIMEType)
	}
	if res.Model != ttsModel {
		t.Errorf("model echo: got %q, want %q", res.Model, ttsModel)
	}

	// Audio must persist as a non-empty file.
	path := filepath.Join(t.TempDir(), "hello.mp3")
	if err := os.WriteFile(path, res.Audio, 0o600); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat audio: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("audio file is empty")
	}
	t.Logf("speak: %d bytes audio, mime=%s, file=%s", len(res.Audio), res.MIMEType, path)
}

func TestE2E_Transcribe_OpenAI(t *testing.T) {
	if os.Getenv("ODEK_E2E") != "true" {
		t.Skip("ODEK_E2E not set — skipping local-only speech e2e test")
	}
	c := newOpenAISpeechClient(t)
	key := os.Getenv("OPENAI_API_KEY") // already validated by the helper below

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	speak, err := c.Speak(ctx, ttsModel, llmclient.SpeakRequest{
		Text:  "Hello from odek",
		Voice: "alloy",
	})
	if err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if len(speak.Audio) == 0 {
		t.Fatal("Speak returned empty audio")
	}

	// Round-trip through the same provider's ASR.
	tc, err := llmclient.Dial("openai", "whisper-1", key, "")
	if err != nil {
		t.Fatalf("Dial openai (transcribe): %v", err)
	}
	res, err := tc.Transcribe(ctx, "whisper-1", llmclient.TranscribeRequest{
		Audio:    speak.Audio,
		Filename: "hello.mp3",
		MIMEType: speak.MIMEType,
	})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if strings.TrimSpace(res.Text) == "" {
		t.Fatal("Transcribe returned empty text")
	}
	// ASR is fuzzy — assert loosely on a recognizable fragment.
	lower := strings.ToLower(res.Text)
	if !strings.Contains(lower, "odek") && !strings.Contains(lower, "hello") {
		t.Errorf("transcript %q contains neither 'odek' nor 'hello'", res.Text)
	}
	t.Logf("transcribe: %q (model=%s lang=%s)", res.Text, res.Model, res.Language)
}
