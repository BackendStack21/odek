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

	"github.com/BackendStack21/kode/internal/llm"
	"github.com/BackendStack21/kode/internal/loop"
	"github.com/BackendStack21/kode/internal/tool"
)

// Tool represents a single capability the agent can invoke.
type Tool interface {
	Name() string
	Description() string
	Schema() any  // JSON Schema for the tool's parameters
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

	// Tools available to the agent.
	Tools []Tool

	// MaxIterations caps the number of think→act cycles (default: 90).
	MaxIterations int

	// SystemMessage is the system prompt injected at the start of every run.
	SystemMessage string
}

// Agent is the agent loop runtime.
type Agent struct {
	config   Config
	engine   *loop.Engine
	registry *tool.Registry
}

// New creates a new Agent with the given configuration.
func New(cfg Config) (*Agent, error) {
	if cfg.MaxIterations <= 0 {
		cfg.MaxIterations = 90
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.deepseek.com/v1"
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
		cfg.Model = "deepseek-chat"
	}

	// Build tool registry from external Tool interface
	tools := make([]tool.Tool, len(cfg.Tools))
	for i, t := range cfg.Tools {
		tools[i] = &toolAdapter{t}
	}

	registry := tool.NewRegistry(tools)
	client := llm.New(cfg.BaseURL, cfg.APIKey, cfg.Model)
	engine := loop.New(client, registry, cfg.MaxIterations, cfg.SystemMessage)

	return &Agent{
		config:   cfg,
		engine:   engine,
		registry: registry,
	}, nil
}

// Run executes the agent loop for the given task and returns the final answer.
func (a *Agent) Run(ctx context.Context, task string) (string, error) {
	return a.engine.Run(ctx, task)
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
