package config

// Regression tests for the 2026-08 security audit: transcription.binary_path
// and vision.binary_path flow into exec.Command in the transcribe/vision
// tools, so a cloned repo must not be able to point them at a planted
// binary through ./odek.json. The sections are operator-only, like
// telegram/memory/web_search.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfig_ProjectTranscriptionIgnored(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	globalDir := filepath.Join(dir, ".odek")
	os.MkdirAll(globalDir, 0755)

	// Project config tries to point the transcribe tool at a planted binary
	// and to auto-run it on every Telegram voice note.
	if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(`{
		"transcription": {
			"binary_path": "./tools/whisper",
			"auto_transcribe": true
		}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if cfg.Transcription.BinaryPath != "" {
		t.Errorf("Transcription.BinaryPath = %q, want empty (project transcription must be ignored)", cfg.Transcription.BinaryPath)
	}
	// Note: AutoTranscribe falls back to the built-in default (true) once
	// the project section is stripped — that flag only selects whether the
	// operator-configured (or PATH-resolved) binary runs, so it is benign
	// without control of binary_path.
}

func TestLoadConfig_ProjectVisionIgnored(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	globalDir := filepath.Join(dir, ".odek")
	os.MkdirAll(globalDir, 0755)

	if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(`{
		"vision": {
			"binary_path": "./tools/mtmd",
			"models_dir": "./tools/models"
		}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if cfg.Vision.BinaryPath != "" {
		t.Errorf("Vision.BinaryPath = %q, want empty (project vision must be ignored)", cfg.Vision.BinaryPath)
	}
	if cfg.Vision.ModelsDir != "" {
		t.Errorf("Vision.ModelsDir = %q, want empty (project vision must be ignored)", cfg.Vision.ModelsDir)
	}
}

func TestLoadConfig_GlobalTranscriptionStillApplies(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	globalDir := filepath.Join(dir, ".odek")
	os.MkdirAll(globalDir, 0755)
	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"transcription": {"binary_path": "/opt/whisper"},
		"vision": {"binary_path": "/opt/mtmd"}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if cfg.Transcription.BinaryPath != "/opt/whisper" {
		t.Errorf("Transcription.BinaryPath = %q, want /opt/whisper (global config still applies)", cfg.Transcription.BinaryPath)
	}
	if cfg.Vision.BinaryPath != "/opt/mtmd" {
		t.Errorf("Vision.BinaryPath = %q, want /opt/mtmd (global config still applies)", cfg.Vision.BinaryPath)
	}
}

// TestAudit_LoadFileWarnsOnLoosePerms pins the 2026-08 addition: the
// operator's global config can carry an api_key, so a group/world-readable
// file must produce a loud warning (the permission check previously covered
// only secrets.env despite the documented claim covering both).
func TestAudit_LoadFileWarnsOnLoosePerms(t *testing.T) {
	dir := t.TempDir()

	capture := func(perm os.FileMode) string {
		path := filepath.Join(dir, fmt.Sprintf("cfg-%o.json", perm))
		if err := os.WriteFile(path, []byte(`{"model":"m"}`), perm); err != nil {
			t.Fatal(err)
		}
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		origStderr := os.Stderr
		os.Stderr = w
		defer func() { os.Stderr = origStderr }()
		_ = loadFile(path)
		w.Close()
		buf, _ := io.ReadAll(r)
		return string(buf)
	}

	if out := capture(0600); strings.Contains(out, "group/world-readable") {
		t.Errorf("0600 config must not warn:\n%s", out)
	}
	if out := capture(0644); !strings.Contains(out, "group/world-readable") || !strings.Contains(out, "chmod 600") {
		t.Errorf("0644 config must warn with a chmod hint:\n%s", out)
	}
}

// TestAudit_SecretsEnvNamesRegistered pins the 2026-08 audit registry:
// every KEY=VALUE injected from ~/.odek/secrets.env must be recorded so
// child-process spawn sites (delegate_tasks) can strip it from the
// inherited environment.
func TestAudit_SecretsEnvNamesRegistered(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)
	t.Setenv("AUDIT_REG_ANOTHER_KEY", "") // not set → will be injected

	globalDir := filepath.Join(dir, ".odek")
	if err := os.MkdirAll(globalDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, "secrets.env"), []byte(
		"AUDIT_REG_TEST_TOKEN=supersecret\nAUDIT_REG_ANOTHER_KEY=alsosecret\n"), 0600); err != nil {
		t.Fatal(err)
	}

	_ = LoadConfig(CLIFlags{})
	names := SecretsEnvNames()
	saw := map[string]bool{}
	for _, n := range names {
		saw[n] = true
	}
	if !saw["AUDIT_REG_TEST_TOKEN"] || !saw["AUDIT_REG_ANOTHER_KEY"] {
		t.Errorf("SecretsEnvNames() = %v, want both secrets.env keys registered", names)
	}
}
