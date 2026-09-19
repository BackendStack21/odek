package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	_ "golang.org/x/image/webp"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/budget"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/events"
)

var videoExts = map[string]bool{
	".mp4": true, ".mov": true, ".avi": true, ".mkv": true,
	".webm": true, ".m4v": true, ".flv": true, ".wmv": true,
}

// llamaMtmdBinary locates the llama-mtmd-cli binary.
// Priority: cfg.BinaryPath > PATH search.
func llamaMtmdBinary(cfg config.VisionConfig) (string, error) {
	if cfg.BinaryPath != "" {
		if _, err := os.Stat(cfg.BinaryPath); err == nil {
			return cfg.BinaryPath, nil
		}
		return "", fmt.Errorf("llama-mtmd-cli not found at configured path %q", cfg.BinaryPath)
	}
	if path, err := exec.LookPath("llama-mtmd-cli"); err == nil {
		return path, nil
	}
	fmt.Fprintln(os.Stderr, `llama-mtmd-cli not found on PATH.

The vision tool requires llama.cpp's multimodal CLI (build b9549+).

To install manually:
  git clone --depth 1 --branch b9549 https://github.com/ggerganov/llama.cpp
  cd llama.cpp
  cmake -B build -DCMAKE_BUILD_TYPE=Release -DGGML_NATIVE=OFF -DLLAMA_CURL=OFF
  cmake --build build -j$(nproc) --target llama-mtmd-cli
  install build/bin/llama-mtmd-cli /usr/local/bin/

Or set binary_path in the vision config.`)
	return "", fmt.Errorf("llama-mtmd-cli not found")
}

// visionModelPaths resolves the model.gguf and mmproj.gguf paths.
// Priority: cfg.ModelsDir > Docker image path > ~/.odek/minicpm-v/models.
func visionModelPaths(cfg config.VisionConfig) (modelPath, mmprojPath string, err error) {
	dir := cfg.ModelsDir
	if dir == "" {
		// Docker image baked path (see docker/Dockerfile minicpm stage)
		const dockerPath = "/usr/local/share/minicpm-v/models"
		if _, statErr := os.Stat(filepath.Join(dockerPath, "model.gguf")); statErr == nil {
			dir = dockerPath
		} else {
			home, homeErr := os.UserHomeDir()
			if homeErr != nil {
				return "", "", fmt.Errorf("cannot determine home directory: %v", homeErr)
			}
			dir = filepath.Join(home, ".odek", "minicpm-v", "models")
		}
	}

	mp := filepath.Join(dir, "model.gguf")
	mmp := filepath.Join(dir, "mmproj.gguf")

	if _, err := os.Stat(mp); err != nil {
		fmt.Fprintf(os.Stderr, `MiniCPM-V model not found at %q.

Download and install:
  mkdir -p %s
  cd %s
  curl -LO "https://huggingface.co/openbmb/MiniCPM-V-4_6-gguf/resolve/78e02f066e9819a60573b78a4275df8a0c27f698/MiniCPM-V-4_6-Q4_K_M.gguf"
  mv MiniCPM-V-4_6-Q4_K_M.gguf model.gguf
  curl -LO "https://huggingface.co/openbmb/MiniCPM-V-4_6-gguf/resolve/78e02f066e9819a60573b78a4275df8a0c27f698/mmproj-model-f16.gguf"
  mv mmproj-model-f16.gguf mmproj.gguf

Or set models_dir in the vision config.`, mp, dir, dir)
		return "", "", fmt.Errorf("MiniCPM-V model not found")
	}
	if _, err := os.Stat(mmp); err != nil {
		return "", "", fmt.Errorf("MiniCPM-V projector not found at %q — download mmproj-model-f16.gguf to %s and rename to mmproj.gguf", mmp, dir)
	}
	return mp, mmp, nil
}

// extractVideoFrames samples n evenly-spaced frames from videoPath into a
// temporary directory. Returns paths to the JPEG frame files; caller must
// remove the directory (filepath.Dir of the first path).
func extractVideoFrames(ctx context.Context, videoPath string, n int) ([]string, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, fmt.Errorf("ffmpeg not found — required for video frame extraction")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		return nil, fmt.Errorf("ffprobe not found — required to read video duration")
	}

	// Get duration with ffprobe
	out, err := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "csv=p=0",
		videoPath,
	).Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe failed: %v", err)
	}
	var duration float64
	fmt.Sscanf(strings.TrimSpace(string(out)), "%f", &duration)
	if duration <= 0 {
		duration = 60
	}

	tmpDir, err := os.MkdirTemp("", "odek-vision-*")
	if err != nil {
		return nil, fmt.Errorf("cannot create temp dir: %v", err)
	}

	// Extract frames at evenly-spaced timestamps, avoiding the very start/end
	interval := duration / float64(n+1)
	var frames []string
	for i := 1; i <= n; i++ {
		ts := interval * float64(i)
		out := filepath.Join(tmpDir, fmt.Sprintf("frame_%02d.jpg", i))
		cmd := exec.CommandContext(ctx, "ffmpeg",
			"-ss", fmt.Sprintf("%.3f", ts),
			"-i", videoPath,
			"-frames:v", "1",
			"-q:v", "2",
			"-y",
			out,
		)
		if cmd.Run() == nil {
			frames = append(frames, out)
		}
	}

	if len(frames) == 0 {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("no frames could be extracted from %q", videoPath)
	}
	return frames, nil
}

// runLlamaMtmd calls llama-mtmd-cli in single-turn mode with one or more images
// and returns the trimmed stdout response.
func runLlamaMtmd(ctx context.Context, binary, modelPath, mmprojPath, prompt string, imagePaths []string) (string, error) {
	args := []string{
		"-m", modelPath,
		"--mmproj", mmprojPath,
		"-c", "4096",
		"--temp", "0.7",
		"--top-p", "0.8",
		"--top-k", "100",
		"--repeat-penalty", "1.05",
		"-n", strconv.Itoa(1024),
		"-p", prompt,
	}
	for _, img := range imagePaths {
		args = append(args, "--image", img)
	}

	cmd := exec.CommandContext(ctx, binary, args...)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("llama-mtmd-cli failed (exit %d): %s",
				exitErr.ExitCode(), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("llama-mtmd-cli failed: %v", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// ═════════════════════════════════════════════════════════════════════════
// vision Tool
// ═════════════════════════════════════════════════════════════════════════

type visionTool struct {
	ctxTool
	dangerousConfig danger.DangerousConfig
	visionCfg       config.VisionConfig
	restrictToCWD   bool // sandbox: reject paths that escape the workspace
	analyzer        visionAnalyzer
	approver        danger.Approver
}

type visionMedia struct {
	MIME string
	Data []byte
}

type visionAnalysis struct {
	Text  string
	Model string
}

type visionAnalyzer interface {
	AnalyzeVision(context.Context, string, string, []visionMedia) (visionAnalysis, error)
}

func visionFailure(result visionResult, err error) (string, error) {
	raw, _ := jsonResult(result)
	return raw, err
}

func newVisionTool(dc danger.DangerousConfig, vc config.VisionConfig) *visionTool {
	return &visionTool{dangerousConfig: dc, visionCfg: vc}
}

func (t *visionTool) SetAnalyzer(a visionAnalyzer)  { t.analyzer = a }
func (t *visionTool) SetApprover(a danger.Approver) { t.approver = a }

func (t *visionTool) SetBudgetView(v budget.View) {
	if a, ok := t.analyzer.(interface{ SetBudgetView(budget.View) }); ok {
		a.SetBudgetView(v)
	}
}

func (t *visionTool) SetEventEmitter(fn func(events.Event)) {
	if a, ok := t.analyzer.(interface{ SetEventEmitter(func(events.Event)) }); ok {
		a.SetEventEmitter(fn)
	}
}

func (t *visionTool) Name() string { return "vision" }
func (t *visionTool) Description() string {
	return `Analyze an image or video file using MiniCPM-V 4.6, a local 1.3B multimodal model (llama-mtmd-cli). Images are described directly; videos are sampled into evenly-spaced frames and analyzed together. Image formats: JPEG, PNG, GIF, WebP, BMP. Video formats: MP4, MOV, AVI, MKV, WebM — video analysis additionally requires ffmpeg and ffprobe in PATH; if video fails while images work, convert or extract frames instead of retrying. Requires llama-mtmd-cli and MiniCPM-V 4.6 model files (bundled in the Docker image).`
}

type visionArgs struct {
	Path   string `json:"path"`
	Prompt string `json:"prompt,omitempty"`
}

type visionResult struct {
	Description string `json:"description"`
	Model       string `json:"model"`
	Type        string `json:"type"` // "image" or "video"
	Frames      int    `json:"frames,omitempty"`
	Error       string `json:"error,omitempty"`
}

func (t *visionTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to an image (JPEG, PNG, GIF, WebP, BMP) or video file (MP4, MOV, AVI, MKV, WebM).",
			},
			"prompt": map[string]any{
				"type":        "string",
				"description": `Instruction or question for the model. Default: "Describe this in detail."`,
			},
		},
		"required": []string{"path"},
	}
}

const maxVisionPayload = 10 << 20

func stageVisionInput(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("vision input is not a regular file")
	}
	if info.Size() > maxFileReadBytes {
		return "", fmt.Errorf("file too large (%d bytes, max %d)", info.Size(), maxFileReadBytes)
	}
	tmp, err := os.CreateTemp("", "odek-vision-input-*"+filepath.Ext(path))
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	var copied int64
	copied, err = io.Copy(tmp, io.LimitReader(f, maxFileReadBytes+1))
	if err != nil {
		tmp.Close()
		return "", err
	}
	if err = tmp.Close(); err != nil {
		return "", err
	}
	if copied > maxFileReadBytes {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("file too large (max %d bytes)", maxFileReadBytes)
	}
	return tmpPath, nil
}

func readVisionMedia(path string) ([]byte, string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() {
		return nil, "", fmt.Errorf("vision input is not a regular file")
	}
	if info.Size() > maxVisionPayload {
		return nil, "", fmt.Errorf("file too large (%d bytes, max %d)", info.Size(), maxVisionPayload)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxVisionPayload+1))
	if err != nil {
		return nil, "", err
	}
	if len(b) > maxVisionPayload {
		return nil, "", fmt.Errorf("file too large (max %d bytes)", maxVisionPayload)
	}
	mimeType := http.DetectContentType(b)
	switch mimeType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
	default:
		return nil, "", fmt.Errorf("unsupported image type %q for provider vision", mimeType)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, "", fmt.Errorf("invalid image: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 8192 || cfg.Height > 8192 || int64(cfg.Width)*int64(cfg.Height) > 40_000_000 {
		return nil, "", fmt.Errorf("image dimensions exceed limits")
	}
	return b, mimeType, nil
}

func (t *visionTool) Call(argsJSON string) (string, error) {
	return t.CallContext(t.toolCtx(), argsJSON)
}

func (t *visionTool) CallContext(ctx context.Context, argsJSON string) (result string, err error) {
	if err := config.ValidateVisionConfig(t.visionCfg); err != nil {
		return jsonError(err.Error())
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("vision: panic: %v", r)
			result = `{"error":"internal error"}`
		}
	}()

	var args visionArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}
	if args.Path == "" {
		return jsonError("path is required")
	}
	if err := confineIfRestricted(t.restrictToCWD, args.Path); err != nil {
		return jsonError(err.Error())
	}
	prompt := args.Prompt
	if prompt == "" {
		prompt = "Describe this in detail."
	}

	// Security: classify the file path
	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "vision", Resource: args.Path, Risk: classifyResolvedPath(args.Path),
	}, nil); err != nil {
		return jsonError(err.Error())
	}
	if strings.EqualFold(t.visionCfg.Backend, "provider") {
		if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
			Name: "vision", Resource: "provider:" + t.visionCfg.Provider, Risk: danger.NetworkEgress,
		}, nil); err != nil {
			return jsonError(err.Error())
		}
		if t.analyzer == nil {
			return visionFailure(visionResult{Error: "vision provider backend is not configured"}, fmt.Errorf("vision provider backend is not configured"))
		}
		ext := strings.ToLower(filepath.Ext(args.Path))
		kind := "image"
		media := make([]visionMedia, 0, 1)
		providerPrompt := prompt
		totalPayload := 0
		if videoExts[ext] {
			kind = "video"
			stagedVideo, serr := stageVisionInput(args.Path)
			if serr != nil {
				return visionFailure(visionResult{Error: serr.Error(), Type: kind}, serr)
			}
			defer os.Remove(stagedVideo)
			n := t.visionCfg.VideoFrames
			if n <= 0 {
				n = 8
			}
			frames, ferr := extractVideoFrames(ctx, stagedVideo, n)
			if ferr != nil {
				return visionFailure(visionResult{Error: ferr.Error(), Type: kind}, ferr)
			}
			defer os.RemoveAll(filepath.Dir(frames[0]))
			providerPrompt = fmt.Sprintf("These are %d frames sampled evenly from a video. %s", len(frames), prompt)
			for _, frame := range frames {
				data, mediaType, rerr := readVisionMedia(frame)
				if rerr != nil {
					return visionFailure(visionResult{Error: rerr.Error(), Type: kind}, rerr)
				}
				totalPayload += len(data)
				if totalPayload > maxVisionPayload {
					err := fmt.Errorf("video frames exceed provider payload limit (%d bytes)", maxVisionPayload)
					return visionFailure(visionResult{Error: err.Error(), Type: kind}, err)
				}
				media = append(media, visionMedia{MIME: mediaType, Data: data})
			}
		} else {
			data, mediaType, rerr := readVisionMedia(args.Path)
			if rerr != nil {
				return visionFailure(visionResult{Error: rerr.Error(), Type: kind}, rerr)
			}
			media = append(media, visionMedia{MIME: mediaType, Data: data})
		}
		analysis, err := t.analyzer.AnalyzeVision(ctx, t.visionCfg.Model, providerPrompt, media)
		if err != nil {
			return visionFailure(visionResult{Error: err.Error(), Model: analysis.Model, Type: kind}, err)
		}
		frames := 0
		if kind == "video" {
			frames = len(media)
		}
		return jsonResult(visionResult{Description: wrapUntrusted(ctx, "vision:"+args.Path, analysis.Text), Model: analysis.Model, Type: kind, Frames: frames})
	}

	// Stage the verified input so the local subprocess and ffmpeg read a
	// private snapshot rather than reopening a path that may have changed.
	stagedPath, err := stageVisionInput(args.Path)
	if err != nil {
		return visionFailure(visionResult{Error: fmt.Sprintf("cannot stage file %q: %v", args.Path, err)}, err)
	}
	defer os.Remove(stagedPath)

	binary, err := llamaMtmdBinary(t.visionCfg)
	if err != nil {
		return visionFailure(visionResult{Error: err.Error()}, err)
	}
	modelPath, mmprojPath, err := visionModelPaths(t.visionCfg)
	if err != nil {
		return visionFailure(visionResult{Error: err.Error()}, err)
	}

	ext := strings.ToLower(filepath.Ext(args.Path))
	source := "vision:" + args.Path

	if videoExts[ext] {
		return t.analyzeVideo(ctx, binary, modelPath, mmprojPath, stagedPath, prompt, source)
	}
	return t.analyzeImage(ctx, binary, modelPath, mmprojPath, stagedPath, prompt, source)
}

func (t *visionTool) analyzeImage(ctx context.Context, binary, modelPath, mmprojPath, imgPath, prompt, source string) (string, error) {
	desc, err := runLlamaMtmd(ctx, binary, modelPath, mmprojPath, prompt, []string{imgPath})
	if err != nil {
		return visionFailure(visionResult{Error: err.Error()}, err)
	}
	return jsonResult(visionResult{
		Description: wrapUntrusted(ctx, source, desc),
		Model:       "minicpm-v-4.6",
		Type:        "image",
	})
}

func (t *visionTool) analyzeVideo(ctx context.Context, binary, modelPath, mmprojPath, videoPath, prompt, source string) (string, error) {
	n := t.visionCfg.VideoFrames
	if n <= 0 {
		n = 8
	}

	frames, err := extractVideoFrames(ctx, videoPath, n)
	if err != nil {
		return visionFailure(visionResult{Error: err.Error(), Type: "video"}, err)
	}
	defer os.RemoveAll(filepath.Dir(frames[0]))

	videoPrompt := fmt.Sprintf(
		"These are %d frames sampled evenly from a video. %s",
		len(frames), prompt,
	)
	desc, err := runLlamaMtmd(ctx, binary, modelPath, mmprojPath, videoPrompt, frames)
	if err != nil {
		return visionFailure(visionResult{Error: err.Error(), Type: "video", Frames: len(frames)}, err)
	}
	return jsonResult(visionResult{
		Description: wrapUntrusted(ctx, source, desc),
		Model:       "minicpm-v-4.6",
		Type:        "video",
		Frames:      len(frames),
	})
}

// Ensure visionTool implements odek.Tool
var _ odek.Tool = (*visionTool)(nil)
