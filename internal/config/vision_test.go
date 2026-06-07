package config

import "testing"

func TestResolveVision_Defaults(t *testing.T) {
	v := resolveVision(nil)
	if v.VideoFrames != 8 {
		t.Errorf("VideoFrames = %d, want 8", v.VideoFrames)
	}
	if v.ModelsDir != "" {
		t.Errorf("ModelsDir = %q, want empty", v.ModelsDir)
	}
	if v.BinaryPath != "" {
		t.Errorf("BinaryPath = %q, want empty", v.BinaryPath)
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
