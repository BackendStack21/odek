package main

import (
	"context"
	"fmt"
	"os"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/mcp"
	"github.com/BackendStack21/odek/internal/skills"
)

// ── MCP Command ────────────────────────────────────────────────────────

// mcpCmd starts odek as an MCP server over stdio.
// This allows MCP clients (Claude Code, Cursor, etc.) to use odek's tools.
//
// Usage:
//
//	odek mcp [--sandbox]
//
// The server reads JSON-RPC 2.0 requests from stdin and writes responses
// to stdout. Stderr is used for logging. The server exposes all odek
// built-in tools (shell, read_file, write_file, search_files, patch,
// browser) via the tools/list and tools/call MCP methods.
func mcpCmd(args []string) error {
	// Parse CLI flags
	cliFlags := config.CLIFlags{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--sandbox":
			cliFlags.Sandbox = boolPtr(true)
		case "--help", "-h":
			fmt.Println(`Usage: odek mcp [flags]

Start odek as an MCP server over stdio.

odek exposes all its built-in tools (shell, read/write files, search,
patch, browser) via the Model Context Protocol. Connect any MCP client
(Claude Code, Cursor, etc.) to use odek's tools.

Flags:
  --sandbox    Run shell commands inside Docker sandbox
  --help, -h   Show this help`)
			return nil
		default:
			return fmt.Errorf("unknown flag %q for mcp", args[i])
		}
	}

	// Load config
	resolved := config.LoadConfig(cliFlags)

	// Start agent loop (mcp)
	sbCfg := sandboxConfig{
		Image:    resolved.SandboxImage,
		Network:  resolved.SandboxNetwork,
		Readonly: resolved.SandboxReadonly,
		Memory:   resolved.SandboxMemory,
		CPUs:     resolved.SandboxCPUs,
		User:     resolved.SandboxUser,
		Env:      resolved.SandboxEnv,
		Volumes:  resolved.SandboxVolumes,
	}

	// Build skills manager (for skill tools)
	var sm *skills.SkillManager
	if resolved.Skills.Learn {
		sm = skills.NewSkillManagerWithEmbedding(
			expandHome("~/.odek/skills"),
			"./.odek/skills",
			resolved.Skills.Embedding,
		)
	}

	// Build tools
	toolSet := builtinTools(resolved.Dangerous, sm, nil, resolved.MaxConcurrency, resolved.APIKey, toolConfig{WebSearch: resolved.WebSearch}, nil)

	// MCP server tools — connect and discover before sandbox
	var mcpCleanup func()
	if len(resolved.MCPServers) > 0 {
		cl, err := loadMCPTools(resolved.MCPServers, &toolSet)
		if err != nil {
			return fmt.Errorf("mcp: %w", err)
		}
		mcpCleanup = cl
		defer mcpCleanup()
	}

	// Sandbox setup (must happen after tools are created)
	var sandboxCleanup func() error
	if resolved.Sandbox {
		var mcpContainerName string
		mcpContainerName, cleanup, err := setupSandbox(toolSet, sbCfg)
		if err != nil {
			return fmt.Errorf("setup sandbox: %w", err)
		}
		_ = mcpContainerName
		sandboxCleanup = cleanup
		defer sandboxCleanup()
	}

	// Convert to MCP NativeTool slice
	var callers []mcp.ToolCaller
	for _, t := range toolSet {
		callers = append(callers, t)
	}
	nativeTools := mcp.BuildNativeTools(callers)

	// Create and run the MCP server
	version := getVersion()
	server := mcp.NewServer(version, nativeTools, os.Stdin, os.Stdout)
	return server.Run(context.Background())
}
