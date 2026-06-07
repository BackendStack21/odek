package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
)

// ── Helpers ──────────────────────────────────────────────────────────────

// createMockLlamaMtmd creates a shell script that mimics llama-mtmd-cli
// in single-turn mode: it ignores all flags and writes a fixed description
// to stdout. This lets tests exercise the full tool.Call() path without a
// real model or GPU.
func createMockLlamaMtmd(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
echo 'A vivid test scene with colorful objects arranged neatly. The foreground shows a simple geometric shape and the background is uniformly lit.'
`
	path := filepath.Join(dir, "llama-mtmd-cli")
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("createMockLlamaMtmd: %v", err)
	}
	return path
}

// createMockFftools creates mock ffprobe and ffmpeg binaries in a temp dir
// and returns the dir. Prepend it to PATH so extractVideoFrames uses them.
//
//	ffprobe: outputs a fixed duration (10.0 seconds)
//	ffmpeg:  creates a minimal JPEG stub at the last argument path
func createMockFftools(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	ffprobe := `#!/bin/sh
echo '10.000000'
`
	// ffmpeg mock: the output path is always the last argument.
	// "shift $(($# - 1))" leaves $1 as the last original arg.
	ffmpeg := `#!/bin/sh
shift $(($# - 1))
mkdir -p "$(dirname "$1")"
printf '\xff\xd8\xff\xe0' > "$1"
`
	for name, body := range map[string]string{"ffprobe": ffprobe, "ffmpeg": ffmpeg} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0755); err != nil {
			t.Fatalf("createMockFftools: %v", err)
		}
	}
	return dir
}

// fakeModelsDir creates a directory with stub model.gguf and mmproj.gguf files.
func fakeModelsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "model.gguf"), []byte("fake model"), 0644)
	os.WriteFile(filepath.Join(dir, "mmproj.gguf"), []byte("fake mmproj"), 0644)
	return dir
}

// fakeImageFile writes a minimal file with a supported image extension.
func fakeImageFile(t *testing.T, ext string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test"+ext)
	os.WriteFile(path, []byte("fake image data"), 0644)
	return path
}

// fakeVideoFile writes a minimal file with a supported video extension.
func fakeVideoFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.mp4")
	os.WriteFile(path, []byte("fake video data"), 0644)
	return path
}

// decodeVisionResult is a convenience wrapper for parsing tool output.
func decodeVisionResult(t *testing.T, raw string) visionResult {
	t.Helper()
	var r visionResult
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("decodeVisionResult: unmarshal failed: %v\nraw: %s", err, raw)
	}
	return r
}

// ── Unit Tests ────────────────────────────────────────────────────────────

func TestVision_EmptyPath(t *testing.T) {
	tool := newVisionTool(danger.DangerousConfig{}, config.VisionConfig{})
	result, err := tool.Call(`{"path":""}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var r struct{ Error string `json:"error"` }
	json.Unmarshal([]byte(result), &r)
	if !strings.Contains(r.Error, "required") {
		t.Errorf("expected 'required' in error, got: %s", r.Error)
	}
}

func TestVision_InvalidJSON(t *testing.T) {
	tool := newVisionTool(danger.DangerousConfig{}, config.VisionConfig{})
	result, err := tool.Call(`{bad json}`)
	if err != nil {
		return // error return is also acceptable
	}
	var r struct{ Error string `json:"error"` }
	json.Unmarshal([]byte(result), &r)
	if !strings.Contains(r.Error, "invalid") {
		t.Errorf("expected 'invalid' in error, got: %s", r.Error)
	}
}

func TestVision_FileNotFound(t *testing.T) {
	tool := newVisionTool(danger.DangerousConfig{}, config.VisionConfig{})
	result, err := tool.Call(`{"path":"/nonexistent/image.jpg"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	r := decodeVisionResult(t, result)
	if !strings.Contains(r.Error, "cannot open") {
		t.Errorf("expected 'cannot open' in error, got: %s", r.Error)
	}
}

func TestVision_SymlinkRejected(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.png")
	os.WriteFile(target, []byte("data"), 0644)
	link := filepath.Join(dir, "link.jpg")
	os.Symlink(target, link)

	tool := newVisionTool(danger.DangerousConfig{}, config.VisionConfig{})
	result, err := tool.Call(fmt.Sprintf(`{"path":"%s"}`, link))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	r := decodeVisionResult(t, result)
	if r.Error == "" {
		t.Error("expected an error for symlink path, got none")
	}
}

func TestVision_MissingBinary(t *testing.T) {
	tool := newVisionTool(danger.DangerousConfig{}, config.VisionConfig{
		BinaryPath: "/nonexistent/llama-mtmd-cli",
	})
	path := fakeImageFile(t, ".jpg")
	result, err := tool.Call(fmt.Sprintf(`{"path":"%s"}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	r := decodeVisionResult(t, result)
	if !strings.Contains(r.Error, "llama") && !strings.Contains(r.Error, "not found") {
		t.Errorf("expected error mentioning 'llama' or 'not found', got: %s", r.Error)
	}
}

func TestVision_MissingModel(t *testing.T) {
	tool := newVisionTool(danger.DangerousConfig{}, config.VisionConfig{
		BinaryPath: createMockLlamaMtmd(t),
		ModelsDir:  t.TempDir(), // empty — no model.gguf
	})
	path := fakeImageFile(t, ".jpg")
	result, err := tool.Call(fmt.Sprintf(`{"path":"%s"}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	r := decodeVisionResult(t, result)
	if !strings.Contains(r.Error, "model") && !strings.Contains(r.Error, "not found") {
		t.Errorf("expected error about missing model, got: %s", r.Error)
	}
}

func TestVision_MissingMmproj(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "model.gguf"), []byte("fake"), 0644)
	// mmproj.gguf intentionally absent

	tool := newVisionTool(danger.DangerousConfig{}, config.VisionConfig{
		BinaryPath: createMockLlamaMtmd(t),
		ModelsDir:  dir,
	})
	path := fakeImageFile(t, ".jpg")
	result, err := tool.Call(fmt.Sprintf(`{"path":"%s"}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	r := decodeVisionResult(t, result)
	if !strings.Contains(r.Error, "mmproj") && !strings.Contains(r.Error, "projector") {
		t.Errorf("expected error about missing mmproj, got: %s", r.Error)
	}
}

// ── Mock Happy-Path Tests ─────────────────────────────────────────────────

// TestVision_MockHappyPath_Image exercises the full image analysis flow
// with a mock llama-mtmd-cli binary — no GPU or real model required.
func TestVision_MockHappyPath_Image(t *testing.T) {
	tool := newVisionTool(danger.DangerousConfig{}, config.VisionConfig{
		BinaryPath: createMockLlamaMtmd(t),
		ModelsDir:  fakeModelsDir(t),
	})

	for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp"} {
		t.Run("ext="+ext, func(t *testing.T) {
			path := fakeImageFile(t, ext)
			result, err := tool.Call(fmt.Sprintf(`{"path":"%s"}`, path))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			r := decodeVisionResult(t, result)
			if r.Error != "" {
				t.Fatalf("expected success, got error: %s", r.Error)
			}
			if r.Type != "image" {
				t.Errorf("type = %q, want 'image'", r.Type)
			}
			if r.Model != "minicpm-v-4.6" {
				t.Errorf("model = %q, want 'minicpm-v-4.6'", r.Model)
			}
			if r.Description == "" {
				t.Error("description is empty")
			}
			if !strings.Contains(r.Description, "test scene") {
				t.Errorf("description = %q, expected mock output containing 'test scene'", r.Description)
			}
		})
	}
}

// TestVision_MockHappyPath_CustomPrompt verifies that a custom prompt is
// accepted (the mock binary ignores it, but the tool must not error).
func TestVision_MockHappyPath_CustomPrompt(t *testing.T) {
	tool := newVisionTool(danger.DangerousConfig{}, config.VisionConfig{
		BinaryPath: createMockLlamaMtmd(t),
		ModelsDir:  fakeModelsDir(t),
	})
	path := fakeImageFile(t, ".png")
	result, err := tool.Call(fmt.Sprintf(`{"path":"%s","prompt":"What text is visible?"}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	r := decodeVisionResult(t, result)
	if r.Error != "" {
		t.Fatalf("expected success, got error: %s", r.Error)
	}
	if r.Type != "image" {
		t.Errorf("type = %q, want 'image'", r.Type)
	}
}

// TestVision_MockHappyPath_Video exercises the full video analysis flow
// with mock ffprobe, ffmpeg, and llama-mtmd-cli. It verifies frame extraction
// and the multi-image prompt path end-to-end.
func TestVision_MockHappyPath_Video(t *testing.T) {
	ffDir := createMockFftools(t)
	// Prepend mock tool dir to PATH so exec.LookPath finds them first.
	orig := os.Getenv("PATH")
	t.Setenv("PATH", ffDir+string(os.PathListSeparator)+orig)

	tool := newVisionTool(danger.DangerousConfig{}, config.VisionConfig{
		BinaryPath:  createMockLlamaMtmd(t),
		ModelsDir:   fakeModelsDir(t),
		VideoFrames: 3,
	})

	videoPath := fakeVideoFile(t)
	result, err := tool.Call(fmt.Sprintf(`{"path":"%s"}`, videoPath))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	r := decodeVisionResult(t, result)
	if r.Error != "" {
		t.Fatalf("expected success, got error: %s", r.Error)
	}
	if r.Type != "video" {
		t.Errorf("type = %q, want 'video'", r.Type)
	}
	if r.Model != "minicpm-v-4.6" {
		t.Errorf("model = %q, want 'minicpm-v-4.6'", r.Model)
	}
	if r.Frames == 0 {
		t.Error("frames count is 0, expected > 0")
	}
	if r.Description == "" {
		t.Error("description is empty")
	}
}

// TestVision_VideoFallsBackOnMissingFfmpeg verifies that a missing ffmpeg
// produces a clear, actionable error rather than a panic.
func TestVision_VideoFallsBackOnMissingFfmpeg(t *testing.T) {
	// Point PATH at an empty dir so neither ffmpeg nor ffprobe are found.
	t.Setenv("PATH", t.TempDir())

	tool := newVisionTool(danger.DangerousConfig{}, config.VisionConfig{
		BinaryPath:  createMockLlamaMtmd(t),
		ModelsDir:   fakeModelsDir(t),
		VideoFrames: 4,
	})
	videoPath := fakeVideoFile(t)
	result, err := tool.Call(fmt.Sprintf(`{"path":"%s"}`, videoPath))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	r := decodeVisionResult(t, result)
	if !strings.Contains(r.Error, "ffmpeg") && !strings.Contains(r.Error, "ffprobe") {
		t.Errorf("expected error mentioning ffmpeg/ffprobe, got: %s", r.Error)
	}
}

// TestVision_SchemaShape verifies the JSON schema contains the required keys.
func TestVision_SchemaShape(t *testing.T) {
	tool := newVisionTool(danger.DangerousConfig{}, config.VisionConfig{})
	schema := tool.Schema()
	b, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	s := string(b)
	for _, want := range []string{`"path"`, `"prompt"`, `"required"`} {
		if !strings.Contains(s, want) {
			t.Errorf("schema missing %q; schema: %s", want, s)
		}
	}
}
