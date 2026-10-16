package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
)

// TestRED_TelegramMediaFallbackMatchesAvailableTools: when no vision or
// transcription backend is installed, the Telegram fallback must not tell
// the model to call a tool it was not given.
func TestRED_TelegramMediaFallbackMatchesAvailableTools(t *testing.T) {
	path := "/home/odek/.odek/media/photo_abc.jpg"
	for _, caption := range []string{"", "what is this?"} {
		msg := photoFallbackMessage(path, caption, false)
		if strings.Contains(msg, "vision tool") {
			t.Errorf("photo fallback without a vision backend points at the vision tool: %q", msg)
		}
		if !strings.Contains(msg, path) || !strings.Contains(msg, "no vision backend") {
			t.Errorf("photo fallback must keep the path and say no vision backend is installed: %q", msg)
		}
		if ready := photoFallbackMessage(path, caption, true); !strings.Contains(ready, "vision tool") {
			t.Errorf("photo fallback with a vision backend lost the vision tool hint: %q", ready)
		}
	}

	voice := voiceFallbackMessage("/tmp/v.ogg", false)
	if strings.Contains(voice, "transcribe()") || !strings.Contains(voice, "no transcription backend") {
		t.Errorf("voice fallback without a backend: %q", voice)
	}
	if ready := voiceFallbackMessage("/tmp/v.ogg", true); !strings.Contains(ready, "transcribe") {
		t.Errorf("voice fallback with a backend lost the transcribe hint: %q", ready)
	}
}

// TestRED_AllowlistedUnavailableToolWarns: an explicit allowlist naming a
// gated tool whose backend is missing says so instead of silently running
// without it.
func TestRED_AllowlistedUnavailableToolWarns(t *testing.T) {
	mediaScratch(t)
	all := builtinTools(danger.DangerousConfig{}, nil, nil, 1, "", mediaToolConfig(), nil)
	gatedToolWarned.Delete("vision")
	stderr := captureStderrDuring(t, func() {
		filterBuiltinTools(all, config.ToolConfig{Enabled: []string{"vision", "read_file"}}, nil)
	})
	if !strings.Contains(stderr, `tool "vision" is enabled but not available`) {
		t.Errorf("no warning for an allowlisted, unavailable vision tool; stderr:\n%s", stderr)
	}
	if strings.Contains(stderr, "read_file") {
		t.Errorf("warned about an available tool: %s", stderr)
	}
}

// TestRED_APIToolsReportsUnavailableGatedTools: /api/tools must not report
// a gated tool as enabled when it is not registered, matching list_tools.
func TestRED_APIToolsReportsUnavailableGatedTools(t *testing.T) {
	mediaScratch(t)
	resolved := config.ResolvedConfig{}
	w := httptest.NewRecorder()
	handleTools(resolved)(w, httptest.NewRequest(http.MethodGet, "/api/tools", nil))
	var body struct {
		Tools []toolSummary `json:"tools"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	seen := map[string]toolSummary{}
	for _, ts := range body.Tools {
		seen[ts.Name] = ts
	}
	for _, name := range []string{"transcribe", "vision", "speak", "web_search"} {
		ts, ok := seen[name]
		if !ok {
			t.Errorf("%s missing from /api/tools", name)
			continue
		}
		if ts.Enabled || ts.Reason == "" {
			t.Errorf("%s: enabled=%v reason=%q, want disabled with a reason", name, ts.Enabled, ts.Reason)
		}
	}
	if !seen["read_file"].Enabled {
		t.Error("read_file should stay enabled")
	}
}

// TestRED_GatedToolNamesReservedLikeMain: transcribe and vision were always
// registered (so reserved) before gating and stay reserved; speak and
// web_search were never reserved when unconfigured, and reserving them now
// would make an MCP server with a "web_search" or "speak" tool abort startup.
func TestRED_GatedToolNamesReservedLikeMain(t *testing.T) {
	mediaScratch(t)
	reserved := reservedBuiltinToolNames()
	for _, name := range []string{"transcribe", "vision"} {
		if !reserved[name] {
			t.Errorf("%s is not reserved against MCP shadowing", name)
		}
	}
	for _, name := range []string{"web_search", "speak"} {
		if reserved[name] {
			t.Errorf("unconfigured %s is reserved; an MCP tool of that name would abort startup", name)
		}
	}
}

// TestRED_TelegramMediaHintsRespectToolFilter: a media tool removed by the
// operator's tools filter is not offered in Telegram hints, even when its
// backend is installed.
func TestRED_TelegramMediaHintsRespectToolFilter(t *testing.T) {
	home, bin := mediaScratch(t)
	installFake(t, filepath.Join(bin, "whisper-cli"))
	installFake(t, filepath.Join(home, ".odek", "whisper", "models", "ggml-tiny.bin"))
	tcfg := mediaToolConfig()
	if _, toolReady := telegramVoiceBackends(tcfg, config.ToolConfig{}); !toolReady {
		t.Fatal("transcribe not available with whisper installed and no filter")
	}
	if _, toolReady := telegramVoiceBackends(tcfg, config.ToolConfig{Disabled: []string{"transcribe"}}); toolReady {
		t.Error("disabled transcribe still offered in the Telegram voice hint")
	}
	if _, toolReady := telegramVoiceBackends(tcfg, config.ToolConfig{Enabled: []string{"read_file"}}); toolReady {
		t.Error("transcribe outside tools.enabled still offered in the Telegram voice hint")
	}
	vc := config.VisionConfig{Backend: config.VisionBackendProvider, Model: "m"}
	if !telegramVisionReady(vc, config.ToolConfig{}) || telegramVisionReady(vc, config.ToolConfig{Disabled: []string{"vision"}}) {
		t.Error("telegramVisionReady ignores the tools filter")
	}
}

// TestRED_RunChecksKeyBeforeMCPApproval: with no API key, `odek run` reports
// the key error before asking to approve (or spawning) MCP servers.
func TestRED_RunChecksKeyBeforeMCPApproval(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	for _, k := range []string{"ODEK_API_KEY", "DEEPSEEK_API_KEY", "OPENAI_API_KEY", "ODEK_APPROVE_MCP"} {
		t.Setenv(k, "")
	}
	if err := os.MkdirAll(filepath.Join(home, ".odek"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := `{"mcp_servers":{"probe":{"command":"true"}}}`
	if err := os.WriteFile(filepath.Join(home, ".odek", "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	var err error
	stderr := captureStderrDuring(t, func() { err = run([]string{"--no-sandbox", "hello"}) })
	if err == nil || !strings.Contains(err.Error(), "no API key") {
		t.Fatalf("run error = %v, want the missing-key error first; stderr:\n%s", err, stderr)
	}
}

// TestRED_TelegramAutoTranscribeStaysLocal: Telegram voice auto-transcription
// runs before the agent run starts, outside execution budgets, usage
// accounting and /stop, so it must never make a paid provider STT call.
// With only provider STT configured, auto-transcription is not ready, but
// the fallback still points the agent at the transcribe tool (which runs
// inside the run).
func TestRED_TelegramAutoTranscribeStaysLocal(t *testing.T) {
	mediaScratch(t)
	tcfg := mediaToolConfig()
	tcfg.STT = config.STTConfig{Backend: config.STTBackendProvider, Provider: "openai", Model: "whisper-1"}
	autoReady, toolReady := telegramVoiceBackends(tcfg, config.ToolConfig{})
	if autoReady {
		t.Error("auto-transcription is ready with only a provider STT backend (would bypass run budgets)")
	}
	if !toolReady {
		t.Error("transcribe tool not reported available with a provider STT backend")
	}
}

func TestRED_TranscribeArgsAreValidJSON(t *testing.T) {
	path := `C:\media\voice "1".ogg`
	var got struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(transcribeCallArgs(path)), &got); err != nil || got.Path != path {
		t.Errorf("transcribeCallArgs(%q) = %q (err %v)", path, transcribeCallArgs(path), err)
	}
}
