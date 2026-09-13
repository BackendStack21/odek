package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
)

// ── Transcribe Tests ─────────────────────────────────────────────────

// TestTranscribe_MissingBinary tests that transcribe returns a clear
// setup instruction when whisper is not installed.
func TestTranscribe_MissingBinary(t *testing.T) {
	// Use an empty binary_path that will fail
	tool := newTranscribeTool(danger.DangerousConfig{}, config.TranscriptionConfig{
		BinaryPath: "/nonexistent/whisper",
	})

	dir := t.TempDir()
	path := filepath.Join(dir, "test.ogg")
	os.WriteFile(path, []byte("fake audio data"), 0644)

	args := fmt.Sprintf(`{"path":"%s"}`, path)
	result, err := tool.Call(args)
	if err == nil {
		t.Fatal("expected typed operation failure alongside result")
	}

	var r struct {
		Text  string `json:"text"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(result), &r); err != nil {
		t.Fatalf("unmarshal failed: %v\nraw: %s", err, result)
	}
	if !strings.Contains(r.Error, "whisper") {
		t.Errorf("expected error mentioning 'whisper', got: %s", r.Error)
	}
}

// TestTranscribe_MissingModel tests that transcribe returns a clear
// setup instruction when the model file is not found.
func TestTranscribe_MissingModel(t *testing.T) {
	// Use a mock binary that exists but no model
	mockWhisper := createMockWhisper(t)
	tool := newTranscribeTool(danger.DangerousConfig{}, config.TranscriptionConfig{
		BinaryPath: mockWhisper,
		ModelsDir:  t.TempDir(), // empty directory
	})

	dir := t.TempDir()
	path := filepath.Join(dir, "test.ogg")
	os.WriteFile(path, []byte("fake audio data"), 0644)

	args := fmt.Sprintf(`{"path":"%s"}`, path)
	result, err := tool.Call(args)
	if err == nil {
		t.Fatal("expected typed operation failure alongside result")
	}

	var r struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(result), &r); err != nil {
		t.Fatalf("unmarshal failed: %v\nraw: %s", err, result)
	}
	if !strings.Contains(r.Error, "model") && !strings.Contains(r.Error, "not found") {
		t.Errorf("expected error about missing model, got: %s", r.Error)
	}
}

// TestTranscribe_FileNotFound tests that transcribe errors on missing audio file.
func TestTranscribe_FileNotFound(t *testing.T) {
	tool := newTranscribeTool(danger.DangerousConfig{}, config.TranscriptionConfig{})
	args := `{"path":"/nonexistent/audio.ogg"}`
	result, err := tool.Call(args)
	if err == nil {
		t.Fatal("expected typed operation failure alongside result")
	}

	var r struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(result), &r); err != nil {
		t.Fatalf("unmarshal failed: %v\nraw: %s", err, result)
	}
	if !strings.Contains(r.Error, "cannot open") {
		t.Errorf("expected error mentioning 'cannot open', got: %s", r.Error)
	}
}

// TestTranscribe_EmptyPath tests that transcribe rejects empty path.
func TestTranscribe_EmptyPath(t *testing.T) {
	tool := newTranscribeTool(danger.DangerousConfig{}, config.TranscriptionConfig{})
	result, err := tool.Call(`{"path":""}`)
	if err == nil {
		t.Fatal("expected typed operation failure alongside result")
	}
	var r struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(result), &r); err != nil {
		t.Fatalf("unmarshal failed: %v\nraw: %s", err, result)
	}
	if !strings.Contains(r.Error, "required") {
		t.Errorf("expected error mentioning 'required', got: %s", r.Error)
	}
}

// TestTranscribe_InvalidJSON tests invalid input.
func TestTranscribe_InvalidJSON(t *testing.T) {
	tool := newTranscribeTool(danger.DangerousConfig{}, config.TranscriptionConfig{})
	result, err := tool.Call(`{bad json}`)
	if err != nil {
		return // error is acceptable
	}
	var r struct {
		Error string `json:"error"`
	}
	json.Unmarshal([]byte(result), &r)
	if !strings.Contains(r.Error, "invalid") {
		t.Errorf("expected 'invalid' in error, got: %s", r.Error)
	}
}

// TestTranscribe_SymlinkRejected tests that transcribe rejects symlinks.
func TestTranscribe_SymlinkRejected(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.txt")
	os.WriteFile(target, []byte("data"), 0644)
	link := filepath.Join(dir, "link.ogg")
	os.Symlink(target, link)

	tool := newTranscribeTool(danger.DangerousConfig{}, config.TranscriptionConfig{})
	args := fmt.Sprintf(`{"path":"%s"}`, link)
	result, err := tool.Call(args)
	if err == nil {
		t.Fatal("expected typed operation failure alongside result")
	}
	var r struct {
		Error string `json:"error"`
	}
	json.Unmarshal([]byte(result), &r)
	if r.Error == "" {
		t.Error("expected error for symlink path")
	}
}

// createMockWhisper creates a tiny shell script that acts as a mock whisper binary.
// It outputs JSON in whisper.cpp format for testing.
func createMockWhisper(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Determine shell: use sh on Unix
	shell := "sh"
	if _, err := exec.LookPath("sh"); err != nil {
		if p, err := exec.LookPath("bash"); err == nil {
			shell = p
		} else {
			t.Skip("no shell available for mock whisper")
		}
	}

	// Write a script that outputs valid whisper JSON
	_ = shell // suppress unused warning — we use exec.LookPath to check availability
	script := `#!/bin/sh
echo '{
  "text": " Hello world this is a test transcription. ",
  "language": "en",
  "duration": 3.5,
  "segments": [
    {"start": 0.0, "end": 1.0, "text": " Hello world"},
    {"start": 1.0, "end": 3.5, "text": " this is a test transcription. "}
  ]
}'`

	scriptPath := filepath.Join(dir, "mock-whisper")
	os.WriteFile(scriptPath, []byte(script), 0755)
	return scriptPath
}

// TestTranscribe_MockHappyPath tests with a real mock binary that outputs
// valid whisper JSON. This validates the full parse path.
func TestTranscribe_MockHappyPath(t *testing.T) {
	mockWhisper := createMockWhisper(t)
	modelsDir := t.TempDir()

	// Create fake model file
	modelPath := filepath.Join(modelsDir, "ggml-tiny.bin")
	os.WriteFile(modelPath, []byte("fake model"), 0644)

	// Create fake audio file
	audioPath := filepath.Join(t.TempDir(), "voice.ogg")
	os.WriteFile(audioPath, []byte("fake ogg"), 0644)

	tool := newTranscribeTool(danger.DangerousConfig{}, config.TranscriptionConfig{
		BinaryPath: mockWhisper,
		ModelsDir:  modelsDir,
		Model:      "tiny",
	})

	args := fmt.Sprintf(`{"path":"%s"}`, audioPath)
	result, err := tool.Call(args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var r struct {
		Text     string  `json:"text"`
		Duration float64 `json:"duration_sec"`
		Language string  `json:"language"`
		Model    string  `json:"model"`
		Error    string  `json:"error"`
	}
	if err := json.Unmarshal([]byte(result), &r); err != nil {
		t.Fatalf("unmarshal failed: %v\nraw: %s", err, result)
	}
	if r.Error != "" {
		t.Fatalf("expected success, got error: %s", r.Error)
	}
	if !strings.Contains(strings.ToLower(r.Text), "hello world") {
		t.Errorf("text = %q, should contain 'hello world'", r.Text)
	}
	if r.Duration != 3.5 {
		t.Errorf("duration = %f, want 3.5", r.Duration)
	}
	if r.Language != "en" {
		t.Errorf("language = %q, want 'en'", r.Language)
	}
	if r.Model != "tiny" {
		t.Errorf("model = %q, want 'tiny'", r.Model)
	}
}
