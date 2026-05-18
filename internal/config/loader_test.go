package config

import (
	"os"
	"path/filepath"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

func TestLoadConfig_Defaults(t *testing.T) {
	// No files, no env, no CLI — everything should be zero-valued
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
	os.Setenv("KODE_MODEL", "env-model")
	os.Setenv("KODE_BASE_URL", "https://env.example.com/v1")
	os.Setenv("KODE_THINKING", "low")
	os.Setenv("KODE_SANDBOX", "true")
	defer os.Unsetenv("KODE_MODEL")
	defer os.Unsetenv("KODE_BASE_URL")
	defer os.Unsetenv("KODE_THINKING")
	defer os.Unsetenv("KODE_SANDBOX")

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
	os.Setenv("KODE_MODEL", "deepseek-v4-flash")
	os.Setenv("KODE_BASE_URL", "https://custom.deepseek.com/v1")
	os.Setenv("KODE_API_KEY", "sk-env-key")
	os.Setenv("KODE_THINKING", "enabled")
	os.Setenv("KODE_MAX_ITER", "50")
	os.Setenv("KODE_SANDBOX", "true")
	os.Setenv("KODE_NO_COLOR", "false")
	os.Setenv("KODE_NO_AGENTS", "true")
	os.Setenv("KODE_SYSTEM", "Env system prompt.")
	defer func() {
		os.Unsetenv("KODE_MODEL")
		os.Unsetenv("KODE_BASE_URL")
		os.Unsetenv("KODE_API_KEY")
		os.Unsetenv("KODE_THINKING")
		os.Unsetenv("KODE_MAX_ITER")
		os.Unsetenv("KODE_SANDBOX")
		os.Unsetenv("KODE_NO_COLOR")
		os.Unsetenv("KODE_NO_AGENTS")
		os.Unsetenv("KODE_SYSTEM")
	}()

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
	// No config files, no KODE_API_KEY — falls back to DEEPSEEK_API_KEY
	os.Setenv("DEEPSEEK_API_KEY", "sk-deepseek-fallback")
	defer os.Unsetenv("DEEPSEEK_API_KEY")

	cfg := LoadConfig(CLIFlags{})
	if cfg.APIKey != "sk-deepseek-fallback" {
		t.Errorf("APIKey = %q, want DEEPSEEK_API_KEY fallback", cfg.APIKey)
	}
}

func TestLoadConfig_APIKeyFallback_OpenAI(t *testing.T) {
	os.Unsetenv("DEEPSEEK_API_KEY")
	os.Setenv("OPENAI_API_KEY", "sk-openai-fallback")
	defer os.Unsetenv("OPENAI_API_KEY")

	cfg := LoadConfig(CLIFlags{})
	if cfg.APIKey != "sk-openai-fallback" {
		t.Errorf("APIKey = %q, want OPENAI_API_KEY fallback", cfg.APIKey)
	}
}

func TestLoadConfig_APIKey_KODEOverridesLegacy(t *testing.T) {
	os.Setenv("KODE_API_KEY", "sk-kode")
	os.Setenv("DEEPSEEK_API_KEY", "sk-deepseek")
	defer os.Unsetenv("KODE_API_KEY")
	defer os.Unsetenv("DEEPSEEK_API_KEY")

	cfg := LoadConfig(CLIFlags{})
	if cfg.APIKey != "sk-kode" {
		t.Errorf("APIKey = %q, want KODE_API_KEY (higher priority)", cfg.APIKey)
	}
}

func TestLoadConfig_EnvBoolParsing(t *testing.T) {
	os.Setenv("KODE_SANDBOX", "1")
	os.Setenv("KODE_NO_COLOR", "0")
	defer os.Unsetenv("KODE_SANDBOX")
	defer os.Unsetenv("KODE_NO_COLOR")

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
	prevHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", prevHome)

	// Create ~/kode/config.json
	cfgDir := filepath.Join(dir, "kode")
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

func TestLoadConfig_ProjectOverridesGlobal(t *testing.T) {
	dir := t.TempDir()

	// Set HOME to temp dir for global config
	prevHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", prevHome)

	// Create ~/kode/config.json (global)
	globalDir := filepath.Join(dir, "kode")
	os.MkdirAll(globalDir, 0755)
	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"model": "global-model",
		"base_url": "https://global.example.com/v1",
		"thinking": "enabled",
		"max_iterations": 30
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Create ./kode.json in temp dir (project)
	cwd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(cwd)

	if err := os.WriteFile(filepath.Join(dir, "kode.json"), []byte(`{
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

func TestLoadConfig_EnvOverridesProjectFile(t *testing.T) {
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(cwd)

	// Create ./kode.json
	if err := os.WriteFile(filepath.Join(dir, "kode.json"), []byte(`{
		"model": "project-model",
		"max_iterations": 50
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Set env vars
	os.Setenv("KODE_MODEL", "env-model")
	defer os.Unsetenv("KODE_MODEL")

	cfg := LoadConfig(CLIFlags{})
	if cfg.Model != "env-model" {
		t.Errorf("Model = %q, want 'env-model' (env overrides project)", cfg.Model)
	}
	if cfg.MaxIter != 50 {
		t.Errorf("MaxIter = %d, want 50 (env didn't set this)", cfg.MaxIter)
	}
}

func TestLoadConfig_CLIOverridesProjectFile(t *testing.T) {
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(cwd)

	// Create ./kode.json
	if err := os.WriteFile(filepath.Join(dir, "kode.json"), []byte(`{
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

	prevHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", prevHome)

	os.Setenv("KODE_MODEL_VAR", "expanded-model")
	os.Setenv("KODE_API_KEY_VAR", "sk-expanded")
	defer os.Unsetenv("KODE_MODEL_VAR")
	defer os.Unsetenv("KODE_API_KEY_VAR")

	globalDir := filepath.Join(dir, "kode")
	os.MkdirAll(globalDir, 0755)
	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"model": "${KODE_MODEL_VAR}",
		"api_key": "${KODE_API_KEY_VAR}"
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
	prevHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", prevHome)

	// Don't create any config files
	cfg := LoadConfig(CLIFlags{})
	if cfg.Model != "" {
		t.Errorf("Model = %q, want empty", cfg.Model)
	}
}

func TestLoadConfig_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	prevHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", prevHome)

	globalDir := filepath.Join(dir, "kode")
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

func TestLoadConfig_SkillsLearnEnvDoesNotClobberSkillsConfig(t *testing.T) {
	// Regression: KODE_SKILLS_LEARN env var should merge Learn into
	// existing skills config from files, not replace the entire struct.
	dir := t.TempDir()

	prevHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", prevHome)

	// Create project file with skills settings
	cwd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(cwd)

	if err := os.WriteFile(filepath.Join(dir, "kode.json"), []byte(`{
		"skills": {
			"max_auto_load": 5,
			"max_lazy_slots": 10,
			"dirs": ["/custom/skills"],
			"import": {
				"max_size_bytes": 524288,
				"timeout_seconds": 10,
				"require_https": true
			},
			"curation": {
				"staleness_days": 60,
				"auto_prune": true
			}
		}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Set KODE_SKILLS_LEARN — should NOT clobber other skills fields
	os.Setenv("KODE_SKILLS_LEARN", "true")
	defer os.Unsetenv("KODE_SKILLS_LEARN")

	cfg := LoadConfig(CLIFlags{})
	if !cfg.Skills.Learn {
		t.Error("Skills.Learn should be true from env")
	}
	if cfg.Skills.MaxAutoLoad != 5 {
		t.Errorf("Skills.MaxAutoLoad = %d, want 5 (survives env override)", cfg.Skills.MaxAutoLoad)
	}
	if cfg.Skills.MaxLazySlots != 10 {
		t.Errorf("Skills.MaxLazySlots = %d, want 10", cfg.Skills.MaxLazySlots)
	}
	if len(cfg.Skills.Dirs) != 1 || cfg.Skills.Dirs[0] != "/custom/skills" {
		t.Errorf("Skills.Dirs = %v, want [\"/custom/skills\"]", cfg.Skills.Dirs)
	}
	if !cfg.Skills.Import.RequireHTTPS {
		t.Error("Skills.Import.RequireHTTPS should be true")
	}
	if cfg.Skills.Curation.StalenessDays != 60 {
		t.Errorf("Skills.Curation.StalenessDays = %d, want 60", cfg.Skills.Curation.StalenessDays)
	}
}

func TestLoadConfig_SkillsLearnCLIDoesNotClobberSkillsConfig(t *testing.T) {
	// Regression: --learn CLI flag should merge, not replace.
	dir := t.TempDir()

	cwd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(cwd)

	if err := os.WriteFile(filepath.Join(dir, "kode.json"), []byte(`{
		"skills": {
			"max_auto_load": 7,
			"curation": {"staleness_days": 30}
		}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	b := true
	cfg := LoadConfig(CLIFlags{Learn: &b})
	if !cfg.Skills.Learn {
		t.Error("Skills.Learn should be true from CLI")
	}
	if cfg.Skills.MaxAutoLoad != 7 {
		t.Errorf("Skills.MaxAutoLoad = %d, want 7 (survives CLI override)", cfg.Skills.MaxAutoLoad)
	}
	if cfg.Skills.Curation.StalenessDays != 30 {
		t.Errorf("Skills.Curation.StalenessDays = %d, want 30", cfg.Skills.Curation.StalenessDays)
	}
}
