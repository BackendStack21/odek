package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/llmclient"
)

// fakeSpeechBackend records calls so tests can assert what crossed the
// backend boundary without any network access.
type fakeSpeechBackend struct {
	speakRes   *llmclient.SpeakResult
	speakErr   error
	transRes   *llmclient.TranscribeResult
	transErr   error
	gotText    string
	gotAudio   []byte
	gotLang    string
	gotName    string
	speakCalls int
	transCalls int
}

func (f *fakeSpeechBackend) SpeakAudio(ctx context.Context, text string) (*llmclient.SpeakResult, error) {
	f.speakCalls++
	f.gotText = text
	if f.speakErr != nil {
		return nil, f.speakErr
	}
	return f.speakRes, nil
}

func (f *fakeSpeechBackend) TranscribeAudio(ctx context.Context, filename string, audio []byte, language string) (*llmclient.TranscribeResult, error) {
	f.transCalls++
	f.gotName, f.gotAudio, f.gotLang = filename, audio, language
	if f.transErr != nil {
		return nil, f.transErr
	}
	return f.transRes, nil
}

// ── speak tool ───────────────────────────────────────────────────────────

func TestSpeakTool_Success(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	fake := &fakeSpeechBackend{speakRes: &llmclient.SpeakResult{
		Audio: []byte("fakeaudio"), Model: "tts-1", MIMEType: "audio/mpeg",
	}}
	tool := newSpeakTool(danger.DangerousConfig{}, config.TTSConfig{Backend: config.TTSBackendProvider, MaxChars: 100}, fake)

	raw, _ := tool.Call(`{"text":"hello world"}`)
	var res speakResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected error in result: %s", res.Error)
	}
	if fake.speakCalls != 1 || fake.gotText != "hello world" {
		t.Fatalf("backend not called correctly: calls=%d text=%q", fake.speakCalls, fake.gotText)
	}
	if _, err := os.Stat(res.Path); err != nil {
		t.Fatalf("audio file missing: %v", err)
	}
	if !strings.HasPrefix(filepath.Base(res.Path), "speak-") {
		t.Fatalf("expected auto-generated speak- name, got %q", res.Path)
	}
	if !strings.HasSuffix(res.Path, ".mp3") {
		t.Fatalf("expected mp3 extension from audio/mpeg, got %q", res.Path)
	}
	if res.Model != "tts-1" || res.MIME != "audio/mpeg" || res.Bytes != len("fakeaudio") {
		t.Fatalf("unexpected metadata: %+v", res)
	}
}

func TestSpeakTool_ErrorPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	tool := newSpeakTool(danger.DangerousConfig{}, config.TTSConfig{Backend: config.TTSBackendProvider}, &fakeSpeechBackend{})

	// Missing/empty text is an argument error.
	if _, err := tool.Call(`{}`); err == nil {
		t.Fatal("expected error for missing text")
	}
	if _, err := tool.Call(`{"text":"   "}`); err == nil {
		t.Fatal("expected error for blank text")
	}
	// Malformed JSON.
	if _, err := tool.Call(`not-json`); err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	// Path arguments are rejected.
	if _, err := tool.Call(`{"text":"hi","path":"../escape.mp3"}`); err == nil {
		t.Fatal("expected error for path traversal")
	}

	// Backend failure surfaces as an in-band error, not a Go error.
	failing := newSpeakTool(danger.DangerousConfig{}, config.TTSConfig{}, &fakeSpeechBackend{speakErr: errors.New("boom")})
	raw, _ := failing.Call(`{"text":"hi"}`)
	var res speakResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil || res.Error == "" {
		t.Fatalf("expected in-band error, got raw=%q err=%v", raw, err)
	}

	// Provider returning zero bytes is an error.
	empty := newSpeakTool(danger.DangerousConfig{}, config.TTSConfig{}, &fakeSpeechBackend{speakRes: &llmclient.SpeakResult{}})
	raw, _ = empty.Call(`{"text":"hi"}`)
	if err := json.Unmarshal([]byte(raw), &res); err != nil || res.Error == "" {
		t.Fatalf("expected empty-audio error, got raw=%q err=%v", raw, err)
	}
}

func TestSpeakTool_MaxChars(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := &fakeSpeechBackend{speakRes: &llmclient.SpeakResult{Audio: []byte("x")}}
	tool := newSpeakTool(danger.DangerousConfig{}, config.TTSConfig{Backend: config.TTSBackendProvider, MaxChars: 10}, fake)

	raw, _ := tool.Call(`{"text":"this text is way too long for the cap"}`)
	var res speakResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil || res.Error == "" {
		t.Fatalf("expected max-chars error, got raw=%q err=%v", raw, err)
	}
	if fake.speakCalls != 0 {
		t.Fatalf("backend must not be called when the cap trips, calls=%d", fake.speakCalls)
	}
	// Exactly at the cap is accepted.
	fake.speakRes = &llmclient.SpeakResult{Audio: []byte("x"), MIMEType: "audio/wav"}
	if _, err := tool.Call(`{"text":"0123456789"}`); err != nil {
		t.Fatalf("at-cap text rejected: %v", err)
	}
	// Zero config falls back to the documented default.
	def := newSpeakTool(danger.DangerousConfig{}, config.TTSConfig{}, fake)
	long := strings.Repeat("a", config.DefaultTTSMaxChars+1)
	raw, _ = def.Call(`{"text":"` + long + `"}`)
	if err := json.Unmarshal([]byte(raw), &res); err != nil || res.Error == "" {
		t.Fatal("expected default max-chars to apply")
	}
}

func TestSpeakTool_ConfigGating(t *testing.T) {
	// Unconfigured config ⇒ speak absent from the registry.
	base := builtinTools(danger.DangerousConfig{}, nil, nil, 4, "", toolConfig{}, nil)
	for _, tl := range base {
		if tl.Name() == "speak" {
			t.Fatal("speak registered without tts config")
		}
	}

	withTTS := builtinTools(danger.DangerousConfig{}, nil, nil, 4, "", toolConfig{
		TTS: config.TTSConfig{Backend: config.TTSBackendProvider, Provider: "openai", Model: "tts-1", Voice: "alloy"},
	}, nil)
	found := false
	for _, tl := range withTTS {
		if tl.Name() == "speak" {
			found = true
		}
	}
	if !found {
		t.Fatal("speak not registered with tts.backend=provider")
	}
}

// ── transcribe provider dispatch ─────────────────────────────────────────

func writeTempAudio(t *testing.T, size int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "audio.mp3")
	if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	return p
}

func TestTranscribeProviderDispatch(t *testing.T) {
	fake := &fakeSpeechBackend{transRes: &llmclient.TranscribeResult{
		Text: "hello there", Model: "whisper-1", Language: "en", DurationSec: 1.5,
	}}
	tool := newTranscribeTool(danger.DangerousConfig{}, config.TranscriptionConfig{})
	tool.SetSpeechBackend(config.STTConfig{Backend: config.STTBackendProvider, MaxAudioMB: 1}, fake)

	audio := writeTempAudio(t, 32)
	raw, _ := tool.Call(`{"path":"` + audio + `","language":"de"}`)
	var res transcribeResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if fake.transCalls != 1 || fake.gotLang != "de" || len(fake.gotAudio) != 32 {
		t.Fatalf("backend got wrong call: %+v", fake)
	}
	if fake.gotName != "audio.mp3" {
		t.Fatalf("expected basename filename, got %q", fake.gotName)
	}
	if res.Text == "" || !strings.Contains(res.Text, "hello there") {
		t.Fatalf("expected wrapped transcription text, got %q", res.Text)
	}
	if res.Model != "whisper-1" || res.Language != "en" || res.Duration != 1.5 {
		t.Fatalf("unexpected result fields: %+v", res)
	}
}

func TestTranscribeProviderSizeCap(t *testing.T) {
	fake := &fakeSpeechBackend{}
	tool := newTranscribeTool(danger.DangerousConfig{}, config.TranscriptionConfig{})
	tool.SetSpeechBackend(config.STTConfig{Backend: config.STTBackendProvider, MaxAudioMB: 1}, fake)

	audio := writeTempAudio(t, 1<<20+1)
	raw, _ := tool.Call(`{"path":"` + audio + `"}`)
	var res transcribeResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil || res.Error == "" {
		t.Fatalf("expected size-cap error, got raw=%q err=%v", raw, err)
	}
	if fake.transCalls != 0 {
		t.Fatalf("backend must not be called for oversized audio, calls=%d", fake.transCalls)
	}

	// Backend failure is in-band.
	failing := &fakeSpeechBackend{transErr: errors.New("provider down")}
	tool2 := newTranscribeTool(danger.DangerousConfig{}, config.TranscriptionConfig{})
	tool2.SetSpeechBackend(config.STTConfig{Backend: config.STTBackendProvider, MaxAudioMB: 1}, failing)
	small := writeTempAudio(t, 8)
	raw, _ = tool2.Call(`{"path":"` + small + `"}`)
	if err := json.Unmarshal([]byte(raw), &res); err != nil || res.Error == "" {
		t.Fatalf("expected in-band provider error, got raw=%q", raw)
	}
}

func TestTranscribeLocalModeUnchanged(t *testing.T) {
	// A tool with no speech backend keeps the local whisper path: the
	// provider branch must not trip and size limits stay the local ones.
	fake := &fakeSpeechBackend{}
	tool := newTranscribeTool(danger.DangerousConfig{}, config.TranscriptionConfig{})

	// Missing file reports the local-path open error (result carries it).
	missing := filepath.Join(t.TempDir(), "nope.mp3")
	raw, _ := tool.Call(`{"path":"` + missing + `"}`)
	var res transcribeResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil || res.Error == "" {
		t.Fatalf("expected in-band open error, got raw=%q", raw)
	}
	if fake.transCalls != 0 {
		t.Fatal("provider backend must be untouched in local mode")
	}

	// The 10 MiB local cap still applies when no backend is set.
	big := writeTempAudio(t, maxFileReadBytes+1)
	raw, _ = tool.Call(`{"path":"` + big + `"}`)
	if err := json.Unmarshal([]byte(raw), &res); err != nil || res.Error == "" {
		t.Fatalf("expected local size-cap error, got raw=%q", raw)
	}
}

func TestNewSpeechClientFromResolved(t *testing.T) {
	// Provider-mode backends populate the client; unset sections leave the
	// corresponding side disabled; nothing configured returns nil.
	c := newSpeechClient(
		config.TTSConfig{Backend: config.TTSBackendProvider, Provider: "openai", Model: "tts-1", Voice: "alloy", Format: "mp3", Speed: 1.2},
		config.STTConfig{Backend: config.STTBackendProvider, Provider: "openai", Model: "whisper-1"},
		llmclient.Options{},
	)
	if c == nil || c.ttsProvider != "openai" || c.sttModel != "whisper-1" || c.voice != "alloy" || c.format != "mp3" || c.speed != 1.2 {
		t.Fatalf("unexpected client: %+v", c)
	}

	c = newSpeechClient(config.TTSConfig{}, config.STTConfig{Backend: config.STTBackendLocal, MaxAudioMB: config.DefaultSTTMaxAudioMB}, llmclient.Options{})
	if c != nil {
		t.Fatal("expected nil client with no provider backends")
	}
}
