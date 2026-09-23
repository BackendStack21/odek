package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
)

// maxSpeakAudioBytes bounds the synthesized audio written to the artifacts
// dir — generous headroom over any realistic TTS response, tight enough that
// a misbehaving provider cannot fill the disk.
const maxSpeakAudioBytes = 64 << 20

// speakCounter disambiguates default output names within a process run.
var speakCounter atomic.Int64

// speakTool synthesizes speech through the configured TTS backend and writes
// the audio into the artifacts directory. The model only supplies text (and
// optionally a filename); voice/format/speed come from operator config, so
// the tool surface stays small and deterministic.
type speakTool struct {
	ctxTool
	dangerousConfig danger.DangerousConfig
	cfg             config.TTSConfig
	backend         speechBackend
}

func newSpeakTool(dc danger.DangerousConfig, cfg config.TTSConfig, backend speechBackend) *speakTool {
	return &speakTool{dangerousConfig: dc, cfg: cfg, backend: backend}
}

func (t *speakTool) Name() string { return "speak" }

func (t *speakTool) Description() string {
	return `Synthesize speech from text using the configured text-to-speech provider. Writes an audio file to the artifacts directory and returns its metadata (path, bytes, MIME type, model). Voice, format, and speed are fixed by configuration — do not mention them. Text is capped by the operator's max_chars setting.`
}

type speakArgs struct {
	Text string `json:"text"`
	Path string `json:"path,omitempty"`
}

type speakResult struct {
	Path string `json:"path"`
	Bytes int   `json:"bytes"`
	MIME string `json:"mime,omitempty"`
	Model string `json:"model,omitempty"`
	Error string `json:"error,omitempty"`
}

func (t *speakTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{
				"type":        "string",
				"description": "Text to synthesize.",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Optional output filename (basename only) inside the artifacts directory. Omit to get an auto-generated speak-<n> name.",
			},
		},
		"required": []string{"text"},
	}
}

// speakExt maps a MIME type (or the configured format) to a file extension.
func speakExt(mime, format string) string {
	switch strings.ToLower(strings.SplitN(mime, ";", 2)[0]) {
	case "audio/mpeg", "audio/mp3":
		return "mp3"
	case "audio/wav", "audio/x-wav", "audio/wave":
		return "wav"
	case "audio/ogg":
		return "ogg"
	case "audio/flac":
		return "flac"
	case "audio/aac":
		return "aac"
	case "audio/opus":
		return "opus"
	}
	if format != "" {
		return sanitizeSpeakFormat(format)
	}
	return "mp3"
}

// sanitizeSpeakFormat restricts the operator-configured tts.format to a
// short lowercase alphanumeric extension so it can never smuggle path
// separators, dots, or shell metacharacters into a filename.
func sanitizeSpeakFormat(format string) string {
	format = strings.TrimPrefix(strings.ToLower(format), ".")
	if n := len(format); n == 0 || n > 8 {
		return "mp3"
	}
	for _, r := range format {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return "mp3"
		}
	}
	return format
}

// safeSpeakBasename restricts an operator/model-supplied filename to a plain
// basename inside the artifacts root.
func safeSpeakBasename(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}
	base := filepath.Base(name)
	if base != name || base == "." || base == ".." || strings.HasPrefix(base, ".") || strings.ContainsAny(base, `/\`) {
		return "", fmt.Errorf("path must be a plain filename, not a path")
	}
	return base, nil
}

func (t *speakTool) Call(argsJSON string) (string, error) {
	var args speakArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}
	text := strings.TrimSpace(args.Text)
	if text == "" {
		return jsonError("text is required")
	}
	maxChars := t.cfg.MaxChars
	if maxChars <= 0 {
		maxChars = config.DefaultTTSMaxChars
	}
	if len(text) > maxChars {
		return jsonResult(speakResult{Error: fmt.Sprintf("text too long (%d chars, max %d)", len(text), maxChars)})
	}

	baseName, err := safeSpeakBasename(args.Path)
	if err != nil {
		return jsonError(err.Error())
	}

	root, err := artifactsHome()
	if err != nil {
		return jsonResult(speakResult{Error: fmt.Sprintf("cannot resolve artifacts directory: %v", err)})
	}
	outPath := ""
	if baseName != "" {
		outPath = filepath.Join(root, baseName)
	} else {
		n := speakCounter.Add(1)
		outPath = filepath.Join(root, fmt.Sprintf("speak-%d.%s", n, speakExt("", t.cfg.Format)))
	}

	// Approval classification: writing inside the artifacts root is a local
	// write; the provider synthesis call additionally sends the text to a
	// third-party endpoint, so it is classified as network egress.
	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "speak", Resource: outPath, Risk: danger.LocalWrite,
	}, nil); err != nil {
		return jsonError(err.Error())
	}
	if t.cfg.Backend == config.TTSBackendProvider {
		if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
			Name: "speak", Resource: "provider:" + t.cfg.Provider, Risk: danger.NetworkEgress,
		}, nil); err != nil {
			return jsonError(err.Error())
		}
	}

	if err := os.MkdirAll(root, 0o700); err != nil {
		return jsonResult(speakResult{Error: fmt.Sprintf("cannot create artifacts directory: %v", err)})
	}

	res, err := t.backend.SpeakAudio(t.toolCtx(), text)
	if err != nil {
		return jsonResult(speakResult{Error: err.Error()})
	}
	if len(res.Audio) == 0 {
		return jsonResult(speakResult{Error: "tts: provider returned no audio"})
	}
	if len(res.Audio) > maxSpeakAudioBytes {
		return jsonResult(speakResult{Error: fmt.Sprintf("tts: provider audio exceeds %d bytes", maxSpeakAudioBytes)})
	}

	// Refine the extension from the provider's declared MIME when the model
	// did not pick an explicit filename.
	if baseName == "" {
		if ext := speakExt(res.MIMEType, t.cfg.Format); ext != "" {
			outPath = strings.TrimSuffix(outPath, filepath.Ext(outPath)) + "." + ext
		}
	}

	if err := os.WriteFile(outPath, res.Audio, 0o600); err != nil {
		return jsonResult(speakResult{Error: fmt.Sprintf("cannot write audio file: %v", err)})
	}
	return jsonResult(speakResult{
		Path:  outPath,
		Bytes: len(res.Audio),
		MIME:  res.MIMEType,
		Model: res.Model,
	})
}

var _ odek.Tool = (*speakTool)(nil)
