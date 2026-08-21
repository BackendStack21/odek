package config

// Regression tests for the 2026-08 security audit: transcription.binary_path
// and vision.binary_path flow into exec.Command in the transcribe/vision
// tools, so a cloned repo must not be able to point them at a planted
// binary through ./odek.json. The sections are operator-only, like
// telegram/memory/web_search.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig_ProjectTranscriptionIgnored(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	globalDir := filepath.Join(dir, ".odek")
	os.MkdirAll(globalDir, 0755)

	// Project config tries to point the transcribe tool at a planted binary
	// and to auto-run it on every Telegram voice note.
	if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(`{
		"transcription": {
			"binary_path": "./tools/whisper",
			"auto_transcribe": true
		}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if cfg.Transcription.BinaryPath != "" {
		t.Errorf("Transcription.BinaryPath = %q, want empty (project transcription must be ignored)", cfg.Transcription.BinaryPath)
	}
	// Note: AutoTranscribe falls back to the built-in default (true) once
	// the project section is stripped — that flag only selects whether the
	// operator-configured (or PATH-resolved) binary runs, so it is benign
	// without control of binary_path.
}

func TestLoadConfig_ProjectVisionIgnored(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	globalDir := filepath.Join(dir, ".odek")
	os.MkdirAll(globalDir, 0755)

	if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(`{
		"vision": {
			"binary_path": "./tools/mtmd",
			"models_dir": "./tools/models"
		}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if cfg.Vision.BinaryPath != "" {
		t.Errorf("Vision.BinaryPath = %q, want empty (project vision must be ignored)", cfg.Vision.BinaryPath)
	}
	if cfg.Vision.ModelsDir != "" {
		t.Errorf("Vision.ModelsDir = %q, want empty (project vision must be ignored)", cfg.Vision.ModelsDir)
	}
}

func TestLoadConfig_GlobalTranscriptionStillApplies(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	globalDir := filepath.Join(dir, ".odek")
	os.MkdirAll(globalDir, 0755)
	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"transcription": {"binary_path": "/opt/whisper"},
		"vision": {"binary_path": "/opt/mtmd"}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if cfg.Transcription.BinaryPath != "/opt/whisper" {
		t.Errorf("Transcription.BinaryPath = %q, want /opt/whisper (global config still applies)", cfg.Transcription.BinaryPath)
	}
	if cfg.Vision.BinaryPath != "/opt/mtmd" {
		t.Errorf("Vision.BinaryPath = %q, want /opt/mtmd (global config still applies)", cfg.Vision.BinaryPath)
	}
}
