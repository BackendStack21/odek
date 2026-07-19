package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/guard"
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
//  1. Set ODEK_APPROVE_MCP=1 (useful for CI/non-interactive use).
//  2. Answer the interactive y/N prompt when running on a TTY.
//  3. A prior approval for the same project/server/command/args fingerprint is
//     persisted in ~/.odek/mcp_approvals.json.
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
			envKeys := sortedEnvKeys(cfg.Env)
			fmt.Fprintf(stdout, "  env:\n")
			for _, k := range envKeys {
				fmt.Fprintf(stdout, "    %s=%s\n", k, cfg.Env[k])
			}
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
// includes the project directory, server name, command, arguments, and env
// overrides so a change to any of those invalidates the prior approval.
func mcpApprovalKey(projectDir, name string, cfg mcpclient.ServerConfig) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s", projectDir, name, cfg.Command)
	for _, a := range cfg.Args {
		fmt.Fprintf(h, "\x00%s", a)
	}
	hashEnv(h, cfg.Env)
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
func approveMCPTools(projectDir, serverName string, cfg mcpclient.ServerConfig, defs []mcpclient.ToolDef, stdin io.Reader, stdout io.Writer, g guard.Guard, guardCfg guard.Config) ([]mcpclient.ToolDef, error) {
	isTTY := stdin == os.Stdin && term.IsTerminal(int(os.Stdin.Fd()))
	return approveMCPToolsWithTTY(projectDir, serverName, cfg, defs, stdin, stdout, isTTY, g, guardCfg)
}

// approveMCPToolsWithTTY is the testable core.
func approveMCPToolsWithTTY(projectDir, serverName string, cfg mcpclient.ServerConfig, defs []mcpclient.ToolDef, stdin io.Reader, stdout io.Writer, tty bool, g guard.Guard, guardCfg guard.Config) ([]mcpclient.ToolDef, error) {
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
		// Guard-scan every string in the input schema and cap its size. A
		// tainted or oversized schema is rejected before it can enter the
		// model's tool catalogue.
		if err := scanMCPSchema(def.InputSchema, serverName, def.Name, g, guardCfg); err != nil {
			fmt.Fprintf(os.Stderr, "odek: warning: %v; skipping tool %q\n", err, def.Name)
			continue
		}
		schemaHash, schemaSize, err := mcpSchemaSummary(def.InputSchema)
		if err != nil {
			fmt.Fprintf(os.Stderr, "odek: warning: mcp server %q tool %q: schema serialization failed: %v; skipping\n", serverName, def.Name, err)
			continue
		}
		if schemaSize > maxMCPSchemaBytes {
			fmt.Fprintf(os.Stderr, "odek: warning: mcp server %q tool %q: schema too large (%d bytes, max %d); skipping\n",
				serverName, def.Name, schemaSize, maxMCPSchemaBytes)
			continue
		}

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
			fmt.Fprintf(stdout, "  description: %s\n", sanitizeTerminal(truncateDescription(def.Description, 200)))
		}
		fmt.Fprintf(stdout, "  schema: sha256:%s (%d bytes)\n", schemaHash, schemaSize)
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
	hashEnv(h, cfg.Env)
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

// sortedEnvKeys returns the keys of an env map in deterministic order.
func sortedEnvKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// hashEnv writes a canonical representation of the env map into h.
// The order is deterministic and key/value pairs are separated by NUL bytes
// so that distinct key/value boundaries cannot collide.
func hashEnv(h hash.Hash, env map[string]string) {
	keys := sortedEnvKeys(env)
	for _, k := range keys {
		fmt.Fprintf(h, "\x00env\x00%s\x00%s", k, env[k])
	}
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

// sanitizeTerminal removes ANSI escape sequences and replaces other terminal
// control characters with a replacement character so a malicious MCP server
// cannot disguise an approval prompt with cursor movement or colour codes.
func sanitizeTerminal(s string) string {
	// Strip ANSI escape sequences: ESC [ ... m (and similar).
	ansi := regexp.MustCompile("\x1b\\[[0-9;]*[a-zA-Z]")
	s = ansi.ReplaceAllString(s, "")
	// Replace remaining control characters (except tab/newline) with �.
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			b.WriteRune('�')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// maxMCPSchemaBytes caps the serialized JSON schema size for a single MCP tool.
// Schemas are part of the model's tool catalogue, so an oversized schema can be
// used for prompt stuffing. Real-world MCP schemas are typically small; 256 KiB
// is generous while preventing abuse.
const maxMCPSchemaBytes = 256 * 1024

// mcpSchemaSummary returns the canonical JSON bytes for a schema and a short
// SHA-256 hash for display in approval prompts.
func mcpSchemaSummary(schema any) (hash string, size int, err error) {
	data, err := json.Marshal(schema)
	if err != nil {
		return "", 0, err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:16], len(data), nil
}

// scanMCPSchema recursively walks an MCP inputSchema and guard-scans every
// string value for injection patterns. It is intentionally strict: a single
// tainted string causes the whole schema to be rejected, because a malicious
// server can hide instructions in property descriptions, defaults, or enum
// values.
func scanMCPSchema(schema any, serverName, toolName string, g guard.Guard, cfg guard.Config) error {
	return walkMCPSchema(schema, func(s string) error {
		if err := guard.ScanContentWithScope(context.Background(), s, g, &cfg, "mcp_schema"); err != nil {
			return fmt.Errorf("mcp server %q tool %q: schema guard scan failed: %w", serverName, toolName, err)
		}
		return nil
	})
}

// walkMCPSchema recursively invokes fn on every string in a JSON-schema-like
// value (maps, slices, and scalars).
func walkMCPSchema(v any, fn func(string) error) error {
	switch x := v.(type) {
	case string:
		return fn(x)
	case map[string]any:
		for _, val := range x {
			if err := walkMCPSchema(val, fn); err != nil {
				return err
			}
		}
	case []any:
		for _, val := range x {
			if err := walkMCPSchema(val, fn); err != nil {
				return err
			}
		}
	}
	return nil
}
