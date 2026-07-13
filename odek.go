// Package odek is a minimal Go agent loop runtime.
//
// odek implements the ReAct (Reasoning + Acting) pattern — the "think,
// therefore act" loop that powers autonomous AI agents. It is not a
// framework or an SDK. It is a runtime: one loop, one binary, minimal deps.
//
// # Design
//
//   - Minimal external dependencies. stdlib + a few focused packages.
//   - Session isolation via Docker containers (--sandbox).
//   - LLM-agnostic. Any OpenAI-compatible endpoint works.
//   - Tool-first. Tools are the only extension point.
//
// # Security
//
// When running with --sandbox, each session executes in a fresh Docker
// container. The container has no network access, no host mounts beyond
// the working directory, and is destroyed on exit. The agent can never
// access files outside its working directory.
package odek

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/guard"
	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/loop"
	"github.com/BackendStack21/odek/internal/memory"
	"github.com/BackendStack21/odek/internal/memory/extended"
	"github.com/BackendStack21/odek/internal/narrate"
	"github.com/BackendStack21/odek/internal/render"
	"github.com/BackendStack21/odek/internal/skills"
	"github.com/BackendStack21/odek/internal/tool"
)

// Tool represents a single capability the agent can invoke.
type Tool interface {
	Name() string
	Description() string
	Schema() any // JSON Schema for the tool's parameters
	Call(args string) (string, error)
}

// Config configures an Agent instance.
type Config struct {
	// Model is the LLM model identifier (e.g., "deepseek-v4-flash").
	Model string

	// BaseURL is the OpenAI-compatible API endpoint.
	// Default: "https://api.deepseek.com/v1"
	BaseURL string

	// APIKey authenticates with the LLM provider.
	// Falls back to DEEPSEEK_API_KEY, then OPENAI_API_KEY env vars.
	APIKey string

	// Thinking controls the model's reasoning depth. Provider-specific:
	//
	//   Deepseek: "enabled" or "disabled" → {"type": "enabled"}
	//   OpenAI o-series: "low", "medium", "high" → {"reasoning_effort": "low"}
	//
	// When empty, the model's profile default is used. If the profile also
	// has no default, the field is not sent (provider default behavior).
	Thinking string

	// Temperature controls LLM output randomness (0.0–2.0).
	// Negative = omit from request (use provider default).
	// 0.0 = deterministic, 1.0 = creative. Default: 0.0 for benchmark
	// stability; set to -1 to use provider defaults.
	Temperature float64

	// ThinkingBudget is the maximum thinking tokens for Anthropic extended thinking (default 5000).
	ThinkingBudget int

	// Tools available to the agent.
	Tools []Tool

	// ToolFilter controls which auto-registered tools are exposed to the LLM
	// (for example the memory tool when a MemoryManager is provided). It is
	// not applied to caller-supplied Tools; callers are responsible for
	// filtering their own tool slices. Enabled is a whitelist; Disabled is a
	// blacklist. Empty Enabled means "no whitelist".
	ToolFilter ToolFilterConfig

	// MaxIterations caps the number of think→act cycles (default: 90).
	MaxIterations int

	// SystemMessage is the system prompt injected at the start of every run.
	// Runtime context (OS, hostname, cwd, date, platform) is automatically
	// prepended to this message before it reaches the LLM.
	// If AGENTS.md exists in the working directory, its content is appended
	// automatically. Set NoProjectFile to true to skip this.
	SystemMessage string

	// RuntimeContext, when set, prepends environment awareness to the system
	// message: OS, hostname, working directory, current date/time, and
	// platform-specific formatting rules. Each entry point (CLI, Telegram,
	// WebUI) sets this automatically. When empty, BuildRuntimeContext("")
	// provides generic terminal context.
	RuntimeContext string

	// NoProjectFile disables automatic loading of AGENTS.md from the
	// working directory. By default, odek reads AGENTS.md and appends
	// its content to the system message with a "Project Instructions" header.
	NoProjectFile bool

	// SandboxCleanup, if set, is called by Agent.Close() to destroy the
	// Docker sandbox container. Set by the CLI when --sandbox is active.
	// Programmatic API users can set this to their own cleanup logic
	// (e.g., remove a container, delete a VM, tear down a network).
	// When nil, Close() is a no-op.
	SandboxCleanup func() error

	// Renderer, if set, produces colored terminal output for each phase
	// of the agent loop. When nil, the agent runs silently (programmatic API).
	Renderer *render.Renderer

	// ToolEventHandler, if set, is invoked for each tool call and result
	// during the agent loop. Fires "tool_call" before and "tool_result"
	// after each tool invocation. Used by the WebUI for live streaming.
	ToolEventHandler func(event string, name string, data string)

	// InteractionMode controls tool-call rendering: "engaging" (default), "enhance", "verbose", or "off".
	InteractionMode string

	// IterationCallback, if set, is invoked after each iteration of the
	// agent loop with progress info (turn number, tokens, tools called).
	// Used by the Telegram handler for periodic progress updates.
	IterationCallback loop.IterationCallback

	// Skills configures the skill system. When nil, skills are disabled.
	Skills *skills.SkillsConfig

	// SkillManager holds the loaded skill state. Passed by the CLI layer;
	// when nil, New() auto-loads from default directories.
	SkillManager *skills.SkillManager

	// MemoryDir sets the directory for persistent memory storage.
	// Default: ~/.odek/memory/
	MemoryDir string

	// MemoryConfig controls the memory system (facts, buffer, episodes).
	// Default: memory.DefaultMemoryConfig()
	MemoryConfig memory.MemoryConfig

	// Guard is the prompt-injection detector shared across subsystems.
	// When nil, subsystems fall back to local rule-based scanning on demand.
	Guard guard.Guard

	// GuardConfig is the resolved guard configuration used to decide which
	// surfaces are scanned. It mirrors the guard instance passed above.
	GuardConfig guard.Config

	// PromptCaching enables prompt caching markers for supported providers.
	// When enabled (default: false), the system prompt and first user message
	// are annotated with cache_control markers, and Anthropic-style system
	// blocks are used. Supported by:
	//   - Anthropic (explicit cache_control markers)
	//   - DeepSeek (automatic — prefix stability helps)
	//   - OpenAI (automatic — prefix stability helps)
	//
	// When disabled (default), no cache markers are sent and the system
	// prompt stays in the messages array for maximum provider compatibility.
	// Enable this when using Anthropic models to get ~90% cost reduction
	// on cached tokens and ~60-80% TTFT latency reduction.
	PromptCaching bool

	// MaxToolParallel controls how many tool calls run concurrently per
	// agent iteration. 0 = use default (4). Models that emit multiple
	// parallel tool calls benefit from concurrent execution of I/O-bound
	// tools like read_file, search_files, and web_search.
	MaxToolParallel int

	// SkillEventHandler, if set, is invoked when a skill lifecycle event
	// occurs (loaded, autoloaded, saved, deleted, etc.). Used by WebUI
	// (WebSocket streaming) and Telegram (inline messages).
	SkillEventHandler func(event skills.SkillEvent)

	// MemoryEventHandler, if set, is invoked when a memory lifecycle event
	// occurs (fact add/merge/consolidate, episode store/dedup/evict/promote).
	// Fans out alongside the terminal renderer so embedding programs, the WebUI
	// (WebSocket streaming), and Telegram can observe memory activity that was
	// previously silent.
	MemoryEventHandler func(event memory.MemoryEvent)

	// AgentSignalHandler, if set, is invoked on internal agent-loop signals
	// (context-window trim, tool-failure recovery) that the engine previously
	// handled silently. Used for observability across all surfaces.
	AgentSignalHandler func(event loop.SignalEvent)

	// Approver gates dangerous tool operations. When set and the LLM returns
	// multiple tool calls in one iteration, a single batch approval prompt
	// is shown instead of N individual prompts. If denied, no tools run
	// for that iteration. If approved, individual tool-level PromptCommand
	// calls are bypassed via SetTrustAll.
	Approver danger.Approver

	// DangerousConfig holds the user's risk class configuration (Allow/Deny/
	// Prompt per risk class). Used by the batch gate to decide whether a
	// tool call needs approval before showing the prompt. When nil, the
	// batch gate plays safe and shows the prompt for any classified tool.
	DangerousConfig *danger.DangerousConfig

	// UntrustedWrapper, if set, is applied to skill and episode context before
	// injection into the model's system context. It should wrap externally-
	// sourced content with a nonce'd boundary (and record it for audit). When
	// nil, skill/episode content is injected directly (not recommended for
	// production surfaces).
	UntrustedWrapper func(source, content string) string
}

// Agent is the agent loop runtime.
type Agent struct {
	config         Config
	engine         *loop.Engine
	registry       *tool.Registry
	sandboxCleanup func() error // destroys the sandbox container on Close()
	skillManager   *skills.SkillManager
	memoryManager  *memory.MemoryManager
}

// ── Model Profiles ────────────────────────────────────────────────────
//
// A ModelProfile overrides default settings for a particular model or
// model family. Profiles are matched by longest model-name prefix.
//
// To add support for a new model, append an entry to KnownProfiles with
// the model prefix, a human-readable label, and any defaults (thinking,
// timeout). The rest of odek picks it up automatically — no changes to
// the LLM client, loop engine, or CLI parsing needed.

// ToolFilterConfig controls which tools are exposed to the LLM.
type ToolFilterConfig struct {
	// Enabled is a whitelist. When non-nil, only tools whose names appear
	// here are registered. An empty (but non-nil) slice means no tools.
	Enabled []string
	// Disabled is a blacklist. Tools whose names appear here are removed
	// after the whitelist is applied.
	Disabled []string
}

// ModelProfile holds per-model defaults applied when the user hasn't
// explicitly provided a value. Zero values leave the system default.
type ModelProfile struct {
	// Label is a human-readable name for the model family.
	Label string

	// DefaultThinking is the thinking value applied when Config.Thinking
	// is empty. Empty string means don't send the field (provider default).
	DefaultThinking string

	// Timeout is the default request timeout in seconds.
	// Zero means use the global default (120s). Increased for
	// models that take longer to reason (e.g. deepseek-v4-pro).
	Timeout int

	// MaxContext is the model's maximum context window in tokens.
	// The loop engine automatically trims conversation history when
	// estimated tokens approach this limit. Zero means no limit
	// enforcement (unknown or effectively unlimited models).
	MaxContext int
}

// KnownProfiles lists all built-in model profiles. Each entry is matched
// by longest prefix — "deepseek-v4-flash" matches before "deepseek-" would.
// Add new profiles here; the rest of odek consumes them automatically.
var KnownProfiles = []struct {
	Prefix  string
	Profile ModelProfile
}{
	{
		Prefix: "deepseek-v4-pro",
		Profile: ModelProfile{
			Label:           "DeepSeek v4 Pro",
			DefaultThinking: "enabled", // full reasoning enabled by default
			Timeout:         180,       // may take longer to think
			MaxContext:      1_000_000, // 1M token context window
		},
	},
	{
		Prefix: "deepseek-v4-flash",
		Profile: ModelProfile{
			Label:           "DeepSeek v4 Flash",
			DefaultThinking: "", // no extended thinking (faster / cheaper)
			Timeout:         90,
			MaxContext:      131_072, // 128K token context window
		},
	},
	{
		Prefix: "deepseek-",
		Profile: ModelProfile{
			Label:      "DeepSeek (generic)",
			MaxContext: 131_072, // 128K safe default for unknown DeepSeek models
		},
	},
}

// LookupProfile returns the best-matching ModelProfile for a model name,
// or nil if no profile matches. Matching uses longest prefix — a model
// named "deepseek-v4-flash-custom" would match "deepseek-v4-flash".
func LookupProfile(model string) *ModelProfile {
	var best *ModelProfile
	bestLen := 0
	for _, entry := range KnownProfiles {
		if strings.HasPrefix(model, entry.Prefix) && len(entry.Prefix) > bestLen {
			p := entry.Profile // copy (KnownProfiles entries are immutable)
			best = &p
			bestLen = len(entry.Prefix)
		}
	}
	return best
}

// ProfileLabel returns the human-readable label for a model, or the model
// name itself if no profile matches. Used in CLI headers and status output.
func ProfileLabel(model string) string {
	if p := LookupProfile(model); p != nil && p.Label != "" {
		return p.Label
	}
	return model
}

// ── Project File (AGENTS.md) ─────────────────────────────────────────

// ProjectFileName is the name of the project-level instructions file
// that odek automatically loads from the working directory.
const ProjectFileName = "AGENTS.md"

// maxProjectFileBytes caps the size of AGENTS.md that will be loaded into the
// system prompt. A maliciously huge project file could otherwise OOM the
// process at startup or bloat every prompt.
const maxProjectFileBytes = 256 * 1024 // 256 KiB

// LoadProjectFile reads ProjectFileName from the current working directory.
// Returns the file content (trimmed) if it exists and is readable.
// Returns empty string if the file doesn't exist or can't be read.
// Checks for symlinks to prevent following attacker-controlled paths.
// The content is intended to be appended to the system message with a
// clear header — use it for project conventions, architecture notes, etc.
func LoadProjectFile() string {
	// Prevent symlink attacks: stat the file first
	info, err := os.Lstat(ProjectFileName)
	if err != nil {
		return ""
	}
	// If it's a symlink, refuse to follow it
	if info.Mode()&os.ModeSymlink != 0 {
		fmt.Fprintf(os.Stderr, "odek: warning: %s is a symlink — refusing to follow for security\n", ProjectFileName)
		return ""
	}
	if info.Size() > maxProjectFileBytes {
		fmt.Fprintf(os.Stderr, "odek: warning: %s is too large (%d bytes, max %d) — ignoring\n", ProjectFileName, info.Size(), maxProjectFileBytes)
		return ""
	}
	data, err := os.ReadFile(ProjectFileName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ── Defaults ──────────────────────────────────────────────────────────

const (
	defaultBaseURL    = "https://api.deepseek.com/v1"
	defaultModel      = "deepseek-v4-flash"
	defaultMaxIter    = 90
	defaultHTTPTimout = 120 // seconds
)

// ── Constructor ───────────────────────────────────────────────────────

// New creates a new Agent with the given configuration.
//
// If Config.SandboxCleanup is set, the cleanup function is called when
// Close() is invoked. The caller is responsible for creating the sandbox
// container and wiring up tool executables to use it before calling New().
func New(cfg Config) (*Agent, error) {
	if cfg.MaxIterations <= 0 {
		cfg.MaxIterations = defaultMaxIter
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("DEEPSEEK_API_KEY")
		if cfg.APIKey == "" {
			cfg.APIKey = os.Getenv("OPENAI_API_KEY")
		}
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("odek: no API key provided (set ODEK_API_KEY, DEEPSEEK_API_KEY, or OPENAI_API_KEY)")
	}
	if cfg.Model == "" {
		cfg.Model = defaultModel
	}

	// ── Runtime Context ─────────────────────────────────────────────
	// Prepend environment awareness so the agent knows its host, cwd,
	// date/time, and platform without burning tokens on shell commands.
	// Each entry point can set RuntimeContext explicitly (CLI, Telegram,
	// WebUI); when empty, a generic terminal context is built.
	if cfg.RuntimeContext == "" {
		cfg.RuntimeContext = BuildRuntimeContext("terminal")
	}
	if cfg.SystemMessage != "" {
		cfg.SystemMessage = cfg.RuntimeContext + "\n\n" + cfg.SystemMessage
	} else {
		cfg.SystemMessage = cfg.RuntimeContext
	}

	// Apply model profile defaults (only when user hasn't explicitly set them)
	if profile := LookupProfile(cfg.Model); profile != nil {
		if cfg.Thinking == "" && profile.DefaultThinking != "" {
			cfg.Thinking = profile.DefaultThinking
		}
	}

	// Resolve timeout: profile > default
	timeout := defaultHTTPTimout
	if profile := LookupProfile(cfg.Model); profile != nil && profile.Timeout > 0 {
		timeout = profile.Timeout
	}

	// Resolve max context: discovered API values > profile > 0 (no limit)
	maxContext := 0
	// Priority 1: dynamic discovery via GET /models endpoint
	if discovered := llm.DiscoverModelContext(cfg.BaseURL, cfg.APIKey, cfg.Model); discovered > 0 {
		maxContext = discovered
	}
	// Priority 2: static profile fallback (only if discovery returned nothing)
	if maxContext == 0 {
		if profile := LookupProfile(cfg.Model); profile != nil && profile.MaxContext > 0 {
			maxContext = profile.MaxContext
		}
	}
	if maxContext > 0 {
		log.Printf("odek: model %q context window: %d tokens", cfg.Model, maxContext)
	}

	// Build tool registry from external Tool interface
	tools := make([]tool.Tool, len(cfg.Tools))
	for i, t := range cfg.Tools {
		tools[i] = &toolAdapter{t}
	}

	// Load AGENTS.md from the working directory and append to system message.
	// Content is scanned for prompt injection before being trusted.
	if !cfg.NoProjectFile {
		if projectContent := LoadProjectFile(); projectContent != "" {
			if err := guard.ScanContentWithScope(context.Background(), projectContent, cfg.Guard, &cfg.GuardConfig, "system_prompt"); err != nil {
				log.Printf("skipping AGENTS.md: guard rejected: %v", err)
			} else if cfg.SystemMessage != "" {
				cfg.SystemMessage += "\n\n# Project Instructions\n\n" + projectContent
			} else {
				cfg.SystemMessage = "# Project Instructions\n\n" + projectContent
			}
		}
	}

	client := llm.New(cfg.BaseURL, cfg.APIKey, cfg.Model, cfg.Thinking, cfg.ThinkingBudget, time.Duration(timeout)*time.Second)
	if cfg.Temperature >= 0 {
		client.Temperature = cfg.Temperature
	}

	// Load skills and inject auto-load skills into system message
	var sm *skills.SkillManager
	if cfg.Skills != nil {
		sm = cfg.SkillManager
		if sm == nil {
			sm = skills.NewSkillManager(
				expandHome("~/.odek/skills"),
				"./.odek/skills",
			)
		}

		// Build a MultiNotifier from SkillEventHandler + Renderer (if set)
		var notifiers []skills.SkillNotifier
		if cfg.SkillEventHandler != nil {
			notifiers = append(notifiers, &skillEventHandlerAdapter{fn: cfg.SkillEventHandler})
		}
		if cfg.Renderer != nil {
			notifiers = append(notifiers, &renderNotifier{r: cfg.Renderer})
		}
		if len(notifiers) > 0 {
			sm.SetNotifier(skills.NewMultiNotifier(notifiers...))
		}

		// Install the shared guard so skill loading and saving are scanned
		// when the skills scan scope is enabled.
		sm.SetGuard(cfg.Guard, cfg.GuardConfig)

		// Append auto-load skills to system message
		var skillContext string
		var autoLoadNames []string
		count := 0
		for _, s := range sm.Result.AutoLoad {
			if count >= cfg.Skills.MaxAutoLoad {
				break
			}
			skillContext += "\n\n" + skills.FormatAsContext(s)
			autoLoadNames = append(autoLoadNames, s.Name)
			count++
		}
		if skillContext != "" {
			cfg.SystemMessage += "\n\n# Loaded Skills\n\n" + skillContext
		}

		// Fire autoloaded event
		if len(autoLoadNames) > 0 {
			sm.Notifier.Notify(skills.SkillEvent{
				Type:      "autoloaded",
				Skills:    autoLoadNames,
				Timestamp: time.Now().UTC(),
			})
		}
	}

	// Create memory manager
	memoryDir := cfg.MemoryDir
	if memoryDir == "" {
		memoryDir = expandHome("~/.odek/memory")
	}
	memoryManager := memory.NewMemoryManager(memoryDir, client, cfg.MemoryConfig)

	// Resolve a dedicated LLM for Extended Memory. Falls back to the main agent
	// LLM when not configured; warns if the main model has thinking enabled
	// because reasoning tokens are wasted on memory-only calls.
	var memoryLLM extended.LLMClient = client
	if cfg.MemoryConfig.Extended != nil {
		memoryLLM = extended.ResolveLLM(*cfg.MemoryConfig.Extended, client, cfg.Thinking)
	}
	memoryManager.InitExtended(memoryLLM, memoryDir)
	memoryManager.SetGuard(cfg.Guard, cfg.GuardConfig)

	// Wire memory lifecycle observability: fan out events to the programmatic
	// handler (WebUI/Telegram/embedders) and the terminal renderer. Mirrors the
	// skills notifier pattern so memory activity is no longer silent.
	var memNotifiers []memory.MemoryNotifier
	if cfg.MemoryEventHandler != nil {
		memNotifiers = append(memNotifiers, &memoryEventHandlerAdapter{fn: cfg.MemoryEventHandler})
	}
	if cfg.Renderer != nil {
		memNotifiers = append(memNotifiers, &memoryRenderNotifier{r: cfg.Renderer})
	}
	if len(memNotifiers) > 0 {
		memoryManager.SetNotifier(memory.NewMultiMemoryNotifier(memNotifiers...))
	}

	agent := &Agent{
		config:        cfg,
		skillManager:  sm,
		memoryManager: memoryManager,
	}

	// Wire per-turn memory injection so the agent sees the latest facts
	// and the loop engine refreshes it before each LLM call.
	// (Memory is injected per-turn via SetMemoryPromptFunc below.)

	// Append memory tool to registry unless the filter excludes it.
	if shouldRegisterTool("memory", cfg.ToolFilter) {
		tools = append(tools, &toolAdapter{memory.NewMemoryTool(memoryManager)})
	}
	registry := tool.NewRegistry(tools)

	engine := loop.New(client, registry, cfg.MaxIterations, cfg.SystemMessage, cfg.Renderer, maxContext)
	engine.PromptCaching = cfg.PromptCaching
	engine.SetUntrustedWrapper(cfg.UntrustedWrapper)
	if cfg.MaxToolParallel > 0 {
		engine.SetMaxToolParallel(cfg.MaxToolParallel)
	}
	if cfg.Approver != nil {
		engine.SetApprover(cfg.Approver)
	}
	if cfg.DangerousConfig != nil {
		engine.SetDangerousConfig(cfg.DangerousConfig)
	}

	// Set skill verbosity: condensed by default, full banners when verbose.
	if cfg.Skills != nil {
		engine.SetSkillVerbose(cfg.Skills.Verbose)
	}

	// Set per-turn memory refresh callback
	engine.SetMemoryPromptFunc(func() string {
		return memoryManager.BuildSystemPrompt()
	})

	// Set the skill loader for lazy loading. MatchLazySkills prefers semantic
	// matching when an HTTP embedding backend is configured (time-bounded, with
	// keyword fallback), otherwise uses the keyword ScoredMatcher.
	if sm != nil && cfg.Skills != nil && cfg.Skills.MaxLazySlots > 0 {
		maxSlots := cfg.Skills.MaxLazySlots

		engine.SetSkillLoader(func(userInput string) string {
			matched := sm.MatchLazySkills(userInput, maxSlots)
			if len(matched) == 0 {
				return ""
			}
			var context string
			names := make([]string, 0, len(matched))
			for _, sk := range matched {
				sm.RecordUsage(sk.Name)
				context += "\n" + skills.FormatAsContext(sk)
				names = append(names, sk.Name)
			}

			// Fire loaded event
			sm.Notifier.Notify(skills.SkillEvent{
				Type:      "loaded",
				Skills:    names,
				Timestamp: time.Now().UTC(),
			})

			return context
		})
	}

	// Wire tool event handler for live streaming
	if cfg.ToolEventHandler != nil {
		engine.SetToolEventHandler(cfg.ToolEventHandler)
	}

	// Wire agent-loop signal observability (context trim, tool recovery): fan
	// out to the programmatic handler and the terminal renderer.
	if cfg.AgentSignalHandler != nil || cfg.Renderer != nil {
		handler := cfg.AgentSignalHandler
		renderer := cfg.Renderer
		engine.SetSignalHandler(func(ev loop.SignalEvent) {
			if handler != nil {
				handler(ev)
			}
			if renderer != nil {
				switch ev.Type {
				case "context_trimmed":
					renderer.ContextTrimmed(ev.Detail, ev.Count)
				case "tool_recovery":
					renderer.ToolRecovery(ev.Tool, ev.Detail)
				}
			}
		})
	}

	// Wire iteration callback for progress reporting
	if cfg.IterationCallback != nil {
		engine.SetIterationCallback(cfg.IterationCallback)
	}

	// Wire narrator for engaging/enhance interaction modes.
	// In verbose mode, narrator stays nil → existing renderer behavior.
	// In "off" mode, narrator stays nil and render output is suppressed.
	if cfg.InteractionMode == "" || cfg.InteractionMode == "engaging" || cfg.InteractionMode == "enhance" {
		engine.SetNarrator(narrate.New(true))
	}

	// Wire interaction mode to the engine for render gating
	engine.SetInteractionMode(cfg.InteractionMode)

	// Wire per-turn episode search — searches past session episodes
	// using the user's message as a query, then injects relevant summaries.
	// Uses recency-based ranking (no LLM) to avoid recursion in the loop.
	// Only active when memory is enabled.
	engine.SetEpisodeContextFunc(func(userInput string) string {
		return memoryManager.FormatEpisodeContext(userInput)
	})

	// Wire per-turn Extended Memory search. Injected after the legacy memory
	// prompt block so recent facts/buffer take precedence.
	engine.SetExtendedMemoryContextFunc(func(ctx context.Context, userInput string) string {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return memoryManager.FormatExtendedContext(ctx, userInput)
	})

	// Notify memory manager when a new user message arrives so Extended Memory
	// can extract atomic facts/preferences.
	engine.SetUserMessageHandler(func(ctx context.Context, msg string) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		memoryManager.OnUserMessageLoop(ctx, msg)
	})

	agent.engine = engine
	agent.registry = registry
	agent.sandboxCleanup = cfg.SandboxCleanup
	return agent, nil
}

// Run executes the agent loop for the given task and returns the final answer.
func (a *Agent) Run(ctx context.Context, task string) (string, error) {
	return a.engine.Run(ctx, task)
}

// RunWithMessages executes the agent loop starting from a pre-built
// message history. Use this for multi-turn conversations where the
// full conversation context (system prompt, prior turns) has been
// loaded from a session file and the new user message appended.
//
// Returns the final answer plus the complete updated message history.
// The caller should persist the history (e.g. to a session file) so
// the conversation can be continued in a future call.
func (a *Agent) RunWithMessages(ctx context.Context, messages []llm.Message) (string, []llm.Message, error) {
	return a.engine.RunWithMessages(ctx, messages)
}

// TotalInputTokens returns the cumulative prompt tokens consumed across all
// iterations of the most recent RunWithMessages call.
func (a *Agent) TotalInputTokens() int {
	return a.engine.TotalInputTokens
}

// TotalOutputTokens returns the cumulative completion tokens generated
// across all iterations of the most recent RunWithMessages call.
func (a *Agent) TotalOutputTokens() int {
	return a.engine.TotalOutputTokens
}

// TotalCacheCreationTokens returns the cumulative Anthropic cache creation
// tokens across all iterations of the most recent run.
func (a *Agent) TotalCacheCreationTokens() int {
	return a.engine.TotalCacheCreationTokens
}

// TotalCacheReadTokens returns the cumulative Anthropic cache read tokens
// across all iterations of the most recent run.
func (a *Agent) TotalCacheReadTokens() int {
	return a.engine.TotalCacheReadTokens
}

// TotalCachedTokens returns the cumulative OpenAI cached prompt tokens
// across all iterations of the most recent run.
func (a *Agent) TotalCachedTokens() int {
	return a.engine.TotalCachedTokens
}

// Close cleans up resources. If a sandbox container was created, it is
// destroyed. Always call Close() when done with the agent.
func (a *Agent) Close() error {
	if a.sandboxCleanup != nil {
		return a.sandboxCleanup()
	}
	return nil
}

// Memory returns the agent's memory manager. Used by the CLI layer to
// append buffer entries after each turn and signal session end.
// Returns nil if memory is disabled.
func (a *Agent) Memory() *memory.MemoryManager {
	if a == nil {
		return nil
	}
	return a.memoryManager
}

// SkillManager returns the agent's skill manager. Used by the CLI,
// WebUI, and Telegram layers to run learning heuristics after agent
// completion. Returns nil if skills are disabled.
func (a *Agent) SkillManager() *skills.SkillManager {
	if a == nil {
		return nil
	}
	return a.skillManager
}

// SwitchModel updates the LLM model used by this agent at runtime.
// The model string must be a valid OpenAI-compatible model identifier.
// This is safe to call between RunWithMessages calls to switch models
// mid-session. Empty strings are silently ignored.
func (a *Agent) SwitchModel(model string) {
	if a == nil || model == "" {
		return
	}
	a.config.Model = model
	if a.engine != nil {
		a.engine.SetModel(model)
	}
}

// SwitchThinking updates the reasoning/thinking mode used by this agent at
// runtime. Accepts the same values as Config.Thinking: "enabled",
// "disabled", "low", "medium", "high", or "" (provider default / off).
// Safe to call between RunWithMessages calls to toggle thinking per-query.
func (a *Agent) SwitchThinking(thinking string) {
	if a == nil {
		return
	}
	a.config.Thinking = thinking
	if a.engine != nil {
		a.engine.SetThinking(thinking)
	}
}

// shouldRegisterTool reports whether a built-in tool name should be registered
// given a ToolFilterConfig. If Enabled is non-nil, the name must be present.
// The name must not be present in Disabled.
func shouldRegisterTool(name string, filter ToolFilterConfig) bool {
	if filter.Enabled != nil {
		found := false
		for _, n := range filter.Enabled {
			if n == name {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, n := range filter.Disabled {
		if n == name {
			return false
		}
	}
	return true
}

// expandHome replaces the leading ~/ with the user's home directory.
func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return strings.Replace(path, "~/", home+"/", 1)
		}
	}
	return path
}

// toolAdapter bridges odek.Tool to internal/tool.Tool.
type toolAdapter struct {
	t Tool
}

func (a *toolAdapter) Name() string        { return a.t.Name() }
func (a *toolAdapter) Description() string { return a.t.Description() }
func (a *toolAdapter) Schema() any         { return a.t.Schema() }
func (a *toolAdapter) Call(args string) (string, error) {
	return a.t.Call(args)
}

// SetContext propagates the agent context to tools that implement the
// context-aware interface. This lets odek.Tool implementations receive the
// per-run context (including the audit ingest recorder) without changing the
// public Tool interface.
func (a *toolAdapter) SetContext(ctx context.Context) {
	if ct, ok := a.t.(interface{ SetContext(context.Context) }); ok {
		ct.SetContext(ctx)
	}
}

// ── Skill Event Adapters ──────────────────────────────────────────────

// skillEventHandlerAdapter bridges Config.SkillEventHandler to skills.SkillNotifier.
type skillEventHandlerAdapter struct {
	fn func(event skills.SkillEvent)
}

func (a *skillEventHandlerAdapter) Notify(event skills.SkillEvent) {
	if a.fn != nil {
		a.fn(event)
	}
}

// renderNotifier bridges *render.Renderer to skills.SkillNotifier.
type renderNotifier struct {
	r *render.Renderer
}

func (n *renderNotifier) Notify(event skills.SkillEvent) {
	switch event.Type {
	case "loaded":
		n.r.SkillLoaded(event.Skills)
	case "autoloaded":
		n.r.SkillAutoLoaded(event.Skills)
	case "suggested":
		n.r.SkillSuggested(event.SkillName, event.Heuristic)
	case "saved":
		n.r.SkillSaved(event.SkillName)
	case "deleted":
		n.r.SkillDeleted(event.SkillName)
	}
}

// ── Memory Event Adapters ─────────────────────────────────────────────

// memoryEventHandlerAdapter bridges Config.MemoryEventHandler to
// memory.MemoryNotifier.
type memoryEventHandlerAdapter struct {
	fn func(event memory.MemoryEvent)
}

func (a *memoryEventHandlerAdapter) Notify(event memory.MemoryEvent) {
	if a.fn != nil {
		a.fn(event)
	}
}

// memoryRenderNotifier bridges *render.Renderer to memory.MemoryNotifier,
// translating each memory lifecycle event into the matching renderer call.
type memoryRenderNotifier struct {
	r *render.Renderer
}

func (n *memoryRenderNotifier) Notify(event memory.MemoryEvent) {
	switch event.Type {
	case "fact_added":
		n.r.MemoryFact("added", event.Target, event.Content)
	case "fact_merged":
		n.r.MemoryFact("merged", event.Target, event.Content)
	case "fact_replaced":
		n.r.MemoryFact("replaced", event.Target, event.Content)
	case "fact_removed":
		n.r.MemoryFact("removed", event.Target, event.Content)
	case "fact_consolidated":
		n.r.MemoryConsolidated(event.Target, event.Count, event.NewCount)
	case "episode_stored":
		detail := event.SessionID
		if event.Untrusted {
			detail += " (untrusted)"
		}
		n.r.MemoryEpisode("stored", detail)
	case "episode_deduped":
		n.r.MemoryEpisode("deduped", event.SessionID)
	case "episode_evicted":
		n.r.MemoryEpisode("evicted", fmt.Sprintf("%d episode(s)", event.Count))
	case "episode_promoted":
		n.r.MemoryEpisode("promoted", event.SessionID)
	case "episode_pending_review":
		n.r.MemoryEpisode("pending_review", event.SessionID)
	}
}
