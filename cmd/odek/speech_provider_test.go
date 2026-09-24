package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/BackendStack21/go-llm-sdk"

	"github.com/BackendStack21/odek/internal/budget"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/llmclient"
)

// fakeBudgetOwner implements the budget-owner interface speakTimeout probes
// so tests can bound the effective request timeout from the run budget.
type fakeBudgetOwner struct {
	maxRuntime int64
}

func (f *fakeBudgetOwner) BudgetSnapshot() budget.Snapshot { return budget.Snapshot{} }
func (f *fakeBudgetOwner) ReserveInferenceBudget() (budget.Grant, error) {
	return budget.Grant{Limits: budget.Limits{MaxRuntimeSeconds: f.maxRuntime}}, nil
}
func (f *fakeBudgetOwner) SettleExternalBudget(budget.Grant, *budget.Usage) {}

// speechTestClient builds a providerSpeechClient pointed at an
// OpenAI-compatible httptest server, exercising the same construction path
// the production config uses (provider override with a BaseURL).
func speechTestClient(t *testing.T, handler http.HandlerFunc, ttsModel, sttModel string) *providerSpeechClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	opts := llmclient.Options{
		Provider: "openai",
		Providers: map[string]llmclient.ProviderOverride{
			"openai": {APIKey: "test-key", BaseURL: srv.URL, Format: "openai"},
		},
		Timeout: 5 * time.Second,
	}
	return newSpeechClient(
		config.TTSConfig{Backend: config.TTSBackendProvider, Provider: "openai", Model: ttsModel, Voice: "alloy", Format: "mp3", Speed: 1.0},
		config.STTConfig{Backend: config.STTBackendProvider, Provider: "openai", Model: sttModel},
		opts,
	)
}

func TestProviderSpeechClient_SpeakAudio_Success(t *testing.T) {
	var gotPath, gotAuth string
	c := speechTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("synth-audio"))
	}, "tts-1", "whisper-1")

	res, err := c.SpeakAudio(context.Background(), "hello world")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/audio/speech" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if string(res.Audio) != "synth-audio" || res.Model != "tts-1" || res.MIMEType != "audio/mpeg" {
		t.Fatalf("result = %+v", res)
	}
}

func TestProviderSpeechClient_SpeakAudio_ProviderFailure(t *testing.T) {
	c := speechTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream exploded", http.StatusInternalServerError)
	}, "tts-1", "whisper-1")

	_, err := c.SpeakAudio(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error from provider failure")
	}
	if strings.Contains(err.Error(), "upstream exploded") {
		t.Fatalf("transport detail leaked: %v", err)
	}
	if !strings.Contains(err.Error(), "provider request failed") {
		t.Fatalf("expected generic provider failure, got: %v", err)
	}
}

func TestProviderSpeechClient_TranscribeAudio_Success(t *testing.T) {
	var gotPath string
	var gotFilename string
	c := speechTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := r.ParseMultipartForm(10 << 20); err == nil {
			if fhdr := r.MultipartForm.File["file"]; len(fhdr) > 0 {
				gotFilename = fhdr[0].Filename
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"recognized words","model":"whisper-1","language":"en","duration":2.5}`))
	}, "tts-1", "whisper-1")

	res, err := c.TranscribeAudio(context.Background(), "audio.wav", []byte("wav-bytes"), "en")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/audio/transcriptions" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotFilename != "audio.wav" {
		t.Fatalf("filename = %q", gotFilename)
	}
	if res.Text != "recognized words" || res.DurationSec != 2.5 || res.Language != "en" {
		t.Fatalf("result = %+v", res)
	}
}

func TestProviderSpeechClient_TranscribeAudio_ProviderFailure(t *testing.T) {
	c := speechTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream exploded", http.StatusInternalServerError)
	}, "tts-1", "whisper-1")

	_, err := c.TranscribeAudio(context.Background(), "audio.wav", []byte("wav"), "")
	if err == nil || !strings.HasPrefix(err.Error(), "stt: provider request failed") {
		t.Fatalf("expected generic stt failure, got: %v", err)
	}
}

func TestProviderSpeechClient_CanceledContext(t *testing.T) {
	c := speechTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler must not run for a canceled context")
	}, "tts-1", "whisper-1")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.SpeakAudio(ctx, "hi"); !errors.Is(err, context.Canceled) {
		t.Fatalf("speak: %v", err)
	}
	if _, err := c.TranscribeAudio(ctx, "a.wav", []byte("x"), ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("transcribe: %v", err)
	}
}

func TestSpeechError_MapsConfigError(t *testing.T) {
	if err := speechError("tts", nil); err != nil {
		t.Fatalf("nil error mapped to %v", err)
	}
	ce := &sdk.ConfigError{Msg: "api key missing for provider"}
	err := speechError("tts", ce)
	if err == nil || err.Error() != "tts: api key missing for provider" {
		t.Fatalf("config error not passed through: %v", err)
	}
	wrapped := errors.New("outer: " + "inner")
	_ = wrapped
	if err := speechError("stt", errors.New("connection refused: 10.0.0.1:443 sk-abc123")); err == nil ||
		!strings.Contains(err.Error(), "provider request failed") || strings.Contains(err.Error(), "sk-abc123") {
		t.Fatalf("generic error must be collapsed key-free, got: %v", err)
	}
}

func TestSpeakTimeout_BudgetBound(t *testing.T) {
	// No budget view: falls back to the operator timeout.
	c := newSpeechClient(
		config.TTSConfig{Backend: config.TTSBackendProvider, Provider: "openai", Model: "tts-1"},
		config.STTConfig{}, llmclient.Options{Timeout: 30 * time.Second},
	)
	ctx, cancel := c.speakTimeout(context.Background())
	deadline, _ := ctx.Deadline()
	cancel()
	if d := time.Until(deadline); d > 31*time.Second || d < 25*time.Second {
		t.Fatalf("operator timeout not honored: %v", d)
	}

	// A budget owner with a smaller runtime cap bounds the operator timeout.
	c.view = &fakeBudgetOwner{maxRuntime: 2}
	ctx, cancel = c.speakTimeout(context.Background())
	deadline, _ = ctx.Deadline()
	cancel()
	if d := time.Until(deadline); d > 3*time.Second {
		t.Fatalf("budget runtime bound not applied: %v", d)
	}
}

func TestSpeakTimeout_ShortBudgetCancelsSlowHandler(t *testing.T) {
	c := speechTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("late"))
	}, "tts-1", "whisper-1")
	c.SetBudgetView(&fakeBudgetOwner{maxRuntime: 1})

	start := time.Now()
	_, err := c.SpeakAudio(context.Background(), "hello")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error from slow handler under a 1s budget")
	}
	if elapsed > 1400*time.Millisecond {
		t.Fatalf("request not canceled by the budget bound, took %v", elapsed)
	}
}

func TestSetBudgetView_StoresView(t *testing.T) {
	c := newSpeechClient(
		config.TTSConfig{Backend: config.TTSBackendProvider, Provider: "openai", Model: "tts-1"},
		config.STTConfig{}, llmclient.Options{},
	)
	owner := &fakeBudgetOwner{maxRuntime: 9}
	c.SetBudgetView(owner)
	if c.view != budget.View(owner) {
		t.Fatal("SetBudgetView did not store the view")
	}
}

// ── speak tool metadata + extension helpers ─────────────────────────────

func TestSpeakTool_DescriptionAndSchema(t *testing.T) {
	tool := newSpeakTool(danger.DangerousConfig{}, config.TTSConfig{}, &fakeSpeechBackend{})
	if tool.Name() != "speak" {
		t.Fatalf("name = %q", tool.Name())
	}
	if strings.TrimSpace(tool.Description()) == "" {
		t.Fatal("description must not be empty")
	}
	schema, ok := tool.Schema().(map[string]any)
	if !ok {
		t.Fatalf("schema type = %T", tool.Schema())
	}
	if schema["type"] != "object" {
		t.Fatalf("schema type = %v", schema["type"])
	}
	req, ok := schema["required"].([]string)
	if !ok || len(req) != 1 || req[0] != "text" {
		t.Fatalf("required = %#v", schema["required"])
	}
	props := schema["properties"].(map[string]any)
	for _, k := range []string{"text", "path"} {
		if _, ok := props[k]; !ok {
			t.Fatalf("missing property %q", k)
		}
	}
}

func TestSpeakExt_Branches(t *testing.T) {
	cases := []struct {
		mime, format, want string
	}{
		{"audio/mpeg", "", "mp3"},
		{"audio/mp3", "", "mp3"},
		{"audio/wav", "", "wav"},
		{"audio/x-wav", "", "wav"},
		{"audio/wave", "", "wav"},
		{"audio/ogg", "", "ogg"},
		{"audio/flac", "", "flac"},
		{"audio/aac", "", "aac"},
		{"audio/opus", "", "opus"},
		{"Audio/MPEG;charset=utf-8", "", "mp3"}, // case + parameters stripped
		{"application/octet-stream", "wav", "wav"},
		{"application/octet-stream", "OPUS", "opus"}, // sanitized + lowercased
		{"application/octet-stream", "", "mp3"},      // no format → default
	}
	for _, tc := range cases {
		if got := speakExt(tc.mime, tc.format); got != tc.want {
			t.Errorf("speakExt(%q, %q) = %q, want %q", tc.mime, tc.format, got, tc.want)
		}
	}
}

func TestSanitizeSpeakFormat_Branches(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "mp3"},          // empty → default
		{"123456789", "mp3"}, // > 8 chars → default
		{"12345678", "12345678"},
		{".mp3", "mp3"},    // leading dot stripped
		{"..ogg", "mp3"},   // only the first dot stripped, second rejected
		{"WAV", "wav"},     // lowercased
		{"a-b", "mp3"},     // non-alphanumeric rejected
		{"a/b", "mp3"},     // separator rejected
		{"a.b", "mp3"},     // dot inside rejected
		{"opus1", "opus1"}, // mixed letters+digits accepted
		{"accenté", "mp3"}, // non-ASCII rejected
	}
	for _, tc := range cases {
		if got := sanitizeSpeakFormat(tc.in); got != tc.want {
			t.Errorf("sanitizeSpeakFormat(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ── telegram voice-reply options ─────────────────────────────────────────

func TestTelegramSpeechOptions(t *testing.T) {
	resolved := config.ResolvedConfig{
		Provider: "openai",
		APIKey:   "sk-test",
		BaseURL:  "https://example.internal/v1",
		Providers: map[string]config.FileProviderOverride{
			"custom": {APIKey: "k2", BaseURL: "https://custom.example", Format: "openai"},
		},
	}

	// A positive request timeout is stamped into the options.
	opts := telegramSpeechOptions(resolved, 45)
	if opts.Provider != "openai" || opts.APIKey != "sk-test" || opts.BaseURL != "https://example.internal/v1" {
		t.Fatalf("provider fields not mapped: %+v", opts)
	}
	if opts.Timeout != 45*time.Second {
		t.Fatalf("timeout = %v", opts.Timeout)
	}
	if ov, ok := opts.Providers["custom"]; !ok || ov.APIKey != "k2" || ov.BaseURL != "https://custom.example" {
		t.Fatalf("provider overrides not mapped: %+v", opts.Providers)
	}

	// A non-positive timeout stays zero (client falls back to its default).
	opts = telegramSpeechOptions(resolved, 0)
	if opts.Timeout != 0 {
		t.Fatalf("timeout = %v, want 0", opts.Timeout)
	}
}

// ── provider STT dispatch (transcribeProvider) ───────────────────────────

func writeTempAudioP(t *testing.T, n int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "audio.mp3")
	if err := os.WriteFile(p, make([]byte, n), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTranscribeProvider_Success(t *testing.T) {
	fake := &fakeSpeechBackend{transRes: &llmclient.TranscribeResult{
		Text: "  spoken words  ", Model: "whisper-1", Language: "en", DurationSec: 3.2,
	}}
	tool := newTranscribeTool(danger.DangerousConfig{}, config.TranscriptionConfig{})
	tool.SetSpeechBackend(config.STTConfig{Backend: config.STTBackendProvider, Provider: "openai"}, fake)

	src := writeTempAudioP(t, 8)
	raw, err := tool.Call(`{"path":"` + src + `"}`)
	if err != nil {
		t.Fatal(err)
	}
	var res transcribeResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatal(err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if !strings.Contains(res.Text, "spoken words") || res.Model != "whisper-1" || res.Duration != 3.2 || res.Language != "en" {
		t.Fatalf("result = %+v", res)
	}
	// Provider transcription output is wrapped in a per-call untrusted-content
	// boundary (external ASR data), so equality is asserted via containment.
	if !strings.Contains(res.Text, "<untrusted") {
		t.Fatalf("transcription text not untrusted-wrapped: %q", res.Text)
	}
	if fake.gotName != "audio.mp3" || len(fake.gotAudio) != 8 {
		t.Fatalf("backend got name=%q audio=%d bytes", fake.gotName, len(fake.gotAudio))
	}
}

func TestTranscribeProvider_ConfiguredLanguageFallback(t *testing.T) {
	fake := &fakeSpeechBackend{transRes: &llmclient.TranscribeResult{Text: "hi", Model: "whisper-1"}}
	tool := newTranscribeTool(danger.DangerousConfig{}, config.TranscriptionConfig{Language: "de"})
	tool.SetSpeechBackend(config.STTConfig{Backend: config.STTBackendProvider, Provider: "openai"}, fake)

	src := writeTempAudioP(t, 4)
	if _, err := tool.Call(`{"path":"` + src + `"}`); err != nil {
		t.Fatal(err)
	}
	if fake.gotLang != "de" {
		t.Fatalf("language = %q, want configured de", fake.gotLang)
	}
}

func TestTranscribeProvider_ErrorPaths(t *testing.T) {
	// Backend failure surfaces in-band.
	tool := newTranscribeTool(danger.DangerousConfig{}, config.TranscriptionConfig{})
	tool.SetSpeechBackend(config.STTConfig{Backend: config.STTBackendProvider, Provider: "openai"},
		&fakeSpeechBackend{transErr: errors.New("boom")})
	src := writeTempAudioP(t, 4)
	raw, _ := tool.Call(`{"path":"` + src + `"}`)
	var res transcribeResult
	_ = json.Unmarshal([]byte(raw), &res)
	if res.Error == "" {
		t.Fatalf("expected in-band backend error, got %q", raw)
	}

	// Empty transcription text is an error.
	tool.SetSpeechBackend(config.STTConfig{Backend: config.STTBackendProvider, Provider: "openai"},
		&fakeSpeechBackend{transRes: &llmclient.TranscribeResult{Text: "   "}})
	raw, _ = tool.Call(`{"path":"` + src + `"}`)
	_ = json.Unmarshal([]byte(raw), &res)
	if res.Error == "" {
		t.Fatalf("expected empty-text error, got %q", raw)
	}

	// stt.max_audio_mb caps what is uploaded, below the global 10 MiB cap.
	small := writeTempAudioP(t, 64)
	tool.SetSpeechBackend(config.STTConfig{Backend: config.STTBackendProvider, Provider: "openai", MaxAudioMB: 1},
		&fakeSpeechBackend{transRes: &llmclient.TranscribeResult{Text: "ok"}})
	_ = small
	big := filepath.Join(t.TempDir(), "big.mp3")
	if err := os.WriteFile(big, make([]byte, 2<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, _ = tool.Call(`{"path":"` + big + `"}`)
	res = transcribeResult{}
	_ = json.Unmarshal([]byte(raw), &res)
	if res.Error == "" || !strings.Contains(res.Error, "too large") {
		t.Fatalf("expected size-cap error, got %q", raw)
	}

	// Missing file reports the open error without touching the backend.
	fake := &fakeSpeechBackend{}
	tool.SetSpeechBackend(config.STTConfig{Backend: config.STTBackendProvider, Provider: "openai"}, fake)
	raw, _ = tool.Call(`{"path":"` + filepath.Join(t.TempDir(), "nope.mp3") + `"}`)
	res = transcribeResult{}
	_ = json.Unmarshal([]byte(raw), &res)
	if res.Error == "" || fake.transCalls != 0 {
		t.Fatalf("expected open error without backend call, got %q", raw)
	}
}
