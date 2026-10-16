package main

import (
	"os"
	"path/filepath"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
)

// transcribeBackendReady reports whether the transcribe tool can run: a
// provider STT backend is wired, or the whisper CLI and its model file are
// installed on this host. Lookups are silent; install hints are printed
// only when a call actually needs the missing piece.
func transcribeBackendReady(tc config.TranscriptionConfig, providerSTT bool) bool {
	if providerSTT {
		return true
	}
	if _, err := findWhisperBinary(tc); err != nil {
		return false
	}
	_, _, err := findWhisperModel(tc)
	return err == nil
}

// visionBackendReady reports whether the vision tool can run: the provider
// backend is selected, or llama-mtmd-cli plus both MiniCPM-V files are
// installed on this host.
func visionBackendReady(vc config.VisionConfig) bool {
	if vc.Backend == config.VisionBackendProvider {
		return true
	}
	if _, err := findLlamaMtmdBinary(vc); err != nil {
		return false
	}
	dir, err := visionModelsDir(vc)
	if err != nil {
		return false
	}
	for _, name := range []string{"model.gguf", "mmproj.gguf"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return false
		}
	}
	return true
}

// newConfiguredTranscribeTool builds the transcribe tool with the provider
// STT backend wired when stt.backend is "provider", and reports whether its
// backend can run. The agent tool set and Telegram voice auto-transcription
// share it so both honour the same backend.
func newConfiguredTranscribeTool(dc danger.DangerousConfig, tcfg toolConfig) (*transcribeTool, bool) {
	t := newTranscribeTool(dc, tcfg.Transcription)
	providerSTT := false
	if tcfg.STT.Backend == config.STTBackendProvider {
		if speech := newSpeechClient(tcfg.TTS, tcfg.STT, tcfg.SpeechOptions); speech != nil {
			t.SetSpeechBackend(tcfg.STT, speech)
			providerSTT = true
		}
	}
	return t, transcribeBackendReady(tcfg.Transcription, providerSTT)
}
