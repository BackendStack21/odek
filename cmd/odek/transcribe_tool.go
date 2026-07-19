package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
)

// ── Audio Format Conversion ──────────────────────────────────────────────

// convertToWAV converts an audio file to WAV format using ffmpeg if needed.
// Returns the path to the WAV file (may be the same as input if already WAV/MP3/FLAC
// or if ffmpeg is unavailable/fails — in which case whisper will produce its own error).
// The caller must remove the returned path if it differs from the input path.
func convertToWAV(ctx context.Context, srcPath string) string {
	ext := strings.ToLower(filepath.Ext(srcPath))
	// whisper.cpp supports WAV, MP3, FLAC natively via dr_wav/dr_mp3/dr_flac.
	switch ext {
	case ".wav", ".mp3", ".flac":
		return srcPath
	}

	// Check if ffmpeg is available
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return srcPath
	}

	// Convert to WAV using ffmpeg — best-effort, fall through on failure.
	// Write the output to a temp file in the system temp directory so we never
	// clobber an existing .wav file next to the source path.
	dstFile, err := os.CreateTemp("", "odek-transcribe-*.wav")
	if err != nil {
		return srcPath
	}
	dstPath := dstFile.Name()
	dstFile.Close()

	cmd := exec.CommandContext(ctx, "ffmpeg", "-y", "-i", srcPath, "-acodec", "pcm_s16le", "-ar", "16000", "-ac", "1", dstPath)
	if err := cmd.Run(); err != nil {
		// If ffmpeg fails (corrupt file, unsupported codec, etc.),
		// just pass the original path — whisper will produce its own error.
		os.Remove(dstPath)
		return srcPath
	}
	return dstPath
}

// ── Resolved Paths ──────────────────────────────────────────────────

// whisperBinary attempts to locate the whisper CLI binary.
// Priority: cfg.BinaryPath > PATH search for whisper/whisper-cli.
func whisperBinary(cfg config.TranscriptionConfig) (string, error) {
	if cfg.BinaryPath != "" {
		if _, err := os.Stat(cfg.BinaryPath); err == nil {
			return cfg.BinaryPath, nil
		}
		return "", fmt.Errorf("whisper binary not found at configured path %q", cfg.BinaryPath)
	}

	for _, name := range []string{"whisper", "whisper-cli"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}

	fmt.Fprintln(os.Stderr, `whisper CLI not found. Install it:

  macOS: brew install whisper-cpp
  Linux: apt install whisper-cpp  or  git clone https://github.com/ggerganov/whisper.cpp && cd whisper.cpp && make

Then download a model:
  mkdir -p ~/.odek/whisper/models/
  cd ~/.odek/whisper/models/
  curl -LO https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-tiny.bin

Or set binary_path in config if installed elsewhere.`)
	return "", fmt.Errorf("whisper CLI not found")
}

// modelPath resolves the absolute path to the whisper model file.
// Priority: absolute path in config > ~/.odek/whisper/models/ggml-<model>.bin
func modelPath(cfg config.TranscriptionConfig) (string, error) {
	model := cfg.Model
	if model == "" {
		model = "tiny"
	}

	// If cfg.Model is already an absolute path to a .bin file, use it directly
	if strings.HasSuffix(model, ".bin") && filepath.IsAbs(model) {
		if _, err := os.Stat(model); err == nil {
			return model, nil
		}
		return "", fmt.Errorf("whisper model not found at %q", model)
	}

	// Build path from default or configured models dir
	modelsDir := cfg.ModelsDir
	if modelsDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot determine home directory: %v", err)
		}
		modelsDir = filepath.Join(home, ".odek", "whisper", "models")
	}

	// Try ggml-<model>.bin first, then ggml-<model>.en.bin for English-only variant
	candidates := []string{
		filepath.Join(modelsDir, fmt.Sprintf("ggml-%s.bin", model)),
		filepath.Join(modelsDir, fmt.Sprintf("ggml-%s.en.bin", model)),
	}

	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	// Check for tiny.en as special case
	if model == "tiny" {
		enPath := filepath.Join(modelsDir, "ggml-tiny.en.bin")
		if _, err := os.Stat(enPath); err == nil {
			return enPath, nil
		}
	}

	fmt.Fprintf(os.Stderr, `whisper model "ggml-%s.bin" not found.

Download it:
  mkdir -p %s
  cd %s
  curl -LO https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-%s.bin

Available models: tiny (75MB, fastest), base (150MB), small (500MB), medium (1.5GB)
Set "model" in transcription config to change which model is expected.`,
		model, modelsDir, modelsDir, model)
	return "", fmt.Errorf("whisper model not found")
}

// ═════════════════════════════════════════════════════════════════════════
// transcribe Tool
// ═════════════════════════════════════════════════════════════════════════

type transcribeTool struct {
	ctxTool
	dangerousConfig  danger.DangerousConfig
	transcriptionCfg config.TranscriptionConfig
}

func newTranscribeTool(dc danger.DangerousConfig, tc config.TranscriptionConfig) *transcribeTool {
	return &transcribeTool{
		dangerousConfig:  dc,
		transcriptionCfg: tc,
	}
}

func (t *transcribeTool) Name() string { return "transcribe" }
func (t *transcribeTool) Description() string {
	return `Transcribe an audio file to text using a local whisper model (whisper.cpp CLI). Returns transcribed text with segments and duration. Requires whisper CLI and a model file to be installed locally.`
}

type transcribeArgs struct {
	Path     string `json:"path"`
	Language string `json:"language,omitempty"`
}

type transcribeSegment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

type transcribeResult struct {
	Text     string              `json:"text"`
	Duration float64             `json:"duration_sec"`
	Segments []transcribeSegment `json:"segments"`
	Model    string              `json:"model"`
	Language string              `json:"language"`
	Error    string              `json:"error,omitempty"`
}

func (t *transcribeTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the audio file (OGG, WAV, MP3, etc.).",
			},
			"language": map[string]any{
				"type":        "string",
				"description": "ISO language code (e.g. 'en', 'fr'). Empty = auto-detect.",
			},
		},
		"required": []string{"path"},
	}
}

func (t *transcribeTool) Call(argsJSON string) (result string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("transcribe: panic: %v", r)
			result = `{"error":"internal error"}`
		}
	}()

	var args transcribeArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}
	if args.Path == "" {
		return jsonError("path is required")
	}

	// Security: classify the audio file path
	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "transcribe", Resource: args.Path, Risk: danger.ClassifyPath(args.Path),
	}, nil); err != nil {
		return jsonError(err.Error())
	}

	// Check the audio file exists (O_NOFOLLOW to prevent symlink attacks) and
	// reject inputs that would exhaust memory during conversion / transcription.
	f, err := os.OpenFile(args.Path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return jsonResult(transcribeResult{
			Error: fmt.Sprintf("cannot open audio file %q: %v", args.Path, err),
		})
	}
	info, err := f.Stat()
	f.Close()
	if err != nil {
		return jsonResult(transcribeResult{
			Error: fmt.Sprintf("cannot stat audio file %q: %v", args.Path, err),
		})
	}
	const maxAudioFileBytes = maxFileReadBytes // 10 MiB — same cap as other file-reading tools
	if info.Size() > maxAudioFileBytes {
		return jsonResult(transcribeResult{
			Error: fmt.Sprintf("audio file too large (%d bytes, max %d)", info.Size(), maxAudioFileBytes),
		})
	}

	// Convert to WAV if needed (whisper.cpp doesn't support OGG Opus natively).
	wavPath := convertToWAV(t.toolCtx(), args.Path)
	cleanup := func() {
		if wavPath != args.Path {
			os.Remove(wavPath)
		}
	}
	defer cleanup()

	// Locate whisper binary
	binary, err := whisperBinary(t.transcriptionCfg)
	if err != nil {
		return jsonResult(transcribeResult{
			Error: err.Error(),
		})
	}

	// Locate model file
	modelPathResolved, err := modelPath(t.transcriptionCfg)
	if err != nil {
		return jsonResult(transcribeResult{
			Error: err.Error(),
		})
	}

	// Build whisper command
	lang := args.Language
	if lang == "" {
		lang = t.transcriptionCfg.Language
	}

	args2 := []string{
		"--model", modelPathResolved,
		"--output-json",
		"--file", wavPath,
	}
	if lang != "" {
		args2 = append(args2, "--language", lang)
	}

	const maxWhisperOutputBytes = 10 << 20 // 10 MiB
	cmd := exec.CommandContext(t.toolCtx(), binary, args2...)
	output, err := cmd.Output()
	if err == nil && len(output) > maxWhisperOutputBytes {
		return jsonResult(transcribeResult{
			Error: fmt.Sprintf("whisper output too large (%d bytes, max %d)", len(output), maxWhisperOutputBytes),
		})
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return jsonResult(transcribeResult{
				Error: fmt.Sprintf("whisper failed (exit %d): %s", exitErr.ExitCode(), string(exitErr.Stderr)),
			})
		}
		return jsonResult(transcribeResult{
			Error: fmt.Sprintf("whisper failed: %v", err),
		})
	}

	// Parse whisper JSON output
	var whisperOut struct {
		Text     string  `json:"text"`
		Language string  `json:"language"`
		Duration float64 `json:"duration"`
		Segments []struct {
			Start float64 `json:"start"`
			End   float64 `json:"end"`
			Text  string  `json:"text"`
		} `json:"segments"`
	}

	if err := json.Unmarshal(output, &whisperOut); err != nil {
		// whisper output may not be valid JSON depending on version
		// Fallback: use raw text output
		return jsonResult(transcribeResult{
			Text:     strings.TrimSpace(string(output)),
			Duration: 0,
			Model:    filepath.Base(modelPathResolved),
		})
	}

	// Convert segments
	source := "transcribe:" + args.Path
	segments := make([]transcribeSegment, len(whisperOut.Segments))
	for i, s := range whisperOut.Segments {
		segments[i] = transcribeSegment{Start: s.Start, End: s.End, Text: wrapUntrusted(t.toolCtx(), source, s.Text)}
	}

	modelLabel := filepath.Base(modelPathResolved)
	modelLabel = strings.TrimPrefix(modelLabel, "ggml-")
	modelLabel = strings.TrimSuffix(modelLabel, ".bin")

	return jsonResult(transcribeResult{
		Text:     wrapUntrusted(t.toolCtx(), source, strings.TrimSpace(whisperOut.Text)),
		Duration: whisperOut.Duration,
		Segments: segments,
		Model:    modelLabel,
		Language: whisperOut.Language,
	})
}

// Ensure transcribeTool implements odek.Tool
var _ odek.Tool = (*transcribeTool)(nil)
