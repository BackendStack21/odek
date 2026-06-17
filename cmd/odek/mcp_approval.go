package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/mcpclient"
	"golang.org/x/term"
)

// mcpApprovalsFile is the persistent store for user-approved project-level MCP
// servers. It lives next to config.json under ~/.odek and is created 0600.
const mcpApprovalsFile = "mcp_approvals.json"

// mcpApprovalEnv returns true if the user has opted in globally via the
// ODEK_APPROVE_MCP environment variable.
func mcpApprovalEnv() bool {
	return os.Getenv("ODEK_APPROVE_MCP") == "1"
}

// approveMCPServers requires explicit user approval for any MCP servers that
// were introduced by the project-level ./odek.json config. Global servers from
// ~/.odek/config.json are considered operator-trusted and do not require
// approval.
//
// Approval can be granted in three ways:
//   1. Set ODEK_APPROVE_MCP=1 (useful for CI/non-interactive use).
//   2. Answer the interactive y/N prompt when running on a TTY.
//   3. A prior approval for the same project/server/command/args fingerprint is
//      persisted in ~/.odek/mcp_approvals.json.
//
// If approval is required and cannot be obtained, approveMCPServers returns an
// error and the command should abort before spawning any MCP subprocess.
func approveMCPServers(resolved config.ResolvedConfig, stdin io.Reader, stdout io.Writer) error {
	isTTY := stdin == os.Stdin && term.IsTerminal(int(os.Stdin.Fd()))
	return approveMCPServersWithTTY(resolved, stdin, stdout, isTTY)
}

// approveMCPServersWithTTY is the testable core of approveMCPServers. The tty
// argument tells the function whether it may prompt interactively.
func approveMCPServersWithTTY(resolved config.ResolvedConfig, stdin io.Reader, stdout io.Writer, tty bool) error {
	if len(resolved.ProjectMCPServerNames) == 0 {
		return nil
	}

	if mcpApprovalEnv() {
		return nil
	}

	projectDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("mcp approval: get working directory: %w", err)
	}
	projectDir, err = filepath.Abs(projectDir)
	if err != nil {
		return fmt.Errorf("mcp approval: abs working directory: %w", err)
	}

	approved, err := loadMCPApprovals()
	if err != nil {
		return fmt.Errorf("mcp approval: load approvals: %w", err)
	}

	reader := bufio.NewReader(stdin)

	for _, name := range resolved.ProjectMCPServerNames {
		cfg, ok := resolved.MCPServers[name]
		if !ok {
			continue
		}

		key := mcpApprovalKey(projectDir, name, cfg)
		if approved[key] {
			continue
		}

		if !tty {
			return fmt.Errorf(
				"project-level MCP server %q (%s %q) requires explicit approval\n"+
					"set ODEK_APPROVE_MCP=1 to approve all project MCP servers, or run interactively",
				name, cfg.Command, strings.Join(cfg.Args, " "),
			)
		}

		fmt.Fprintf(stdout, "\nProject-level MCP server %q wants to run:\n", name)
		fmt.Fprintf(stdout, "  command: %s\n", cfg.Command)
		if len(cfg.Args) > 0 {
			fmt.Fprintf(stdout, "  args:    %s\n", strings.Join(cfg.Args, " "))
		}
		if len(cfg.Env) > 0 {
			envKeys := make([]string, 0, len(cfg.Env))
			for k := range cfg.Env {
				envKeys = append(envKeys, k)
			}
			sort.Strings(envKeys)
			fmt.Fprintf(stdout, "  env:     %s\n", strings.Join(envKeys, ", "))
		}
		fmt.Fprintf(stdout, "Approve? [y/N] ")

		line, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("mcp approval: read prompt: %w", err)
		}
		line = strings.ToLower(strings.TrimSpace(line))
		if line != "y" && line != "yes" {
			return fmt.Errorf("mcp approval: server %q was not approved", name)
		}

		approved[key] = true
		if err := saveMCPApprovals(approved); err != nil {
			return fmt.Errorf("mcp approval: save approvals: %w", err)
		}
	}

	return nil
}

// mcpApprovalKey returns a stable key for the persisted approval store. It
// includes the project directory, server name, command, and arguments so a
// change to any of those invalidates the prior approval.
func mcpApprovalKey(projectDir, name string, cfg mcpclient.ServerConfig) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s", projectDir, name, cfg.Command)
	for _, a := range cfg.Args {
		fmt.Fprintf(h, "\x00%s", a)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// loadMCPApprovals reads the persisted approval map. A missing file is treated
// as an empty approval set.
func loadMCPApprovals() (map[string]bool, error) {
	path := filepath.Join(expandHome("~/.odek"), mcpApprovalsFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]bool), nil
		}
		return nil, err
	}

	var approvals map[string]bool
	if err := json.Unmarshal(data, &approvals); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if approvals == nil {
		approvals = make(map[string]bool)
	}
	return approvals, nil
}

// saveMCPApprovals writes the approval map to disk with 0600 permissions.
func saveMCPApprovals(approvals map[string]bool) error {
	dir := expandHome("~/.odek")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, mcpApprovalsFile)
	data, err := json.MarshalIndent(approvals, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// mcpToolApprovalsFile is the persistent store for user-approved MCP tools.
const mcpToolApprovalsFile = "mcp_tool_approvals.json"

// approveMCPTools requires explicit user approval for each tool an MCP server
// advertises. Project-level servers must already have passed approveMCPServers
// before discovery; this layer asks about each individual tool so a server
// cannot silently register a spoofed or unwanted tool.
//
// Approval can be granted via ODEK_APPROVE_MCP=1, an interactive y/N prompt,
// or a prior persisted approval in ~/.odek/mcp_tool_approvals.json.
func approveMCPTools(projectDir, serverName string, cfg mcpclient.ServerConfig, defs []mcpclient.ToolDef, stdin io.Reader, stdout io.Writer) ([]mcpclient.ToolDef, error) {
	isTTY := stdin == os.Stdin && term.IsTerminal(int(os.Stdin.Fd()))
	return approveMCPToolsWithTTY(projectDir, serverName, cfg, defs, stdin, stdout, isTTY)
}

// approveMCPToolsWithTTY is the testable core.
func approveMCPToolsWithTTY(projectDir, serverName string, cfg mcpclient.ServerConfig, defs []mcpclient.ToolDef, stdin io.Reader, stdout io.Writer, tty bool) ([]mcpclient.ToolDef, error) {
	if len(defs) == 0 {
		return nil, nil
	}

	if mcpApprovalEnv() {
		return defs, nil
	}

	approved, err := loadMCPToolApprovals()
	if err != nil {
		return nil, fmt.Errorf("mcp tool approval: load approvals: %w", err)
	}

	reader := bufio.NewReader(stdin)
	var out []mcpclient.ToolDef

	for _, def := range defs {
		key := mcpToolApprovalKey(projectDir, serverName, def.Name, cfg)
		if approved[key] {
			out = append(out, def)
			continue
		}

		if !tty {
			return nil, fmt.Errorf(
				"MCP tool %q from server %q requires explicit approval\n"+
					"set ODEK_APPROVE_MCP=1 to approve all tools from all project MCP servers, or run interactively",
				def.Name, serverName,
			)
		}

		fmt.Fprintf(stdout, "\nMCP server %q wants to register tool %q\n", serverName, def.Name)
		if def.Description != "" {
			fmt.Fprintf(stdout, "  description: %s\n", truncateDescription(def.Description, 200))
		}
		fmt.Fprintf(stdout, "Approve? [y/N] ")

		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("mcp tool approval: read prompt: %w", err)
		}
		line = strings.ToLower(strings.TrimSpace(line))
		if line != "y" && line != "yes" {
			continue // user declined this tool; skip it
		}

		approved[key] = true
		if err := saveMCPToolApprovals(approved); err != nil {
			return nil, fmt.Errorf("mcp tool approval: save approvals: %w", err)
		}
		out = append(out, def)
	}

	return out, nil
}

// mcpToolApprovalKey returns a stable key for the persisted tool approval store.
func mcpToolApprovalKey(projectDir, serverName, toolName string, cfg mcpclient.ServerConfig) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s", projectDir, serverName, toolName, cfg.Command)
	for _, a := range cfg.Args {
		fmt.Fprintf(h, "\x00%s", a)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// loadMCPToolApprovals reads the persisted tool approval map.
func loadMCPToolApprovals() (map[string]bool, error) {
	path := filepath.Join(expandHome("~/.odek"), mcpToolApprovalsFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]bool), nil
		}
		return nil, err
	}

	var approvals map[string]bool
	if err := json.Unmarshal(data, &approvals); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if approvals == nil {
		approvals = make(map[string]bool)
	}
	return approvals, nil
}

// saveMCPToolApprovals writes the tool approval map to disk with 0600 permissions.
func saveMCPToolApprovals(approvals map[string]bool) error {
	dir := expandHome("~/.odek")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, mcpToolApprovalsFile)
	data, err := json.MarshalIndent(approvals, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// truncateDescription limits a tool description for the approval prompt.
func truncateDescription(desc string, max int) string {
	if len(desc) <= max {
		return desc
	}
	if max <= 3 {
		return desc[:max]
	}
	return desc[:max-3] + "..."
}
