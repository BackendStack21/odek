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
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/BackendStack21/odek/internal/budget"
	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/embedding"
	"github.com/BackendStack21/odek/internal/guard"
	"github.com/BackendStack21/odek/internal/maintenance"
	"github.com/BackendStack21/odek/internal/mcpclient"
	"github.com/BackendStack21/odek/internal/memory"
	"github.com/BackendStack21/odek/internal/memory/extended"
	"github.com/BackendStack21/odek/internal/redact"
	"github.com/BackendStack21/odek/internal/skills"
	"github.com/BackendStack21/odek/internal/telegram"
)

// maxConfigFileBytes caps how large a config file may be before it is rejected.
// This prevents a malicious or broken config from OOMing startup.
const maxConfigFileBytes = 5 << 20 // 5 MiB

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

	// ToolsEnabled and ToolsDisabled control which tools are exposed to the LLM.
	// These override file/env config.
	ToolsEnabled  []string
	ToolsDisabled []string

	// PromptCaching enables prompt caching markers for supported providers.
	// Config: prompt_caching, ODEK_PROMPT_CACHING, --prompt-caching.
	PromptCaching *bool // nil = not set

	// Stream enables SSE streaming of LLM responses for the main think
	// step (default: off). Config: stream, ODEK_STREAM, --stream.
	Stream *bool // nil = not set

	// Compaction enables LLM-based rolling compaction of trimmed context
	// (default: on). Config: compaction, ODEK_COMPACTION,
	// --compaction / --no-compaction.
	Compaction *bool // nil = not set

	// Planning enables the built-in plan tool and its protected plan message
	// (default: on). Config: planning.enabled, ODEK_PLANNING,
	// --planning / --no-planning.
	Planning *bool // nil = not set

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

	// Extended memory subsystem CLI overrides.
	MemoryExtendedEnabled                     *bool // nil = not set
	MemoryExtendedMaxSizeMB                   int   // 0 = not set
	MemoryExtendedAtomMaxChars                int   // 0 = not set
	MemoryExtendedMemoryBudgetChars           int   // 0 = not set
	MemoryExtendedUserStateTurnInterval       int   // 0 = not set
	MemoryExtendedUserStateMaxPending         int   // 0 = not set
	MemoryExtendedAssociationsEnabled         *bool // nil = not set
	MemoryExtendedAssociationSemanticTopK     int   // 0 = not set
	MemoryExtendedProactiveReturnAfterBreak   *bool // nil = not set
	MemoryExtendedStyleMirroringEnabled       *bool // nil = not set
	MemoryExtendedAnaphoraResolutionEnabled   *bool // nil = not set
	MemoryExtendedFollowUpAnticipationEnabled *bool // nil = not set

	// Guard subsystem CLI overrides.
	GuardProvider         string  // "" = not set
	GuardURL              string  // "" = not set
	GuardBatchURL         string  // "" = not set
	GuardLongURL          string  // "" = not set
	GuardSocketPath       string  // "" = not set
	GuardThreshold        float64 // 0 = not set
	GuardTimeoutSeconds   int     // 0 = not set
	GuardFallbackToLocal  *bool   // nil = not set
	GuardScanMemory       *bool   // nil = not set
	GuardScanSystemPrompt *bool   // nil = not set
	GuardScanMCP          *bool   // nil = not set
	GuardScanSkills       *bool   // nil = not set
	GuardScanToolOutputs  *bool   // nil = not set
	GuardScanTelegram     *bool   // nil = not set

	// TrustedProxies is a list of IP addresses or CIDR ranges of reverse proxies
	// whose X-Forwarded-For / X-Real-Ip headers are trusted. Empty means headers
	// are ignored even from loopback. Only used by `odek serve`.
	TrustedProxies []string

	// Execution-budget CLI overrides (odek-extension/v1). CLI flags are
	// operator intent: they set limits explicitly, 0 = flag not passed.
	MaxRuntimeSeconds int64
	MaxToolCalls      int64
	MaxInputTokens    int64
	MaxOutputTokens   int64
	MaxCostUSD        float64
}

// SkillsConfig holds the skills configuration section from JSON files.
type SkillsConfig struct {
	MaxAutoLoad  *int                 `json:"max_auto_load,omitempty"`
	MaxLazySlots *int                 `json:"max_lazy_slots,omitempty"`
	Dirs         []string             `json:"dirs,omitempty"`
	Import       *skills.ImportConfig `json:"import,omitempty"`
	Verbose      *bool                `json:"verbose,omitempty"`
	Embedding    *embedding.Config    `json:"embedding,omitempty"`
}

// SessionsConfig is the "sessions" section of odek.json. It currently only
// carries an optional embedding override for semantic session_search; when
// unset, sessions use the shared top-level embedding default.
type SessionsConfig struct {
	Embedding *embedding.Config `json:"embedding,omitempty"`
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

// WebSearchConfig controls the web_search tool (self-hosted SearXNG backend).
// Populated from the "web_search" section of odek.json or ~/.odek/config.json.
// The tool is registered only when BaseURL is non-empty — without a reachable
// SearXNG instance there is no backend, so the tool stays hidden by default
// (a plain `go install` has no sidecar; the Docker compose setup sets BaseURL).
type WebSearchConfig struct {
	// BaseURL is the SearXNG instance the tool queries, e.g.
	// "http://searxng:8080" (Docker compose) or "http://127.0.0.1:8888"
	// (host). Empty disables the tool.
	BaseURL string `json:"base_url,omitempty"`
	// Categories optionally restricts the SearXNG categories queried
	// (comma-separated, e.g. "general" or "general,news"). Empty = SearXNG default.
	Categories string `json:"categories,omitempty"`
	// Language optionally sets the SearXNG language code (e.g. "en"). Empty = SearXNG default.
	Language string `json:"language,omitempty"`
	// MaxResults caps how many results are returned to the agent. Default: 10.
	MaxResults int `json:"max_results,omitempty"`
	// Timeout is the per-request timeout in seconds. Default: 15.
	Timeout int `json:"timeout_seconds,omitempty"`
}

// ToolConfig controls which tools are exposed to the LLM.
// Config: tools.enabled, tools.disabled; ODEK_TOOLS_ENABLED,
// ODEK_TOOLS_DISABLED; --tool, --no-tool.
type ToolConfig struct {
	Enabled  []string `json:"enabled,omitempty"`
	Disabled []string `json:"disabled,omitempty"`
}

// MaintenanceConfig is the file-level "maintenance" section. Pointer fields
// distinguish "not set" (inherit the default) from an explicit 0, which is
// meaningful for the retention knobs (0 = keep forever / disable).
// Operator-controlled: rejected from project-level ./odek.json because it
// governs DELETION of user data.
type MaintenanceConfig struct {
	Enabled              *bool  `json:"enabled,omitempty"`
	IntervalMinutes      *int   `json:"interval_minutes,omitempty"`
	SessionsMaxAgeDays   *int   `json:"sessions_max_age_days,omitempty"`
	AuditMaxAgeDays      *int   `json:"audit_max_age_days,omitempty"`
	LogMaxMB             *int64 `json:"log_max_mb,omitempty"`
	PlansMaxAgeDays      *int   `json:"plans_max_age_days,omitempty"`
	ArtifactsMaxAgeHours *int   `json:"artifacts_max_age_hours,omitempty"`
}

// ToolsConfig is the "tools" section of odek.json. It is intentionally a
// pointer in FileConfig so "not set" can be distinguished from an explicit
// empty list.
type ToolsConfig = ToolConfig

// SubagentConfig is the file-level "subagent" section (docs/SUBAGENTS.md).
// Pointer fields distinguish "not set" (inherit the default) from explicit
// values, mirroring MaintenanceConfig.
// Operator-controlled: rejected from project-level ./odek.json — a malicious
// repo must not be able to extend its own sub-agents' runtime/iteration
// budgets or weaken budget inheritance.
type SubagentConfig struct {
	MaxConcurrency *int   `json:"max_concurrency,omitempty"`
	TimeoutSeconds *int   `json:"timeout_seconds,omitempty"`
	MaxIterations  *int   `json:"max_iterations,omitempty"`
	MaxDepth       *int   `json:"max_depth,omitempty"`
	AnnounceBudget *bool  `json:"announce_budget,omitempty"`
	BudgetInherit  string `json:"budget_inherit,omitempty"`
}

// PlanningFileConfig is the "planning" section of odek.json. Pointer fields
// distinguish "not set" from explicit values so partial sections merge
// field-by-field across the global/project layers.
type PlanningFileConfig struct {
	Enabled        *bool `json:"enabled,omitempty"`
	MaxSteps       *int  `json:"max_steps,omitempty"`
	MaxRenderChars *int  `json:"max_render_chars,omitempty"`
}

// PlanningConfig is the resolved planning configuration (docs/PLANNING.md).
type PlanningConfig struct {
	// Enabled is the master switch: false removes the plan tool from the
	// registry and skips all plan logic.
	Enabled bool
	// MaxSteps caps plan(create) size; enforced fail-closed.
	MaxSteps int
	// MaxRenderChars caps the rendered plan message; overflow drops the
	// oldest done steps first with an explicit omission marker.
	MaxRenderChars int
}

// Planning clamp ranges applied to the resolved values regardless of layer.
const (
	planningMinSteps       = 1
	planningMaxSteps       = 50
	planningMinRenderChars = 200
	planningMaxRenderChars = 8000
)

// DefaultPlanningConfig returns the shipped defaults: planning on, 12 steps,
// 2000-char render cap (~500 estimated tokens at ~4 chars/token).
func DefaultPlanningConfig() PlanningConfig {
	return PlanningConfig{Enabled: true, MaxSteps: 12, MaxRenderChars: 2000}
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

	// Stream enables SSE streaming of LLM responses for the main think
	// step (default: off). Config: stream, ODEK_STREAM, --stream.
	Stream *bool `json:"stream,omitempty"`

	// Compaction enables LLM-based rolling compaction of trimmed context
	// (default: on; set false to explicitly disable).
	Compaction *bool `json:"compaction,omitempty"`

	// Planning configures the built-in plan tool (docs/PLANNING.md).
	// The global config may set anything; the project config may set
	// enabled:false and may only LOWER the caps (see clampProjectPlanning).
	Planning *PlanningFileConfig `json:"planning,omitempty"`

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

	// Guard configures the prompt-injection guard subsystem.
	// Operator-controlled: rejected from project-level ./odek.json.
	Guard *guard.Config `json:"guard,omitempty"`

	// Embedding is the shared default embedding backend for semantic retrieval.
	// Every subsystem (memory, sessions, skills) uses it unless that subsystem
	// sets its own override (memory.embedding / sessions.embedding /
	// skills.embedding). See internal/embedding.Config.
	Embedding *embedding.Config `json:"embedding,omitempty"`

	// Sessions configures the session subsystem. Currently only an optional
	// embedding override for semantic session_search.
	Sessions *SessionsConfig `json:"sessions,omitempty"`

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

	// TrustedProxies lists IP addresses or CIDR ranges of reverse proxies whose
	// X-Forwarded-For / X-Real-Ip headers are trusted. Empty means headers are
	// ignored even from loopback. Config: trusted_proxies, ODEK_TRUSTED_PROXIES.
	// Only used by `odek serve`.
	TrustedProxies []string `json:"trusted_proxies,omitempty"`

	// Telegram configures the Telegram bot integration.
	Telegram *telegram.TelegramConfig `json:"telegram,omitempty"`

	// Transcription configures local audio transcription (whisper.cpp).
	Transcription *TranscriptionConfig `json:"transcription,omitempty"`

	// Vision configures local image/video understanding (MiniCPM-V 4.6 via llama-mtmd-cli).
	Vision *VisionConfig `json:"vision,omitempty"`

	// WebSearch configures the web_search tool (self-hosted SearXNG backend).
	WebSearch *WebSearchConfig `json:"web_search,omitempty"`

	// Schedules configures the native in-process task scheduler.
	Schedules *SchedulesConfig `json:"schedules,omitempty"`

	// Maintenance configures the storage-maintenance janitor (retention and
	// deletion of sessions, audit records, plans, logs, skip-list entries).
	// Operator-controlled: rejected from project-level ./odek.json.
	Maintenance *MaintenanceConfig `json:"maintenance,omitempty"`

	// Subagent configures delegate_tasks sub-agent execution (docs/SUBAGENTS.md).
	// Operator-controlled: rejected from project-level ./odek.json.
	Subagent *SubagentConfig `json:"subagent,omitempty"`

	// Profiles are named capability profiles (P4): when a task selects one,
	// its settings OVERRIDE the corresponding operator permissions
	// (max_risk clamp, allowlist, tool filter) for that sub-agent.
	// Operator-controlled: rejected from project-level ./odek.json — a
	// cloned repo must not be able to author its own permission envelope.
	Profiles map[string]ProfileConfig `json:"profiles,omitempty"`

	// Tools controls which tools are exposed to the LLM.
	// Project-level ./odek.json may only disable tools, not enable them.
	Tools *ToolsConfig `json:"tools,omitempty"`

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

	// Limits is the "limits" section: hard execution budgets
	// (odek-extension/v1). The global config may set any limit; the project
	// config may only LOWER an existing one (see clampProjectLimits).
	Limits *budget.Limits `json:"limits,omitempty"`
}

// ProjectSandboxOverride records which sandbox knobs were supplied by the
// project-level ./odek.json config. These require explicit operator approval
// before they are applied, because a malicious repo could otherwise
// exfiltrate host secrets (via ${VAR} interpolation in sandbox_env), pull an
// attacker-controlled image, or widen the container's network access.
type ProjectSandboxOverride struct {
	HasEnv              bool
	EnvKeys             []string
	EnvHasInterpolation bool
	HasImage            bool
	Image               string
	HasNetwork          bool
	Network             string
	HasVolumes          bool
	Volumes             []string
}

// ResolvedConfig is the fully merged result. Every field has a concrete
// value — callers can read directly without checking for "not set".
type ResolvedConfig struct {
	Model           string
	BaseURL         string
	APIKey          string
	Thinking        string
	MaxIter         int
	Sandbox         bool
	SandboxExplicit bool // true when any config layer explicitly set sandbox
	NoColor         bool
	NoAgents        bool
	Stream          bool
	PromptCaching   bool
	Compaction      bool

	// Planning is the resolved planning configuration (docs/PLANNING.md).
	Planning PlanningConfig
	System   string

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

	// Guard is the resolved injection-guard config with default values.
	Guard guard.Config

	// Embedding is the resolved shared embedding backend — the default every
	// subsystem inherits unless it overrides. nil = default RandomProjections.
	Embedding *embedding.Config

	// SessionEmbedding is the embedding backend sessions use for semantic
	// session_search: sessions.embedding when set, else the shared Embedding.
	SessionEmbedding *embedding.Config

	// MCPServers maps server names to external MCP server configurations.
	// Populated from the mcp_servers section of odek.json.
	MCPServers map[string]mcpclient.ServerConfig

	// ProjectMCPServerNames lists the MCP server names that were introduced by
	// the project-level ./odek.json config. These require explicit user approval
	// before their subprocesses are spawned, because a malicious repo could
	// otherwise execute arbitrary code via the mcp_servers section.
	ProjectMCPServerNames []string

	// ProjectSandboxOverride records sandbox knobs supplied by the project-level
	// ./odek.json config. These require explicit operator approval before they
	// are applied, because a malicious repo could otherwise exfiltrate host
	// secrets or pull an attacker-controlled sandbox image.
	ProjectSandboxOverride ProjectSandboxOverride

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

	// WebSearch is the resolved web_search config.
	// Default: MaxResults=10, Timeout=15, BaseURL="" (tool disabled until set).
	WebSearch WebSearchConfig

	// Schedules is the resolved scheduler config.
	// Default: enabled=true, max_concurrent=2, timezone="UTC", catchup=false.
	Schedules ScheduleConfig

	// Maintenance is the resolved storage-maintenance config.
	// Default: maintenance.DefaultConfig() (enabled, 60min tick, sessions 30d,
	// audit 14d, logs 50MB, plans 30d, skip-list 90d).
	Maintenance maintenance.Config

	// Subagent is the resolved sub-agent execution config.
	// Default: MaxConcurrency=0 (fall back to global), TimeoutSeconds=1800 (30m),
	// MaxIterations=15, MaxDepth=2, AnnounceBudget=true,
	// BudgetInherit="operator".
	Subagent SubagentResolved

	// Profiles is the resolved set of operator-defined capability profiles
	// (P4). nil when none are defined. Selecting an unknown profile name
	// fails closed at the consumer.
	Profiles map[string]ProfileConfig

	// Tools is the resolved tool-list configuration.
	// Empty Enabled/Disabled means "no restriction" for that direction.
	Tools ToolConfig

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

	// TrustedProxies lists IP addresses or CIDR ranges of reverse proxies whose
	// X-Forwarded-For / X-Real-Ip headers are trusted by `odek serve`.
	TrustedProxies []string

	// Limits is the resolved execution-budget configuration
	// (odek-extension/v1). Zero fields mean "no limit". Merge rule: the global
	// config may set any limit; the untrusted project ./odek.json may only
	// LOWER an existing limit (never raise, never disable); CLI flags are
	// operator intent and set limits explicitly.
	Limits budget.Limits
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
	f, err := os.Open(path)
	if err != nil {
		return FileConfig{} // missing or unreadable = empty
	}
	defer f.Close()

	// The operator's global config can carry an api_key (and telegram bot
	// tokens), so a group/world-readable file leaks them to other local
	// users. secrets.env is refused outright (#78); config.json often holds
	// only non-secret settings, so warn loudly instead of refusing
	// (audit 2026-08: the documented "permission-checked" claim previously
	// covered only secrets.env).
	if info, serr := f.Stat(); serr == nil {
		if perm := info.Mode().Perm(); perm&0077 != 0 {
			fmt.Fprintf(os.Stderr, "odek: WARNING: config %s is group/world-readable (%04o) and may contain secrets; run `chmod 600 %s`\n", path, perm, path)
		}
	}

	// Read at most maxConfigFileBytes+1 so we can detect files that exceed the
	// limit without loading them entirely. Using a single Open+LimitReader
	// closes the TOCTOU window between stat and read.
	data, err := io.ReadAll(io.LimitReader(f, maxConfigFileBytes+1))
	if err != nil {
		return FileConfig{}
	}
	if int64(len(data)) > maxConfigFileBytes {
		fmt.Fprintf(os.Stderr, "odek: warning: config %s: file exceeds maximum size %d bytes — ignoring file\n", path, maxConfigFileBytes)
		return FileConfig{}
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

// envIntPtr parses a ODEK_* env var as an integer. Returns nil if unset or
// unparseable, so an explicit 0 (meaningful for retention knobs) stays
// distinguishable from "not set".
func envIntPtr(key string) *int {
	v := os.Getenv("ODEK_" + key)
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil
	}
	return &n
}

// envInt64Ptr parses a ODEK_* env var as an int64. Returns nil if unset or
// unparseable, like envIntPtr.
func envInt64Ptr(key string) *int64 {
	v := os.Getenv("ODEK_" + key)
	if v == "" {
		return nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

// envFloat parses a ODEK_* env var as a float64. Returns 0 if unset/unparseable.
func envFloat(key string) float64 {
	v := os.Getenv("ODEK_" + key)
	if v == "" {
		return 0
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0
	}
	return n
}

// envInt64List parses a comma-separated ODEK_* env var into a slice of int64.
// Empty/unparseable entries are silently dropped.
func envInt64List(key string) []int64 {
	v := os.Getenv("ODEK_" + key)
	if v == "" {
		return nil
	}
	var out []int64
	for _, s := range strings.Split(v, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// envStringList parses a comma-separated ODEK_* env var into a slice of strings.
// Empty entries are dropped.
func envStringList(key string) []string {
	v := os.Getenv("ODEK_" + key)
	if v == "" {
		return nil
	}
	var out []string
	for _, s := range strings.Split(v, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

// ensureExtended returns a non-nil *extended.Config, allocating one if needed.
func ensureExtended(cfg *extended.Config) *extended.Config {
	if cfg == nil {
		return &extended.Config{}
	}
	return cfg
}

// ensureGuard returns a non-nil *guard.Config, allocating one with defaults if needed.
// It also ensures the Scan sub-config is non-nil so callers can set individual toggles.
func ensureGuard(cfg *guard.Config) *guard.Config {
	if cfg == nil {
		return guard.DefaultConfig()
	}
	if cfg.Scan == nil {
		cfg.Scan = guard.DefaultScanConfig()
	}
	return cfg
}

// ensureMaintenance returns a non-nil *MaintenanceConfig, allocating one if needed.
func ensureMaintenance(cfg *MaintenanceConfig) *MaintenanceConfig {
	if cfg == nil {
		return &MaintenanceConfig{}
	}
	return cfg
}

// resolveMaintenance merges the file-level maintenance section over the
// package defaults. Unset (nil) fields inherit the default; explicit 0 keeps
// its meaning (keep forever / disable).
func resolveMaintenance(cfg *MaintenanceConfig) maintenance.Config {
	def := maintenance.DefaultConfig()
	if cfg == nil {
		return def
	}
	if cfg.Enabled != nil {
		def.Enabled = *cfg.Enabled
	}
	if cfg.IntervalMinutes != nil {
		def.IntervalMinutes = *cfg.IntervalMinutes
	}
	if cfg.SessionsMaxAgeDays != nil {
		def.SessionsMaxAgeDays = *cfg.SessionsMaxAgeDays
	}
	if cfg.AuditMaxAgeDays != nil {
		def.AuditMaxAgeDays = *cfg.AuditMaxAgeDays
	}
	if cfg.LogMaxMB != nil {
		def.LogMaxMB = *cfg.LogMaxMB
	}
	if cfg.PlansMaxAgeDays != nil {
		def.PlansMaxAgeDays = *cfg.PlansMaxAgeDays
	}
	if cfg.ArtifactsMaxAgeHours != nil {
		def.ArtifactsMaxAgeHours = *cfg.ArtifactsMaxAgeHours
	}
	return def
}

// Budget inheritance modes for the subagent section.
const (
	// BudgetInheritOperator gives every sub-agent the operator-configured
	// limits regardless of what the parent has already spent (pre-1.28
	// behavior; the default).
	BudgetInheritOperator = "operator"
	// BudgetInheritShare gives a sub-agent min(operator limits, parent's
	// remaining budget) so a near-exhausted parent cannot spawn children
	// with fresh headroom.
	BudgetInheritShare = "share"
)

// SubagentResolved is the resolved "subagent" configuration.
type SubagentResolved struct {
	// MaxConcurrency caps parallel sub-agent tasks per delegate_tasks call.
	// 0 = fall back to the global max_concurrency. Clamped to 8.
	MaxConcurrency int
	// TimeoutSeconds is the default per-sub-agent wall-clock budget in
	// seconds. Clamped to 1800.
	TimeoutSeconds int
	// MaxIterations is the default think→act cycle budget per sub-agent.
	// Clamped to 100.
	MaxIterations int
	// MaxDepth caps delegation nesting via ODEK_SUBAGENT_DEPTH (1 = no
	// sub-agent may delegate further). Clamped to 8.
	MaxDepth int
	// AnnounceBudget makes sub-agent engines inject budget-awareness hints
	// (50/75/90% of the iteration or wall-clock budget) and announce the
	// effective limits in the sub-agent system prompt.
	AnnounceBudget bool
	// BudgetInherit is BudgetInheritOperator or BudgetInheritShare.
	BudgetInherit string
}

// resolveSubagent merges the file-level subagent section over the defaults.
// Unset (nil) fields inherit the default. Numeric values are clamped to the
// documented ceilings (max_concurrency ≤ 8, timeout_seconds ≤ 1800,
// max_iterations ≤ 100, max_depth ≤ 8) so a config file cannot lift the
// runaway-process guards enforced by the CLI flags.
func resolveSubagent(cfg *SubagentConfig) SubagentResolved {
	res := SubagentResolved{
		TimeoutSeconds: 1800,
		MaxIterations:  15,
		MaxDepth:       2,
		AnnounceBudget: true,
		BudgetInherit:  BudgetInheritOperator,
	}
	if cfg == nil {
		return res
	}
	if cfg.MaxConcurrency != nil {
		v := *cfg.MaxConcurrency
		if v < 0 {
			v = 0
		}
		if v > 8 {
			v = 8
		}
		res.MaxConcurrency = v
	}
	if cfg.TimeoutSeconds != nil {
		v := *cfg.TimeoutSeconds
		if v < 0 {
			v = 0
		}
		if v > 1800 {
			v = 1800
		}
		res.TimeoutSeconds = v
	}
	if cfg.MaxIterations != nil {
		v := *cfg.MaxIterations
		if v < 0 {
			v = 0
		}
		if v > 100 {
			v = 100
		}
		res.MaxIterations = v
	}
	if cfg.MaxDepth != nil {
		v := *cfg.MaxDepth
		if v < 1 {
			v = 1
		}
		if v > 8 {
			v = 8
		}
		res.MaxDepth = v
	}
	if cfg.AnnounceBudget != nil {
		res.AnnounceBudget = *cfg.AnnounceBudget
	}
	if cfg.BudgetInherit != "" && cfg.BudgetInherit != BudgetInheritOperator {
		if cfg.BudgetInherit == BudgetInheritShare {
			res.BudgetInherit = BudgetInheritShare
		} else {
			fmt.Fprintf(os.Stderr, "odek: WARNING: unknown subagent.budget_inherit %q; using %q\n", cfg.BudgetInherit, BudgetInheritOperator)
		}
	}
	return res
}

// ProfileConfig is one named capability profile (P4). When a task
// selects the profile, its settings OVERRIDE the corresponding operator
// config for that sub-agent: max_risk clamps every higher-ranked class to
// deny, allowlist REPLACES the global allowlist, and the tools filter
// replaces the global one. Operator-authored only (project config is
// stripped), so the override is policy rather than escalation — the P2
// non-interactive deny and the P3 trust lockdown are applied afterwards
// and cannot be lifted by selecting a profile.
type ProfileConfig struct {
	MaxRisk   string      `json:"max_risk,omitempty"`
	Allowlist []string    `json:"allowlist,omitempty"`
	Tools     *ToolConfig `json:"tools,omitempty"`
}

// validRiskClass reports whether s names a known risk class.
func validRiskClass(s string) bool {
	switch danger.RiskClass(s) {
	case danger.Safe, danger.LocalWrite, danger.SystemWrite, danger.Persistence,
		danger.Destructive, danger.NetworkEgress, danger.CodeExecution,
		danger.Install, danger.Blocked, danger.Unknown, danger.UnreadExec:
		return true
	}
	return false
}

// resolveProfiles validates the operator's capability profiles. Profiles
// with an unknown max_risk are dropped with a warning (fail closed: a
// typo must not silently yield an unclamped envelope).
func resolveProfiles(cfg map[string]ProfileConfig) map[string]ProfileConfig {
	if cfg == nil {
		return nil
	}
	out := make(map[string]ProfileConfig, len(cfg))
	for name, prof := range cfg {
		if prof.MaxRisk != "" && !validRiskClass(prof.MaxRisk) {
			fmt.Fprintf(os.Stderr, "odek: WARNING: dropping profile %q: unknown max_risk %q\n", name, prof.MaxRisk)
			continue
		}
		out[name] = prof
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// envScheduleDangerousConfig parses ODEK_SCHEDULES_DANGEROUS_* env vars into a
// DangerousConfig. Returns nil if none are set.
func envScheduleDangerousConfig(prefix string) *danger.DangerousConfig {
	classesJSON := os.Getenv("ODEK_" + prefix + "_CLASSES")
	allowlist := envStringList(prefix + "_ALLOWLIST")
	denylist := envStringList(prefix + "_DENYLIST")
	action := os.Getenv("ODEK_" + prefix + "_ACTION")
	nonInteractive := os.Getenv("ODEK_" + prefix + "_NON_INTERACTIVE")

	if classesJSON == "" && len(allowlist) == 0 && len(denylist) == 0 && action == "" && nonInteractive == "" {
		return nil
	}

	cfg := &danger.DangerousConfig{}
	if classesJSON != "" {
		classes := make(map[danger.RiskClass]danger.Action)
		if err := json.Unmarshal([]byte(classesJSON), &classes); err != nil {
			fmt.Fprintf(os.Stderr, "odek: warning: invalid ODEK_%s_CLASSES JSON (%q) — ignoring: %v\n", prefix, classesJSON, err)
		} else {
			cfg.Classes = classes
		}
	}
	if len(allowlist) > 0 {
		cfg.Allowlist = allowlist
	}
	if len(denylist) > 0 {
		cfg.Denylist = denylist
	}
	if action != "" {
		cfg.DefaultAction = &action
	}
	if nonInteractive != "" {
		cfg.NonInteractive = &nonInteractive
	}
	return cfg
}

// mergeDangerousConfig overlays override onto base. Lists are appended; scalar
// and map fields in override win. Used to merge env-var schedule overrides on
// top of file-based schedule policy.
func mergeDangerousConfig(base, override *danger.DangerousConfig) {
	if override.Classes != nil {
		if base.Classes == nil {
			base.Classes = make(map[danger.RiskClass]danger.Action)
		}
		for k, v := range override.Classes {
			base.Classes[k] = v
		}
	}
	base.Allowlist = append(base.Allowlist, override.Allowlist...)
	base.Denylist = append(base.Denylist, override.Denylist...)
	if override.DefaultAction != nil {
		base.DefaultAction = override.DefaultAction
	}
	if override.NonInteractive != nil {
		base.NonInteractive = override.NonInteractive
	}
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

	// Project config is untrusted: a malicious repo must not be able to steal
	// the API key, poison the system prompt, or disable safety policy.
	// Keep global values for these sensitive fields; env vars and CLI flags can
	// still override below.
	if project.BaseURL != "" {
		fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring base_url from project config (%s); set it via ~/.odek/config.json, ODEK_BASE_URL, or --base-url\n", ProjectConfigPath())
		project.BaseURL = ""
	}
	if project.APIKey != "" {
		fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring api_key from project config (%s); set it via ~/.odek/config.json, ODEK_API_KEY, or ~/.odek/secrets.env\n", ProjectConfigPath())
		project.APIKey = ""
	}
	if project.System != "" {
		fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring system from project config (%s); set it via ~/.odek/config.json, ODEK_SYSTEM, or --system\n", ProjectConfigPath())
		project.System = ""
	}
	if project.Dangerous != nil {
		fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring dangerous section from project config (%s); set it via ~/.odek/config.json\n", ProjectConfigPath())
		project.Dangerous = nil
	}
	if project.Schedules != nil && project.Schedules.Dangerous != nil {
		fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring schedules.dangerous from project config (%s); set it via ~/.odek/config.json or ODEK_SCHEDULES_DANGEROUS_*\n", ProjectConfigPath())
		project.Schedules.Dangerous = nil
	}
	// Backend redirection: a malicious repo must not be able to send memory,
	// session, or skill embeddings, Telegram messages, or web searches to an
	// attacker-controlled endpoint. Only operator-controlled sources
	// (~/.odek/config.json, and ODEK_TELEGRAM_* env vars for telegram) may
	// set these.
	if project.Embedding != nil {
		fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring embedding from project config (%s); set it via ~/.odek/config.json\n", ProjectConfigPath())
		project.Embedding = nil
	}
	if project.Memory != nil {
		fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring memory from project config (%s); set it via ~/.odek/config.json\n", ProjectConfigPath())
		project.Memory = nil
	}
	// The maintenance section governs DELETION of user data (sessions, audit
	// records, plans, logs). A malicious repo must not be able to set it.
	if project.Maintenance != nil {
		fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring maintenance from project config (%s); set it via ~/.odek/config.json or ODEK_MAINTENANCE_*\n", ProjectConfigPath())
		project.Maintenance = nil
	}
	// The subagent section controls sub-agent budgets (runtime, iterations,
	// nesting depth) and budget inheritance. A malicious repo must not be
	// able to extend its own sub-agents' lifespans or re-widen inheritance.
	if project.Subagent != nil {
		fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring subagent from project config (%s); set it via ~/.odek/config.json\n", ProjectConfigPath())
		project.Subagent = nil
	}
	// Profiles define permission envelopes. A malicious repo must not be
	// able to author (or shadow) the operator's profiles.
	if project.Profiles != nil {
		fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring profiles from project config (%s); set them via ~/.odek/config.json\n", ProjectConfigPath())
		project.Profiles = nil
	}
	if project.Guard != nil {
		fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring guard from project config (%s); set it via ~/.odek/config.json, ODEK_GUARD_*, or the CLI\n", ProjectConfigPath())
		project.Guard = nil
	}
	if project.Sessions != nil {
		fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring sessions from project config (%s); set it via ~/.odek/config.json\n", ProjectConfigPath())
		project.Sessions = nil
	}
	if project.Skills != nil {
		if len(project.Skills.Dirs) > 0 {
			fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring skills.dirs from project config (%s); set it via ~/.odek/config.json\n", ProjectConfigPath())
			project.Skills.Dirs = nil
		}
		if project.Skills.Embedding != nil {
			fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring skills.embedding from project config (%s); set it via ~/.odek/config.json\n", ProjectConfigPath())
			project.Skills.Embedding = nil
		}
	}
	if project.Telegram != nil {
		fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring telegram from project config (%s); set it via ~/.odek/config.json or ODEK_TELEGRAM_* env vars\n", ProjectConfigPath())
		project.Telegram = nil
	}
	if project.WebSearch != nil {
		fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring web_search from project config (%s); set it via ~/.odek/config.json\n", ProjectConfigPath())
		project.WebSearch = nil
	}
	// transcription.binary_path / vision.binary_path are executed verbatim
	// by the transcribe/vision tools (and auto_transcribe triggers that
	// execution automatically on Telegram voice notes), so a cloned repo
	// must not be able to point them at a planted binary. Both sections are
	// operator-only, like telegram/memory above.
	if project.Transcription != nil {
		fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring transcription from project config (%s); set it via ~/.odek/config.json\n", ProjectConfigPath())
		project.Transcription = nil
	}
	if project.Vision != nil {
		fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring vision from project config (%s); set it via ~/.odek/config.json\n", ProjectConfigPath())
		project.Vision = nil
	}
	if len(project.TrustedProxies) > 0 {
		fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring trusted_proxies from project config (%s); set it via ~/.odek/config.json or ODEK_TRUSTED_PROXIES\n", ProjectConfigPath())
		project.TrustedProxies = nil
	}
	// A malicious repo must not be able to widen the tool surface. It may only
	// disable tools, never enable them.
	if project.Tools != nil && len(project.Tools.Enabled) > 0 {
		fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring tools.enabled from project config (%s); set it via ~/.odek/config.json, ODEK_TOOLS_ENABLED, or --tool\n", ProjectConfigPath())
		project.Tools.Enabled = nil
	}
	// A malicious repo must not be able to turn OFF the sandbox or its
	// read-only mode via ./odek.json — that would undo the container isolation
	// the operator opted into. Only the weakening direction is ignored; a
	// project may still enable the sandbox or request read-only. Other sandbox
	// knobs (image, user, network, volumes) keep their global/env/CLI
	// precedence and project values are confined elsewhere (volumes are bound
	// to the working directory in internal/sandbox).
	if project.Sandbox != nil && !*project.Sandbox {
		fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring sandbox=false from project config (%s); set sandbox policy via ~/.odek/config.json or the CLI\n", ProjectConfigPath())
		project.Sandbox = nil
	}
	if project.SandboxReadonly != nil && !*project.SandboxReadonly {
		fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring sandbox_readonly=false from project config (%s); set it via ~/.odek/config.json or CLI\n", ProjectConfigPath())
		project.SandboxReadonly = nil
	}

	// Execution budgets ("limits" section): the global config may set any
	// limit; the untrusted project config may only LOWER an existing limit —
	// never raise it and never disable (zero-out) one. Unlike the sections
	// above this is a clamp, not an outright rejection: a repo tightening its
	// own budgets is safe, loosening the operator's budgets is not.
	clampProjectLimits(global.Limits, project.Limits)

	// Planning ("planning" section): same clamp philosophy. A repo may opt
	// out entirely and may tighten its own caps, but cannot re-enable a
	// globally-disabled feature or raise an explicitly-set global cap.
	clampProjectPlanning(global.Planning, project.Planning)

	// Capture which sandbox knobs the project requested, before the overlay
	// hides them behind CLI/env values. This drives the approval gate in cmd/odek.
	var projectSandboxOverride ProjectSandboxOverride
	if len(project.SandboxEnv) > 0 {
		projectSandboxOverride.HasEnv = true
		projectSandboxOverride.EnvKeys = make([]string, 0, len(project.SandboxEnv))
		for k, v := range project.SandboxEnv {
			projectSandboxOverride.EnvKeys = append(projectSandboxOverride.EnvKeys, k)
			if strings.Contains(v, "${") {
				projectSandboxOverride.EnvHasInterpolation = true
			}
		}
		sort.Strings(projectSandboxOverride.EnvKeys)
	}
	if project.SandboxImage != "" {
		projectSandboxOverride.HasImage = true
		projectSandboxOverride.Image = project.SandboxImage
	}
	if project.SandboxNetwork != "" {
		projectSandboxOverride.HasNetwork = true
		projectSandboxOverride.Network = project.SandboxNetwork
	}
	if len(project.SandboxVolumes) > 0 {
		projectSandboxOverride.HasVolumes = true
		projectSandboxOverride.Volumes = append([]string(nil), project.SandboxVolumes...)
		sort.Strings(projectSandboxOverride.Volumes)
	}

	// Project-level auto_approve is a self-approval attempt surface: a
	// cloned repo must never be able to mark its own MCP servers trusted.
	// Strip it (the operator can grant the same trust from the global
	// config, where it is honored).
	for name, mc := range project.MCPServers {
		if mc.AutoApprove {
			fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring mcp_servers.%s.auto_approve from project config (%s); set auto_approve in ~/.odek/config.json to trust this server\n", name, ProjectConfigPath())
			mc.AutoApprove = false
			project.MCPServers[name] = mc
		}
	}

	// Start with global, overlay project
	cfg := overlayFile(FileConfig{}, global)
	cfg = overlayFile(cfg, project)

	// Global auto_approve trust markers survive the project overlay: when
	// the global config carries the flag for a name the project defines
	// (preserved in overlayFile below) the merged server keeps it. A marker
	// with no definition behind it (auto_approve + empty command) is not a
	// server — drop it so nothing tries to connect to it.
	for name, mc := range cfg.MCPServers {
		if mc.AutoApprove && mc.Command == "" {
			delete(cfg.MCPServers, name)
		}
	}

	// Remember which MCP servers came from the project config so commands can
	// require explicit approval before spawning potentially untrusted subprocesses.
	projectMCPNames := make([]string, 0, len(project.MCPServers))
	for name := range project.MCPServers {
		projectMCPNames = append(projectMCPNames, name)
	}
	sort.Strings(projectMCPNames)

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
	if v := envBool("STREAM"); v != nil {
		cfg.Stream = v
	}
	if v := envBool("COMPACTION"); v != nil {
		cfg.Compaction = v
	}
	if v := envBool("PLANNING"); v != nil {
		if cfg.Planning == nil {
			cfg.Planning = &PlanningFileConfig{}
		}
		cfg.Planning.Enabled = v
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

	// Skills env vars: none (learning removed).

	// MaxConcurrency env var
	if v := envInt("MAX_CONCURRENCY"); v > 0 {
		cfg.MaxConcurrency = v
	}

	// InteractionMode env var
	if v := envString("INTERACTION_MODE"); v != "" {
		cfg.InteractionMode = v
	}

	// Extended memory env overrides
	if v := envBool("MEMORY_EXTENDED_ENABLED"); v != nil {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.Enabled = v
	}
	if v := envInt("MEMORY_EXTENDED_MAX_SIZE_MB"); v > 0 {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.MaxSizeMB = v
	}
	if v := envInt("MEMORY_EXTENDED_ATOM_MAX_CHARS"); v > 0 {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.AtomMaxChars = v
	}
	if v := envInt("MEMORY_EXTENDED_MEMORY_BUDGET_CHARS"); v > 0 {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.MemoryBudgetChars = v
	}
	if v := envInt("MEMORY_EXTENDED_USER_STATE_TURN_INTERVAL"); v > 0 {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.UserStateTurnInterval = v
	}
	if v := envInt("MEMORY_EXTENDED_USER_STATE_MAX_PENDING"); v > 0 {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.UserStateMaxPending = v
	}
	if v := envBool("MEMORY_EXTENDED_ASSOCIATIONS_ENABLED"); v != nil {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.AssociationsEnabled = v
	}
	if v := envInt("MEMORY_EXTENDED_ASSOCIATION_SEMANTIC_TOP_K"); v > 0 {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.AssociationSemanticTopK = v
	}
	if v := envBool("MEMORY_EXTENDED_PROACTIVE_RETURN_AFTER_BREAK"); v != nil {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.ProactiveReturnAfterBreak = v
	}
	if v := envBool("MEMORY_EXTENDED_STYLE_MIRRORING_ENABLED"); v != nil {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.StyleMirroringEnabled = v
	}
	if v := envBool("MEMORY_EXTENDED_ANAPHORA_RESOLUTION_ENABLED"); v != nil {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.AnaphoraResolutionEnabled = v
	}
	if v := envBool("MEMORY_EXTENDED_FOLLOW_UP_ANTICIPATION_ENABLED"); v != nil {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.FollowUpAnticipationEnabled = v
	}
	if v := envBool("MEMORY_EXTENDED_FOLLOW_UP_SUGGESTIONS_ENABLED"); v != nil {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.FollowUpSuggestionsEnabled = v
	}
	if v := envFloat("MEMORY_EXTENDED_FOLLOW_UP_SUGGESTION_MIN_CONFIDENCE"); v > 0 {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.FollowUpSuggestionMinConfidence = float32(v)
	}
	if v := envBool("MEMORY_EXTENDED_PROACTIVE_NUDGES_ENABLED"); v != nil {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.ProactiveNudgesEnabled = v
	}
	if v := envInt("MEMORY_EXTENDED_NUDGE_MAX_PER_DAY"); v > 0 {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.NudgeMaxPerDay = v
	}
	if v := envInt("MEMORY_EXTENDED_NUDGE_COOLDOWN_HOURS"); v > 0 {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.NudgeCooldownHours = v
	}
	if v := envInt("MEMORY_EXTENDED_NUDGE_STALE_GOAL_DAYS"); v > 0 {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.NudgeStaleGoalDays = v
	}
	if v := envInt("MEMORY_EXTENDED_NUDGE_OPEN_QUESTION_MIN_AGE_HOURS"); v > 0 {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.NudgeOpenQuestionMinAgeHours = v
	}

	// Guard env overrides
	if v := envString("GUARD_PROVIDER"); v != "" {
		cfg.Guard = ensureGuard(cfg.Guard)
		cfg.Guard.Provider = v
	}
	if v := envString("GUARD_URL"); v != "" {
		cfg.Guard = ensureGuard(cfg.Guard)
		cfg.Guard.URL = v
	}
	if v := envString("GUARD_BATCH_URL"); v != "" {
		cfg.Guard = ensureGuard(cfg.Guard)
		cfg.Guard.BatchURL = v
	}
	if v := envString("GUARD_LONG_URL"); v != "" {
		cfg.Guard = ensureGuard(cfg.Guard)
		cfg.Guard.LongURL = v
	}
	if v := envString("GUARD_SOCKET_PATH"); v != "" {
		cfg.Guard = ensureGuard(cfg.Guard)
		cfg.Guard.SocketPath = v
	}
	if v := envFloat("GUARD_THRESHOLD"); v != 0 {
		cfg.Guard = ensureGuard(cfg.Guard)
		cfg.Guard.Threshold = v
	}
	if v := envInt("GUARD_TIMEOUT_SECONDS"); v > 0 {
		cfg.Guard = ensureGuard(cfg.Guard)
		cfg.Guard.TimeoutSeconds = v
	}
	if v := envBool("GUARD_FALLBACK_TO_LOCAL"); v != nil {
		cfg.Guard = ensureGuard(cfg.Guard)
		cfg.Guard.FallbackToLocal = v
	}
	if v := envBool("GUARD_SCAN_MEMORY"); v != nil {
		cfg.Guard = ensureGuard(cfg.Guard)
		cfg.Guard.Scan.Memory = v
	}
	if v := envBool("GUARD_SCAN_SYSTEM_PROMPT"); v != nil {
		cfg.Guard = ensureGuard(cfg.Guard)
		cfg.Guard.Scan.SystemPrompt = v
	}
	if v := envBool("GUARD_SCAN_MCP_DESCRIPTIONS"); v != nil {
		cfg.Guard = ensureGuard(cfg.Guard)
		cfg.Guard.Scan.MCPDescriptions = v
	}
	if v := envBool("GUARD_SCAN_SKILLS"); v != nil {
		cfg.Guard = ensureGuard(cfg.Guard)
		cfg.Guard.Scan.Skills = v
	}
	if v := envBool("GUARD_SCAN_TOOL_OUTPUTS"); v != nil {
		cfg.Guard = ensureGuard(cfg.Guard)
		cfg.Guard.Scan.ToolOutputs = v
	}
	if v := envBool("GUARD_SCAN_TELEGRAM"); v != nil {
		cfg.Guard = ensureGuard(cfg.Guard)
		cfg.Guard.Scan.Telegram = v
	}

	// Trusted proxy list for `odek serve`. Empty means X-Forwarded-For /
	// X-Real-Ip headers are ignored even from loopback.
	if v := envStringList("TRUSTED_PROXIES"); v != nil {
		cfg.TrustedProxies = v
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
	if v := envInt64List("SCHEDULES_TELEGRAM_ADMIN_CHATS"); v != nil {
		cfg.Schedules.TelegramAdminChats = v
	}
	if v := envInt64List("SCHEDULES_TELEGRAM_ADMIN_USERS"); v != nil {
		cfg.Schedules.TelegramAdminUsers = v
	}
	if v := envScheduleDangerousConfig("SCHEDULES_DANGEROUS"); v != nil {
		if cfg.Schedules.Dangerous == nil {
			cfg.Schedules.Dangerous = v
		} else {
			mergeDangerousConfig(cfg.Schedules.Dangerous, v)
		}
	}

	// Maintenance env overrides (ODEK_MAINTENANCE_*). Explicit 0 is meaningful
	// for the retention knobs (0 = keep forever / disable), so they parse via
	// the pointer helpers rather than envInt.
	if v := envBool("MAINTENANCE_ENABLED"); v != nil {
		cfg.Maintenance = ensureMaintenance(cfg.Maintenance)
		cfg.Maintenance.Enabled = v
	}
	if v := envIntPtr("MAINTENANCE_INTERVAL_MINUTES"); v != nil {
		cfg.Maintenance = ensureMaintenance(cfg.Maintenance)
		cfg.Maintenance.IntervalMinutes = v
	}
	if v := envIntPtr("MAINTENANCE_SESSIONS_MAX_AGE_DAYS"); v != nil {
		cfg.Maintenance = ensureMaintenance(cfg.Maintenance)
		cfg.Maintenance.SessionsMaxAgeDays = v
	}
	if v := envIntPtr("MAINTENANCE_AUDIT_MAX_AGE_DAYS"); v != nil {
		cfg.Maintenance = ensureMaintenance(cfg.Maintenance)
		cfg.Maintenance.AuditMaxAgeDays = v
	}
	if v := envInt64Ptr("MAINTENANCE_LOG_MAX_MB"); v != nil {
		cfg.Maintenance = ensureMaintenance(cfg.Maintenance)
		cfg.Maintenance.LogMaxMB = v
	}
	if v := envIntPtr("MAINTENANCE_PLANS_MAX_AGE_DAYS"); v != nil {
		cfg.Maintenance = ensureMaintenance(cfg.Maintenance)
		cfg.Maintenance.PlansMaxAgeDays = v
	}
	if v := envIntPtr("MAINTENANCE_ARTIFACTS_MAX_AGE_HOURS"); v != nil {
		cfg.Maintenance = ensureMaintenance(cfg.Maintenance)
		cfg.Maintenance.ArtifactsMaxAgeHours = v
	}

	// Telegram env overrides: merge env vars on top of file config.
	baseTelegram := telegram.DefaultConfig()
	if cfg.Telegram != nil {
		baseTelegram = *cfg.Telegram
	}
	mergedTelegram := telegram.ConfigFromEnv(baseTelegram)
	cfg.Telegram = &mergedTelegram

	if v := envStringList("TOOLS_ENABLED"); v != nil {
		if cfg.Tools == nil {
			cfg.Tools = &ToolsConfig{}
		}
		cfg.Tools.Enabled = v
	}
	if v := envStringList("TOOLS_DISABLED"); v != nil {
		if cfg.Tools == nil {
			cfg.Tools = &ToolsConfig{}
		}
		cfg.Tools.Disabled = v
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
	if cli.PromptCaching != nil {
		cfg.PromptCaching = cli.PromptCaching
	}
	if cli.Stream != nil {
		cfg.Stream = cli.Stream
	}
	if cli.Compaction != nil {
		cfg.Compaction = cli.Compaction
	}
	if cli.Planning != nil {
		if cfg.Planning == nil {
			cfg.Planning = &PlanningFileConfig{}
		}
		cfg.Planning.Enabled = cli.Planning
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
	if cli.MemoryExtendedEnabled != nil {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.Enabled = cli.MemoryExtendedEnabled
	}
	if cli.MemoryExtendedMaxSizeMB > 0 {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.MaxSizeMB = cli.MemoryExtendedMaxSizeMB
	}
	if cli.MemoryExtendedAtomMaxChars > 0 {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.AtomMaxChars = cli.MemoryExtendedAtomMaxChars
	}
	if cli.MemoryExtendedMemoryBudgetChars > 0 {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.MemoryBudgetChars = cli.MemoryExtendedMemoryBudgetChars
	}
	if cli.MemoryExtendedUserStateTurnInterval > 0 {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.UserStateTurnInterval = cli.MemoryExtendedUserStateTurnInterval
	}
	if cli.MemoryExtendedUserStateMaxPending > 0 {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.UserStateMaxPending = cli.MemoryExtendedUserStateMaxPending
	}
	if cli.MemoryExtendedAssociationsEnabled != nil {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.AssociationsEnabled = cli.MemoryExtendedAssociationsEnabled
	}
	if cli.MemoryExtendedAssociationSemanticTopK > 0 {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.AssociationSemanticTopK = cli.MemoryExtendedAssociationSemanticTopK
	}
	if cli.MemoryExtendedProactiveReturnAfterBreak != nil {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.ProactiveReturnAfterBreak = cli.MemoryExtendedProactiveReturnAfterBreak
	}
	if cli.MemoryExtendedStyleMirroringEnabled != nil {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.StyleMirroringEnabled = cli.MemoryExtendedStyleMirroringEnabled
	}
	if cli.MemoryExtendedAnaphoraResolutionEnabled != nil {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.AnaphoraResolutionEnabled = cli.MemoryExtendedAnaphoraResolutionEnabled
	}
	if cli.MemoryExtendedFollowUpAnticipationEnabled != nil {
		if cfg.Memory == nil {
			cfg.Memory = &memory.MemoryConfig{}
		}
		cfg.Memory.Extended = ensureExtended(cfg.Memory.Extended)
		cfg.Memory.Extended.FollowUpAnticipationEnabled = cli.MemoryExtendedFollowUpAnticipationEnabled
	}

	// Guard CLI overrides
	if cli.GuardProvider != "" {
		cfg.Guard = ensureGuard(cfg.Guard)
		cfg.Guard.Provider = cli.GuardProvider
	}
	if cli.GuardURL != "" {
		cfg.Guard = ensureGuard(cfg.Guard)
		cfg.Guard.URL = cli.GuardURL
	}
	if cli.GuardBatchURL != "" {
		cfg.Guard = ensureGuard(cfg.Guard)
		cfg.Guard.BatchURL = cli.GuardBatchURL
	}
	if cli.GuardLongURL != "" {
		cfg.Guard = ensureGuard(cfg.Guard)
		cfg.Guard.LongURL = cli.GuardLongURL
	}
	if cli.GuardSocketPath != "" {
		cfg.Guard = ensureGuard(cfg.Guard)
		cfg.Guard.SocketPath = cli.GuardSocketPath
	}
	if cli.GuardThreshold != 0 {
		cfg.Guard = ensureGuard(cfg.Guard)
		cfg.Guard.Threshold = cli.GuardThreshold
	}
	if cli.GuardTimeoutSeconds > 0 {
		cfg.Guard = ensureGuard(cfg.Guard)
		cfg.Guard.TimeoutSeconds = cli.GuardTimeoutSeconds
	}
	if cli.GuardFallbackToLocal != nil {
		cfg.Guard = ensureGuard(cfg.Guard)
		cfg.Guard.FallbackToLocal = cli.GuardFallbackToLocal
	}
	if cli.GuardScanMemory != nil {
		cfg.Guard = ensureGuard(cfg.Guard)
		cfg.Guard.Scan.Memory = cli.GuardScanMemory
	}
	if cli.GuardScanSystemPrompt != nil {
		cfg.Guard = ensureGuard(cfg.Guard)
		cfg.Guard.Scan.SystemPrompt = cli.GuardScanSystemPrompt
	}
	if cli.GuardScanMCP != nil {
		cfg.Guard = ensureGuard(cfg.Guard)
		cfg.Guard.Scan.MCPDescriptions = cli.GuardScanMCP
	}
	if cli.GuardScanSkills != nil {
		cfg.Guard = ensureGuard(cfg.Guard)
		cfg.Guard.Scan.Skills = cli.GuardScanSkills
	}
	if cli.GuardScanToolOutputs != nil {
		cfg.Guard = ensureGuard(cfg.Guard)
		cfg.Guard.Scan.ToolOutputs = cli.GuardScanToolOutputs
	}
	if cli.GuardScanTelegram != nil {
		cfg.Guard = ensureGuard(cfg.Guard)
		cfg.Guard.Scan.Telegram = cli.GuardScanTelegram
	}

	if len(cli.ToolsEnabled) > 0 {
		if cfg.Tools == nil {
			cfg.Tools = &ToolsConfig{}
		}
		cfg.Tools.Enabled = cli.ToolsEnabled
	}
	if len(cli.ToolsDisabled) > 0 {
		if cfg.Tools == nil {
			cfg.Tools = &ToolsConfig{}
		}
		cfg.Tools.Disabled = append(cfg.Tools.Disabled, cli.ToolsDisabled...)
	}
	if len(cli.TrustedProxies) > 0 {
		cfg.TrustedProxies = cli.TrustedProxies
	}

	// Execution-budget CLI flags (operator intent — set explicitly; they may
	// raise or lower any file-configured limit).
	if cli.MaxRuntimeSeconds > 0 || cli.MaxToolCalls > 0 || cli.MaxInputTokens > 0 ||
		cli.MaxOutputTokens > 0 || cli.MaxCostUSD > 0 {
		if cfg.Limits == nil {
			cfg.Limits = &budget.Limits{}
		}
		if cli.MaxRuntimeSeconds > 0 {
			cfg.Limits.MaxRuntimeSeconds = cli.MaxRuntimeSeconds
		}
		if cli.MaxToolCalls > 0 {
			cfg.Limits.MaxToolCalls = cli.MaxToolCalls
		}
		if cli.MaxInputTokens > 0 {
			cfg.Limits.MaxInputTokens = cli.MaxInputTokens
		}
		if cli.MaxOutputTokens > 0 {
			cfg.Limits.MaxOutputTokens = cli.MaxOutputTokens
		}
		if cli.MaxCostUSD > 0 {
			cfg.Limits.MaxCostUSD = cli.MaxCostUSD
		}
	}

	// Build resolved config with concrete values
	resolved := ResolvedConfig{
		Model:    cfg.Model,
		BaseURL:  cfg.BaseURL,
		APIKey:   cfg.APIKey,
		Thinking: cfg.Thinking,
		MaxIter:  cfg.MaxIter,
		System:   cfg.System,

		SandboxImage:           cfg.SandboxImage, // empty = resolve at call site (Dockerfile.odek or alpine:latest)
		SandboxNetwork:         ifZero(cfg.SandboxNetwork, DefaultSandboxNetwork),
		SandboxMemory:          cfg.SandboxMemory,
		SandboxCPUs:            cfg.SandboxCPUs,
		SandboxUser:            cfg.SandboxUser,
		SandboxEnv:             cfg.SandboxEnv,
		SandboxVolumes:         cfg.SandboxVolumes,
		Skills:                 resolveSkills(cfg.Skills),
		Dangerous:              resolveDangerous(cfg.Dangerous, true),
		Memory:                 resolveMemory(cfg.Memory),
		Guard:                  resolveGuard(cfg.Guard),
		Embedding:              cfg.Embedding,
		MCPServers:             cfg.MCPServers,
		ProjectMCPServerNames:  projectMCPNames,
		ProjectSandboxOverride: projectSandboxOverride,
		Telegram:               resolveTelegram(cfg.Telegram),
		Transcription:          resolveTranscription(cfg.Transcription),
		Vision:                 resolveVision(cfg.Vision),
		WebSearch:              resolveWebSearch(cfg.WebSearch),
		Schedules:              resolveSchedules(cfg.Schedules),
		Maintenance:            resolveMaintenance(cfg.Maintenance),
		Subagent:               resolveSubagent(cfg.Subagent),
		Profiles:               resolveProfiles(cfg.Profiles),
		Tools:                  resolveTools(cfg.Tools),
		InteractionMode:        ifZero(cfg.InteractionMode, "engaging"),
		ToolProgress:           ifZero(cfg.ToolProgress, "all"),
	}

	// Every subsystem inherits the shared top-level embedding default unless it
	// set its own override. Memory and skills carry their resolved embedder on
	// their own config struct; sessions expose it via SessionEmbedding.
	if resolved.Memory.Embedding == nil {
		resolved.Memory.Embedding = cfg.Embedding
	}
	if resolved.Skills.Embedding == nil {
		resolved.Skills.Embedding = skillsInheritedEmbedding(cfg.Embedding)
	}
	resolved.SessionEmbedding = cfg.Embedding
	if cfg.Sessions != nil && cfg.Sessions.Embedding != nil {
		resolved.SessionEmbedding = cfg.Sessions.Embedding
	}

	// MaxConcurrency: default to 3 if not set
	if cfg.MaxConcurrency > 0 {
		resolved.MaxConcurrency = cfg.MaxConcurrency
	} else {
		resolved.MaxConcurrency = 3
	}

	// Telegram operator identity: schedule management and /restart are restricted
	// to configured operator chats/users. If the operator did not configure
	// explicit admin lists, fall back to telegram.default_chat_id (the operator's
	// own chat). If that is also unset, mutating /schedule commands and /restart
	// are rejected until an admin list is configured; read-only commands still
	// work.
	if len(resolved.Schedules.TelegramAdminChats) == 0 && len(resolved.Schedules.TelegramAdminUsers) == 0 && resolved.Telegram.DefaultChatID != 0 {
		resolved.Schedules.TelegramAdminChats = []int64{resolved.Telegram.DefaultChatID}
	}

	// MaxToolParallel: 0 = use loop engine default (4)
	resolved.MaxToolParallel = cfg.MaxToolParallel

	// Booleans: default to false if not set (Compaction below is the
	// exception — it defaults to true).
	// Sandbox is the second exception in effect: the loader records whether
	// any layer set it (SandboxExplicit); when nobody did, the CLI surfaces
	// default it ON with a loud unsandboxed fallback (H-8, cmd/odek).
	if cfg.Sandbox != nil {
		resolved.Sandbox = *cfg.Sandbox
		resolved.SandboxExplicit = true
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
	if cfg.Stream != nil {
		resolved.Stream = *cfg.Stream
	}
	// Compaction defaults to ON: for long sessions, turn groups dropped by
	// context trimming are summarized into a rolling digest instead of
	// vanishing. An explicit false from any layer (config file,
	// ODEK_COMPACTION=false, --no-compaction) disables it.
	resolved.Compaction = true
	if cfg.Compaction != nil {
		resolved.Compaction = *cfg.Compaction
	}
	// Planning defaults to ON (docs/PLANNING.md): the plan tool registers and
	// the protected plan message logic runs. An explicit false from any layer
	// (config file, ODEK_PLANNING=false, --no-planning) disables it. Caps are
	// range-clamped regardless of which layer set them.
	resolved.Planning = DefaultPlanningConfig()
	if cfg.Planning != nil {
		if cfg.Planning.Enabled != nil {
			resolved.Planning.Enabled = *cfg.Planning.Enabled
		}
		if cfg.Planning.MaxSteps != nil {
			resolved.Planning.MaxSteps = *cfg.Planning.MaxSteps
		}
		if cfg.Planning.MaxRenderChars != nil {
			resolved.Planning.MaxRenderChars = *cfg.Planning.MaxRenderChars
		}
	}
	switch {
	case resolved.Planning.MaxSteps < planningMinSteps:
		resolved.Planning.MaxSteps = planningMinSteps
	case resolved.Planning.MaxSteps > planningMaxSteps:
		resolved.Planning.MaxSteps = planningMaxSteps
	}
	switch {
	case resolved.Planning.MaxRenderChars < planningMinRenderChars:
		resolved.Planning.MaxRenderChars = planningMinRenderChars
	case resolved.Planning.MaxRenderChars > planningMaxRenderChars:
		resolved.Planning.MaxRenderChars = planningMaxRenderChars
	}
	if cfg.SandboxReadonly != nil {
		resolved.SandboxReadonly = *cfg.SandboxReadonly
	}
	if cfg.ToolProgressCleanup != nil {
		resolved.ToolProgressCleanup = *cfg.ToolProgressCleanup
	} else {
		resolved.ToolProgressCleanup = true // default: delete progress messages
	}

	resolved.TrustedProxies = cfg.TrustedProxies

	// Execution budgets: concrete value; zero fields = no limit.
	if cfg.Limits != nil {
		resolved.Limits = *cfg.Limits
	}
	// Cost enforcement needs operator-configured per-million prices — odek
	// never hard-codes provider prices. Warn loudly when the operator set a
	// cost cap but neither model_prices[model] nor the flat pair yields
	// positive prices, so a silent no-op is impossible; token budgets stay
	// active regardless.
	if resolved.Limits.MaxCostUSD > 0 {
		inPrice, outPrice := resolved.Limits.ResolvePrices(resolved.Model)
		if inPrice <= 0 || outPrice <= 0 {
			fmt.Fprintf(os.Stderr, "odek: warning: limits.max_cost_usd is set but input/output per-million prices are not both configured — cost enforcement is DISABLED (token budgets remain active)\n")
		}
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

// resolveTools returns a concrete ToolConfig from a possibly-nil file config.
// Empty Enabled/Disabled slices mean "no restriction" for that direction.
func resolveTools(cfg *ToolsConfig) ToolConfig {
	if cfg == nil {
		return ToolConfig{}
	}
	return ToolConfig{
		Enabled:  cfg.Enabled,
		Disabled: cfg.Disabled,
	}
}

// ifZero returns the default value if s is empty, otherwise returns s.
func ifZero(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// maxSkillsInheritedTimeout bounds (seconds) the per-turn query embed when
// skills inherit the shared embedding default. Skill matching runs every turn,
// so a longer memory/session-oriented timeout must not leak onto the hot path.
// An explicit skills.embedding is respected as-is (not capped here).
const maxSkillsInheritedTimeout = 2

// skillsInheritedEmbedding returns the shared embedding config bounded to a
// short per-turn timeout for skill matching. nil stays nil (local RP default).
func skillsInheritedEmbedding(shared *embedding.Config) *embedding.Config {
	if shared == nil {
		return nil
	}
	c := *shared
	if c.TimeoutSeconds == 0 || c.TimeoutSeconds > maxSkillsInheritedTimeout {
		c.TimeoutSeconds = maxSkillsInheritedTimeout
	}
	return &c
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
	if cfg.Verbose != nil {
		def.Verbose = *cfg.Verbose
	}
	// skills.embedding overrides the shared default; inheritance of the shared
	// default (when this is nil) is applied by the caller after resolution.
	if cfg.Embedding != nil {
		def.Embedding = cfg.Embedding
	}
	return def
}

// resolveDangerous merges file-level and potential env-level dangerous config.
// If no config is provided, returns an empty DangerousConfig (safe defaults).
// When validate is true, invalid non_interactive values are rejected with a
// warning and forced to "deny" so headless runs cannot accidentally
// auto-approve dangerous ops.
func resolveDangerous(cfg *danger.DangerousConfig, validate bool) danger.DangerousConfig {
	if cfg == nil {
		return danger.DangerousConfig{}
	}
	resolved := *cfg
	if validate && resolved.NonInteractive != nil {
		if _, ok := danger.ParseNonInteractiveAction(*resolved.NonInteractive); !ok {
			fmt.Fprintf(os.Stderr, "odek: warning: invalid non_interactive value %q — must be 'allow', 'deny', or 'read_only'; using 'deny'\n", *resolved.NonInteractive)
			deny := "deny"
			resolved.NonInteractive = &deny
		}
	}
	return resolved
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
	if cfg.Embedding != nil {
		def.Embedding = cfg.Embedding
	}
	if cfg.Extended != nil {
		resolved := extended.Resolve(*cfg.Extended)
		def.Extended = &resolved
	}
	return def
}

// resolveGuard merges file-level guard config with defaults.
// Starts from DefaultConfig and overlays any non-zero/non-nil fields from
// cfg. A partial config won't disable the local scan or the default surfaces.
func resolveGuard(cfg *guard.Config) guard.Config {
	def := guard.DefaultConfig()
	if cfg == nil {
		return *def
	}
	if cfg.Provider != "" {
		def.Provider = cfg.Provider
	}
	if cfg.URL != "" {
		def.URL = cfg.URL
	}
	if cfg.LongURL != "" {
		def.LongURL = cfg.LongURL
	}
	if cfg.BatchURL != "" {
		def.BatchURL = cfg.BatchURL
	}
	if cfg.SocketPath != "" {
		def.SocketPath = cfg.SocketPath
	}
	if cfg.Threshold > 0 {
		def.Threshold = cfg.Threshold
	}
	if cfg.TimeoutSeconds > 0 {
		def.TimeoutSeconds = cfg.TimeoutSeconds
	}
	if cfg.FallbackToLocal != nil {
		def.FallbackToLocal = cfg.FallbackToLocal
	}
	if cfg.MaxTextLength > 0 {
		def.MaxTextLength = cfg.MaxTextLength
	}
	if cfg.Scan != nil {
		if cfg.Scan.Memory != nil {
			def.Scan.Memory = cfg.Scan.Memory
		}
		if cfg.Scan.SystemPrompt != nil {
			def.Scan.SystemPrompt = cfg.Scan.SystemPrompt
		}
		if cfg.Scan.MCPDescriptions != nil {
			def.Scan.MCPDescriptions = cfg.Scan.MCPDescriptions
		}
		if cfg.Scan.Skills != nil {
			def.Scan.Skills = cfg.Scan.Skills
		}
		if cfg.Scan.ToolOutputs != nil {
			def.Scan.ToolOutputs = cfg.Scan.ToolOutputs
		}
		if cfg.Scan.Telegram != nil {
			def.Scan.Telegram = cfg.Scan.Telegram
		}
	}
	return *def
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
	// MaxDownloadSize: 0 (unset) -> default 5 MiB; negative -> unlimited (0);
	// positive -> explicit cap.
	if cfg.MaxDownloadSize < 0 {
		base.MaxDownloadSize = 0
	} else if cfg.MaxDownloadSize > 0 {
		base.MaxDownloadSize = cfg.MaxDownloadSize
	} else {
		base.MaxDownloadSize = telegram.DefaultMaxDownloadSize
	}
	// MediaQuotaPerChat: 0 = disabled (default); positive = quota in bytes.
	if cfg.MediaQuotaPerChat > 0 {
		base.MediaQuotaPerChat = cfg.MediaQuotaPerChat
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

// resolveWebSearch returns the resolved web_search config, filling zero-valued
// numeric fields with defaults. BaseURL has no default — it stays empty (and
// the tool stays unregistered) until the operator points it at a SearXNG instance.
func resolveWebSearch(cfg *WebSearchConfig) WebSearchConfig {
	if cfg != nil {
		if cfg.MaxResults == 0 {
			cfg.MaxResults = 10
		}
		if cfg.Timeout == 0 {
			cfg.Timeout = 15
		}
		return *cfg
	}
	return WebSearchConfig{
		MaxResults: 10,
		Timeout:    15,
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
	// TelegramAdminChats restricts mutating `/schedule` commands to the listed
	// chat IDs. When empty, management falls back to telegram.default_chat_id
	// (if set). Read-only commands are not affected.
	TelegramAdminChats []int64 `json:"telegram_admin_chats,omitempty"`
	// TelegramAdminUsers restricts mutating `/schedule` commands to the listed
	// user IDs. Read-only commands are not affected.
	TelegramAdminUsers []int64 `json:"telegram_admin_users,omitempty"`
	// Dangerous overrides the global dangerous-operations policy for scheduled
	// (unattended) runs only. It is applied on top of the global dangerous
	// config, then a non-overrideable safety floor is applied by the scheduler
	// itself: destructive and blocked classes are always denied, and
	// non_interactive is always deny because no human is present to approve.
	// This lets operators allow network_egress/system_write/etc. for cron jobs
	// without widening the policy for interactive CLI/REPL/WebUI use.
	Dangerous *danger.DangerousConfig `json:"dangerous,omitempty"`
}

// ScheduleConfig is the resolved scheduler config (all fields concrete).
type ScheduleConfig struct {
	Enabled                 bool
	MaxConcurrent           int
	Timezone                string
	Catchup                 bool
	AllowTelegramManagement bool
	TelegramAdminChats      []int64
	TelegramAdminUsers      []int64
	// Dangerous is the schedule-specific dangerous-operations policy. See
	// SchedulesConfig.Dangerous for semantics.
	Dangerous danger.DangerousConfig
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
	out.TelegramAdminChats = cfg.TelegramAdminChats
	out.TelegramAdminUsers = cfg.TelegramAdminUsers
	out.Dangerous = resolveDangerous(cfg.Dangerous, false)
	return out
}

// overlayFile overlays a higher-priority FileConfig onto a lower-priority one.
// Only fields that are explicitly set (non-zero for scalars, non-nil for
// pointers) override the base value.
// clampProjectLimits enforces the execution-budget merge rule (review note 5
// of the extension MVP): the global (operator) config may set any limit, but
// the untrusted project ./odek.json may only LOWER an existing limit — never
// raise it and never disable (zero-out) a globally-set limit. A project may
// still set a limit the global config does not have (that only tightens the
// run). Per-million prices (flat and per-model) are NOT limits — a lower
// project price would silently weaken cost enforcement — so project-set
// prices are ignored outright and the global values are kept.
//
// The clamp fills every globally-set field back into project.Limits, so the
// plain pointer overlay in overlayFile cannot drop a global limit when the
// project sets a different one.
func clampProjectLimits(global, project *budget.Limits) {
	if project == nil {
		return
	}
	var g budget.Limits
	if global != nil {
		g = *global
	}
	clampInt := func(name string, g, p int64) int64 {
		switch {
		case g <= 0:
			return p // no global limit — any project value only tightens
		case p > g:
			fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring limits.%s=%d from project config (%s) — it would raise the global limit %d\n", name, p, ProjectConfigPath(), g)
			return g
		case p <= 0:
			return g // a global limit cannot be disabled/zero-outed by the project
		default:
			return p // lowered — allowed
		}
	}
	clampFloat := func(name string, g, p float64) float64 {
		switch {
		case g <= 0:
			return p
		case p > g:
			fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring limits.%s=%g from project config (%s) — it would raise the global limit %g\n", name, p, ProjectConfigPath(), g)
			return g
		case p <= 0:
			return g
		default:
			return p
		}
	}
	project.MaxRuntimeSeconds = clampInt("max_runtime_seconds", g.MaxRuntimeSeconds, project.MaxRuntimeSeconds)
	project.MaxToolCalls = clampInt("max_tool_calls", g.MaxToolCalls, project.MaxToolCalls)
	project.MaxInputTokens = clampInt("max_input_tokens", g.MaxInputTokens, project.MaxInputTokens)
	project.MaxOutputTokens = clampInt("max_output_tokens", g.MaxOutputTokens, project.MaxOutputTokens)
	project.MaxCostUSD = clampFloat("max_cost_usd", g.MaxCostUSD, project.MaxCostUSD)

	// Prices come from operator-controlled sources only.
	if project.InputCostPerMillionUSD != 0 {
		fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring limits.input_cost_per_million_usd from project config (%s); set prices via ~/.odek/config.json\n", ProjectConfigPath())
	}
	if project.OutputCostPerMillionUSD != 0 {
		fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring limits.output_cost_per_million_usd from project config (%s); set prices via ~/.odek/config.json\n", ProjectConfigPath())
	}
	if len(project.ModelPrices) > 0 {
		fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring limits.model_prices from project config (%s); set prices via ~/.odek/config.json\n", ProjectConfigPath())
	}
	project.InputCostPerMillionUSD = g.InputCostPerMillionUSD
	project.OutputCostPerMillionUSD = g.OutputCostPerMillionUSD
	project.ModelPrices = g.ModelPrices
}

// clampProjectPlanning enforces the planning merge rule (docs/PLANNING.md —
// Config & API Surface), mirroring clampProjectLimits: the untrusted project
// ./odek.json may set enabled:false (opt out) and may only LOWER caps the
// global config explicitly set; it cannot re-enable a globally-disabled
// feature or raise an operator-set cap. When the global config carries no
// planning section, project values apply freely — they can only opt out or
// deviate from the defaults, not override an operator decision.
func clampProjectPlanning(global, project *PlanningFileConfig) {
	if project == nil || global == nil {
		return
	}
	if global.Enabled != nil && !*global.Enabled && project.Enabled != nil && *project.Enabled {
		fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring planning.enabled=true from project config (%s) — planning is disabled in ~/.odek/config.json\n", ProjectConfigPath())
		project.Enabled = nil // global-off wins
	}
	clampInt := func(name string, g, p *int) *int {
		switch {
		case g == nil || p == nil:
			return p // nothing to clamp against / nothing requested
		case *p > *g:
			fmt.Fprintf(os.Stderr, "odek: WARNING: ignoring planning.%s=%d from project config (%s) — it would raise the global cap %d\n", name, *p, ProjectConfigPath(), *g)
			return g
		default:
			return p // lowered or equal — allowed
		}
	}
	project.MaxSteps = clampInt("max_steps", global.MaxSteps, project.MaxSteps)
	project.MaxRenderChars = clampInt("max_render_chars", global.MaxRenderChars, project.MaxRenderChars)
}

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
		if base.Skills == nil {
			base.Skills = &SkillsConfig{}
		}
		overlaySkills(base.Skills, override.Skills)
	}
	if override.Memory != nil {
		base.Memory = override.Memory
	}
	if override.Maintenance != nil {
		base.Maintenance = override.Maintenance
	}
	if override.Subagent != nil {
		// Reached only after the untrusted project section was stripped,
		// so this carries the operator's global layer over the defaults.
		base.Subagent = override.Subagent
	}
	if override.Profiles != nil {
		base.Profiles = override.Profiles
	}
	if override.Guard != nil {
		base.Guard = override.Guard
	}
	if override.Embedding != nil {
		base.Embedding = override.Embedding
	}
	if override.Sessions != nil {
		base.Sessions = override.Sessions
	}
	if override.Telegram != nil {
		base.Telegram = override.Telegram
	}
	if override.PromptCaching != nil {
		base.PromptCaching = override.PromptCaching
	}
	if override.Stream != nil {
		base.Stream = override.Stream
	}
	if override.Compaction != nil {
		base.Compaction = override.Compaction
	}
	if override.Planning != nil {
		if base.Planning == nil {
			base.Planning = &PlanningFileConfig{}
		}
		o, b := override.Planning, base.Planning
		if o.Enabled != nil {
			b.Enabled = o.Enabled
		}
		if o.MaxSteps != nil {
			b.MaxSteps = o.MaxSteps
		}
		if o.MaxRenderChars != nil {
			b.MaxRenderChars = o.MaxRenderChars
		}
	}
	if override.MaxConcurrency > 0 {
		base.MaxConcurrency = override.MaxConcurrency
	}
	if override.MaxToolParallel > 0 {
		base.MaxToolParallel = override.MaxToolParallel
	}
	if len(override.TrustedProxies) > 0 {
		base.TrustedProxies = override.TrustedProxies
	}
	if override.MCPServers != nil {
		if base.MCPServers == nil {
			base.MCPServers = make(map[string]mcpclient.ServerConfig)
		}
		for k, v := range override.MCPServers {
			// An operator-set auto_approve on the base (global) entry is
			// trust metadata for the NAME: it survives even when the
			// project overrides the definition. (Project-side auto_approve
			// is stripped before this point.)
			if prev, ok := base.MCPServers[k]; ok && prev.AutoApprove {
				v.AutoApprove = true
			}
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
	if override.Limits != nil {
		// Reached only after clampProjectLimits ran, so the override already
		// carries every globally-set field (clamped or inherited).
		base.Limits = override.Limits
	}
	if override.Vision != nil {
		base.Vision = override.Vision
	}
	if override.WebSearch != nil {
		base.WebSearch = override.WebSearch
	}
	if override.Schedules != nil {
		base.Schedules = override.Schedules
	}
	if override.Tools != nil {
		if base.Tools == nil {
			base.Tools = &ToolsConfig{}
		}
		if len(override.Tools.Enabled) > 0 {
			base.Tools.Enabled = override.Tools.Enabled
		}
		base.Tools.Disabled = append(base.Tools.Disabled, override.Tools.Disabled...)
	}
	return base
}

// overlaySkills merges a higher-priority SkillsConfig onto a lower-priority one
// field-by-field. This lets a project config tune settings like `max_auto_load`
// without clobbering the global `dirs` or `embedding` settings.
func overlaySkills(base, override *SkillsConfig) {
	if override.MaxAutoLoad != nil {
		base.MaxAutoLoad = override.MaxAutoLoad
	}
	if override.MaxLazySlots != nil {
		base.MaxLazySlots = override.MaxLazySlots
	}
	if len(override.Dirs) > 0 {
		base.Dirs = override.Dirs
	}
	if override.Import != nil {
		base.Import = override.Import
	}
	if override.Verbose != nil {
		base.Verbose = override.Verbose
	}
	if override.Embedding != nil {
		base.Embedding = override.Embedding
	}
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

	// Refuse to load secrets from a file that is readable by anyone other than
	// the owner. A world/group-readable secrets.env leaks API keys and tokens
	// to other local users (finding #78).
	if info, err := f.Stat(); err == nil {
		if perm := info.Mode().Perm(); perm&0077 != 0 {
			fmt.Fprintf(os.Stderr, "odek: WARNING: %s is group/world-readable (%04o); refusing to load secrets\n", path, perm)
			return
		}
	}

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
			// Record the name so child-process spawn sites can strip it
			// from the inherited environment: sub-agents get their API key
			// via the FD handoff, and every other secrets.env value must
			// stay out of the child's /proc/<pid>/environ (2026-08 audit).
			secretsEnvMu.Lock()
			secretsEnvNames = append(secretsEnvNames, k)
			secretsEnvMu.Unlock()
		}
	}
}

var (
	secretsEnvMu    sync.Mutex
	secretsEnvNames []string
)

// SecretsEnvNames returns the environment variable names that were
// injected from ~/.odek/secrets.env during LoadConfig. Callers spawning
// child processes strip these from the inherited environment.
func SecretsEnvNames() []string {
	secretsEnvMu.Lock()
	defer secretsEnvMu.Unlock()
	return append([]string(nil), secretsEnvNames...)
}
