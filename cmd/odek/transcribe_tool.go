package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	restrictToCWD    bool // sandbox: reject paths that escape the workspace
	// sttCfg/speech carry the provider STT backend (nil = local whisper).
	sttCfg config.STTConfig
	speech speechBackend
}

func newTranscribeTool(dc danger.DangerousConfig, tc config.TranscriptionConfig) *transcribeTool {
	return &transcribeTool{
		dangerousConfig:  dc,
		transcriptionCfg: tc,
	}
}

// SetSpeechBackend switches the tool to the provider STT backend. Local
// whisper paths remain byte-for-byte unchanged when this is never called.
func (t *transcribeTool) SetSpeechBackend(cfg config.STTConfig, backend speechBackend) {
	t.sttCfg = cfg
	t.speech = backend
}

// transcribeProvider dispatches to the provider STT backend. The audio file
// is read with an O_NOFOLLOW open and capped at stt.max_audio_mb before it
// ever leaves the machine.
func (t *transcribeTool) transcribeProvider(args transcribeArgs, source string) (string, error) {
	maxBytes := int64(t.sttCfg.MaxAudioMB) << 20
	if maxBytes <= 0 {
		maxBytes = int64(config.DefaultSTTMaxAudioMB) << 20
	}
	f, err := os.OpenFile(args.Path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return jsonResult(transcribeResult{Error: fmt.Sprintf("cannot open audio file %q: %v", args.Path, err)})
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return jsonResult(transcribeResult{Error: fmt.Sprintf("cannot stat audio file %q: %v", args.Path, err)})
	}
	if info.Size() > maxBytes {
		return jsonResult(transcribeResult{Error: fmt.Sprintf("audio file too large (%d bytes, max %d bytes)", info.Size(), maxBytes)})
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return jsonResult(transcribeResult{Error: fmt.Sprintf("cannot read audio file %q: %v", args.Path, err)})
	}
	lang := args.Language
	if lang == "" {
		lang = t.transcriptionCfg.Language
	}
	res, err := t.speech.TranscribeAudio(t.toolCtx(), filepath.Base(args.Path), data, lang)
	if err != nil {
		return jsonResult(transcribeResult{Error: err.Error()})
	}
	if res == nil || res.Text == "" {
		return jsonResult(transcribeResult{Error: "stt: provider returned no transcription"})
	}
	return jsonResult(transcribeResult{
		Text:     wrapUntrusted(t.toolCtx(), source, strings.TrimSpace(res.Text)),
		Duration: res.DurationSec,
		Model:    res.Model,
		Language: res.Language,
	})
}

func (t *transcribeTool) Name() string { return "transcribe" }
func (t *transcribeTool) Description() string {
	return `Transcribe an audio file to text using a local whisper model (whisper.cpp CLI). Returns transcribed text with segments and duration. Requires whisper CLI and a model file to be installed locally; native audio input is WAV/MP3/FLAC — other containers are auto-converted via ffmpeg, so if conversion fails, supply WAV/MP3/FLAC directly instead of retrying.`
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

type parsedWhisperOutput struct {
	Text     string
	Language string
	Duration float64
	Segments []transcribeSegment
}

// readWhisperJSON reads a whisper.cpp sidecar with the same bound used for
// captured stdout. The output path is in a private temporary directory.
func readWhisperJSON(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("whisper output too large")
	}
	return data, nil
}

func parseWhisperJSON(data []byte) (parsedWhisperOutput, error) {
	var out struct {
		Text     string  `json:"text"`
		Language string  `json:"language"`
		Duration float64 `json:"duration"`
		Segments []struct {
			Start, End float64
			Text       string
		} `json:"segments"`
		Result struct {
			Language string `json:"language"`
		} `json:"result"`
		Transcription []struct {
			Timestamps struct{ From, To string }  `json:"timestamps"`
			Offsets    struct{ From, To float64 } `json:"offsets"`
			Text       string                     `json:"text"`
		} `json:"transcription"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return parsedWhisperOutput{}, err
	}
	parsed := parsedWhisperOutput{Text: out.Text, Language: out.Language, Duration: out.Duration}
	if parsed.Language == "" {
		parsed.Language = out.Result.Language
	}
	for _, s := range out.Segments {
		parsed.Segments = append(parsed.Segments, transcribeSegment{Start: s.Start, End: s.End, Text: s.Text})
	}
	var transcriptionText strings.Builder
	for _, s := range out.Transcription {
		start, end := s.Offsets.From/1000, s.Offsets.To/1000
		if s.Timestamps.From != "" {
			if parsed, ok := parseWhisperTimestamp(s.Timestamps.From); ok {
				start = parsed
			}
		}
		if s.Timestamps.To != "" {
			if parsed, ok := parseWhisperTimestamp(s.Timestamps.To); ok {
				end = parsed
			}
		}
		parsed.Segments = append(parsed.Segments, transcribeSegment{Start: start, End: end, Text: s.Text})
		transcriptionText.WriteString(s.Text)
		if end > parsed.Duration {
			parsed.Duration = end
		}
	}
	if transcriptionText.Len() > 0 {
		parsed.Text += transcriptionText.String()
	}
	return parsed, nil
}

func parseWhisperTimestamp(value string) (float64, bool) {
	var h, m, s, ms int
	if _, err := fmt.Sscanf(value, "%d:%d:%d,%d", &h, &m, &s, &ms); err != nil {
		if _, err := fmt.Sscanf(value, "%d:%d:%d.%d", &h, &m, &s, &ms); err != nil {
			return 0, false
		}
	}
	return float64(h*3600+m*60+s) + float64(ms)/1000, true
}

func (t *transcribeTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the audio file (WAV, MP3, FLAC native; others are converted via ffmpeg).",
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
	if err := confineIfRestricted(t.restrictToCWD, args.Path); err != nil {
		return jsonError(err.Error())
	}

	// Security: classify the audio file path
	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "transcribe", Resource: args.Path, Risk: classifyResolvedPath(args.Path),
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
	// Provider mode skips all local whisper handling entirely.
	source := "transcribe:" + args.Path
	if t.speech != nil {
		return t.transcribeProvider(args, source)
	}
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

	outputDir, err := os.MkdirTemp("", "odek-transcribe-json-")
	if err != nil {
		return jsonResult(transcribeResult{Error: fmt.Sprintf("cannot create whisper output directory: %v", err)})
	}
	defer os.RemoveAll(outputDir)

	args2 := []string{
		"--model", modelPathResolved,
		"--output-json",
		"--output-file", filepath.Join(outputDir, "result"),
		"--file", wavPath,
	}
	if lang != "" {
		args2 = append(args2, "--language", lang)
	}

	const maxWhisperOutputBytes = 10 << 20 // 10 MiB
	cmd := exec.CommandContext(t.toolCtx(), binary, args2...)
	output, err := cmd.Output()
	if len(output) > maxWhisperOutputBytes {
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

	// whisper.cpp writes --output-json to a sidecar named after --output-file;
	// older wrappers (and some test binaries) still print JSON on stdout.
	jsonOutput := output
	hasSidecar := false
	if sidecar, readErr := readWhisperJSON(filepath.Join(outputDir, "result.json"), maxWhisperOutputBytes); readErr == nil {
		jsonOutput = sidecar
		hasSidecar = true
	} else if !os.IsNotExist(readErr) {
		return jsonResult(transcribeResult{Error: fmt.Sprintf("cannot read whisper JSON output: %v", readErr)})
	}
	whisperOut, parseErr := parseWhisperJSON(jsonOutput)
	if parseErr != nil {
		if hasSidecar {
			return jsonResult(transcribeResult{Error: fmt.Sprintf("failed to parse whisper output JSON: %v", parseErr)})
		}
		// whisper output may not be valid JSON depending on version
		// Fallback: use raw text output
		return jsonResult(transcribeResult{
			Text:     strings.TrimSpace(string(output)),
			Duration: 0,
			Model:    filepath.Base(modelPathResolved),
		})
	}

	// Convert segments
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
