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

import "github.com/BackendStack21/odek/internal/agent"

// Public types retain their identity and methods through aliases. Runtime
// implementation and tests live together in internal/agent.
type (
	Tool                 = agent.Tool
	LoggingOptions       = agent.LoggingOptions
	Config               = agent.Config
	Agent                = agent.Agent
	ToolFilterConfig     = agent.ToolFilterConfig
	BudgetUsage          = agent.BudgetUsage
	CallMetrics          = agent.CallMetrics
	PartialResponseError = agent.PartialResponseError
	ToolEffects          = agent.ToolEffects
	ToolDetailEvent      = agent.ToolDetailEvent
)

// ProjectFileName is the project instruction filename.
const ProjectFileName = agent.ProjectFileName

// SecurityPillar is the invariant runtime policy composed into every identity.
const SecurityPillar = agent.SecurityPillar

// New constructs an agent with the supplied configuration.
func New(cfg Config) (*Agent, error) { return agent.New(cfg) }

// ProfileLabel returns the display label for a model.
func ProfileLabel(model string) string { return agent.ProfileLabel(model) }

// LoadProjectFile loads and validates the current project's instructions.
func LoadProjectFile() string { return agent.LoadProjectFile() }

// BuildRuntimeContext returns environment awareness for a transport.
func BuildRuntimeContext(platform string) string { return agent.BuildRuntimeContext(platform) }

// ComposeSecureSystem composes the invariant policy with an identity.
func ComposeSecureSystem(identity string) string { return agent.ComposeSecureSystem(identity) }

// DefaultUntrustedWrapper wraps externally sourced content for the model.
func DefaultUntrustedWrapper(source, content string) string {
	return agent.DefaultUntrustedWrapper(source, content)
}
