package config

import (
	"os"
	"path/filepath"
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

// LoadConfig must apply the tts/stt validators at load time: an invalid
// section disables the feature (with a loud warning) instead of silently
// misbehaving on the first call.
func TestLoadConfig_InvalidTTSConfigDisabled(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	os.MkdirAll(filepath.Join(dir, ".odek"), 0755)
	global := filepath.Join(dir, ".odek", "config.json")
	os.WriteFile(global, []byte(`{
		"tts": {"backend": "provider", "model": "m", "voice": "alloy", "speed": 99}
	}`), 0644)

	cfg := LoadConfig(CLIFlags{})
	if cfg.TTS.Backend != "" {
		t.Errorf("invalid tts.speed must disable tts, got backend %q", cfg.TTS.Backend)
	}
}

func TestLoadConfig_InvalidSTTConfigFallsBackToLocal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	os.MkdirAll(filepath.Join(dir, ".odek"), 0755)
	global := filepath.Join(dir, ".odek", "config.json")
	os.WriteFile(global, []byte(`{
		"stt": {"backend": "provider"}
	}`), 0644)

	cfg := LoadConfig(CLIFlags{})
	if cfg.STT.Backend != STTBackendLocal {
		t.Errorf("invalid stt (provider without model) must fall back to local, got %q", cfg.STT.Backend)
	}
}

func TestLoadConfig_ValidTTSSTTKept(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	os.MkdirAll(filepath.Join(dir, ".odek"), 0755)
	global := filepath.Join(dir, ".odek", "config.json")
	os.WriteFile(global, []byte(`{
		"provider": "openai",
		"providers": {"openai": {"api_key": "sk-test"}},
		"tts": {"backend": "provider", "model": "m", "voice": "alloy", "max_chars": 100},
		"stt": {"backend": "provider", "model": "whisper-1", "max_audio_mb": 10}
	}`), 0644)

	cfg := LoadConfig(CLIFlags{})
	if cfg.TTS.Backend != TTSBackendProvider || cfg.TTS.MaxChars != 100 {
		t.Errorf("valid tts must be kept, got %+v", cfg.TTS)
	}
	if cfg.STT.Backend != STTBackendProvider || cfg.STT.MaxAudioMB != 10 {
		t.Errorf("valid stt must be kept, got %+v", cfg.STT)
	}
}
