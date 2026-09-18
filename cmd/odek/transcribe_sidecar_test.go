package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
)

func TestTranscribe_ReadsWhisperJSONSidecar(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "output-path")
	t.Setenv("ODEK_TEST_WHISPER_MARKER", marker)
	binary := filepath.Join(dir, "whisper-sidecar")
	script := `#!/bin/sh
out=""
input=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output-file" ]; then shift; out="$1"; fi
  if [ "$1" = "--file" ]; then shift; input="$1"; fi
  shift
done
if [ -z "$out" ]; then out="$input"; fi
printf '%s' "$out" > "$ODEK_TEST_WHISPER_MARKER"
cat > "$out.json" <<'JSON'
{"result":{"language":"en"},"transcription":[
  {"timestamps":{"from":"00:00:00,500","to":"00:00:01,250"},"offsets":{"from":500,"to":1250},"text":" hello"},
  {"timestamps":{"from":"00:00:01,250","to":"00:00:02,750"},"offsets":{"from":1250,"to":2750},"text":" sidecar"}
]}
JSON
echo 'whisper progress on stdout'`
	if err := os.WriteFile(binary, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	models := t.TempDir()
	if err := os.WriteFile(filepath.Join(models, "ggml-tiny.bin"), []byte("model"), 0600); err != nil {
		t.Fatal(err)
	}
	audio := filepath.Join(dir, "input.wav")
	if err := os.WriteFile(audio, []byte("audio"), 0600); err != nil {
		t.Fatal(err)
	}

	tool := newTranscribeTool(danger.DangerousConfig{}, config.TranscriptionConfig{
		BinaryPath: binary,
		ModelsDir:  models,
		Model:      "tiny",
	})
	result, err := tool.Call(`{"path":"` + audio + `"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var parsed struct {
		Text     string              `json:"text"`
		Lang     string              `json:"language"`
		Duration float64             `json:"duration_sec"`
		Segments []transcribeSegment `json:"segments"`
		Error    string              `json:"error"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("invalid result: %v\n%s", err, result)
	}
	if parsed.Error != "" || !strings.Contains(parsed.Text, "hello sidecar") || parsed.Lang != "en" {
		t.Fatalf("unexpected result: %+v", parsed)
	}
	if parsed.Duration != 2.75 || len(parsed.Segments) != 2 || parsed.Segments[1].Start != 1.25 {
		t.Fatalf("unexpected timing: %+v", parsed)
	}
	if _, err := os.Stat(audio + ".json"); !os.IsNotExist(err) {
		t.Fatalf("whisper output escaped private directory: stat=%v", err)
	}
	outputPath, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("mock did not record output path: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(string(outputPath))); !os.IsNotExist(err) {
		t.Fatalf("private whisper output directory was not cleaned: stat=%v", err)
	}
}

func TestTranscribe_RejectsMalformedOrOversizedSidecar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	if err := os.WriteFile(path, []byte(`{"transcription":[`), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := readWhisperJSON(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseWhisperJSON(data); err == nil {
		t.Fatal("malformed sidecar was accepted")
	}
	if err := os.WriteFile(path, []byte("12345"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readWhisperJSON(path, 4); err == nil {
		t.Fatal("oversized sidecar was accepted")
	}
}
