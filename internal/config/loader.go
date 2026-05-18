// Package config loads and merges kode configuration from multiple sources.
//
// Priority (lowest to highest):
//  1. ~/kode/config.json   — global defaults (shared across projects)
//  2. ./kode.json          — project-specific overrides
//  3. KODE_* env vars      — runtime/environment overrides
//  4. CLI flags            — explicit invocation overrides (highest)
//
// Both config files are optional. Missing files are silently ignored.
// String values in config files support ${VAR} environment variable
// substitution (e.g. "api_key": "${MY_API_KEY}").
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
)

// ── Types ──────────────────────────────────────────────────────────────

// CLIFlags holds values parsed from the CLI. Zero/nil values mean the
// flag was not explicitly passed — the config loader will look at lower
// priority layers for these fields.
type CLIFlags struct {
	Model    string
	BaseURL  string
	System   string
	Thinking string
	MaxIter  int   // 0 = not set
	Sandbox  *bool // nil = not set
	NoColor  *bool // nil = not set
	NoAgents *bool // nil = not set
	Task     string

	// Sandbox-specific
	SandboxImage    string
	SandboxNetwork  string
	SandboxMemory   string
	SandboxCPUs     string
	SandboxUser     string
	SandboxReadonly *bool // nil = not set
}

// FileConfig is the JSON schema used by ~/kode/config.json and ./kode.json.
// Pointer booleans distinguish "explicitly set to false" from "not set".
type FileConfig struct {
	Model   string `json:"model,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
	APIKey  string `json:"api_key,omitempty"`

	Thinking string `json:"thinking,omitempty"`
	MaxIter  int    `json:"max_iterations,omitempty"`

	Sandbox  *bool `json:"sandbox,omitempty"`
	NoColor  *bool `json:"no_color,omitempty"`
	NoAgents *bool `json:"no_agents,omitempty"`

	System string `json:"system,omitempty"`

	// Sandbox-specific
	SandboxImage    string            `json:"sandbox_image,omitempty"`
	SandboxNetwork  string            `json:"sandbox_network,omitempty"`
	SandboxReadonly *bool             `json:"sandbox_readonly,omitempty"`
	SandboxMemory   string            `json:"sandbox_memory,omitempty"`
	SandboxCPUs     string            `json:"sandbox_cpus,omitempty"`
	SandboxUser     string            `json:"sandbox_user,omitempty"`
	SandboxEnv      map[string]string `json:"sandbox_env,omitempty"`
	SandboxVolumes  []string          `json:"sandbox_volumes,omitempty"`
}

// ResolvedConfig is the fully merged result. Every field has a concrete
// value — callers can read directly without checking for "not set".
type ResolvedConfig struct {
	Model        string
	BaseURL      string
	APIKey       string
	Thinking     string
	MaxIter      int
	Sandbox      bool
	NoColor      bool
	NoAgents     bool
	System       string

	// Sandbox-specific
	SandboxImage    string
	SandboxNetwork  string
	SandboxReadonly bool
	SandboxMemory   string
	SandboxCPUs     string
	SandboxUser     string
	SandboxEnv      map[string]string
	SandboxVolumes  []string
}

// ── Defaults ───────────────────────────────────────────────────────────

const (
	DefaultSandboxNetwork = "bridge"
)

// ── Paths ──────────────────────────────────────────────────────────────

// GlobalConfigPath returns the path to the global config file.
// Uses $HOME/kode/config.json.
func GlobalConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "kode", "config.json")
}

// ProjectConfigPath returns the path to the project-level config file.
// Uses ./kode.json relative to the current working directory.
func ProjectConfigPath() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return filepath.Join(wd, "kode.json")
}

// ── File Loading ───────────────────────────────────────────────────────

// loadFile reads and parses a JSON config file. Returns a zero FileConfig
// if the file doesn't exist or can't be read. String values go through
// expandEnv for ${VAR} substitution.
func loadFile(path string) FileConfig {
	if path == "" {
		return FileConfig{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return FileConfig{} // missing or unreadable = empty
	}
	var cfg FileConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return FileConfig{} // invalid JSON = empty (silent)
	}
	// Expand environment variables in all string fields
	cfg.Model = expandEnv(cfg.Model)
	cfg.BaseURL = expandEnv(cfg.BaseURL)
	cfg.APIKey = expandEnv(cfg.APIKey)
	cfg.Thinking = expandEnv(cfg.Thinking)
	cfg.System = expandEnv(cfg.System)
	cfg.SandboxImage = expandEnv(cfg.SandboxImage)
	cfg.SandboxNetwork = expandEnv(cfg.SandboxNetwork)
	cfg.SandboxMemory = expandEnv(cfg.SandboxMemory)
	cfg.SandboxCPUs = expandEnv(cfg.SandboxCPUs)
	cfg.SandboxUser = expandEnv(cfg.SandboxUser)
	return cfg
}

// expandEnv replaces ${VAR} or $VAR with environment variable values.
func expandEnv(s string) string {
	return os.Expand(s, os.Getenv)
}

// ── Environment Variable Loading ───────────────────────────────────────

// envString returns the value of a KODE_* env var, or empty string if unset.
func envString(key string) string {
	return os.Getenv("KODE_" + key)
}

// envBool parses a KODE_* env var as a boolean. Returns nil if the env var
// is empty or not set, or if the value can't be parsed.
func envBool(key string) *bool {
	v := os.Getenv("KODE_" + key)
	if v == "" {
		return nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return nil
	}
	return &b
}

// envInt parses a KODE_* env var as an integer. Returns 0 if unset/unparseable.
func envInt(key string) int {
	v := os.Getenv("KODE_" + key)
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}

// ── Merge ──────────────────────────────────────────────────────────────

// LoadConfig merges configuration from all four layers and returns the
// fully resolved result.
//
// Priority (lowest → highest):
//
//	global file → project file → KODE_* env → CLI flags
//
// For each field, the highest-priority layer that provides a value wins.
// API key has an additional fallback: if none of the four layers provides
// one, it falls back to DEEPSEEK_API_KEY → OPENAI_API_KEY (legacy env vars).
func LoadConfig(cli CLIFlags) ResolvedConfig {
	// Layer 1: global (~/kode/config.json)
	global := loadFile(GlobalConfigPath())

	// Layer 2: project (./kode.json)
	project := loadFile(ProjectConfigPath())

	// Start with global, overlay project
	cfg := overlayFile(FileConfig{}, global)
	cfg = overlayFile(cfg, project)

	// Layer 3: KODE_* env vars
	if v := envString("MODEL"); v != "" {
		cfg.Model = v
	}
	if v := envString("BASE_URL"); v != "" {
		cfg.BaseURL = v
	}
	if v := envString("API_KEY"); v != "" {
		cfg.APIKey = v
	}
	if v := envString("THINKING"); v != "" {
		cfg.Thinking = v
	}
	if v := envInt("MAX_ITER"); v > 0 {
		cfg.MaxIter = v
	}
	if v := envBool("SANDBOX"); v != nil {
		cfg.Sandbox = v
	}
	if v := envBool("NO_COLOR"); v != nil {
		cfg.NoColor = v
	}
	if v := envBool("NO_AGENTS"); v != nil {
		cfg.NoAgents = v
	}
	if v := envString("SYSTEM"); v != "" {
		cfg.System = v
	}
	if v := envString("SANDBOX_IMAGE"); v != "" {
		cfg.SandboxImage = v
	}
	if v := envString("SANDBOX_NETWORK"); v != "" {
		cfg.SandboxNetwork = v
	}
	if v := envBool("SANDBOX_READONLY"); v != nil {
		cfg.SandboxReadonly = v
	}
	if v := envString("SANDBOX_MEMORY"); v != "" {
		cfg.SandboxMemory = v
	}
	if v := envString("SANDBOX_CPUS"); v != "" {
		cfg.SandboxCPUs = v
	}
	if v := envString("SANDBOX_USER"); v != "" {
		cfg.SandboxUser = v
	}

	// Layer 4: CLI flags (highest priority)
	if cli.Model != "" {
		cfg.Model = cli.Model
	}
	if cli.BaseURL != "" {
		cfg.BaseURL = cli.BaseURL
	}
	if cli.Thinking != "" {
		cfg.Thinking = cli.Thinking
	}
	if cli.MaxIter > 0 {
		cfg.MaxIter = cli.MaxIter
	}
	if cli.Sandbox != nil {
		cfg.Sandbox = cli.Sandbox
	}
	if cli.NoColor != nil {
		cfg.NoColor = cli.NoColor
	}
	if cli.NoAgents != nil {
		cfg.NoAgents = cli.NoAgents
	}
	if cli.System != "" {
		cfg.System = cli.System
	}
	if cli.SandboxImage != "" {
		cfg.SandboxImage = cli.SandboxImage
	}
	if cli.SandboxNetwork != "" {
		cfg.SandboxNetwork = cli.SandboxNetwork
	}
	if cli.SandboxReadonly != nil {
		cfg.SandboxReadonly = cli.SandboxReadonly
	}
	if cli.SandboxMemory != "" {
		cfg.SandboxMemory = cli.SandboxMemory
	}
	if cli.SandboxCPUs != "" {
		cfg.SandboxCPUs = cli.SandboxCPUs
	}
	if cli.SandboxUser != "" {
		cfg.SandboxUser = cli.SandboxUser
	}

	// Build resolved config with concrete values
	resolved := ResolvedConfig{
		Model:    cfg.Model,
		BaseURL:  cfg.BaseURL,
		APIKey:   cfg.APIKey,
		Thinking: cfg.Thinking,
		MaxIter:  cfg.MaxIter,
		System:   cfg.System,

		SandboxImage:   cfg.SandboxImage, // empty = resolve at call site (Dockerfile.kode or alpine:latest)
		SandboxNetwork: ifZero(cfg.SandboxNetwork, DefaultSandboxNetwork),
		SandboxMemory:  cfg.SandboxMemory,
		SandboxCPUs:    cfg.SandboxCPUs,
		SandboxUser:    cfg.SandboxUser,
		SandboxEnv:     cfg.SandboxEnv,
		SandboxVolumes: cfg.SandboxVolumes,
	}

	// Booleans: default to false if not set
	if cfg.Sandbox != nil {
		resolved.Sandbox = *cfg.Sandbox
	}
	if cfg.NoColor != nil {
		resolved.NoColor = *cfg.NoColor
	}
	if cfg.NoAgents != nil {
		resolved.NoAgents = *cfg.NoAgents
	}
	if cfg.SandboxReadonly != nil {
		resolved.SandboxReadonly = *cfg.SandboxReadonly
	}

	// API key fallback chain: resolved → DEEPSEEK_API_KEY → OPENAI_API_KEY
	if resolved.APIKey == "" {
		resolved.APIKey = os.Getenv("DEEPSEEK_API_KEY")
	}
	if resolved.APIKey == "" {
		resolved.APIKey = os.Getenv("OPENAI_API_KEY")
	}

	return resolved
}

// ifZero returns the default value if s is empty, otherwise s.
func ifZero(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// overlayFile overlays a higher-priority FileConfig onto a lower-priority one.
// Only fields that are explicitly set (non-zero for scalars, non-nil for
// pointers) override the base value.
func overlayFile(base, override FileConfig) FileConfig {
	if override.Model != "" {
		base.Model = override.Model
	}
	if override.BaseURL != "" {
		base.BaseURL = override.BaseURL
	}
	if override.APIKey != "" {
		base.APIKey = override.APIKey
	}
	if override.Thinking != "" {
		base.Thinking = override.Thinking
	}
	if override.MaxIter > 0 {
		base.MaxIter = override.MaxIter
	}
	if override.Sandbox != nil {
		base.Sandbox = override.Sandbox
	}
	if override.NoColor != nil {
		base.NoColor = override.NoColor
	}
	if override.NoAgents != nil {
		base.NoAgents = override.NoAgents
	}
	if override.System != "" {
		base.System = override.System
	}
	if override.SandboxImage != "" {
		base.SandboxImage = override.SandboxImage
	}
	if override.SandboxNetwork != "" {
		base.SandboxNetwork = override.SandboxNetwork
	}
	if override.SandboxReadonly != nil {
		base.SandboxReadonly = override.SandboxReadonly
	}
	if override.SandboxMemory != "" {
		base.SandboxMemory = override.SandboxMemory
	}
	if override.SandboxCPUs != "" {
		base.SandboxCPUs = override.SandboxCPUs
	}
	if override.SandboxUser != "" {
		base.SandboxUser = override.SandboxUser
	}
	if override.SandboxEnv != nil {
		if base.SandboxEnv == nil {
			base.SandboxEnv = make(map[string]string)
		}
		for k, v := range override.SandboxEnv {
			base.SandboxEnv[k] = expandEnv(v)
		}
	}
	if override.SandboxVolumes != nil {
		base.SandboxVolumes = append([]string{}, override.SandboxVolumes...)
	}
	return base
}
