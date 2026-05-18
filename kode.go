// Package kode is a minimal, zero-dependency Go agent loop runtime.
//
// kode implements the ReAct (Reasoning + Acting) pattern — the "think,
// therefore act" loop that powers autonomous AI agents. It is not a
// framework or an SDK. It is a runtime: one loop, one binary, zero deps.
//
// # Design
//
//   - Zero external dependencies. stdlib only.
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
package kode

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BackendStack21/kode/internal/llm"
	"github.com/BackendStack21/kode/internal/loop"
	"github.com/BackendStack21/kode/internal/render"
	"github.com/BackendStack21/kode/internal/tool"
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

	// Tools available to the agent.
	Tools []Tool

	// MaxIterations caps the number of think→act cycles (default: 90).
	MaxIterations int

	// SystemMessage is the system prompt injected at the start of every run.
	// If AGENTS.md exists in the working directory, its content is appended
	// automatically. Set NoProjectFile to true to skip this.
	SystemMessage string

	// NoProjectFile disables automatic loading of AGENTS.md from the
	// working directory. By default, kode reads AGENTS.md and appends
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
}

// Agent is the agent loop runtime.
type Agent struct {
	config         Config
	engine         *loop.Engine
	registry       *tool.Registry
	sandboxCleanup func() error // destroys the sandbox container on Close()
}

// ── Model Profiles ────────────────────────────────────────────────────
//
// A ModelProfile overrides default settings for a particular model or
// model family. Profiles are matched by longest model-name prefix.
//
// To add support for a new model, append an entry to KnownProfiles with
// the model prefix, a human-readable label, and any defaults (thinking,
// timeout). The rest of kode picks it up automatically — no changes to
// the LLM client, loop engine, or CLI parsing needed.

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
// Add new profiles here; the rest of kode consumes them automatically.
var KnownProfiles = []struct {
	Prefix  string
	Profile ModelProfile
}{
	{
		Prefix: "deepseek-v4-pro",
		Profile: ModelProfile{
			Label:           "DeepSeek v4 Pro",
			DefaultThinking: "enabled", // full reasoning enabled by default
			Timeout:         180,        // may take longer to think
			MaxContext:      1_000_000,  // 1M token context window
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
			Label:     "DeepSeek (generic)",
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
// that kode automatically loads from the working directory.
const ProjectFileName = "AGENTS.md"

// LoadProjectFile reads ProjectFileName from the current working directory.
// Returns the file content (trimmed) if it exists and is readable.
// Returns empty string if the file doesn't exist or can't be read.
// The content is intended to be appended to the system message with a
// clear header — use it for project conventions, architecture notes, etc.
func LoadProjectFile() string {
	data, err := os.ReadFile(ProjectFileName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ── Defaults ──────────────────────────────────────────────────────────

const (
	defaultBaseURL    = "https://api.deepseek.com/v1"
	defaultModel      = "deepseek-chat"
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
		return nil, fmt.Errorf("kode: no API key provided (set DEEPSEEK_API_KEY or OPENAI_API_KEY)")
	}
	if cfg.Model == "" {
		cfg.Model = defaultModel
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

	// Resolve max context: profile value (0 = no limit)
	maxContext := 0
	if profile := LookupProfile(cfg.Model); profile != nil && profile.MaxContext > 0 {
		maxContext = profile.MaxContext
	}

	// Build tool registry from external Tool interface
	tools := make([]tool.Tool, len(cfg.Tools))
	for i, t := range cfg.Tools {
		tools[i] = &toolAdapter{t}
	}

	// Load AGENTS.md from the working directory and append to system message
	if !cfg.NoProjectFile {
		if projectContent := LoadProjectFile(); projectContent != "" {
			if cfg.SystemMessage != "" {
				cfg.SystemMessage += "\n\n# Project Instructions\n\n" + projectContent
			} else {
				cfg.SystemMessage = "# Project Instructions\n\n" + projectContent
			}
		}
	}

	registry := tool.NewRegistry(tools)
	client := llm.New(cfg.BaseURL, cfg.APIKey, cfg.Model, cfg.Thinking, time.Duration(timeout)*time.Second)
	engine := loop.New(client, registry, cfg.MaxIterations, cfg.SystemMessage, cfg.Renderer, maxContext)

	return &Agent{
		config:         cfg,
		engine:         engine,
		registry:       registry,
		sandboxCleanup: cfg.SandboxCleanup,
	}, nil
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

// Close cleans up resources. If a sandbox container was created, it is
// destroyed. Always call Close() when done with the agent.
func (a *Agent) Close() error {
	if a.sandboxCleanup != nil {
		return a.sandboxCleanup()
	}
	return nil
}

// toolAdapter bridges kode.Tool to internal/tool.Tool.
type toolAdapter struct {
	t Tool
}

func (a *toolAdapter) Name() string        { return a.t.Name() }
func (a *toolAdapter) Description() string { return a.t.Description() }
func (a *toolAdapter) Schema() any         { return a.t.Schema() }
func (a *toolAdapter) Call(args string) (string, error) {
	return a.t.Call(args)
}
