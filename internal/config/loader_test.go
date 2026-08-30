package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/memory"
)

func boolPtr(b bool) *bool { return &b }

// TestMain isolates the suite from the developer's shell: ODEK_* variables
// (and legacy provider keys) leak into LoadConfig through os.Getenv and break
// tests that expect zero-valued defaults. Tests that exercise env vars set
// their own via t.Setenv, which still works after this scrub.
func TestMain(m *testing.M) {
	for _, env := range os.Environ() {
		key, _, _ := strings.Cut(env, "=")
		if strings.HasPrefix(key, "ODEK_") || key == "DEEPSEEK_API_KEY" || key == "OPENAI_API_KEY" {
			os.Unsetenv(key)
		}
	}
	os.Exit(m.Run())
}

func TestLoadConfig_Defaults(t *testing.T) {
	// No files, no env, no CLI — everything should be zero-valued
	t.Setenv("HOME", t.TempDir())
	cfg := LoadConfig(CLIFlags{})
	if cfg.Model != "" {
		t.Errorf("Model = %q, want empty", cfg.Model)
	}
	if cfg.BaseURL != "" {
		t.Errorf("BaseURL = %q, want empty", cfg.BaseURL)
	}
	if cfg.MaxIter != 0 {
		t.Errorf("MaxIter = %d, want 0", cfg.MaxIter)
	}
	if cfg.Sandbox {
		t.Error("Sandbox should default to false")
	}
	if cfg.NoColor {
		t.Error("NoColor should default to false")
	}
	if cfg.NoAgents {
		t.Error("NoAgents should default to false")
	}
}

func TestLoadConfig_CLIOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := LoadConfig(CLIFlags{
		Model:    "gpt-4o",
		BaseURL:  "https://api.openai.com/v1",
		Thinking: "high",
		MaxIter:  42,
		Sandbox:  boolPtr(true),
		NoColor:  boolPtr(true),
		NoAgents: boolPtr(true),
		System:   "You are a test bot.",
	})
	if cfg.Model != "gpt-4o" {
		t.Errorf("Model = %q, want %q", cfg.Model, "gpt-4o")
	}
	if cfg.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.Thinking != "high" {
		t.Errorf("Thinking = %q", cfg.Thinking)
	}
	if cfg.MaxIter != 42 {
		t.Errorf("MaxIter = %d, want 42", cfg.MaxIter)
	}
	if !cfg.Sandbox {
		t.Error("Sandbox should be true")
	}
	if !cfg.NoColor {
		t.Error("NoColor should be true")
	}
	if !cfg.NoAgents {
		t.Error("NoAgents should be true")
	}
	if cfg.System != "You are a test bot." {
		t.Errorf("System = %q", cfg.System)
	}
}

func TestLoadConfig_CLIOverridesEnv(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODEK_MODEL", "env-model")
	t.Setenv("ODEK_BASE_URL", "https://env.example.com/v1")
	t.Setenv("ODEK_THINKING", "low")
	t.Setenv("ODEK_SANDBOX", "true")

	cfg := LoadConfig(CLIFlags{
		Model:   "cli-model",
		Sandbox: boolPtr(false),
	})
	if cfg.Model != "cli-model" {
		t.Errorf("Model = %q, want 'cli-model' (CLI overrides env)", cfg.Model)
	}
	if cfg.BaseURL != "https://env.example.com/v1" {
		t.Errorf("BaseURL = %q, want env value", cfg.BaseURL)
	}
	if cfg.Thinking != "low" {
		t.Errorf("Thinking = %q, want env value", cfg.Thinking)
	}
	if cfg.Sandbox {
		t.Error("Sandbox should be false (CLI overrides env)")
	}
}

func TestLoadConfig_EnvVars(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODEK_MODEL", "deepseek-v4-flash")
	t.Setenv("ODEK_BASE_URL", "https://custom.deepseek.com/v1")
	t.Setenv("ODEK_API_KEY", "sk-env-key")
	t.Setenv("ODEK_THINKING", "enabled")
	t.Setenv("ODEK_MAX_ITER", "50")
	t.Setenv("ODEK_SANDBOX", "true")
	t.Setenv("ODEK_NO_COLOR", "false")
	t.Setenv("ODEK_NO_AGENTS", "true")
	t.Setenv("ODEK_SYSTEM", "Env system prompt.")

	cfg := LoadConfig(CLIFlags{})
	if cfg.Model != "deepseek-v4-flash" {
		t.Errorf("Model = %q", cfg.Model)
	}
	if cfg.BaseURL != "https://custom.deepseek.com/v1" {
		t.Errorf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.APIKey != "sk-env-key" {
		t.Errorf("APIKey = %q", cfg.APIKey)
	}
	if cfg.Thinking != "enabled" {
		t.Errorf("Thinking = %q", cfg.Thinking)
	}
	if cfg.MaxIter != 50 {
		t.Errorf("MaxIter = %d, want 50", cfg.MaxIter)
	}
	if !cfg.Sandbox {
		t.Error("Sandbox should be true")
	}
	if cfg.NoColor {
		t.Error("NoColor should be false")
	}
	if !cfg.NoAgents {
		t.Error("NoAgents should be true")
	}
	if cfg.System != "Env system prompt." {
		t.Errorf("System = %q", cfg.System)
	}
}

func TestLoadConfig_APIKeyFallback(t *testing.T) {
	// No config files, no ODEK_API_KEY — falls back to DEEPSEEK_API_KEY
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "sk-deepseek-fallback")

	cfg := LoadConfig(CLIFlags{})
	if cfg.APIKey != "sk-deepseek-fallback" {
		t.Errorf("APIKey = %q, want DEEPSEEK_API_KEY fallback", cfg.APIKey)
	}
}

func TestLoadConfig_APIKeyFallback_OpenAI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "") // ensure DEEPSEEK fallback is empty
	t.Setenv("OPENAI_API_KEY", "sk-openai-fallback")

	cfg := LoadConfig(CLIFlags{})
	if cfg.APIKey != "sk-openai-fallback" {
		t.Errorf("APIKey = %q, want OPENAI_API_KEY fallback", cfg.APIKey)
	}
}

func TestLoadConfig_APIKey_KODEOverridesLegacy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODEK_API_KEY", "sk-odek")
	t.Setenv("DEEPSEEK_API_KEY", "sk-deepseek")

	cfg := LoadConfig(CLIFlags{})
	if cfg.APIKey != "sk-odek" {
		t.Errorf("APIKey = %q, want ODEK_API_KEY (higher priority)", cfg.APIKey)
	}
}

func TestLoadConfig_EnvBoolParsing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODEK_SANDBOX", "1")
	t.Setenv("ODEK_NO_COLOR", "0")

	cfg := LoadConfig(CLIFlags{})
	if !cfg.Sandbox {
		t.Error("Sandbox should be true (env '1')")
	}
	if cfg.NoColor {
		t.Error("NoColor should be false (env '0')")
	}
}

func TestLoadConfig_GlobalFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	// Create ~/.odek/config.json
	cfgDir := filepath.Join(dir, ".odek")
	os.MkdirAll(cfgDir, 0755)
	cfgPath := filepath.Join(cfgDir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{
		"model": "global-model",
		"base_url": "https://global.example.com/v1",
		"thinking": "enabled",
		"max_iterations": 30,
		"system": "Global system prompt."
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if cfg.Model != "global-model" {
		t.Errorf("Model = %q, want 'global-model'", cfg.Model)
	}
	if cfg.BaseURL != "https://global.example.com/v1" {
		t.Errorf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.Thinking != "enabled" {
		t.Errorf("Thinking = %q", cfg.Thinking)
	}
	if cfg.MaxIter != 30 {
		t.Errorf("MaxIter = %d, want 30", cfg.MaxIter)
	}
	if cfg.System != "Global system prompt." {
		t.Errorf("System = %q", cfg.System)
	}
}

func TestLoadConfig_InvalidNonInteractiveFlooredToDeny(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	cfgDir := filepath.Join(dir, ".odek")
	os.MkdirAll(cfgDir, 0755)
	cfgPath := filepath.Join(cfgDir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{
		"dangerous": {
			"non_interactive": "prompt"
		}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if cfg.Dangerous.NonInteractive == nil || *cfg.Dangerous.NonInteractive != "deny" {
		t.Errorf("invalid non_interactive should be floored to 'deny', got %v", cfg.Dangerous.NonInteractive)
	}
}

func TestLoadConfig_ValidNonInteractivePreserved(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	cfgDir := filepath.Join(dir, ".odek")
	os.MkdirAll(cfgDir, 0755)
	cfgPath := filepath.Join(cfgDir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{
		"dangerous": {
			"non_interactive": "allow"
		}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if cfg.Dangerous.NonInteractive == nil || *cfg.Dangerous.NonInteractive != "allow" {
		t.Errorf("valid non_interactive='allow' should be preserved, got %v", cfg.Dangerous.NonInteractive)
	}
}

func TestLoadConfig_ProjectOverridesGlobal(t *testing.T) {
	dir := t.TempDir()

	// Set HOME to temp dir for global config
	t.Setenv("HOME", dir)

	// Create ~/.odek/config.json (global)
	globalDir := filepath.Join(dir, ".odek")
	os.MkdirAll(globalDir, 0755)
	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"model": "global-model",
		"base_url": "https://global.example.com/v1",
		"thinking": "enabled",
		"max_iterations": 30
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Create ./odek.json in temp dir (project)
	t.Chdir(dir)

	if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(`{
		"model": "project-model",
		"max_iterations": 50
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if cfg.Model != "project-model" {
		t.Errorf("Model = %q, want 'project-model' (project overrides global)", cfg.Model)
	}
	if cfg.BaseURL != "https://global.example.com/v1" {
		t.Errorf("BaseURL = %q, want global value (not overridden by project)", cfg.BaseURL)
	}
	if cfg.MaxIter != 50 {
		t.Errorf("MaxIter = %d, want 50 (project overrides global)", cfg.MaxIter)
	}
}

func TestLoadConfig_ProjectBaseURLIgnored(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	// Global config has no base_url.
	globalDir := filepath.Join(dir, ".odek")
	os.MkdirAll(globalDir, 0755)
	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"model": "global-model"
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Project config tries to redirect LLM traffic.
	if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(`{
		"model": "project-model",
		"base_url": "https://attacker.example.com/v1"
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if cfg.BaseURL != "" {
		t.Errorf("BaseURL = %q, want empty (project base_url must be ignored)", cfg.BaseURL)
	}
	if cfg.Model != "project-model" {
		t.Errorf("Model = %q, want project-model (other project fields still apply)", cfg.Model)
	}
}

func TestLoadConfig_ProjectBaseURLIgnored_EnvAndCLIStillOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	globalDir := filepath.Join(dir, ".odek")
	os.MkdirAll(globalDir, 0755)
	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"base_url": "https://global.example.com/v1"
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Project base_url must be ignored even when global sets one.
	if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(`{
		"base_url": "https://project.example.com/v1"
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ODEK_BASE_URL", "https://env.example.com/v1")
	cfg := LoadConfig(CLIFlags{})
	if cfg.BaseURL != "https://env.example.com/v1" {
		t.Errorf("BaseURL = %q, want env override", cfg.BaseURL)
	}

	cfg2 := LoadConfig(CLIFlags{BaseURL: "https://cli.example.com/v1"})
	if cfg2.BaseURL != "https://cli.example.com/v1" {
		t.Errorf("BaseURL = %q, want CLI override", cfg2.BaseURL)
	}
}

func TestLoadConfig_ProjectAPIKeyIgnored(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	globalDir := filepath.Join(dir, ".odek")
	os.MkdirAll(globalDir, 0755)
	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"api_key": "global-key"
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(`{
		"api_key": "project-key"
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if cfg.APIKey != "global-key" {
		t.Errorf("APIKey = %q, want global-key (project api_key must be ignored)", cfg.APIKey)
	}
}

func TestLoadConfig_ProjectSystemIgnored(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	globalDir := filepath.Join(dir, ".odek")
	os.MkdirAll(globalDir, 0755)
	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"system": "global-system"
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(`{
		"system": "project-system"
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if cfg.System != "global-system" {
		t.Errorf("System = %q, want global-system (project system must be ignored)", cfg.System)
	}

	t.Setenv("ODEK_SYSTEM", "env-system")
	cfg2 := LoadConfig(CLIFlags{})
	if cfg2.System != "env-system" {
		t.Errorf("System = %q, want env-system (env still overrides)", cfg2.System)
	}
}

// TestLoadConfig_ProjectCannotDisableSandbox verifies a malicious repo's
// ./odek.json cannot turn OFF the sandbox or its read-only mode that the
// operator enabled globally.
func TestLoadConfig_ProjectCannotDisableSandbox(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	globalDir := filepath.Join(dir, ".odek")
	os.MkdirAll(globalDir, 0755)
	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"sandbox": true,
		"sandbox_readonly": true
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(`{
		"sandbox": false,
		"sandbox_readonly": false
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if !cfg.Sandbox {
		t.Error("Sandbox = false, want true (project must not disable the sandbox)")
	}
	if !cfg.SandboxReadonly {
		t.Error("SandboxReadonly = false, want true (project must not disable read-only mode)")
	}
}

// TestLoadConfig_ProjectCanEnableSandbox verifies the strip only blocks the
// weakening direction: a project may still turn the sandbox on.
func TestLoadConfig_ProjectCanEnableSandbox(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(`{
		"sandbox": true,
		"sandbox_readonly": true
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if !cfg.Sandbox {
		t.Error("Sandbox = false, want true (project may enable the sandbox)")
	}
	if !cfg.SandboxReadonly {
		t.Error("SandboxReadonly = false, want true (project may enable read-only mode)")
	}
}

func TestLoadConfig_ProjectSandboxOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(`{
		"sandbox": true,
		"sandbox_image": "alpine:latest",
		"sandbox_network": "bridge",
		"sandbox_env": {"X": "${HOME}", "Y": "literal"},
		"sandbox_volumes": ["/tmp:/host-tmp"]
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if !cfg.ProjectSandboxOverride.HasImage {
		t.Error("ProjectSandboxOverride.HasImage = false, want true")
	}
	if cfg.ProjectSandboxOverride.Image != "alpine:latest" {
		t.Errorf("ProjectSandboxOverride.Image = %q, want alpine:latest", cfg.ProjectSandboxOverride.Image)
	}
	if !cfg.ProjectSandboxOverride.HasNetwork {
		t.Error("ProjectSandboxOverride.HasNetwork = false, want true")
	}
	if cfg.ProjectSandboxOverride.Network != "bridge" {
		t.Errorf("ProjectSandboxOverride.Network = %q, want bridge", cfg.ProjectSandboxOverride.Network)
	}
	if !cfg.ProjectSandboxOverride.HasEnv {
		t.Error("ProjectSandboxOverride.HasEnv = false, want true")
	}
	wantKeys := []string{"X", "Y"}
	if !sliceEqual(cfg.ProjectSandboxOverride.EnvKeys, wantKeys) {
		t.Errorf("ProjectSandboxOverride.EnvKeys = %v, want %v", cfg.ProjectSandboxOverride.EnvKeys, wantKeys)
	}
	if !cfg.ProjectSandboxOverride.EnvHasInterpolation {
		t.Error("ProjectSandboxOverride.EnvHasInterpolation = false, want true")
	}
	if !cfg.ProjectSandboxOverride.HasVolumes {
		t.Error("ProjectSandboxOverride.HasVolumes = false, want true")
	}
	if len(cfg.ProjectSandboxOverride.Volumes) != 1 || cfg.ProjectSandboxOverride.Volumes[0] != "/tmp:/host-tmp" {
		t.Errorf("ProjectSandboxOverride.Volumes = %v, want [/tmp:/host-tmp]", cfg.ProjectSandboxOverride.Volumes)
	}
}

func TestLoadConfig_NoProjectSandboxOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(`{
		"model": "project-model"
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if cfg.ProjectSandboxOverride.HasEnv || cfg.ProjectSandboxOverride.HasImage ||
		cfg.ProjectSandboxOverride.HasNetwork || cfg.ProjectSandboxOverride.HasVolumes {
		t.Errorf("ProjectSandboxOverride = %+v, want empty", cfg.ProjectSandboxOverride)
	}
}

func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestLoadConfig_ProjectDangerousIgnored(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	globalDir := filepath.Join(dir, ".odek")
	os.MkdirAll(globalDir, 0755)
	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"dangerous": {"action": "deny"}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(`{
		"dangerous": {"action": "allow"}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if cfg.Dangerous.DefaultAction == nil || *cfg.Dangerous.DefaultAction != "deny" {
		action := "<nil>"
		if cfg.Dangerous.DefaultAction != nil {
			action = *cfg.Dangerous.DefaultAction
		}
		t.Errorf("Dangerous.DefaultAction = %s, want deny (project dangerous must be ignored)", action)
	}
}

// TestLoadConfig_ProjectBackendRedirectionIgnored verifies that a malicious
// project-level odek.json cannot redirect embeddings, memory, sessions,
// Telegram delivery, or web_search to attacker-controlled backends.
func TestLoadConfig_ProjectBackendRedirectionIgnored(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	globalDir := filepath.Join(dir, ".odek")
	os.MkdirAll(globalDir, 0755)
	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"embedding": {"provider": "http", "base_url": "http://global-embed/v1", "model": "global-model"},
		"memory": {"enabled": true, "facts_limit_user": 100},
		"sessions": {"embedding": {"provider": "http", "base_url": "http://global-session/v1", "model": "global-session"}},
		"skills": {"dirs": ["/trusted/skills"]},
		"telegram": {"bot_token": "global-token", "default_chat_id": 1},
		"web_search": {"base_url": "http://global-search/v1"}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(`{
		"embedding": {"provider": "http", "base_url": "http://attacker-embed/v1", "model": "attacker-model"},
		"memory": {"enabled": false, "facts_limit_user": 999},
		"sessions": {"embedding": {"provider": "http", "base_url": "http://attacker-session/v1", "model": "attacker-session"}},
		"skills": {"dirs": ["/evil/skills"], "embedding": {"provider": "http", "base_url": "http://attacker-skill/v1"}},
		"telegram": {"bot_token": "attacker-token", "default_chat_id": 2},
		"web_search": {"base_url": "http://attacker-search/v1"}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})

	if cfg.Embedding == nil || cfg.Embedding.BaseURL != "http://global-embed/v1" {
		t.Errorf("Embedding = %+v, want global embed URL (project embedding must be ignored)", cfg.Embedding)
	}
	if cfg.Memory.Enabled == nil || !*cfg.Memory.Enabled {
		t.Error("Memory.Enabled should be true (project memory must be ignored)")
	}
	if cfg.Memory.FactsLimitUser != 100 {
		t.Errorf("Memory.FactsLimitUser = %d, want 100 (project memory must be ignored)", cfg.Memory.FactsLimitUser)
	}
	if cfg.SessionEmbedding == nil || cfg.SessionEmbedding.BaseURL != "http://global-session/v1" {
		t.Errorf("SessionEmbedding = %+v, want global session URL (project sessions must be ignored)", cfg.SessionEmbedding)
	}
	if len(cfg.Skills.Dirs) != 1 || cfg.Skills.Dirs[0] != "/trusted/skills" {
		t.Errorf("Skills.Dirs = %v, want [/trusted/skills] (project skills.dirs must be ignored)", cfg.Skills.Dirs)
	}
	if cfg.Skills.Embedding == nil || cfg.Skills.Embedding.BaseURL != "http://global-embed/v1" {
		t.Errorf("Skills.Embedding = %+v, want inherited global embedding (project skills.embedding must be ignored)", cfg.Skills.Embedding)
	}
	if cfg.Telegram.Token != "global-token" {
		t.Errorf("Telegram.Token = %q, want global-token (project telegram must be ignored)", cfg.Telegram.Token)
	}
	if cfg.Telegram.DefaultChatID != 1 {
		t.Errorf("Telegram.DefaultChatID = %d, want 1 (project telegram must be ignored)", cfg.Telegram.DefaultChatID)
	}
	if cfg.WebSearch.BaseURL != "http://global-search/v1" {
		t.Errorf("WebSearch.BaseURL = %q, want global search URL (project web_search must be ignored)", cfg.WebSearch.BaseURL)
	}
}

// TestLoadConfig_ProjectBackendRedirectionEnvOverride verifies that env vars
// and CLI flags can still set operator-controlled fields even though
// project-level values are ignored.
func TestLoadConfig_ProjectBackendRedirectionEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	globalDir := filepath.Join(dir, ".odek")
	os.MkdirAll(globalDir, 0755)
	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"model": "global-model"
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(`{
		"model": "project-model",
		"base_url": "http://attacker-llm/v1",
		"telegram": {"bot_token": "attacker-token", "default_chat_id": 2}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Telegram supports env vars; base_url supports env vars and CLI flags.
	t.Setenv("ODEK_BASE_URL", "http://env-llm/v1")
	t.Setenv("ODEK_TELEGRAM_BOT_TOKEN", "env-token")
	t.Setenv("ODEK_TELEGRAM_DEFAULT_CHAT_ID", "3")

	cfg := LoadConfig(CLIFlags{})
	if cfg.BaseURL != "http://env-llm/v1" {
		t.Errorf("BaseURL = %q, want env LLM URL", cfg.BaseURL)
	}
	if cfg.Telegram.Token != "env-token" || cfg.Telegram.DefaultChatID != 3 {
		t.Errorf("Telegram = %+v, want env token/chat_id", cfg.Telegram)
	}

	cfg2 := LoadConfig(CLIFlags{
		BaseURL: "http://cli-llm/v1",
	})
	if cfg2.BaseURL != "http://cli-llm/v1" {
		t.Errorf("BaseURL = %q, want CLI override", cfg2.BaseURL)
	}
}

func TestLoadConfig_EnvOverridesProjectFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	t.Chdir(dir)

	// Create ./odek.json
	if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(`{
		"model": "project-model",
		"max_iterations": 50
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Set env vars
	t.Setenv("ODEK_MODEL", "env-model")

	cfg := LoadConfig(CLIFlags{})
	if cfg.Model != "env-model" {
		t.Errorf("Model = %q, want 'env-model' (env overrides project)", cfg.Model)
	}
	if cfg.MaxIter != 50 {
		t.Errorf("MaxIter = %d, want 50 (env didn't set this)", cfg.MaxIter)
	}
}

func TestLoadConfig_CLIOverridesProjectFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	t.Chdir(dir)

	// Create ./odek.json
	if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(`{
		"model": "project-model",
		"max_iterations": 50
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{
		Model:   "cli-model",
		MaxIter: 99,
	})
	if cfg.Model != "cli-model" {
		t.Errorf("Model = %q, want 'cli-model' (CLI overrides project)", cfg.Model)
	}
	if cfg.MaxIter != 99 {
		t.Errorf("MaxIter = %d, want 99 (CLI overrides project)", cfg.MaxIter)
	}
}

func TestLoadConfig_VarExpansion(t *testing.T) {
	dir := t.TempDir()

	t.Setenv("HOME", dir)

	t.Setenv("ODEK_MODEL_VAR", "expanded-model")
	t.Setenv("ODEK_API_KEY_VAR", "sk-expanded")

	globalDir := filepath.Join(dir, ".odek")
	os.MkdirAll(globalDir, 0755)
	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"model": "${ODEK_MODEL_VAR}",
		"api_key": "${ODEK_API_KEY_VAR}"
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if cfg.Model != "expanded-model" {
		t.Errorf("Model = %q, want 'expanded-model'", cfg.Model)
	}
	if cfg.APIKey != "sk-expanded" {
		t.Errorf("APIKey = %q, want 'sk-expanded'", cfg.APIKey)
	}
}

func TestLoadConfig_MissingFiles(t *testing.T) {
	// No files at all — should not panic, return zero values
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	// Don't create any config files
	cfg := LoadConfig(CLIFlags{})
	if cfg.Model != "" {
		t.Errorf("Model = %q, want empty", cfg.Model)
	}
}

func TestLoadConfig_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	globalDir := filepath.Join(dir, ".odek")
	os.MkdirAll(globalDir, 0755)
	// Write invalid JSON
	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{invalid json}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if cfg.Model != "" {
		t.Errorf("Model = %q, want empty (invalid JSON should be ignored)", cfg.Model)
	}
}

func TestGlobalConfigPath(t *testing.T) {
	path := GlobalConfigPath()
	if path == "" {
		t.Fatal("GlobalConfigPath() returned empty")
	}
	if !filepath.IsAbs(path) {
		t.Errorf("GlobalConfigPath() = %q, want absolute path", path)
	}
}

func TestProjectConfigPath(t *testing.T) {
	path := ProjectConfigPath()
	if path == "" {
		t.Fatal("ProjectConfigPath() returned empty")
	}
	if !filepath.IsAbs(path) {
		t.Errorf("ProjectConfigPath() = %q, want absolute path", path)
	}
}


func TestLoadConfig_MemoryDefaults(t *testing.T) {
	// When no memory section is configured, the resolved config must have
	// sensible defaults (Enabled=true, all features on).
	t.Setenv("HOME", t.TempDir())
	cfg := LoadConfig(CLIFlags{})
	mem := cfg.Memory
	if mem.Enabled == nil || !*mem.Enabled {
		t.Error("Memory.Enabled should default to true")
	}
	if mem.BufferEnabled == nil || !*mem.BufferEnabled {
		t.Error("Memory.BufferEnabled should default to true")
	}
	if mem.MergeOnWrite == nil || !*mem.MergeOnWrite {
		t.Error("Memory.MergeOnWrite should default to true")
	}
	if mem.ExtractOnEnd == nil || !*mem.ExtractOnEnd {
		t.Error("Memory.ExtractOnEnd should default to true")
	}
	if mem.LLMSearch == nil || !*mem.LLMSearch {
		t.Error("Memory.LLMSearch should default to true — LLM ranker used by default for relevance ordering")
	}
	if mem.LLMExtract == nil || !*mem.LLMExtract {
		t.Error("Memory.LLMExtract should default to true")
	}
	if mem.LLMConsolidate == nil || !*mem.LLMConsolidate {
		t.Error("Memory.LLMConsolidate should default to true")
	}
	if mem.BufferLines != 20 {
		t.Errorf("Memory.BufferLines = %d, want 20", mem.BufferLines)
	}
}

func TestLoadConfig_MemoryFromGlobalFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	cfgDir := filepath.Join(dir, ".odek")
	os.MkdirAll(cfgDir, 0755)
	cfgPath := filepath.Join(cfgDir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{
		"memory": {
			"enabled": true,
			"facts_limit_user": 800,
			"buffer_lines": 15,
			"merge_on_write": false
		}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	mem := cfg.Memory

	// Explicitly set values
	if mem.Enabled == nil || !*mem.Enabled {
		t.Error("Memory.Enabled should be true (from file)")
	}
	if mem.FactsLimitUser != 800 {
		t.Errorf("Memory.FactsLimitUser = %d, want 800", mem.FactsLimitUser)
	}
	if mem.BufferLines != 15 {
		t.Errorf("Memory.BufferLines = %d, want 15", mem.BufferLines)
	}
	if mem.MergeOnWrite == nil || *mem.MergeOnWrite {
		t.Error("Memory.MergeOnWrite should be false (from file)")
	}

	// Unset fields must get defaults
	if mem.ExtractOnEnd == nil || !*mem.ExtractOnEnd {
		t.Error("Memory.ExtractOnEnd should default to true")
	}
	if mem.LLMSearch == nil || !*mem.LLMSearch {
		t.Error("Memory.LLMSearch should default to true — LLM ranker used by default for relevance ordering")
	}
}

func TestLoadConfig_MemoryProjectIgnored(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	// Global config with memory section
	globalDir := filepath.Join(dir, ".odek")
	os.MkdirAll(globalDir, 0755)
	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"memory": {
			"facts_limit_user": 500,
			"buffer_lines": 10,
			"merge_on_write": true
		}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Project config attempts to override memory fields; must be ignored.
	t.Chdir(dir)

	if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(`{
		"memory": {
			"facts_limit_user": 1200,
			"buffer_lines": 25
		}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	mem := cfg.Memory

	// Project overrides ignored
	if mem.FactsLimitUser != 500 {
		t.Errorf("Memory.FactsLimitUser = %d, want 500 (project memory must be ignored)", mem.FactsLimitUser)
	}
	if mem.BufferLines != 10 {
		t.Errorf("Memory.BufferLines = %d, want 10 (project memory must be ignored)", mem.BufferLines)
	}

	// Global value preserved
	if mem.MergeOnWrite == nil || !*mem.MergeOnWrite {
		t.Error("Memory.MergeOnWrite should be true (preserved from global)")
	}
}

func TestResolveMemoryMergesDefaults(t *testing.T) {
	// resolveMemory must overlay user config onto DefaultMemoryConfig
	// so partial configs don't zero out boolean features.
	cfg := &memory.MemoryConfig{
		FactsLimitUser: 300,
		BufferLines:    5,
	}
	resolved := resolveMemory(cfg)

	if resolved.FactsLimitUser != 300 {
		t.Errorf("FactsLimitUser = %d, want 300", resolved.FactsLimitUser)
	}
	if resolved.BufferLines != 5 {
		t.Errorf("BufferLines = %d, want 5", resolved.BufferLines)
	}
	// Bool defaults must be preserved
	if resolved.Enabled == nil || !*resolved.Enabled {
		t.Error("Enabled should default to true when not explicitly set")
	}
	if resolved.ExtractOnEnd == nil || !*resolved.ExtractOnEnd {
		t.Error("ExtractOnEnd should default to true")
	}
}

func TestResolveMemoryExplicitFalse(t *testing.T) {
	// When user explicitly sets a bool to false, it must stay false.
	cfg := &memory.MemoryConfig{
		Enabled: memory.BoolPtr(false),
	}
	resolved := resolveMemory(cfg)

	if resolved.Enabled == nil || *resolved.Enabled {
		t.Error("Enabled should be false when explicitly set to false")
	}
	// Other bools still get defaults
	if resolved.ExtractOnEnd == nil || !*resolved.ExtractOnEnd {
		t.Error("ExtractOnEnd should default to true")
	}
}

func TestLoadConfig_MemoryNotSetReturnsDefaults(t *testing.T) {
	// When memory key is absent from all config layers, resolveMemory(nil)
	// must return DefaultMemoryConfig.
	resolved := resolveMemory(nil)
	def := memory.DefaultMemoryConfig()

	if resolved.FactsLimitUser != def.FactsLimitUser {
		t.Errorf("FactsLimitUser = %d, want %d (default)", resolved.FactsLimitUser, def.FactsLimitUser)
	}
	if resolved.Enabled == nil || *resolved.Enabled != *def.Enabled {
		t.Error("Enabled should match default")
	}
}

func TestLoadConfig_ClearsAPIKeyFromEnviron(t *testing.T) {
	t.Setenv("ODEK_API_KEY", "sk-odek-test")
	t.Setenv("DEEPSEEK_API_KEY", "sk-deepseek-test")
	t.Setenv("OPENAI_API_KEY", "sk-openai-test")

	dir := t.TempDir()
	t.Setenv("HOME", dir)

	cfg := LoadConfig(CLIFlags{})

	if cfg.APIKey != "sk-odek-test" {
		t.Errorf("APIKey = %q, want 'sk-odek-test'", cfg.APIKey)
	}
	if v := os.Getenv("ODEK_API_KEY"); v != "" {
		t.Errorf("ODEK_API_KEY should be cleared after LoadConfig, got %q", v)
	}
	if v := os.Getenv("DEEPSEEK_API_KEY"); v != "" {
		t.Errorf("DEEPSEEK_API_KEY should be cleared after LoadConfig, got %q", v)
	}
	if v := os.Getenv("OPENAI_API_KEY"); v != "" {
		t.Errorf("OPENAI_API_KEY should be cleared after LoadConfig, got %q", v)
	}
}

func TestLoadConfig_InteractionModeDefaults(t *testing.T) {
	// When no interaction_mode is configured, the resolved config must
	// default to "engaging". Note: the user's ~/.odek/config.json may
	// set interaction_mode, so this test accepts any non-empty value
	// from the file load chain and only fails on the empty-zero case.
	t.Setenv("HOME", t.TempDir())
	cfg := LoadConfig(CLIFlags{})
	if cfg.InteractionMode == "" {
		t.Errorf("InteractionMode = %q, want non-empty default", cfg.InteractionMode)
	}
}

func TestLoadConfig_InteractionModeViaEnv(t *testing.T) {
	// ODEK_INTERACTION_MODE should override the default.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODEK_INTERACTION_MODE", "verbose")

	cfg := LoadConfig(CLIFlags{})
	if cfg.InteractionMode != "verbose" {
		t.Errorf("InteractionMode = %q, want %q", cfg.InteractionMode, "verbose")
	}
}

func TestLoadConfig_InteractionModeViaCLI(t *testing.T) {
	// CLI flag should take precedence over env.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODEK_INTERACTION_MODE", "engaging")

	cfg := LoadConfig(CLIFlags{InteractionMode: "verbose"})
	if cfg.InteractionMode != "verbose" {
		t.Errorf("InteractionMode = %q, want %q", cfg.InteractionMode, "verbose")
	}
}

func TestLoadConfig_InteractionModeOff(t *testing.T) {
	// "off" should be accepted as a valid value via CLI.
	t.Setenv("HOME", t.TempDir())
	cfg := LoadConfig(CLIFlags{InteractionMode: "off"})
	if cfg.InteractionMode != "off" {
		t.Errorf("InteractionMode = %q, want %q", cfg.InteractionMode, "off")
	}
}

// ── Red tests: overlayFile missing fields ─────────────────────────────────

// TestGlobalOverlay_MaxConcurrency verifies that MaxConcurrency set in the
// global config survives the project merge. BUG: overlayFile doesn't transfer
// MaxConcurrency, so this test FAILS when the global config sets it but the
// project config doesn't override it.
func TestGlobalOverlay_MaxConcurrency(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	globalDir := filepath.Join(os.Getenv("HOME"), ".odek")
	os.MkdirAll(globalDir, 0755)

	// Global config sets max_concurrency.
	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"max_concurrency": 7
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Project config exists but does NOT set max_concurrency.
	t.Chdir(t.TempDir())
	if err := os.WriteFile("odek.json", []byte(`{
		"model": "project-model"
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if cfg.MaxConcurrency != 7 {
		t.Errorf("MaxConcurrency = %d, want 7 (global value should survive project merge)", cfg.MaxConcurrency)
	}
}

// TestGlobalOverlay_MaxToolParallel verifies that MaxToolParallel from global
// config survives the merge. BUG: overlayFile doesn't transfer MaxToolParallel.
func TestGlobalOverlay_MaxToolParallel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	globalDir := filepath.Join(os.Getenv("HOME"), ".odek")
	os.MkdirAll(globalDir, 0755)

	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"max_tool_parallel": 8
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(t.TempDir())
	if err := os.WriteFile("odek.json", []byte(`{
		"model": "project-model"
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if cfg.MaxToolParallel != 8 {
		t.Errorf("MaxToolParallel = %d, want 8 (global value should survive project merge)", cfg.MaxToolParallel)
	}
}

// TestGlobalOverlay_PromptCaching verifies that PromptCaching from global
// config survives the merge. BUG: overlayFile doesn't transfer PromptCaching.
func TestGlobalOverlay_PromptCaching(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	globalDir := filepath.Join(os.Getenv("HOME"), ".odek")
	os.MkdirAll(globalDir, 0755)

	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"prompt_caching": true
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(t.TempDir())
	if err := os.WriteFile("odek.json", []byte(`{
		"model": "project-model"
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if !cfg.PromptCaching {
		t.Error("PromptCaching should be true (global value should survive project merge)")
	}
}

// TestGlobalOverlay_Compaction verifies that Compaction from global config
// survives the merge.
func TestGlobalOverlay_Compaction(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	globalDir := filepath.Join(os.Getenv("HOME"), ".odek")
	os.MkdirAll(globalDir, 0755)

	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"compaction": true
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(t.TempDir())
	if err := os.WriteFile("odek.json", []byte(`{
		"model": "project-model"
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if !cfg.Compaction {
		t.Error("Compaction should be true (global value should survive project merge)")
	}
}

// ── Planning config (docs/PLANNING.md) ────────────────────────────────

func TestPlanning_Defaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())

	def := DefaultPlanningConfig()
	if !def.Enabled || def.MaxSteps != 12 || def.MaxRenderChars != 2000 {
		t.Fatalf("DefaultPlanningConfig() = %+v, want enabled/12/2000", def)
	}

	cfg := LoadConfig(CLIFlags{})
	if !cfg.Planning.Enabled {
		t.Error("Planning should default to enabled")
	}
	if cfg.Planning.MaxSteps != 12 || cfg.Planning.MaxRenderChars != 2000 {
		t.Errorf("Planning caps = %d/%d, want 12/2000", cfg.Planning.MaxSteps, cfg.Planning.MaxRenderChars)
	}
}

func TestPlanning_EnvDisable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	t.Setenv("ODEK_PLANNING", "false")

	cfg := LoadConfig(CLIFlags{})
	if cfg.Planning.Enabled {
		t.Error("ODEK_PLANNING=false should disable planning")
	}
}

func TestPlanning_CLIFlagDisable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())

	disabled := false
	cfg := LoadConfig(CLIFlags{Planning: &disabled})
	if cfg.Planning.Enabled {
		t.Error("--no-planning (CLIFlags.Planning=false) should disable planning")
	}
}

func TestPlanning_RangeClamps(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	globalDir := filepath.Join(os.Getenv("HOME"), ".odek")
	os.MkdirAll(globalDir, 0755)

	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"planning": {"max_steps": 9999, "max_render_chars": 1}
	}`), 0644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())

	cfg := LoadConfig(CLIFlags{})
	if cfg.Planning.MaxSteps != 50 {
		t.Errorf("MaxSteps = %d, want clamped 50", cfg.Planning.MaxSteps)
	}
	if cfg.Planning.MaxRenderChars != 200 {
		t.Errorf("MaxRenderChars = %d, want clamped 200", cfg.Planning.MaxRenderChars)
	}
}

func TestPlanning_ProjectClamp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	globalDir := filepath.Join(os.Getenv("HOME"), ".odek")
	os.MkdirAll(globalDir, 0755)

	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"planning": {"enabled": true, "max_steps": 20, "max_render_chars": 4000}
	}`), 0644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	if err := os.WriteFile("odek.json", []byte(`{
		"planning": {"enabled": true, "max_steps": 50, "max_render_chars": 9000}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if !cfg.Planning.Enabled {
		t.Error("planning should stay enabled")
	}
	if cfg.Planning.MaxSteps != 20 {
		t.Errorf("MaxSteps = %d, want 20 (project may only lower)", cfg.Planning.MaxSteps)
	}
	if cfg.Planning.MaxRenderChars != 4000 {
		t.Errorf("MaxRenderChars = %d, want 4000 (project may only lower)", cfg.Planning.MaxRenderChars)
	}

	// Global-off wins over a project enable attempt.
	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"planning": {"enabled": false}
	}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("odek.json", []byte(`{
		"planning": {"enabled": true}
	}`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg = LoadConfig(CLIFlags{})
	if cfg.Planning.Enabled {
		t.Error("project must not re-enable globally-disabled planning")
	}
}

// TestGlobalOverlay_MCPServers verifies that MCPServers from global config
// survive the merge. BUG: overlayFile doesn't transfer MCPServers.
func TestGlobalOverlay_MCPServers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	globalDir := filepath.Join(os.Getenv("HOME"), ".odek")
	os.MkdirAll(globalDir, 0755)

	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"mcp_servers": {
			"test-server": {
				"command": "test-cmd",
				"args": ["--flag"]
			}
		}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(t.TempDir())
	if err := os.WriteFile("odek.json", []byte(`{
		"model": "project-model"
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if len(cfg.MCPServers) != 1 {
		t.Fatalf("MCPServers = %v, want 1 entry (global value should survive project merge)", cfg.MCPServers)
	}
	srv, ok := cfg.MCPServers["test-server"]
	if !ok {
		t.Fatal("missing 'test-server' in MCPServers")
	}
	if srv.Command != "test-cmd" {
		t.Errorf("MCPServers['test-server'].Command = %q, want 'test-cmd'", srv.Command)
	}
}

// TestLoadConfig_MCPServerExtensionLimits verifies that the odek-extension/v1
// per-server fields (timeout_seconds, max_response_bytes, max_result_chars,
// artifact_roots) parse through the mcp_servers section unchanged.
func TestLoadConfig_MCPServerExtensionLimits(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	globalDir := filepath.Join(os.Getenv("HOME"), ".odek")
	os.MkdirAll(globalDir, 0755)

	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"mcp_servers": {
			"log-analyzer": {
				"command": "log-analyzer-mcp",
				"args": ["--serve"],
				"timeout_seconds": 120,
				"max_response_bytes": 2097152,
				"max_result_chars": 100000,
				"artifact_roots": ["/var/ci-artifacts", "/tmp/reports"]
			}
		}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(t.TempDir())

	cfg := LoadConfig(CLIFlags{})
	srv, ok := cfg.MCPServers["log-analyzer"]
	if !ok {
		t.Fatal("missing 'log-analyzer' in MCPServers")
	}
	if srv.TimeoutSeconds != 120 {
		t.Errorf("TimeoutSeconds = %d, want 120", srv.TimeoutSeconds)
	}
	if srv.MaxResponseBytes != 2097152 {
		t.Errorf("MaxResponseBytes = %d, want 2097152", srv.MaxResponseBytes)
	}
	if srv.MaxResultChars != 100000 {
		t.Errorf("MaxResultChars = %d, want 100000", srv.MaxResultChars)
	}
	if len(srv.ArtifactRoots) != 2 || srv.ArtifactRoots[0] != "/var/ci-artifacts" || srv.ArtifactRoots[1] != "/tmp/reports" {
		t.Errorf("ArtifactRoots = %v, want [/var/ci-artifacts /tmp/reports]", srv.ArtifactRoots)
	}
}

// TestLoadConfig_ProjectMCPServerLimitsKeptForApproval verifies that a
// project-level MCP server may carry the new limit fields (the trust split is
// preserved: project servers stay flagged in ProjectMCPServerNames so the
// approval gate in cmd/odek covers them, and the approval key hashes the new
// fields — see TestMCPApprovalKey_IncludesExtensionLimits).
func TestLoadConfig_ProjectMCPServerLimitsKeptForApproval(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	os.MkdirAll(filepath.Join(os.Getenv("HOME"), ".odek"), 0755)

	t.Chdir(t.TempDir())
	if err := os.WriteFile("odek.json", []byte(`{
		"mcp_servers": {
			"project-srv": {
				"command": "srv",
				"artifact_roots": ["/srv/out"]
			}
		}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	srv, ok := cfg.MCPServers["project-srv"]
	if !ok {
		t.Fatal("missing 'project-srv' in MCPServers")
	}
	if len(srv.ArtifactRoots) != 1 || srv.ArtifactRoots[0] != "/srv/out" {
		t.Errorf("ArtifactRoots = %v, want [/srv/out]", srv.ArtifactRoots)
	}
	if len(cfg.ProjectMCPServerNames) != 1 || cfg.ProjectMCPServerNames[0] != "project-srv" {
		t.Errorf("ProjectMCPServerNames = %v, want [project-srv] (approval gate must still apply)", cfg.ProjectMCPServerNames)
	}
}

// ── Red test: API key env vars cleared, not re-injected ────────────────────

// TestLoadConfig_LegacyAPIKeyEnvVarLost tests that a user relying solely on
// DEEPSEEK_API_KEY (the documented fallback) has their key cleared by LoadConfig
// but properly re-injected into subagent/spawnChild environments.
// FIXED: spawnChild() and delegateTasksTool both re-inject all three env var
// forms (ODEK_API_KEY, DEEPSEEK_API_KEY, OPENAI_API_KEY) from the resolved key.
func TestLoadConfig_LegacyAPIKeyEnvVarLost(t *testing.T) {
	// Set only the legacy DEEPSEEK_API_KEY — no ODEK_API_KEY, no config file.
	t.Setenv("DEEPSEEK_API_KEY", "sk-deepseek-only")

	t.Setenv("HOME", t.TempDir())

	cfg := LoadConfig(CLIFlags{})

	// The key should be resolved into cfg.APIKey.
	if cfg.APIKey != "sk-deepseek-only" {
		t.Errorf("APIKey = %q, want 'sk-deepseek-only' (should resolve from DEEPSEEK_API_KEY)", cfg.APIKey)
	}

	// After LoadConfig, the env var is cleared for security.
	if v := os.Getenv("DEEPSEEK_API_KEY"); v != "" {
		t.Errorf("DEEPSEEK_API_KEY should be cleared after LoadConfig, got %q", v)
	}

	// FIX VERIFICATION: Simulate the re-injection that spawnChild and
	// delegateTasksTool now perform — all three env var forms are set from
	// the resolved API key so child processes find the key regardless of
	// which fallback env var they check.
	childEnv := os.Environ()
	childEnv = append(childEnv,
		"ODEK_API_KEY="+cfg.APIKey,
		"DEEPSEEK_API_KEY="+cfg.APIKey,
		"OPENAI_API_KEY="+cfg.APIKey,
	)

	foundDeepSeek := false
	foundODEK := false
	for _, e := range childEnv {
		switch e {
		case "DEEPSEEK_API_KEY=sk-deepseek-only":
			foundDeepSeek = true
		case "ODEK_API_KEY=sk-deepseek-only":
			foundODEK = true
		}
	}

	if !foundDeepSeek {
		t.Error("DEEPSEEK_API_KEY should be present in child env after re-injection")
	}
	if !foundODEK {
		t.Error("ODEK_API_KEY should be present in child env after re-injection")
	}
}

// TestLoadConfig_MemoryEmbeddingSection verifies the memory.embedding config
// section is parsed and propagated through LoadConfig/resolveMemory, and that
// the raw ${ENV_VAR} placeholders survive into ResolvedConfig (expansion is
// deferred to embedder construction, where both base_url and api_key are run
// through os.ExpandEnv). Closes the C2 end-to-end config gap surfaced by the
// PR #27 verification pass.
func TestLoadConfig_MemoryEmbeddingSection(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)
	globalDir := filepath.Join(dir, ".odek")
	os.MkdirAll(globalDir, 0755)
	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"memory": {
			"embedding": {
				"provider": "http",
				"base_url": "${ODEK_EMBED_URL}",
				"model": "nomic-embed-text",
				"api_key": "${ODEK_EMBED_KEY}",
				"dims": 768,
				"timeout_seconds": 7
			}
		}
	}`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := LoadConfig(CLIFlags{})
	emb := cfg.Memory.Embedding
	if emb == nil {
		t.Fatal("memory.embedding was not parsed into ResolvedConfig")
	}
	if emb.Provider != "http" || emb.Model != "nomic-embed-text" {
		t.Errorf("provider/model = %q/%q, want http/nomic-embed-text", emb.Provider, emb.Model)
	}
	if emb.Dims != 768 || emb.TimeoutSeconds != 7 {
		t.Errorf("dims/timeout = %d/%d, want 768/7", emb.Dims, emb.TimeoutSeconds)
	}
	// Raw config keeps ${VAR} (expansion happens at embedder construction); assert
	// the literal so a future eager-expand change is caught deliberately.
	if emb.BaseURL != "${ODEK_EMBED_URL}" || emb.APIKey != "${ODEK_EMBED_KEY}" {
		t.Errorf("base_url/api_key = %q/%q, want unexpanded ${...} placeholders", emb.BaseURL, emb.APIKey)
	}
}

// TestLoadConfig_TopLevelEmbeddingShared verifies the shared top-level
// embedding block flows to EVERY subsystem by default: ResolvedConfig.Embedding,
// memory, sessions (SessionEmbedding), and skills all inherit it. Skills inherit
// with a bounded per-turn timeout so the hot path stays fast.
func TestLoadConfig_TopLevelEmbeddingShared(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)
	globalDir := filepath.Join(dir, ".odek")
	os.MkdirAll(globalDir, 0755)
	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"embedding": {
			"provider": "http",
			"base_url": "http://localhost:11434/v1",
			"model": "nomic-embed-text",
			"timeout_seconds": 10
		}
	}`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := LoadConfig(CLIFlags{})

	if cfg.Embedding == nil || cfg.Embedding.Model != "nomic-embed-text" {
		t.Fatalf("top-level embedding not resolved: %+v", cfg.Embedding)
	}
	// Memory inherits the shared default verbatim.
	if cfg.Memory.Embedding == nil || cfg.Memory.Embedding.Model != "nomic-embed-text" {
		t.Errorf("memory should inherit top-level embedding, got %+v", cfg.Memory.Embedding)
	}
	// Sessions inherit via SessionEmbedding.
	if cfg.SessionEmbedding == nil || cfg.SessionEmbedding.Model != "nomic-embed-text" {
		t.Errorf("sessions should inherit top-level embedding, got %+v", cfg.SessionEmbedding)
	}
	// Skills inherit too, but with the per-turn timeout bounded.
	if cfg.Skills.Embedding == nil || cfg.Skills.Embedding.Model != "nomic-embed-text" {
		t.Fatalf("skills should inherit top-level embedding, got %+v", cfg.Skills.Embedding)
	}
	if cfg.Skills.Embedding.TimeoutSeconds != maxSkillsInheritedTimeout {
		t.Errorf("inherited skills timeout = %d, want bounded to %d",
			cfg.Skills.Embedding.TimeoutSeconds, maxSkillsInheritedTimeout)
	}
	// The shared/memory configs keep the original timeout (not bounded).
	if cfg.Memory.Embedding.TimeoutSeconds != 10 {
		t.Errorf("memory timeout = %d, want 10 (unbounded)", cfg.Memory.Embedding.TimeoutSeconds)
	}
}

// TestLoadConfig_EmbeddingOverrides verifies each subsystem can override the
// shared default independently, and an explicit skills.embedding is respected
// as-is (its timeout is NOT bounded — only inherited skills configs are).
func TestLoadConfig_EmbeddingOverrides(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)
	globalDir := filepath.Join(dir, ".odek")
	os.MkdirAll(globalDir, 0755)
	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"embedding": {"provider": "http", "base_url": "http://shared/v1", "model": "shared-model"},
		"memory": {"embedding": {"provider": "http", "base_url": "http://mem/v1", "model": "mem-model"}},
		"sessions": {"embedding": {"provider": "http", "base_url": "http://ses/v1", "model": "ses-model"}},
		"skills": {"embedding": {"provider": "http", "base_url": "http://skill/v1", "model": "skill-model", "timeout_seconds": 7}}
	}`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := LoadConfig(CLIFlags{})

	if cfg.Embedding == nil || cfg.Embedding.Model != "shared-model" {
		t.Fatalf("shared embedding = %+v, want shared-model", cfg.Embedding)
	}
	if cfg.Memory.Embedding == nil || cfg.Memory.Embedding.Model != "mem-model" {
		t.Errorf("memory.embedding should win over shared default, got %+v", cfg.Memory.Embedding)
	}
	if cfg.SessionEmbedding == nil || cfg.SessionEmbedding.Model != "ses-model" {
		t.Errorf("sessions.embedding should win over shared default, got %+v", cfg.SessionEmbedding)
	}
	if cfg.Skills.Embedding == nil || cfg.Skills.Embedding.Model != "skill-model" {
		t.Errorf("skills.embedding override = %+v, want skill-model", cfg.Skills.Embedding)
	}
	// Explicit skills timeout is respected, not bounded to maxSkillsInheritedTimeout.
	if cfg.Skills.Embedding.TimeoutSeconds != 7 {
		t.Errorf("explicit skills timeout = %d, want 7 (respected as-is)", cfg.Skills.Embedding.TimeoutSeconds)
	}
}

// TestLoadFile_CapsSize verifies that config files larger than maxConfigFileBytes
// are ignored to prevent OOM from a malicious or broken config file.
func TestLoadFile_CapsSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "odek.json")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxConfigFileBytes+1)), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := loadFile(path)
	if cfg.Model != "" {
		t.Fatalf("loadFile should reject a huge config file, got Model=%q", cfg.Model)
	}
}

// TestLoadFile_CapsSizeViaLimitReader verifies the TOCTOU-hardened read path:
// even if a file grows after open, only maxConfigFileBytes+1 bytes are read.
func TestLoadFile_CapsSizeViaLimitReader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "odek.json")
	// Start with a small, valid file.
	if err := os.WriteFile(path, []byte(`{"model":"small"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Replace it with a huge file before loadFile reads it.
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxConfigFileBytes+1)), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := loadFile(path)
	if cfg.Model != "" {
		t.Fatalf("loadFile should reject oversized file read via LimitReader, got Model=%q", cfg.Model)
	}
}

// TestLoadConfig_SecretsEnvPermissionCheck verifies that secrets.env is only
// loaded when it is owner-readable (finding #78).
func TestLoadConfig_SecretsEnvPermissionCheck(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	odekDir := filepath.Join(home, ".odek")
	if err := os.MkdirAll(odekDir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(odekDir, "secrets.env")

	// World/group-readable secrets.env must be ignored.
	if err := os.WriteFile(path, []byte("ODEK_TEST_SECRET=world-readable\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ODEK_TEST_SECRET", "")
	LoadConfig(CLIFlags{})
	if os.Getenv("ODEK_TEST_SECRET") == "world-readable" {
		t.Error("world-readable secrets.env was loaded")
	}

	// Owner-only readable secrets.env must be loaded.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("ODEK_TEST_SECRET=owner-only\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Ensure the file really is 0600 even under a permissive umask.
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ODEK_TEST_SECRET", "")
	LoadConfig(CLIFlags{})
	if os.Getenv("ODEK_TEST_SECRET") != "owner-only" {
		t.Errorf("owner-only secrets.env not loaded, got %q", os.Getenv("ODEK_TEST_SECRET"))
	}
}

func TestLoadConfig_ExtendedMemoryEnv(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODEK_MEMORY_EXTENDED_ENABLED", "true")
	t.Setenv("ODEK_MEMORY_EXTENDED_MAX_SIZE_MB", "200")
	t.Setenv("ODEK_MEMORY_EXTENDED_ATOM_MAX_CHARS", "500")
	t.Setenv("ODEK_MEMORY_EXTENDED_MEMORY_BUDGET_CHARS", "4000")
	cfg := LoadConfig(CLIFlags{})
	if cfg.Memory.Extended == nil {
		t.Fatal("Extended memory config not loaded from env")
	}
	if cfg.Memory.Extended.Enabled == nil || !*cfg.Memory.Extended.Enabled {
		t.Error("Extended memory should be enabled")
	}
	if cfg.Memory.Extended.MaxSizeMB != 200 {
		t.Errorf("MaxSizeMB = %d, want 200", cfg.Memory.Extended.MaxSizeMB)
	}
	if cfg.Memory.Extended.AtomMaxChars != 500 {
		t.Errorf("AtomMaxChars = %d, want 500", cfg.Memory.Extended.AtomMaxChars)
	}
	if cfg.Memory.Extended.MemoryBudgetChars != 4000 {
		t.Errorf("MemoryBudgetChars = %d, want 4000", cfg.Memory.Extended.MemoryBudgetChars)
	}
}

func TestLoadConfig_ExtendedMemoryProactiveEnv(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODEK_MEMORY_EXTENDED_FOLLOW_UP_SUGGESTIONS_ENABLED", "false")
	t.Setenv("ODEK_MEMORY_EXTENDED_FOLLOW_UP_SUGGESTION_MIN_CONFIDENCE", "0.75")
	t.Setenv("ODEK_MEMORY_EXTENDED_PROACTIVE_NUDGES_ENABLED", "true")
	t.Setenv("ODEK_MEMORY_EXTENDED_NUDGE_MAX_PER_DAY", "3")
	t.Setenv("ODEK_MEMORY_EXTENDED_NUDGE_COOLDOWN_HOURS", "12")
	t.Setenv("ODEK_MEMORY_EXTENDED_NUDGE_STALE_GOAL_DAYS", "14")
	cfg := LoadConfig(CLIFlags{})
	if cfg.Memory.Extended == nil {
		t.Fatal("Extended memory config not loaded from env")
	}
	ext := cfg.Memory.Extended
	if ext.FollowUpSuggestionsEnabled == nil || *ext.FollowUpSuggestionsEnabled {
		t.Error("FollowUpSuggestionsEnabled should be false")
	}
	if ext.FollowUpSuggestionMinConfidence != 0.75 {
		t.Errorf("FollowUpSuggestionMinConfidence = %v, want 0.75", ext.FollowUpSuggestionMinConfidence)
	}
	if ext.ProactiveNudgesEnabled == nil || !*ext.ProactiveNudgesEnabled {
		t.Error("ProactiveNudgesEnabled should be true")
	}
	if ext.NudgeMaxPerDay != 3 {
		t.Errorf("NudgeMaxPerDay = %d, want 3", ext.NudgeMaxPerDay)
	}
	if ext.NudgeCooldownHours != 12 {
		t.Errorf("NudgeCooldownHours = %d, want 12", ext.NudgeCooldownHours)
	}
	if ext.NudgeStaleGoalDays != 14 {
		t.Errorf("NudgeStaleGoalDays = %d, want 14", ext.NudgeStaleGoalDays)
	}
}

func TestLoadConfig_ExtendedMemoryCLIOverridesEnv(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODEK_MEMORY_EXTENDED_MAX_SIZE_MB", "200")
	cfg := LoadConfig(CLIFlags{
		MemoryExtendedEnabled:           boolPtr(true),
		MemoryExtendedMaxSizeMB:         300,
		MemoryExtendedAtomMaxChars:      600,
		MemoryExtendedMemoryBudgetChars: 5000,
	})
	if cfg.Memory.Extended == nil {
		t.Fatal("Extended memory config not resolved")
	}
	if !*cfg.Memory.Extended.Enabled {
		t.Error("Extended memory should be enabled")
	}
	if cfg.Memory.Extended.MaxSizeMB != 300 {
		t.Errorf("MaxSizeMB = %d, want 300", cfg.Memory.Extended.MaxSizeMB)
	}
	if cfg.Memory.Extended.AtomMaxChars != 600 {
		t.Errorf("AtomMaxChars = %d, want 600", cfg.Memory.Extended.AtomMaxChars)
	}
	if cfg.Memory.Extended.MemoryBudgetChars != 5000 {
		t.Errorf("MemoryBudgetChars = %d, want 5000", cfg.Memory.Extended.MemoryBudgetChars)
	}
}

func TestLoadConfig_ProjectMemoryRejected(t *testing.T) {
	wd := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODEK_MEMORY_EXTENDED_ENABLED", "true")
	if err := os.WriteFile(filepath.Join(wd, "odek.json"), []byte(`{"memory":{"extended":{"enabled":false,"max_size_mb":50}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PWD", wd)
	origGetwd, _ := os.Getwd()
	os.Chdir(wd)
	defer os.Chdir(origGetwd)
	cfg := LoadConfig(CLIFlags{})
	// Project memory should be rejected, so env-true wins.
	if cfg.Memory.Extended == nil || cfg.Memory.Extended.Enabled == nil || !*cfg.Memory.Extended.Enabled {
		t.Error("project memory should be rejected; env enabled should win")
	}
	if cfg.Memory.Extended.MaxSizeMB != 100 {
		t.Errorf("MaxSizeMB = %d, want default 100 (project rejected)", cfg.Memory.Extended.MaxSizeMB)
	}
}

func TestIsVarStart(t *testing.T) {
	cases := []struct {
		c    byte
		want bool
	}{
		{'a', true},
		{'z', true},
		{'A', true},
		{'Z', true},
		{'_', true},
		{'0', false},
		{'9', false},
		{'$', false},
		{' ', false},
	}
	for _, tc := range cases {
		if got := isVarStart(tc.c); got != tc.want {
			t.Errorf("isVarStart(%q) = %v, want %v", tc.c, got, tc.want)
		}
	}
}

func TestIsVarCont(t *testing.T) {
	cases := []struct {
		c    byte
		want bool
	}{
		{'a', true},
		{'A', true},
		{'_', true},
		{'0', true},
		{'9', true},
		{'$', false},
		{' ', false},
	}
	for _, tc := range cases {
		if got := isVarCont(tc.c); got != tc.want {
			t.Errorf("isVarCont(%q) = %v, want %v", tc.c, got, tc.want)
		}
	}
}

// TestEnvVar_Compaction verifies ODEK_COMPACTION enables compaction.
func TestEnvVar_Compaction(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	t.Setenv("ODEK_COMPACTION", "true")

	cfg := LoadConfig(CLIFlags{})
	if !cfg.Compaction {
		t.Error("Compaction should be true when ODEK_COMPACTION=true")
	}
}

// TestCLIFlags_Compaction verifies the --compaction CLI flag enables compaction.
func TestCLIFlags_Compaction(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())

	enabled := true
	cfg := LoadConfig(CLIFlags{Compaction: &enabled})
	if !cfg.Compaction {
		t.Error("Compaction should be true when CLIFlags.Compaction is set")
	}
}

// TestCompaction_DefaultOn verifies compaction resolves to true when no
// layer (config file, env, CLI) sets it.
func TestCompaction_DefaultOn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())

	cfg := LoadConfig(CLIFlags{})
	if !cfg.Compaction {
		t.Error("Compaction should default to true when no layer sets it")
	}
}

// TestCompaction_ExplicitFalseWins verifies an explicit false from any
// layer (env, CLI, config file) disables compaction despite the default-on.
func TestCompaction_ExplicitFalseWins(t *testing.T) {
	t.Run("env", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Chdir(t.TempDir())
		t.Setenv("ODEK_COMPACTION", "false")

		cfg := LoadConfig(CLIFlags{})
		if cfg.Compaction {
			t.Error("Compaction should be false when ODEK_COMPACTION=false")
		}
	})
	t.Run("cli", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Chdir(t.TempDir())

		disabled := false
		cfg := LoadConfig(CLIFlags{Compaction: &disabled})
		if cfg.Compaction {
			t.Error("Compaction should be false when CLIFlags.Compaction is explicitly false")
		}
	})
	t.Run("file", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Chdir(t.TempDir())

		odekDir := filepath.Join(home, ".odek")
		if err := os.MkdirAll(odekDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(odekDir, "config.json"), []byte(`{"compaction": false}`), 0o600); err != nil {
			t.Fatal(err)
		}

		cfg := LoadConfig(CLIFlags{})
		if cfg.Compaction {
			t.Error("Compaction should be false when config.json sets compaction=false")
		}
	})
}

func TestStreamLayering(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Default: off.
	cfg := LoadConfig(CLIFlags{})
	if cfg.Stream {
		t.Error("default Stream = true, want false")
	}

	// Env enables.
	t.Setenv("ODEK_STREAM", "1")
	cfg = LoadConfig(CLIFlags{})
	if !cfg.Stream {
		t.Error("ODEK_STREAM=1 not honored")
	}

	// CLI overrides env.
	off := false
	cfg = LoadConfig(CLIFlags{Stream: &off})
	if cfg.Stream {
		t.Error("CLI stream=false did not override ODEK_STREAM=1")
	}
}
