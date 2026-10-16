package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/BackendStack21/odek"
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

// newConfiguredTranscribeTool builds the transcribe tool, wiring speech as
// its STT backend when stt.backend is "provider" (speech may be nil), and
// reports whether its backend can run.
func newConfiguredTranscribeTool(dc danger.DangerousConfig, tcfg toolConfig, speech *providerSpeechClient) (*transcribeTool, bool) {
	t := newTranscribeTool(dc, tcfg.Transcription)
	providerSTT := false
	if tcfg.STT.Backend == config.STTBackendProvider && speech != nil {
		t.SetSpeechBackend(tcfg.STT, speech)
		providerSTT = true
	}
	return t, transcribeBackendReady(tcfg.Transcription, providerSTT)
}

// telegramVoiceBackends reports whether Telegram voice auto-transcription
// can run and whether the agent has a transcribe tool. Auto-transcription
// happens before the agent run starts — outside execution budgets, usage
// accounting and /stop — so it uses local whisper only and never a paid
// provider STT call; with only provider STT the agent transcribes through
// the tool, inside the run.
func telegramVoiceBackends(tcfg toolConfig) (autoReady, toolReady bool) {
	autoReady = transcribeBackendReady(tcfg.Transcription, false)
	providerSTT := tcfg.STT.Backend == config.STTBackendProvider &&
		newSpeechClient(tcfg.TTS, tcfg.STT, tcfg.SpeechOptions) != nil
	return autoReady, autoReady || providerSTT
}

// transcribeCallArgs encodes the transcribe tool's arguments for path.
func transcribeCallArgs(path string) string {
	b, _ := json.Marshal(struct {
		Path string `json:"path"`
	}{path})
	return string(b)
}

// gatedBuiltinTools are built-in tools registered only when their backend
// is configured or installed, mapped to what is missing when they are
// absent. Their names stay reserved against MCP shadowing, and surfaces
// that list tools report them as unavailable rather than enabled.
var gatedBuiltinTools = map[string]string{
	"transcribe": "no transcription backend: install whisper.cpp and a model, or set stt.backend to provider",
	"vision":     "no vision backend: install llama-mtmd-cli and the MiniCPM-V files, or set vision.backend to provider",
	"speak":      "no speech backend: set tts.backend to provider",
	"web_search": "no search backend: set web_search.base_url to a SearXNG instance",
}

// gatedToolWarned records gated tools already reported as missing from an
// explicit allowlist, so repeated tool-set builds warn once per process.
var gatedToolWarned sync.Map

// warnUnavailableEnabledTools reports allowlisted gated tools that are not
// registered: tool.FilterTools skips unknown names silently, which would
// leave e.g. `--tool vision` running with no tools and no explanation.
func warnUnavailableEnabledTools(enabled []string, registered []odek.Tool) {
	have := make(map[string]bool, len(registered))
	for _, t := range registered {
		have[t.Name()] = true
	}
	for _, name := range enabled {
		reason, gated := gatedBuiltinTools[name]
		if !gated || have[name] {
			continue
		}
		if _, seen := gatedToolWarned.LoadOrStore(name, true); seen {
			continue
		}
		fmt.Fprintf(os.Stderr, "odek: WARNING: tool %q is enabled but not available (%s)\n", name, reason)
	}
}
