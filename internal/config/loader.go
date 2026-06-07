// Package config loads and merges odek configuration from multiple sources.
//
// Priority (lowest to highest):
//  1. ~/.odek/config.json   — global defaults (shared across projects)
//  2. ./odek.json          — project-specific overrides
//  3. ODEK_* env vars      — runtime/environment overrides
//  4. CLI flags            — explicit invocation overrides (highest)
//
// Both config files are optional. Missing files are silently ignored.
// String values in config files support ${VAR} environment variable
// substitution (e.g. "api_key": "${MY_API_KEY}"). Use $$ for a literal
// dollar sign.
package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/mcpclient"
	"github.com/BackendStack21/odek/internal/memory"
	"github.com/BackendStack21/odek/internal/redact"
	"github.com/BackendStack21/odek/internal/skills"
	"github.com/BackendStack21/odek/internal/telegram"
)

// ── Types ──────────────────────────────────────────────────────────────

// CLIFlags holds values parsed from the CLI. Zero/nil values mean the
// flag was not explicitly passed — the config loader will look at lower
// priority layers for these fields.
//
// CLIFlags holds CLI-only configuration. These fields participate in
// the same merge chain: global file → project file → ODEK_* env → CLI.
// Fields typed as *bool distinguish "explicitly set to false" from "not set",
// which matters when the config file says "sandbox_readonly: false" (user
// explicitly wants writable) vs the field being absent (inherit from lower
// layer or default).
type CLIFlags struct {
	Model    string
	BaseURL  string
	System   string
	Thinking string
	MaxIter  int   // 0 = not set
	Sandbox  *bool // nil = not set
	NoColor  *bool // nil = not set
	NoAgents *bool // nil = not set
	Learn    *bool // nil = not set
	Task     string

	// PromptCaching enables prompt caching markers for supported providers.
	// Config: prompt_caching, ODEK_PROMPT_CACHING, --prompt-caching.
	PromptCaching *bool // nil = not set

	// Sandbox-specific
	SandboxImage    string
	SandboxNetwork  string
	SandboxMemory   string
	SandboxCPUs     string
	SandboxUser     string
	SandboxReadonly *bool // nil = not set

	// InteractionMode controls how tool-call progress is surfaced.
	// "engaging" (default) = emoji-rich narration, progress message edited.
	// "enhance" = per-tool narrated messages appended, progress header kept.
	// "verbose" = raw tool names, args, and results.
	// "off" = no intermediate progress output, clean answer only.
	InteractionMode string
}

// SkillsConfig holds the skills configuration section from JSON files.
type SkillsConfig struct {
	MaxAutoLoad  *int                   `json:"max_auto_load,omitempty"`
	MaxLazySlots *int                   `json:"max_lazy_slots,omitempty"`
	Learn        *bool                  `json:"learn,omitempty"`
	Dirs         []string               `json:"dirs,omitempty"`
	Import       *skills.ImportConfig   `json:"import,omitempty"`
	Curation     *skills.CurationConfig `json:"curation,omitempty"`
	AutoSave     *skills.AutoSaveConfig `json:"auto_save,omitempty"`
	LLMLearn     *bool                  `json:"llm_learn,omitempty"`
	LLMCurate    *bool                  `json:"llm_curate,omitempty"`
	Verbose      *bool                  `json:"verbose,omitempty"`
}

// TranscriptionConfig controls the transcribe tool (local whisper.cpp).
// Populated from the "transcription" section of odek.json.
type TranscriptionConfig struct {
	Model          string `json:"model,omitempty"`
	Language       string `json:"language,omitempty"`
	AutoTranscribe bool   `json:"auto_transcribe,omitempty"`
	ModelsDir      string `json:"models_dir,omitempty"`
	BinaryPath     string `json:"binary_path,omitempty"`
}

// VisionConfig controls the vision tool (MiniCPM-V 4.6 via llama-mtmd-cli).
// Populated from the "vision" section of odek.json or ~/.odek/config.json.
type VisionConfig struct {
	// ModelsDir is the directory containing model.gguf and mmproj.gguf.
	// Default: /usr/local/share/minicpm-v/models (Docker image path), with
	// fallback to ~/.odek/minicpm-v/models for out-of-container installs.
	ModelsDir string `json:"models_dir,omitempty"`
	// BinaryPath overrides PATH lookup for the llama-mtmd-cli binary.
	BinaryPath string `json:"binary_path,omitempty"`
	// VideoFrames is the number of frames to sample evenly from a video file.
	// Default: 8.
	VideoFrames int `json:"video_frames,omitempty"`
	// AutoDescribe controls whether photos received over Telegram are
	// automatically run through the vision model to extract a description
	// before the agent answers (mirrors transcription.auto_transcribe).
	// Default: true.
	AutoDescribe bool `json:"auto_describe,omitempty"`
}

// FileConfig is the JSON schema used by ~/.odek/config.json and ./odek.json.
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

	// PromptCaching enables prompt caching markers for supported providers.
	PromptCaching *bool `json:"prompt_caching,omitempty"`

	System string `json:"system,omitempty"`

	// Sandbox-specific fields.
	SandboxImage    string            `json:"sandbox_image,omitempty"`
	SandboxNetwork  string            `json:"sandbox_network,omitempty"`
	SandboxReadonly *bool             `json:"sandbox_readonly,omitempty"`
	SandboxMemory   string            `json:"sandbox_memory,omitempty"`
	SandboxCPUs     string            `json:"sandbox_cpus,omitempty"`
	SandboxUser     string            `json:"sandbox_user,omitempty"`
	SandboxEnv      map[string]string `json:"sandbox_env,omitempty"`
	SandboxVolumes  []string          `json:"sandbox_volumes,omitempty"`

	// Dangerous operation approval settings.
	Dangerous *danger.DangerousConfig `json:"dangerous,omitempty"`

	// Skills section (see internal/skills package).
	Skills *SkillsConfig `json:"skills,omitempty"`

	// Memory section controls the persistent memory system.
	Memory *memory.MemoryConfig `json:"memory,omitempty"`

	// MCPServers maps server names to MCP server configurations.
	// Each server is an external MCP server (e.g., Playwright, database,
	// web scraping) whose tools are exposed to the agent.
	// Format matches Claude Code's mcpServers config:
	//
	//	"mcp_servers": {
	//	  "playwright": {
	//	    "command": "npx",
	//	    "args": ["@playwright/mcp"]
	//\t  }
	//\t}
	MCPServers map[string]mcpclient.ServerConfig `json:"mcp_servers,omitempty"`

	// MaxConcurrency limits how many sub-agent tasks run in parallel.
	// Config: max_concurrency, ODEK_MAX_CONCURRENCY.
	// Default: 3.
	MaxConcurrency int `json:"max_concurrency,omitempty"`

	// MaxToolParallel limits how many tool calls run concurrently per
	// agent iteration. Config: max_tool_parallel.
	// Default: 0 (loop uses default of 4).
	MaxToolParallel int `json:"max_tool_parallel,omitempty"`

	// Telegram configures the Telegram bot integration.
	Telegram *telegram.TelegramConfig `json:"telegram,omitempty"`

	// Transcription configures local audio transcription (whisper.cpp).
	Transcription *TranscriptionConfig `json:"transcription,omitempty"`

	// Vision configures local image/video understanding (MiniCPM-V 4.6 via llama-mtmd-cli).
	Vision *VisionConfig `json:"vision,omitempty"`

	// Schedules configures the native in-process task scheduler.
	Schedules *SchedulesConfig `json:"schedules,omitempty"`

	// InteractionMode controls how the agent communicates tool/progress updates.
	// "engaging" (default) = emoji-rich narration, progress message edited.
	// "enhance" = per-tool narrated messages, progress header kept.
	// "verbose" = raw tool names, args, and results.
	// "off" = no progress output, clean answer only.
	InteractionMode string `json:"interaction_mode,omitempty"`

	// ToolProgress controls per-tool progress messages for the Telegram bot.
	//   "all"     (default) — show every tool call
	//   "new"     — only when the tool name changes (dedup consecutive same-tool)
	//   "verbose" — full tool arguments in progress messages
	//   "off"     — no per-tool progress messages (just thinking + final answer)
	ToolProgress string `json:"tool_progress,omitempty"`

	// ToolProgressCleanup controls whether progress messages are deleted after
	// the final answer. Default: true (delete progress messages).
	ToolProgressCleanup *bool `json:"tool_progress_cleanup,omitempty"`
}

// ResolvedConfig is the fully merged result. Every field has a concrete
// value — callers can read directly without checking for "not set".
type ResolvedConfig struct {
	Model         string
	BaseURL       string
	APIKey        string
	Thinking      string
	MaxIter       int
	Sandbox       bool
	NoColor       bool
	NoAgents      bool
	PromptCaching bool
	System        string

	// SandboxImage is the Docker image for the sandbox container.
	// Default: "alpine:latest" (applied at call site, not here —
	// set to "alpine:latest" only if Dockerfile.odek doesn't exist).
	// Config: sandbox_image, ODEK_SANDBOX_IMAGE, --sandbox-image.
	SandboxImage string

	// SandboxNetwork is the Docker network mode.
	// Default: "bridge" (internet access by default).
	// Config: sandbox_network, ODEK_SANDBOX_NETWORK, --sandbox-network.
	SandboxNetwork string

	// SandboxReadonly, when true, mounts the working directory read-only
	// in the container. The agent can read /workspace but cannot write to it.
	// Config: sandbox_readonly, ODEK_SANDBOX_READONLY, --sandbox-readonly.
	SandboxReadonly bool

	// SandboxMemory is the container memory limit (e.g. "512m", "2g").
	// Empty string means no limit.
	// Config: sandbox_memory, ODEK_SANDBOX_MEMORY, --sandbox-memory.
	SandboxMemory string

	// SandboxCPUs is the container CPU limit (e.g. "0.5", "2", "4").
	// Empty string means no limit.
	// Config: sandbox_cpus, ODEK_SANDBOX_CPUS, --sandbox-cpus.
	SandboxCPUs string

	// SandboxUser sets the container user (e.g. "1000:1000" or "node").
	// Empty string means root (default Docker behavior).
	// Config: sandbox_user, ODEK_SANDBOX_USER, --sandbox-user.
	SandboxUser string

	// SandboxEnv holds extra environment variables injected into the
	// container. File-only — no env var or CLI mapping.
	// Config: sandbox_env.
	SandboxEnv map[string]string

	// SandboxVolumes holds extra volume mounts in "host:container" format.
	// File-only — no env var or CLI mapping.
	// Config: sandbox_volumes.
	SandboxVolumes []string

	// Dangerous is the resolved dangerous operations config.
	// Uses danger.DangerousConfig defaults for any unset fields.
	Dangerous danger.DangerousConfig

	// Skills is the resolved skills config with default values.
	Skills skills.SkillsConfig

	// Memory is the resolved memory config with default values.
	Memory memory.MemoryConfig

	// MCPServers maps server names to external MCP server configurations.
	// Populated from the mcp_servers section of odek.json.
	MCPServers map[string]mcpclient.ServerConfig

	// MaxConcurrency limits how many sub-agent tasks run in parallel.
	// Config: max_concurrency, ODEK_MAX_CONCURRENCY.
	// Default: 3.
	MaxConcurrency int

	// MaxToolParallel limits how many tool calls run concurrently per
	// agent iteration. Config: max_tool_parallel.
	// Default: 0 (loop uses default of 4).
	MaxToolParallel int

	// Telegram is the resolved Telegram bot configuration.
	Telegram telegram.TelegramConfig

	// Transcription is the resolved transcription config.
	// Default: auto_transcribe=true, model="tiny", language="", no binary_path.
	Transcription TranscriptionConfig

	// Vision is the resolved vision config.
	// Default: VideoFrames=8, ModelsDir="" (auto-detect), BinaryPath="" (PATH lookup).
	Vision VisionConfig

	// Schedules is the resolved scheduler config.
	// Default: enabled=true, max_concurrent=2, timezone="UTC", catchup=false.
	Schedules ScheduleConfig

	// InteractionMode is the resolved interaction style.
	// Values: "engaging" (default), "enhance", "verbose", or "off".
	// "engaging" (default), "enhance", or "verbose".
	InteractionMode string

	// ToolProgress is the resolved tool progress mode for Telegram.
	// Default: "all".
	ToolProgress string

	// ToolProgressCleanup controls whether progress messages are deleted
	// after the final answer. Default: true.
	ToolProgressCleanup bool
}

// ── Defaults ───────────────────────────────────────────────────────────

const (
	DefaultSandboxNetwork = "none"
)

// ── Paths ──────────────────────────────────────────────────────────────

// GlobalConfigPath returns the path to the global config file.
// Uses $HOME/.odek/config.json.
func GlobalConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".odek", "config.json")
}

// ProjectConfigPath returns the path to the project-level config file.
// Uses ./odek.json relative to the current working directory.
func ProjectConfigPath() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return filepath.Join(wd, "odek.json")
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
		fmt.Fprintf(os.Stderr, "odek: warning: config %s: invalid JSON — ignoring file: %v\n", path, err)
		return FileConfig{} // invalid JSON = empty
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
// Supports $$ as an escape for a literal dollar sign.
func expandEnv(s string) string {
	var buf strings.Builder
	i := 0
	for j := 0; j < len(s); j++ {
		if s[j] != '$' {
			continue
		}
		buf.WriteString(s[i:j])

		// $$ → literal $
		if j+1 < len(s) && s[j+1] == '$' {
			buf.WriteByte('$')
			i = j + 2
			j++ // skip second $
			continue
		}

		// Find variable name: ${VAR} or $VAR or $VAR_NAME
		name, w := parseVarName(s[j+1:])
		i = j + 1 + w

		if name == "" {
			// $ followed by non-identifier: emit as-is
			buf.WriteByte('$')
			continue
		}
		buf.WriteString(os.Getenv(name))
	}
	buf.WriteString(s[i:])
	return buf.String()
}

// parseVarName extracts a shell variable name from s, which is the part
// after the $ sign. Returns (name, width) where width is how many bytes
// the variable reference consumed (including braces for ${VAR}).
// Returns ("", 0) for no match (bare $) or ("", 1) for $?/$!/etc.
func parseVarName(s string) (string, int) {
	if len(s) == 0 {
		return "", 0
	}
	if s[0] == '{' {
		// ${VAR}
		for k := 1; k < len(s); k++ {
			if s[k] == '}' {
				return s[1:k], k + 1
			}
		}
		return "", len(s) // unterminated — consume everything
	}
	// $VAR or $VAR_NAME123
	if !isVarStart(s[0]) {
		return "", 1 // $@, $*, $#, $?, $-, $$, $!, $0...
	}
	// Parse the rest of the name
	k := 1
	for k < len(s) && isVarCont(s[k]) {
		k++
	}
	return s[:k], k
}

// isVarStart returns true for characters that can start a variable name.
func isVarStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

// isVarCont returns true for characters that can continue a variable name.
func isVarCont(c byte) bool {
	return isVarStart(c) || (c >= '0' && c <= '9')
}

// ── Environment Variable Loading ───────────────────────────────────────

// envString returns the value of a ODEK_* env var, or empty string if unset.
func envString(key string) string {
	return os.Getenv("ODEK_" + key)
}

// envBool parses a ODEK_* env var as a boolean. Returns nil if the env var
// is empty or not set, or if the value can't be parsed.
func envBool(key string) *bool {
	v := os.Getenv("ODEK_" + key)
	if v == "" {
		return nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return nil
	}
	return &b
}

// envInt parses a ODEK_* env var as an integer. Returns 0 if unset/unparseable.
func envInt(key string) int {
	v := os.Getenv("ODEK_" + key)
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
//	global file → project file → ODEK_* env → CLI flags
//
// For each field, the highest-priority layer that provides a value wins.
// API key has an additional fallback: if none of the four layers provides
// one, it falls back to DEEPSEEK_API_KEY → OPENAI_API_KEY (legacy env vars).
func LoadConfig(cli CLIFlags) ResolvedConfig {
	// Layer 0: load ~/.odek/secrets.env into the process environment.
	// This makes secrets available as env vars for ${VAR} substitution
	// in config files and for ODEK_* env var lookups.
	loadSecretsEnv()

	// Layer 1: global (~/.odek/config.json)
	global := loadFile(GlobalConfigPath())

	// Layer 2: project (./odek.json)
	project := loadFile(ProjectConfigPath())

	// Start with global, overlay project
	cfg := overlayFile(FileConfig{}, global)
	cfg = overlayFile(cfg, project)

	// Layer 3: ODEK_* env vars
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
	if v := envBool("PROMPT_CACHING"); v != nil {
		cfg.PromptCaching = v
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

	// Skills env vars
	if v := envString("SKILLS_LEARN"); v != "" {
		b, _ := strconv.ParseBool(v)
		if cfg.Skills == nil {
			cfg.Skills = &SkillsConfig{}
		}
		cfg.Skills.Learn = &b
	}

	// MaxConcurrency env var
	if v := envInt("MAX_CONCURRENCY"); v > 0 {
		cfg.MaxConcurrency = v
	}

	// InteractionMode env var
	if v := envString("INTERACTION_MODE"); v != "" {
		cfg.InteractionMode = v
	}

	// Schedules env overrides (ODEK_SCHEDULES_*): lets the scheduler be tuned
	// from the environment, like everything else in a containerised deploy.
	// Allocate once — an all-zero SchedulesConfig resolves identically to nil.
	if cfg.Schedules == nil {
		cfg.Schedules = &SchedulesConfig{}
	}
	if v := envBool("SCHEDULES_ENABLED"); v != nil {
		cfg.Schedules.Enabled = v
	}
	if v := envInt("SCHEDULES_MAX_CONCURRENT"); v > 0 {
		cfg.Schedules.MaxConcurrent = v
	}
	if v := envString("SCHEDULES_TIMEZONE"); v != "" {
		cfg.Schedules.Timezone = v
	}
	if v := envBool("SCHEDULES_CATCHUP"); v != nil {
		cfg.Schedules.Catchup = v
	}
	if v := envBool("SCHEDULES_ALLOW_TELEGRAM_MANAGEMENT"); v != nil {
		cfg.Schedules.AllowTelegramManagement = v
	}

	// Telegram env overrides: merge env vars on top of file config.
	baseTelegram := telegram.DefaultConfig()
	if cfg.Telegram != nil {
		baseTelegram = *cfg.Telegram
	}
	mergedTelegram := telegram.ConfigFromEnv(baseTelegram)
	cfg.Telegram = &mergedTelegram

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
	if cli.PromptCaching != nil {
		cfg.PromptCaching = cli.PromptCaching
	}
	if cli.Learn != nil {
		if cfg.Skills == nil {
			cfg.Skills = &SkillsConfig{}
		}
		cfg.Skills.Learn = cli.Learn
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
	if cli.InteractionMode != "" {
		cfg.InteractionMode = cli.InteractionMode
	}

	// Build resolved config with concrete values
	resolved := ResolvedConfig{
		Model:    cfg.Model,
		BaseURL:  cfg.BaseURL,
		APIKey:   cfg.APIKey,
		Thinking: cfg.Thinking,
		MaxIter:  cfg.MaxIter,
		System:   cfg.System,

		SandboxImage:    cfg.SandboxImage, // empty = resolve at call site (Dockerfile.odek or alpine:latest)
		SandboxNetwork:  ifZero(cfg.SandboxNetwork, DefaultSandboxNetwork),
		SandboxMemory:   cfg.SandboxMemory,
		SandboxCPUs:     cfg.SandboxCPUs,
		SandboxUser:     cfg.SandboxUser,
		SandboxEnv:      cfg.SandboxEnv,
		SandboxVolumes:  cfg.SandboxVolumes,
		Skills:          resolveSkills(cfg.Skills),
		Dangerous:       resolveDangerous(cfg.Dangerous),
		Memory:          resolveMemory(cfg.Memory),
		MCPServers:      cfg.MCPServers,
		Telegram:        resolveTelegram(cfg.Telegram),
		Transcription:   resolveTranscription(cfg.Transcription),
		Vision:          resolveVision(cfg.Vision),
		Schedules:       resolveSchedules(cfg.Schedules),
		InteractionMode: ifZero(cfg.InteractionMode, "engaging"),
		ToolProgress:    ifZero(cfg.ToolProgress, "all"),
	}

	// MaxConcurrency: default to 3 if not set
	if cfg.MaxConcurrency > 0 {
		resolved.MaxConcurrency = cfg.MaxConcurrency
	} else {
		resolved.MaxConcurrency = 3
	}

	// MaxToolParallel: 0 = use loop engine default (4)
	resolved.MaxToolParallel = cfg.MaxToolParallel

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
	if cfg.PromptCaching != nil {
		resolved.PromptCaching = *cfg.PromptCaching
	}
	if cfg.SandboxReadonly != nil {
		resolved.SandboxReadonly = *cfg.SandboxReadonly
	}
	if cfg.ToolProgressCleanup != nil {
		resolved.ToolProgressCleanup = *cfg.ToolProgressCleanup
	} else {
		resolved.ToolProgressCleanup = true // default: delete progress messages
	}

	// API key fallback chain: resolved → DEEPSEEK_API_KEY → OPENAI_API_KEY
	if resolved.APIKey == "" {
		resolved.APIKey = os.Getenv("DEEPSEEK_API_KEY")
	}
	if resolved.APIKey == "" {
		resolved.APIKey = os.Getenv("OPENAI_API_KEY")
	}

	// Clear API key env vars to prevent exposure via /proc/pid/environ.
	// The key is now in the Config struct; the environment shouldn't keep a copy.
	os.Unsetenv("ODEK_API_KEY")
	os.Unsetenv("DEEPSEEK_API_KEY")
	os.Unsetenv("OPENAI_API_KEY")

	// Seed the redaction layer with odek's own secrets so they (and their
	// common encodings) are stripped from any tool output, even when the
	// agent prints them in a format the pattern matchers don't recognise.
	// The API key is registered from its resolved value (the unsets above
	// only remove it from the environment, not from resolved.APIKey);
	// RegisterSecretsFromEnv covers .env / secrets.env injected values.
	redact.RegisterSecret(resolved.APIKey)
	redact.RegisterSecret(resolved.Telegram.Token)
	redact.RegisterSecretsFromEnv()

	return resolved
}

// ifZero returns the default value if s is empty, otherwise returns s.
func ifZero(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// resolveSkills merges file-level skills config with defaults.
func resolveSkills(cfg *SkillsConfig) skills.SkillsConfig {
	def := skills.DefaultSkillsConfig()
	if cfg == nil {
		return def
	}
	if cfg.MaxAutoLoad != nil {
		def.MaxAutoLoad = *cfg.MaxAutoLoad
	}
	if cfg.MaxLazySlots != nil {
		def.MaxLazySlots = *cfg.MaxLazySlots
	}
	if cfg.Learn != nil {
		def.Learn = *cfg.Learn
	}
	if len(cfg.Dirs) > 0 {
		def.Dirs = cfg.Dirs
	}
	if cfg.Import != nil {
		if cfg.Import.MaxSizeBytes > 0 {
			def.Import.MaxSizeBytes = cfg.Import.MaxSizeBytes
		}
		if cfg.Import.TimeoutSecs > 0 {
			def.Import.TimeoutSecs = cfg.Import.TimeoutSecs
		}
		def.Import.RequireHTTPS = cfg.Import.RequireHTTPS
	}
	if cfg.Curation != nil {
		if cfg.Curation.StalenessDays > 0 {
			def.Curation.StalenessDays = cfg.Curation.StalenessDays
		}
		def.Curation.AutoPrune = cfg.Curation.AutoPrune
		if cfg.Curation.SkipThreshold > 0 {
			def.Curation.SkipThreshold = cfg.Curation.SkipThreshold
		}
		if cfg.Curation.SkipResetDays > 0 {
			def.Curation.SkipResetDays = cfg.Curation.SkipResetDays
		}
		def.Curation.AutoCurate = cfg.Curation.AutoCurate
	}
	if cfg.AutoSave != nil {
		def.AutoSave.Enabled = cfg.AutoSave.Enabled
		if cfg.AutoSave.MaxPerRun > 0 {
			def.AutoSave.MaxPerRun = cfg.AutoSave.MaxPerRun
		}
		def.AutoSave.RequireLLM = cfg.AutoSave.RequireLLM
	}
	if cfg.LLMLearn != nil {
		def.LLMLearn = *cfg.LLMLearn
	}
	if cfg.LLMCurate != nil {
		def.LLMCurate = *cfg.LLMCurate
	}
	if cfg.Verbose != nil {
		def.Verbose = *cfg.Verbose
	}
	return def
}

// resolveDangerous merges file-level and potential env-level dangerous config.
// If no config is provided, returns an empty DangerousConfig (safe defaults).
func resolveDangerous(cfg *danger.DangerousConfig) danger.DangerousConfig {
	if cfg != nil {
		return *cfg
	}
	return danger.DangerousConfig{}
}

// resolveMemory merges file-level memory config with defaults.
// Starts from DefaultMemoryConfig and overlays any non-zero/non-nil
// fields from cfg. This means a partial config like {"buffer_lines": 10}
// won't silently disable all the boolean features.
func resolveMemory(cfg *memory.MemoryConfig) memory.MemoryConfig {
	def := memory.DefaultMemoryConfig()
	if cfg == nil {
		return def
	}
	if cfg.Enabled != nil {
		def.Enabled = cfg.Enabled
	}
	if cfg.BufferEnabled != nil {
		def.BufferEnabled = cfg.BufferEnabled
	}
	if cfg.MergeOnWrite != nil {
		def.MergeOnWrite = cfg.MergeOnWrite
	}
	if cfg.ExtractOnEnd != nil {
		def.ExtractOnEnd = cfg.ExtractOnEnd
	}
	if cfg.ExtractFacts != nil {
		def.ExtractFacts = cfg.ExtractFacts
	}
	if cfg.ConsolidateOnEnd != nil {
		def.ConsolidateOnEnd = cfg.ConsolidateOnEnd
	}
	if cfg.LLMSearch != nil {
		def.LLMSearch = cfg.LLMSearch
	}
	if cfg.LLMExtract != nil {
		def.LLMExtract = cfg.LLMExtract
	}
	if cfg.LLMConsolidate != nil {
		def.LLMConsolidate = cfg.LLMConsolidate
	}
	if cfg.FactsLimitUser > 0 {
		def.FactsLimitUser = cfg.FactsLimitUser
	}
	if cfg.FactsLimitEnv > 0 {
		def.FactsLimitEnv = cfg.FactsLimitEnv
	}
	if cfg.BufferLines > 0 {
		def.BufferLines = cfg.BufferLines
	}
	if cfg.MergeThreshold > 0 {
		def.MergeThreshold = cfg.MergeThreshold
	}
	if cfg.AddThreshold > 0 {
		def.AddThreshold = cfg.AddThreshold
	}
	if cfg.MinTurnsForExtraction > 0 {
		def.MinTurnsForExtraction = cfg.MinTurnsForExtraction
	}
	if cfg.AutoApproveEpisodes != nil {
		def.AutoApproveEpisodes = cfg.AutoApproveEpisodes
	}
	if cfg.EpisodeDedupThreshold > 0 {
		def.EpisodeDedupThreshold = cfg.EpisodeDedupThreshold
	}
	if cfg.MaxEpisodes > 0 {
		def.MaxEpisodes = cfg.MaxEpisodes
	}
	if cfg.EpisodeTTLDays > 0 {
		def.EpisodeTTLDays = cfg.EpisodeTTLDays
	}
	return def
}

// resolveTelegram merges file-level telegram config with defaults.
// Starts from DefaultConfig and overlays any non-zero fields from the
// file config, so users only need to specify the fields they want to
// override.
func resolveTelegram(cfg *telegram.TelegramConfig) telegram.TelegramConfig {
	base := telegram.DefaultConfig()
	if cfg == nil {
		return base
	}
	// Overlay non-zero fields from the file config.
	if cfg.Token != "" {
		base.Token = cfg.Token
	}
	if len(cfg.AllowedChats) > 0 {
		base.AllowedChats = cfg.AllowedChats
	}
	if len(cfg.AllowedUsers) > 0 {
		base.AllowedUsers = cfg.AllowedUsers
	}
	if cfg.BotUsername != "" {
		base.BotUsername = cfg.BotUsername
	}
	if cfg.PollInterval > 0 {
		base.PollInterval = cfg.PollInterval
	}
	if cfg.PollTimeout > 0 {
		base.PollTimeout = cfg.PollTimeout
	}
	if cfg.MaxMsgLength > 0 {
		base.MaxMsgLength = cfg.MaxMsgLength
	}
	if cfg.DailyTokenBudget > 0 {
		base.DailyTokenBudget = cfg.DailyTokenBudget
	}
	if cfg.SessionTTL > 0 {
		base.SessionTTL = cfg.SessionTTL
	}
	if len(cfg.FallbackURLs) > 0 {
		base.FallbackURLs = cfg.FallbackURLs
	}
	if cfg.LogLevel != "" {
		base.LogLevel = cfg.LogLevel
	}
	if cfg.LogFile != "" {
		base.LogFile = cfg.LogFile
	}
	if cfg.DefaultChatID != 0 {
		base.DefaultChatID = cfg.DefaultChatID
	}
	return base
}

// resolveTranscription returns the resolved transcription config.
// If the file config is nil, returns sensible defaults.
func resolveTranscription(cfg *TranscriptionConfig) TranscriptionConfig {
	if cfg != nil {
		return *cfg
	}
	return TranscriptionConfig{
		Model:          "tiny",
		AutoTranscribe: true,
	}
}

// resolveVision returns the resolved vision config.
// If the file config is nil, returns sensible defaults.
func resolveVision(cfg *VisionConfig) VisionConfig {
	if cfg != nil {
		if cfg.VideoFrames == 0 {
			cfg.VideoFrames = 8
		}
		return *cfg
	}
	return VisionConfig{
		VideoFrames:  8,
		AutoDescribe: true,
	}
}

// SchedulesConfig is the file-level scheduler configuration. Tri-state fields
// use pointers so "unset" is distinguishable from an explicit false.
type SchedulesConfig struct {
	Enabled       *bool  `json:"enabled,omitempty"`        // run the embedded scheduler inside `odek telegram` (default true)
	MaxConcurrent int    `json:"max_concurrent,omitempty"` // max jobs running at once (default 2)
	Timezone      string `json:"timezone,omitempty"`       // default timezone for jobs with none (default UTC)
	Catchup       *bool  `json:"catchup,omitempty"`        // global default: run a missed fire once on startup (default false)
	// AllowTelegramManagement gates the in-chat `/schedule` management commands.
	// When false, the Telegram bot still lists/previews jobs but refuses to
	// add/remove/enable/disable/run them — manage from the host CLI instead.
	AllowTelegramManagement *bool `json:"allow_telegram_management,omitempty"` // default true
}

// ScheduleConfig is the resolved scheduler config (all fields concrete).
type ScheduleConfig struct {
	Enabled                 bool
	MaxConcurrent           int
	Timezone                string
	Catchup                 bool
	AllowTelegramManagement bool
}

// resolveSchedules merges file-level scheduler config with defaults.
func resolveSchedules(cfg *SchedulesConfig) ScheduleConfig {
	out := ScheduleConfig{
		Enabled:                 true,
		MaxConcurrent:           2,
		Timezone:                "UTC",
		Catchup:                 false,
		AllowTelegramManagement: true,
	}
	if cfg == nil {
		return out
	}
	if cfg.Enabled != nil {
		out.Enabled = *cfg.Enabled
	}
	if cfg.MaxConcurrent > 0 {
		out.MaxConcurrent = cfg.MaxConcurrent
	}
	if cfg.Timezone != "" {
		out.Timezone = cfg.Timezone
	}
	if cfg.Catchup != nil {
		out.Catchup = *cfg.Catchup
	}
	if cfg.AllowTelegramManagement != nil {
		out.AllowTelegramManagement = *cfg.AllowTelegramManagement
	}
	return out
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
		base.SandboxVolumes = append(base.SandboxVolumes, override.SandboxVolumes...)
	}
	if override.Dangerous != nil {
		base.Dangerous = override.Dangerous
	}
	if override.Skills != nil {
		base.Skills = override.Skills
	}
	if override.Memory != nil {
		base.Memory = override.Memory
	}
	if override.Telegram != nil {
		base.Telegram = override.Telegram
	}
	if override.PromptCaching != nil {
		base.PromptCaching = override.PromptCaching
	}
	if override.MaxConcurrency > 0 {
		base.MaxConcurrency = override.MaxConcurrency
	}
	if override.MaxToolParallel > 0 {
		base.MaxToolParallel = override.MaxToolParallel
	}
	if override.MCPServers != nil {
		if base.MCPServers == nil {
			base.MCPServers = make(map[string]mcpclient.ServerConfig)
		}
		for k, v := range override.MCPServers {
			base.MCPServers[k] = v
		}
	}
	if override.InteractionMode != "" {
		base.InteractionMode = override.InteractionMode
	}
	if override.ToolProgress != "" {
		base.ToolProgress = override.ToolProgress
	}
	if override.ToolProgressCleanup != nil {
		base.ToolProgressCleanup = override.ToolProgressCleanup
	}
	if override.Transcription != nil {
		base.Transcription = override.Transcription
	}
	if override.Vision != nil {
		base.Vision = override.Vision
	}
	if override.Schedules != nil {
		base.Schedules = override.Schedules
	}
	return base
}

// secretsEnvPath returns the path to the secrets environment file.
func secretsEnvPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".odek", "secrets.env")
}

// loadSecretsEnv reads ~/.odek/secrets.env and injects each KEY=VALUE pair
// into the process environment via os.Setenv. This makes secrets available
// for ${VAR} substitution in config files and for ODEK_* env var lookups.
//
// Missing or unreadable files are silently ignored — not an error.
// Lines that don't match KEY=VALUE are silently skipped.
func loadSecretsEnv() {
	path := secretsEnvPath()
	if path == "" {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || k == "" {
			continue
		}
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}
