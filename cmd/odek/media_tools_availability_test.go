package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
)

// mediaScratch isolates HOME and PATH so whisper.cpp / llama-mtmd-cli
// lookups see only what the test installs. It returns the HOME and a
// directory on PATH where fake binaries can be placed.
func mediaScratch(t *testing.T) (home, bin string) {
	t.Helper()
	home, bin = t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)
	return home, bin
}

func installFake(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func toolNames(tcfg toolConfig) map[string]bool {
	names := map[string]bool{}
	for _, tl := range builtinTools(danger.DangerousConfig{}, nil, nil, 1, "", tcfg, nil) {
		names[tl.Name()] = true
	}
	return names
}

func mediaToolConfig() toolConfig {
	return toolConfig{
		Transcription: config.TranscriptionConfig{Model: "tiny"},
		Vision:        config.VisionConfig{Backend: config.VisionBackendLocal},
		STT:           config.STTConfig{Backend: config.STTBackendLocal},
	}
}

// TestRED_MediaToolsHiddenWithoutBackend: on a stock install (no whisper.cpp,
// no MiniCPM-V files, no provider backends) the model must not be offered
// transcribe or vision — every call could only fail.
func TestRED_MediaToolsHiddenWithoutBackend(t *testing.T) {
	mediaScratch(t)
	names := toolNames(mediaToolConfig())
	if names["transcribe"] {
		t.Error("transcribe registered without whisper.cpp or a provider STT backend")
	}
	if names["vision"] {
		t.Error("vision registered without a local MiniCPM-V stack or the provider backend")
	}
}

func TestRED_TranscribeRegisteredWhenWhisperInstalled(t *testing.T) {
	home, bin := mediaScratch(t)
	tcfg := mediaToolConfig()

	installFake(t, filepath.Join(bin, "whisper-cli"))
	if toolNames(tcfg)["transcribe"] {
		t.Error("transcribe registered with a binary but no model file")
	}
	installFake(t, filepath.Join(home, ".odek", "whisper", "models", "ggml-tiny.bin"))
	if !toolNames(tcfg)["transcribe"] {
		t.Error("transcribe not registered with whisper-cli and ggml-tiny.bin installed")
	}
}

func TestRED_TranscribeRegisteredWithProviderSTT(t *testing.T) {
	mediaScratch(t)
	tcfg := mediaToolConfig()
	tcfg.STT = config.STTConfig{Backend: config.STTBackendProvider, Provider: "openai", Model: "whisper-1"}
	if !toolNames(tcfg)["transcribe"] {
		t.Error("transcribe not registered with a provider STT backend")
	}
}

func TestRED_VisionRegisteredWhenBackendUsable(t *testing.T) {
	home, bin := mediaScratch(t)
	tcfg := mediaToolConfig()

	installFake(t, filepath.Join(bin, "llama-mtmd-cli"))
	if toolNames(tcfg)["vision"] {
		t.Error("vision registered with llama-mtmd-cli but no model files")
	}
	models := filepath.Join(home, ".odek", "minicpm-v", "models")
	installFake(t, filepath.Join(models, "model.gguf"))
	installFake(t, filepath.Join(models, "mmproj.gguf"))
	if !toolNames(tcfg)["vision"] {
		t.Error("vision not registered with llama-mtmd-cli and both model files installed")
	}

	mediaScratch(t)
	tcfg.Vision = config.VisionConfig{Backend: config.VisionBackendProvider, Model: "gpt-vision"}
	if !toolNames(tcfg)["vision"] {
		t.Error("vision not registered with the provider backend")
	}
}

// TestRED_MediaToolNamesStayReserved: hiding an unusable tool must not free
// its name for an MCP server to shadow.
func TestRED_MediaToolNamesStayReserved(t *testing.T) {
	mediaScratch(t)
	reserved := reservedBuiltinToolNames()
	for _, name := range []string{"transcribe", "vision"} {
		if !reserved[name] {
			t.Errorf("%s is not reserved against MCP shadowing when its backend is missing", name)
		}
	}
}

// TestRED_ConfiguredTranscribeWiresProviderSTT: the transcribe tool uses the
// provider STT backend when one is configured.
func TestRED_ConfiguredTranscribeWiresProviderSTT(t *testing.T) {
	mediaScratch(t)
	tcfg := mediaToolConfig()
	tcfg.STT = config.STTConfig{Backend: config.STTBackendProvider, Provider: "openai", Model: "whisper-1"}
	tool, ready := newConfiguredTranscribeTool(danger.DangerousConfig{}, tcfg, newSpeechClient(tcfg.TTS, tcfg.STT, tcfg.SpeechOptions))
	if !ready {
		t.Fatal("provider STT backend reported as not ready")
	}
	if tool.speech == nil {
		t.Error("transcribe tool has no provider STT backend")
	}

	tcfg.STT = config.STTConfig{Backend: config.STTBackendLocal}
	if _, ready := newConfiguredTranscribeTool(danger.DangerousConfig{}, tcfg, nil); ready {
		t.Error("local STT reported ready without whisper.cpp installed")
	}
}
