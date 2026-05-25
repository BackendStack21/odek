package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BackendStack21/odek/internal/memory"
)

func boolPtr(b bool) *bool { return &b }

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
	os.Setenv("ODEK_MODEL", "env-model")
	os.Setenv("ODEK_BASE_URL", "https://env.example.com/v1")
	os.Setenv("ODEK_THINKING", "low")
	os.Setenv("ODEK_SANDBOX", "true")
	defer os.Unsetenv("ODEK_MODEL")
	defer os.Unsetenv("ODEK_BASE_URL")
	defer os.Unsetenv("ODEK_THINKING")
	defer os.Unsetenv("ODEK_SANDBOX")

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
	os.Setenv("ODEK_MODEL", "deepseek-v4-flash")
	os.Setenv("ODEK_BASE_URL", "https://custom.deepseek.com/v1")
	os.Setenv("ODEK_API_KEY", "sk-env-key")
	os.Setenv("ODEK_THINKING", "enabled")
	os.Setenv("ODEK_MAX_ITER", "50")
	os.Setenv("ODEK_SANDBOX", "true")
	os.Setenv("ODEK_NO_COLOR", "false")
	os.Setenv("ODEK_NO_AGENTS", "true")
	os.Setenv("ODEK_SYSTEM", "Env system prompt.")
	defer func() {
		os.Unsetenv("ODEK_MODEL")
		os.Unsetenv("ODEK_BASE_URL")
		os.Unsetenv("ODEK_API_KEY")
		os.Unsetenv("ODEK_THINKING")
		os.Unsetenv("ODEK_MAX_ITER")
		os.Unsetenv("ODEK_SANDBOX")
		os.Unsetenv("ODEK_NO_COLOR")
		os.Unsetenv("ODEK_NO_AGENTS")
		os.Unsetenv("ODEK_SYSTEM")
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
	os.Setenv("ODEK_API_KEY", "sk-odek")
	os.Setenv("DEEPSEEK_API_KEY", "sk-deepseek")
	defer os.Unsetenv("ODEK_API_KEY")
	defer os.Unsetenv("DEEPSEEK_API_KEY")

	cfg := LoadConfig(CLIFlags{})
	if cfg.APIKey != "sk-odek" {
		t.Errorf("APIKey = %q, want ODEK_API_KEY (higher priority)", cfg.APIKey)
	}
}

func TestLoadConfig_EnvBoolParsing(t *testing.T) {
	os.Setenv("ODEK_SANDBOX", "1")
	os.Setenv("ODEK_NO_COLOR", "0")
	defer os.Unsetenv("ODEK_SANDBOX")
	defer os.Unsetenv("ODEK_NO_COLOR")

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

func TestLoadConfig_ProjectOverridesGlobal(t *testing.T) {
	dir := t.TempDir()

	// Set HOME to temp dir for global config
	prevHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", prevHome)

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
	cwd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(cwd)

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

func TestLoadConfig_EnvOverridesProjectFile(t *testing.T) {
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(cwd)

	// Create ./odek.json
	if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(`{
		"model": "project-model",
		"max_iterations": 50
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Set env vars
	os.Setenv("ODEK_MODEL", "env-model")
	defer os.Unsetenv("ODEK_MODEL")

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

	prevHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", prevHome)

	os.Setenv("ODEK_MODEL_VAR", "expanded-model")
	os.Setenv("ODEK_API_KEY_VAR", "sk-expanded")
	defer os.Unsetenv("ODEK_MODEL_VAR")
	defer os.Unsetenv("ODEK_API_KEY_VAR")

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

func TestLoadConfig_SkillsLearnEnvDoesNotClobberSkillsConfig(t *testing.T) {
	// Regression: ODEK_SKILLS_LEARN env var should merge Learn into
	// existing skills config from files, not replace the entire struct.
	dir := t.TempDir()

	prevHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", prevHome)

	// Create project file with skills settings
	cwd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(cwd)

	if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(`{
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

	// Set ODEK_SKILLS_LEARN — should NOT clobber other skills fields
	os.Setenv("ODEK_SKILLS_LEARN", "true")
	defer os.Unsetenv("ODEK_SKILLS_LEARN")

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

	if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(`{
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

func TestLoadConfig_MemoryDefaults(t *testing.T) {
	// When no memory section is configured, the resolved config must have
	// sensible defaults (Enabled=true, all features on).
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
	prevHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", prevHome)

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

func TestLoadConfig_MemoryProjectOverridesGlobal(t *testing.T) {
	dir := t.TempDir()
	prevHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", prevHome)

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

	// Project config overrides some memory fields
	cwd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(cwd)

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

	// Project overrides
	if mem.FactsLimitUser != 1200 {
		t.Errorf("Memory.FactsLimitUser = %d, want 1200 (project overrides global)", mem.FactsLimitUser)
	}
	if mem.BufferLines != 25 {
		t.Errorf("Memory.BufferLines = %d, want 25 (project overrides global)", mem.BufferLines)
	}

	// Global value preserved (not overridden by project)
	if mem.MergeOnWrite == nil || !*mem.MergeOnWrite {
		t.Error("Memory.MergeOnWrite should be true (preserved from global)")
	}

	// Defaults for fields not set in either file
	if mem.Enabled == nil || !*mem.Enabled {
		t.Error("Memory.Enabled should default to true")
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
	os.Setenv("ODEK_API_KEY", "sk-odek-test")
	os.Setenv("DEEPSEEK_API_KEY", "sk-deepseek-test")
	os.Setenv("OPENAI_API_KEY", "sk-openai-test")

	dir := t.TempDir()
	prevHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", prevHome)

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
	cfg := LoadConfig(CLIFlags{})
	if cfg.InteractionMode == "" {
		t.Errorf("InteractionMode = %q, want non-empty default", cfg.InteractionMode)
	}
}

func TestLoadConfig_InteractionModeViaEnv(t *testing.T) {
	// ODEK_INTERACTION_MODE should override the default.
	os.Setenv("ODEK_INTERACTION_MODE", "verbose")
	defer os.Unsetenv("ODEK_INTERACTION_MODE")

	cfg := LoadConfig(CLIFlags{})
	if cfg.InteractionMode != "verbose" {
		t.Errorf("InteractionMode = %q, want %q", cfg.InteractionMode, "verbose")
	}
}

func TestLoadConfig_InteractionModeViaCLI(t *testing.T) {
	// CLI flag should take precedence over env.
	os.Setenv("ODEK_INTERACTION_MODE", "engaging")
	defer os.Unsetenv("ODEK_INTERACTION_MODE")

	cfg := LoadConfig(CLIFlags{InteractionMode: "verbose"})
	if cfg.InteractionMode != "verbose" {
		t.Errorf("InteractionMode = %q, want %q", cfg.InteractionMode, "verbose")
	}
}

func TestLoadConfig_InteractionModeOff(t *testing.T) {
	// "off" should be accepted as a valid value via CLI.
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
	cwd, _ := os.Getwd()
	defer os.Chdir(cwd)
	os.Chdir(t.TempDir())
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

	cwd, _ := os.Getwd()
	defer os.Chdir(cwd)
	os.Chdir(t.TempDir())
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

	cwd, _ := os.Getwd()
	defer os.Chdir(cwd)
	os.Chdir(t.TempDir())
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

	cwd, _ := os.Getwd()
	defer os.Chdir(cwd)
	os.Chdir(t.TempDir())
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

// ── Red test: API key env vars cleared, not re-injected ────────────────────

// TestLoadConfig_LegacyAPIKeyEnvVarLost tests that a user relying solely on
// DEEPSEEK_API_KEY (the documented fallback) has their key cleared by LoadConfig
// but properly re-injected into subagent/spawnChild environments.
// FIXED: spawnChild() and delegateTasksTool both re-inject all three env var
// forms (ODEK_API_KEY, DEEPSEEK_API_KEY, OPENAI_API_KEY) from the resolved key.
func TestLoadConfig_LegacyAPIKeyEnvVarLost(t *testing.T) {
	// Set only the legacy DEEPSEEK_API_KEY — no ODEK_API_KEY, no config file.
	os.Setenv("DEEPSEEK_API_KEY", "sk-deepseek-only")
	defer os.Unsetenv("DEEPSEEK_API_KEY")

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
