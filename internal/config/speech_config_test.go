package config

import (
	"strings"
	"testing"
)

func TestValidateTTSConfig(t *testing.T) {
	valid := TTSConfig{
		Backend: TTSBackendProvider,
		Model:   "gpt-4o-mini-tts",
		Voice:   "alloy",
	}
	if err := ValidateTTSConfig(valid); err != nil {
		t.Fatalf("expected valid config to pass, got: %v", err)
	}

	cases := []struct {
		name   string
		cfg    TTSConfig
		wantIn string
	}{
		{
			name:   "missing backend",
			cfg:    TTSConfig{Model: "m", Voice: "alloy"},
			wantIn: "tts.backend",
		},
		{
			name:   "unknown backend",
			cfg:    TTSConfig{Backend: "local", Model: "m", Voice: "alloy"},
			wantIn: "tts.backend",
		},
		{
			name:   "missing model",
			cfg:    TTSConfig{Backend: TTSBackendProvider, Voice: "alloy"},
			wantIn: "tts.model",
		},
		{
			name:   "missing voice",
			cfg:    TTSConfig{Backend: TTSBackendProvider, Model: "m"},
			wantIn: "tts.voice",
		},
		{
			name:   "negative speed",
			cfg:    TTSConfig{Backend: TTSBackendProvider, Model: "m", Voice: "alloy", Speed: -1},
			wantIn: "tts.speed",
		},
		{
			name:   "speed above cap",
			cfg:    TTSConfig{Backend: TTSBackendProvider, Model: "m", Voice: "alloy", Speed: MaxTTSSpeed + 0.5},
			wantIn: "tts.speed",
		},
		{
			name:   "max_chars above cap",
			cfg:    TTSConfig{Backend: TTSBackendProvider, Model: "m", Voice: "alloy", MaxChars: MaxTTSChars + 1},
			wantIn: "tts.max_chars",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTTSConfig(tc.cfg)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantIn)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Fatalf("expected error containing %q, got: %v", tc.wantIn, err)
			}
		})
	}
}

func TestValidateSTTConfig(t *testing.T) {
	if err := ValidateSTTConfig(STTConfig{}); err != nil {
		t.Fatalf("empty (local) config must be valid, got: %v", err)
	}
	if err := ValidateSTTConfig(STTConfig{Backend: STTBackendProvider, Model: "whisper-1"}); err != nil {
		t.Fatalf("provider config with model must pass, got: %v", err)
	}

	cases := []struct {
		name   string
		cfg    STTConfig
		wantIn string
	}{
		{
			name:   "unknown backend",
			cfg:    STTConfig{Backend: "remote"},
			wantIn: "stt.backend",
		},
		{
			name:   "provider without model",
			cfg:    STTConfig{Backend: STTBackendProvider},
			wantIn: "stt.model",
		},
		{
			name:   "max_audio_mb above cap",
			cfg:    STTConfig{MaxAudioMB: MaxSTTAudioMB + 1},
			wantIn: "stt.max_audio_mb",
		},
		{
			name:   "negative max_audio_mb",
			cfg:    STTConfig{MaxAudioMB: -1},
			wantIn: "stt.max_audio_mb",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSTTConfig(tc.cfg)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantIn)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Fatalf("expected error containing %q, got: %v", tc.wantIn, err)
			}
		})
	}
}

func TestResolveTTSDefaults(t *testing.T) {
	got := resolveTTS(&TTSConfig{Backend: TTSBackendProvider, Model: "m", Voice: "alloy"})
	if got.Format != "mp3" {
		t.Fatalf("expected default format mp3, got %q", got.Format)
	}
	if got.MaxChars != DefaultTTSMaxChars {
		t.Fatalf("expected default max_chars %d, got %d", DefaultTTSMaxChars, got.MaxChars)
	}
	if got.TelegramVoiceReplies {
		t.Fatal("expected telegram_voice_replies to default to false")
	}
	// Omitting the whole tts section yields a config with no backend — the
	// speak tool stays unregistered rather than erroring at load.
	got = resolveTTS(nil)
	if got.Backend != "" {
		t.Fatalf("expected empty backend when the tts section is omitted, got %q", got.Backend)
	}
}

func TestResolveSTTDefaults(t *testing.T) {
	// Omitting the whole stt section preserves local whisper behavior.
	got := resolveSTT(nil)
	if got.Backend != STTBackendLocal {
		t.Fatalf("expected default backend %q, got %q", STTBackendLocal, got.Backend)
	}
	if got.MaxAudioMB != DefaultSTTMaxAudioMB {
		t.Fatalf("expected default max_audio_mb %d, got %d", DefaultSTTMaxAudioMB, got.MaxAudioMB)
	}
	got = resolveSTT(&STTConfig{Backend: STTBackendProvider, Model: "whisper-1"})
	if got.MaxAudioMB != DefaultSTTMaxAudioMB {
		t.Fatalf("expected max_audio_mb default filled in, got %d", got.MaxAudioMB)
	}
}

func TestResolveSTTForProviderInherits(t *testing.T) {
	got := resolveSTTForProvider(&STTConfig{Backend: STTBackendProvider, Model: "whisper-1"}, "openai")
	if got.Provider != "openai" {
		t.Fatalf("expected provider inheritance, got %q", got.Provider)
	}
	got = resolveSTTForProvider(&STTConfig{Backend: STTBackendProvider, Model: "whisper-1", Provider: "custom"}, "openai")
	if got.Provider != "custom" {
		t.Fatalf("expected explicit provider kept, got %q", got.Provider)
	}
}

func TestResolveTTSForProviderInherits(t *testing.T) {
	got := resolveTTSForProvider(&TTSConfig{Backend: TTSBackendProvider, Model: "m", Voice: "alloy"}, "openai")
	if got.Provider != "openai" {
		t.Fatalf("expected provider inheritance, got %q", got.Provider)
	}
}
