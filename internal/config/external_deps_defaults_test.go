package config

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRED_MediaAutoProcessingOffByDefault: Telegram voice auto-transcription
// needs whisper.cpp plus a model and photo auto-description needs a local
// MiniCPM-V stack (or a vision-capable provider model), so neither may be
// on unless the operator opts in. An absent section and a section without
// the field resolve the same way.
func TestRED_MediaAutoProcessingOffByDefault(t *testing.T) {
	if resolveTranscription(nil).AutoTranscribe {
		t.Error("auto_transcribe defaults to true with no transcription section")
	}
	if resolveTranscription(&TranscriptionConfig{Model: "tiny"}).AutoTranscribe {
		t.Error("auto_transcribe defaults to true with a section that omits it")
	}
	if resolveVision(nil).AutoDescribeEnabled() {
		t.Error("auto_describe defaults to true with no vision section")
	}
	if resolveVision(&VisionConfig{Backend: VisionBackendLocal}).AutoDescribeEnabled() {
		t.Error("auto_describe defaults to true with a section that omits it")
	}
	if !resolveTranscription(&TranscriptionConfig{AutoTranscribe: true}).AutoTranscribe {
		t.Error("explicit auto_transcribe=true was dropped")
	}
	on := true
	if !resolveVision(&VisionConfig{AutoDescribe: &on}).AutoDescribeEnabled() {
		t.Error("explicit auto_describe=true was dropped")
	}
}

// TestRED_WarnsOnEmptyToolsAllowlist: `odek init --global` used to write
// "tools": {"enabled": []}, an allowlist of nothing that silently disables
// every built-in tool. Existing configs keep that line, so the loader says
// so instead of leaving the agent mysteriously tool-less. Unset and null
// stay silent.
func TestRED_WarnsOnEmptyToolsAllowlist(t *testing.T) {
	for body, wantWarn := range map[string]bool{
		`{"tools":{"enabled":[]}}`:        true,
		`{"tools":{"enabled":null}}`:      false,
		`{"tools":{"disabled":[]}}`:       false,
		`{"tools":{"enabled":["shell"]}}`: false,
	} {
		dir := t.TempDir()
		t.Setenv("HOME", dir)
		t.Chdir(dir)
		writeGlobalConfig(t, dir, body)
		os.Chmod(filepath.Join(dir, ".odek", "config.json"), 0o600)
		stderr := captureLoaderStderr(t, func() { LoadConfig(CLIFlags{}) })
		got := strings.Contains(stderr, "tools.enabled is an empty list")
		if got != wantWarn {
			t.Errorf("%s: warned=%v, want %v; stderr:\n%s", body, got, wantWarn, stderr)
		}
	}
}

func captureLoaderStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()
	fn()
	w.Close()
	b, _ := io.ReadAll(r)
	return string(b)
}

// TestRED_EmptyAllowlistWarningHonoursOverrides: ODEK_TOOLS_ENABLED (or
// --tool) replaces the global empty list, so no warning is due then.
func TestRED_EmptyAllowlistWarningHonoursOverrides(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)
	writeGlobalConfig(t, dir, `{"tools":{"enabled":[]}}`)
	os.Chmod(filepath.Join(dir, ".odek", "config.json"), 0o600)
	t.Setenv("ODEK_TOOLS_ENABLED", "shell")
	stderr := captureLoaderStderr(t, func() { LoadConfig(CLIFlags{}) })
	if strings.Contains(stderr, "tools.enabled is an empty list") {
		t.Errorf("warned although ODEK_TOOLS_ENABLED overrides the empty list:\n%s", stderr)
	}
}
