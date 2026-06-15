package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
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
	return "", fmt.Errorf(`llama-mtmd-cli not found on PATH.

The vision tool requires llama.cpp's multimodal CLI (build b9549+).

To install manually:
  git clone --depth 1 --branch b9549 https://github.com/ggerganov/llama.cpp
  cd llama.cpp
  cmake -B build -DCMAKE_BUILD_TYPE=Release -DGGML_NATIVE=OFF -DLLAMA_CURL=OFF
  cmake --build build -j$(nproc) --target llama-mtmd-cli
  install build/bin/llama-mtmd-cli /usr/local/bin/

Or set binary_path in the vision config.`)
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
		return "", "", fmt.Errorf(`MiniCPM-V model not found at %q.

Download and install:
  mkdir -p %s
  cd %s
  curl -LO "https://huggingface.co/openbmb/MiniCPM-V-4_6-gguf/resolve/78e02f066e9819a60573b78a4275df8a0c27f698/MiniCPM-V-4_6-Q4_K_M.gguf"
  mv MiniCPM-V-4_6-Q4_K_M.gguf model.gguf
  curl -LO "https://huggingface.co/openbmb/MiniCPM-V-4_6-gguf/resolve/78e02f066e9819a60573b78a4275df8a0c27f698/mmproj-model-f16.gguf"
  mv mmproj-model-f16.gguf mmproj.gguf

Or set models_dir in the vision config.`, mp, dir, dir)
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
}

func newVisionTool(dc danger.DangerousConfig, vc config.VisionConfig) *visionTool {
	return &visionTool{dangerousConfig: dc, visionCfg: vc}
}

func (t *visionTool) Name() string { return "vision" }
func (t *visionTool) Description() string {
	return `Analyze an image or video file using MiniCPM-V 4.6, a local 1.3B multimodal model (llama-mtmd-cli). Images are described directly; videos are sampled into evenly-spaced frames and analyzed together. Supports JPEG, PNG, GIF, WebP, BMP for images and MP4, MOV, AVI, MKV, WebM for video. Requires llama-mtmd-cli and MiniCPM-V 4.6 model files (bundled in the Docker image).`
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

func (t *visionTool) Call(argsJSON string) (result string, err error) {
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
	prompt := args.Prompt
	if prompt == "" {
		prompt = "Describe this in detail."
	}

	// Security: classify the file path
	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "vision", Resource: args.Path, Risk: danger.ClassifyPath(args.Path),
	}, nil); err != nil {
		return jsonError(err.Error())
	}

	// Check file exists (O_NOFOLLOW prevents symlink attacks)
	f, err := os.OpenFile(args.Path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return jsonResult(visionResult{
			Error: fmt.Sprintf("cannot open file %q: %v", args.Path, err),
		})
	}
	f.Close()

	binary, err := llamaMtmdBinary(t.visionCfg)
	if err != nil {
		return jsonResult(visionResult{Error: err.Error()})
	}
	modelPath, mmprojPath, err := visionModelPaths(t.visionCfg)
	if err != nil {
		return jsonResult(visionResult{Error: err.Error()})
	}

	ext := strings.ToLower(filepath.Ext(args.Path))
	source := "vision:" + args.Path

	if videoExts[ext] {
		return t.analyzeVideo(binary, modelPath, mmprojPath, args.Path, prompt, source)
	}
	return t.analyzeImage(binary, modelPath, mmprojPath, args.Path, prompt, source)
}

func (t *visionTool) analyzeImage(binary, modelPath, mmprojPath, imgPath, prompt, source string) (string, error) {
	desc, err := runLlamaMtmd(t.toolCtx(), binary, modelPath, mmprojPath, prompt, []string{imgPath})
	if err != nil {
		return jsonResult(visionResult{Error: err.Error()})
	}
	return jsonResult(visionResult{
		Description: wrapUntrusted(t.toolCtx(), source, desc),
		Model:       "minicpm-v-4.6",
		Type:        "image",
	})
}

func (t *visionTool) analyzeVideo(binary, modelPath, mmprojPath, videoPath, prompt, source string) (string, error) {
	n := t.visionCfg.VideoFrames
	if n <= 0 {
		n = 8
	}

	frames, err := extractVideoFrames(t.toolCtx(), videoPath, n)
	if err != nil {
		return jsonResult(visionResult{Error: err.Error()})
	}
	defer os.RemoveAll(filepath.Dir(frames[0]))

	videoPrompt := fmt.Sprintf(
		"These are %d frames sampled evenly from a video. %s",
		len(frames), prompt,
	)
	desc, err := runLlamaMtmd(t.toolCtx(), binary, modelPath, mmprojPath, videoPrompt, frames)
	if err != nil {
		return jsonResult(visionResult{Error: err.Error()})
	}
	return jsonResult(visionResult{
		Description: wrapUntrusted(t.toolCtx(), source, desc),
		Model:       "minicpm-v-4.6",
		Type:        "video",
		Frames:      len(frames),
	})
}

// Ensure visionTool implements odek.Tool
var _ odek.Tool = (*visionTool)(nil)
