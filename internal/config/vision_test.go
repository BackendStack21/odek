package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveVision_Defaults(t *testing.T) {
	v := resolveVision(nil)
	if v.Backend != VisionBackendLocal || v.MaxTokens != DefaultVisionMaxTokens {
		t.Errorf("defaults backend/max_tokens = %q/%d", v.Backend, v.MaxTokens)
	}
	if v.VideoFrames != 8 {
		t.Errorf("VideoFrames = %d, want 8", v.VideoFrames)
	}
	if v.ModelsDir != "" {
		t.Errorf("ModelsDir = %q, want empty", v.ModelsDir)
	}
	if v.BinaryPath != "" {
		t.Errorf("BinaryPath = %q, want empty", v.BinaryPath)
	}
	if !v.AutoDescribeEnabled() {
		t.Error("AutoDescribe = false, want true (default when section absent)")
	}
}

func TestValidateVisionConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  VisionConfig
	}{
		{"backend", VisionConfig{Backend: "remote"}},
		{"provider model", VisionConfig{Backend: VisionBackendProvider}},
		{"tokens", VisionConfig{MaxTokens: MaxVisionTokens + 1}},
		{"frames", VisionConfig{VideoFrames: MaxVisionVideoFrames + 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateVisionConfig(tc.cfg); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	if err := ValidateVisionConfig(VisionConfig{Backend: VisionBackendProvider, Model: "vision-model", MaxTokens: 1024, VideoFrames: 8}); err != nil {
		t.Fatal(err)
	}
}

func TestResolveVision_AutoDescribeDefaultWhenSectionPresent(t *testing.T) {
	// A vision section supplied without auto_describe must resolve to the
	// same default as an absent section (true), mirroring
	// transcription.auto_transcribe. Explicit false must still win.
	v := resolveVision(&VisionConfig{Backend: VisionBackendProvider, Model: "m"})
	if !v.AutoDescribeEnabled() {
		t.Error("AutoDescribe = false, want true (default when section present but field absent)")
	}
	off := resolveVision(&VisionConfig{Backend: VisionBackendProvider, Model: "m", AutoDescribe: boolPtr(false)})
	if off.AutoDescribeEnabled() {
		t.Error("AutoDescribe = true, want false (explicitly disabled)")
	}
	on := resolveVision(&VisionConfig{AutoDescribe: boolPtr(true)})
	if !on.AutoDescribeEnabled() {
		t.Error("AutoDescribe = false, want true (explicitly enabled)")
	}
}

func TestResolveVisionProviderDefaultsToMainProvider(t *testing.T) {
	v := resolveVisionForProvider(&VisionConfig{Backend: VisionBackendProvider, Model: "vision-model"}, "openai")
	if v.Provider != "openai" {
		t.Fatalf("provider = %q, want openai", v.Provider)
	}
}

func TestLoadConfigVisionProviderInheritanceUsesFinalProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	global := filepath.Join(home, ".odek")
	if err := os.MkdirAll(global, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		if err := os.WriteFile(filepath.Join(global, "config.json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"vision":{"backend":"provider","model":"vision-model"}}`)
	if got := LoadConfig(CLIFlags{}).Vision.Provider; got != "deepseek" {
		t.Fatalf("default provider = %q", got)
	}
	write(`{"provider":"anthropic","vision":{"backend":"provider","model":"vision-model"}}`)
	if got := LoadConfig(CLIFlags{}).Vision.Provider; got != "anthropic" {
		t.Fatalf("explicit provider = %q", got)
	}
	t.Setenv("ODEK_PROVIDER", "openai")
	if got := LoadConfig(CLIFlags{}).Vision.Provider; got != "openai" {
		t.Fatalf("env provider = %q", got)
	}
}

func TestLoadConfigVisionV1AliasAndProjectRejection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()
	t.Chdir(wd)
	global := filepath.Join(home, ".odek")
	if err := os.MkdirAll(global, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(global, "config.json"), []byte(`{"base_url":"https://api.openai.com/v1","vision":{"backend":"provider","model":"vision-model"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wd, "odek.json"), []byte(`{"vision":{"backend":"local","model":"project-model"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := LoadConfig(CLIFlags{})
	if cfg.Provider != "openai" || cfg.Vision.Provider != "openai" {
		t.Fatalf("v1 alias provider = %q vision provider = %q", cfg.Provider, cfg.Vision.Provider)
	}
	if cfg.Vision.Model != "vision-model" {
		t.Fatalf("project vision was not rejected: model=%q", cfg.Vision.Model)
	}
}

func TestResolveVision_AutoDescribePreserved(t *testing.T) {
	// When a vision section is present, the explicit value is honored.
	on := resolveVision(&VisionConfig{AutoDescribe: boolPtr(true)})
	if !on.AutoDescribeEnabled() {
		t.Error("AutoDescribe = false, want true (explicitly set)")
	}
	off := resolveVision(&VisionConfig{AutoDescribe: boolPtr(false)})
	if off.AutoDescribeEnabled() {
		t.Error("AutoDescribe = true, want false (explicitly unset)")
	}
}

func TestResolveVision_ZeroFramesFilled(t *testing.T) {
	v := resolveVision(&VisionConfig{VideoFrames: 0})
	if v.VideoFrames != 8 {
		t.Errorf("VideoFrames = %d, want 8 (zero filled with default)", v.VideoFrames)
	}
}

func TestResolveVision_CustomValues(t *testing.T) {
	v := resolveVision(&VisionConfig{
		ModelsDir:   "/custom/models",
		BinaryPath:  "/usr/local/bin/llama-mtmd-cli",
		VideoFrames: 16,
	})
	if v.ModelsDir != "/custom/models" {
		t.Errorf("ModelsDir = %q, want '/custom/models'", v.ModelsDir)
	}
	if v.BinaryPath != "/usr/local/bin/llama-mtmd-cli" {
		t.Errorf("BinaryPath = %q, want '/usr/local/bin/llama-mtmd-cli'", v.BinaryPath)
	}
	if v.VideoFrames != 16 {
		t.Errorf("VideoFrames = %d, want 16", v.VideoFrames)
	}
}
