package main

// Introspection surfaces: the config_view and list_tools built-in tools plus
// the shared view builders they use.
//
// Design contract: ONE sanitizer, TWO consumers. The build*View functions
// below produce the sanitized rendering of operator configuration, and both
// the REST management API (serve_api.go / serve.go) and the agent-facing
// tools render exactly that output. Sanitization is STRUCTURAL — secrets
// (api_key, base_url, env maps, search backends) never enter the view map at
// build time; there is no render-time filter that a future edit could
// forget. A config view that leaked the LLM endpoint credentials would turn
// a read-only tool into key exfiltration for anything driving the model.
//
// Security posture of the tools: strictly read-only over in-memory operator
// state (same class as list_subagent_profiles — no approver, no
// DangerousConfig, no filesystem or network access). The tool structs never
// hold the raw config.ResolvedConfig — they receive only the pre-built view
// via toolConfig.Introspection, so the blast radius of a mistake in Call is
// the sanitized map, nothing else.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/BackendStack21/odek/internal/budget"
	"github.com/BackendStack21/odek/internal/config"
)

// ── shared view builders (REST + tools) ──────────────────────────────────

// buildConfigView reports the operator-relevant resolved configuration as
// scalars and flags ONLY. Secrets (api_key, base_url, env maps, search
// backends) are deliberately excluded — see the file comment. Consumed by
// GET /api/config and the config_view tool; the two faces are pinned equal
// by TestRESTConfigViewMatchesToolAll.
func buildConfigView(resolved config.ResolvedConfig) map[string]any {
	boolPtr := func(p *bool) any {
		if p == nil {
			return nil
		}
		return *p
	}
	return map[string]any{
		"provider":          resolved.Provider,
		"model":             resolved.Model,
		"stream":            resolved.Stream,
		"compaction":        resolved.Compaction,
		"prompt_caching":    resolved.PromptCaching,
		"thinking":          resolved.Thinking != "",
		"max_iterations":    resolved.MaxIter,
		"max_tool_parallel": resolved.MaxToolParallel,
		"max_concurrency":   resolved.MaxConcurrency,
		"interaction_mode":  resolved.InteractionMode,
		"no_agents_md":      resolved.NoAgents,
		"sandbox": map[string]any{
			"enabled":  resolved.Sandbox,
			"image":    resolved.SandboxImage,
			"network":  resolved.SandboxNetwork,
			"readonly": resolved.SandboxReadonly,
			"memory":   resolved.SandboxMemory,
			"cpus":     resolved.SandboxCPUs,
			"user":     resolved.SandboxUser,
		},
		"memory": map[string]any{
			"enabled":                  boolPtr(resolved.Memory.Enabled),
			"facts_limit_user":         resolved.Memory.FactsLimitUser,
			"facts_limit_env":          resolved.Memory.FactsLimitEnv,
			"extract_on_end":           boolPtr(resolved.Memory.ExtractOnEnd),
			"consolidate_on_end":       boolPtr(resolved.Memory.ConsolidateOnEnd),
			"min_turns_for_extraction": resolved.Memory.MinTurnsForExtraction,
		},
		"skills": map[string]any{
			"max_auto_load":  resolved.Skills.MaxAutoLoad,
			"max_lazy_slots": resolved.Skills.MaxLazySlots,
		},
		"tools": map[string]any{
			"enabled":  resolved.Tools.Enabled,
			"disabled": resolved.Tools.Disabled,
		},
		"maintenance": map[string]any{
			"enabled":               resolved.Maintenance.Enabled,
			"interval_minutes":      resolved.Maintenance.IntervalMinutes,
			"sessions_max_age_days": resolved.Maintenance.SessionsMaxAgeDays,
			"audit_max_age_days":    resolved.Maintenance.AuditMaxAgeDays,
			"plans_max_age_days":    resolved.Maintenance.PlansMaxAgeDays,
		},
		"dangerous_default_action": resolved.Dangerous.DefaultAction,
		"guard_scan":               guardScanView(resolved.Guard.Scan),
		"subagent": map[string]any{
			// Raw resolved values; 0 = inherit the documented fallback
			// (global max_concurrency, 1800s, 100 iterations, depth 2).
			"max_concurrency": resolved.Subagent.MaxConcurrency,
			"timeout_seconds": resolved.Subagent.TimeoutSeconds,
			"max_iterations":  resolved.Subagent.MaxIterations,
			"max_depth":       resolved.Subagent.MaxDepth,
			"announce_budget": resolved.Subagent.AnnounceBudget,
			"budget_inherit":  resolved.Subagent.BudgetInherit,
			"default_profile": resolved.Subagent.DefaultProfile,
		},
		"background": map[string]any{
			"enabled":             resolved.Background.Enabled,
			"max_jobs":            resolved.Background.MaxJobs,
			"max_output_bytes":    resolved.Background.MaxOutputBytes,
			"max_timeout_seconds": resolved.Background.MaxTimeoutSeconds,
			"notify":              resolved.Background.Notify,
			"on_session_end":      resolved.Background.OnSessionEnd,
			"wake_on_complete":    resolved.Background.WakeOnComplete,
			"wake_coalesce_ms":    resolved.Background.WakeCoalesceMS,
			"max_wakes_per_hour":  resolved.Background.MaxWakesPerHour,
		},
		"limits": buildLimitsView(resolved.Model, resolved.Limits),
	}
}

// buildLimitsView reports the execution-budget configuration plus the
// effective per-million token prices for the configured model
// (Limits.ResolvePrices). Zero/absent prices mean "costs unavailable", never
// $0. Consumed by GET /api/limits and the config_view limits section; the
// two faces are pinned equal by TestRESTLimitsMatchesSharedBuilder.
func buildLimitsView(configuredModel string, limits budget.Limits) map[string]any {
	inPrice, outPrice := limits.ResolvePrices(configuredModel)
	return map[string]any{
		"model":  configuredModel,
		"limits": limits,
		"effective_prices": map[string]float64{
			"input_cost_per_million_usd":  inPrice,
			"output_cost_per_million_usd": outPrice,
		},
	}
}

// buildMCPServersView lists configured MCP servers with their extension
// limits. Command/args are operator config (the interactive approval UI
// already displays them verbatim); env values are withheld — they may carry
// credentials. Consumed by GET /api/mcp and the list_tools tool.
func buildMCPServersView(resolved config.ResolvedConfig) []mcpEntry {
	project := map[string]bool{}
	for _, n := range resolved.ProjectMCPServerNames {
		project[n] = true
	}
	out := make([]mcpEntry, 0, len(resolved.MCPServers))
	for name, cfg := range resolved.MCPServers {
		out = append(out, mcpEntry{
			Name:             name,
			Command:          cfg.Command,
			Args:             cfg.Args,
			Project:          project[name],
			AutoApprove:      cfg.AutoApprove,
			TimeoutSeconds:   cfg.TimeoutSeconds,
			MaxResponseBytes: cfg.MaxResponseBytes,
			MaxResultChars:   cfg.MaxResultChars,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// toolEnabled applies the resolved tools.enabled / tools.disabled filter —
// the same rule internal/tool.FilterTools uses. Shared by the REST tools
// view and the list_tools tool so the answer cannot drift.
func toolEnabled(name string, enabledSet, disabledSet map[string]bool, whitelistActive bool) bool {
	return !disabledSet[name] && (!whitelistActive || enabledSet[name])
}

// credentialArgFlag matches CLI flags that introduce a credential value:
// --api-key, -token, --secret-file, --db-password, --credential, …
var credentialArgFlag = regexp.MustCompile(`(?i)^-{1,2}[a-z0-9_-]*(key|token|secret|password|passwd|credential)[a-z0-9_-]*$`)

const redactedArgPlaceholder = "[redacted]"

// redactMCPServersView is the defense-in-depth pass for the TOOL face of
// the MCP view: argv can carry credentials (--api-key sk-…), and the REST
// face's operator-only gate (CSRF token + loopback) does not exist for a
// model tool call. Command and non-credential args stay verbatim; values
// following a credential-ish flag (and --flag=value forms) are replaced.
// Env values are already withheld at build time. The input slice is never
// mutated — the REST face keeps verbatim argv, pinned by
// TestMCPServerArgsRedactedOnToolFace.
func redactMCPServersView(in []mcpEntry) []mcpEntry {
	out := make([]mcpEntry, len(in))
	for i, e := range in {
		e.Args = redactCredentialArgs(e.Args)
		out[i] = e
	}
	return out
}

func redactCredentialArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	out := make([]string, len(args))
	redactNext := false
	for i, a := range args {
		if redactNext {
			out[i] = redactedArgPlaceholder
			redactNext = false
			continue
		}
		if eq := strings.IndexByte(a, '='); eq > 0 && credentialArgFlag.MatchString(a[:eq]) {
			out[i] = a[:eq+1] + redactedArgPlaceholder
			continue
		}
		if credentialArgFlag.MatchString(a) {
			redactNext = true
		}
		out[i] = a
	}
	return out
}

// ── config_view tool ─────────────────────────────────────────────────────

// configViewSections names the view's section groups. Keys must exist in
// buildConfigView output — TestConfigViewToolSections fails loudly on drift.
var configViewSections = map[string][]string{
	"all":         nil, // whole view
	"core":        {"provider", "model", "stream", "compaction", "prompt_caching", "thinking", "max_iterations", "max_tool_parallel", "max_concurrency", "interaction_mode", "no_agents_md"},
	"security":    {"sandbox", "dangerous_default_action", "guard_scan", "tools"},
	"subagent":    {"max_concurrency", "subagent"},
	"limits":      {"limits"},
	"memory":      {"memory"},
	"skills":      {"skills"},
	"background":  {"background"},
	"maintenance": {"maintenance"},
}

// configViewTool renders the sanitized resolved configuration for model
// consumption.
type configViewTool struct {
	// view is the pre-built sanitized view (buildConfigView output),
	// carried in via toolConfig.Introspection. Nil-safe: reserved-name
	// probing constructs builtinTools with a zero toolConfig.
	view map[string]any
}

func (t *configViewTool) Name() string { return "config_view" }

func (t *configViewTool) Description() string {
	return "Read the sanitized, resolved configuration this odek run operates under — the " +
		"operator's effective settings after the five-layer merge (secrets.env → global → " +
		"project → env → flags). Sections: all (default), core (provider/model/stream/" +
		"iteration limits), security (sandbox, dangerous_default_action, guard_scan, tool filter), " +
		"subagent (delegate_tasks budgets, default profile), limits (execution budgets + " +
		"effective token prices), memory, skills, background, maintenance. Secrets (API " +
		"keys, base URLs, env values) are structurally excluded. Read-only; renders the " +
		"same view as the operator's GET /api/config. Use it to understand the security " +
		"posture and budgets you and your sub-agents are running under."
}

func (t *configViewTool) Schema() any {
	sections := make([]string, 0, len(configViewSections))
	for name := range configViewSections {
		sections = append(sections, name)
	}
	sort.Strings(sections)
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"section": map[string]any{
				"type":        "string",
				"enum":        sections,
				"description": "Config section to view. Omit or \"all\" for the full sanitized view.",
			},
		},
	}
}

func (t *configViewTool) Call(section string) (string, error) {
	view := t.view
	if view == nil {
		view = map[string]any{}
	}
	if section == "" || section == "all" {
		buf, err := json.Marshal(view)
		if err != nil {
			return "", fmt.Errorf("config_view: %w", err)
		}
		return string(buf), nil
	}

	keys, ok := configViewSections[section]
	if !ok {
		names := make([]string, 0, len(configViewSections))
		for name := range configViewSections {
			names = append(names, name)
		}
		sort.Strings(names)
		return "", fmt.Errorf("config_view: unknown section %q (valid: %v)", section, names)
	}
	out := make(map[string]any, len(keys))
	for _, key := range keys {
		if v, ok := view[key]; ok {
			out[key] = v
		}
	}
	buf, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("config_view: %w", err)
	}
	return string(buf), nil
}

// ── list_tools tool ──────────────────────────────────────────────────────

// listToolsTool reports the tool registry actually constructed for THIS run
// plus the operator's filter state and the MCP server posture. The model
// otherwise only sees its own injected schemas — it cannot tell whether a
// missing tool was disabled by config, which MCP server owns a tool, or
// what limits apply to those servers.
type listToolsTool struct {
	// registered is the live registry captured at the end of builtinTools
	// (after conditional registrations), so the list is exactly what this
	// process built — including config_view and list_tools themselves.
	registered []string

	// toolsEnabled / toolsDisabled are the resolved filter lists. A nil
	// toolsEnabled means no whitelist is active.
	toolsEnabled  []string
	toolsDisabled []string

	// mcpServers is the pre-built sanitized MCP view (buildMCPServersView).
	mcpServers []mcpEntry
}

func (t *listToolsTool) Name() string { return "list_tools" }

func (t *listToolsTool) Description() string {
	return "List the tools actually registered for this run with their enabled/disabled " +
		"state after the operator's tools.enabled/tools.disabled filter, plus the " +
		"configured MCP servers (command, approval mode, per-server limits; env values " +
		"withheld). Use it to reason about your own capabilities and what delegate_tasks " +
		"sub-agents can access. Note: MCP tools are additionally withheld from untrusted " +
		"sub-agents. Read-only; complements config_view (settings) and " +
		"list_subagent_profiles (capability profiles)."
}

func (t *listToolsTool) Schema() any {
	// No parameters — the listing is static per-run state.
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func (t *listToolsTool) Call(_ string) (string, error) {
	enabledSet := map[string]bool{}
	for _, n := range t.toolsEnabled {
		enabledSet[n] = true
	}
	disabledSet := map[string]bool{}
	for _, n := range t.toolsDisabled {
		disabledSet[n] = true
	}
	whitelistActive := t.toolsEnabled != nil

	names := append([]string(nil), t.registered...)
	sort.Strings(names)
	out := make([]toolSummary, 0, len(names))
	for _, n := range names {
		out = append(out, toolSummary{Name: n, Enabled: toolEnabled(n, enabledSet, disabledSet, whitelistActive)})
	}

	buf, err := json.Marshal(map[string]any{
		"tools":       out,
		"mcp_servers": t.mcpServers,
		"note":        "MCP tools are withheld from untrusted sub-agents (trust_level=untrusted); profile tool filters apply on top of this list.",
	})
	if err != nil {
		return "", fmt.Errorf("list_tools: %w", err)
	}
	return string(buf), nil
}

// ── registration plumbing ────────────────────────────────────────────────

// IntrospectionState carries the pre-built sanitized views for the
// config_view / list_tools tools. Built ONCE by toolConfigFromResolved so
// the tool structs never hold the raw config.ResolvedConfig.
type IntrospectionState struct {
	ConfigView    map[string]any
	ToolsEnabled  []string
	ToolsDisabled []string
	MCPServers    []mcpEntry
}

func buildIntrospectionState(resolved config.ResolvedConfig) IntrospectionState {
	return IntrospectionState{
		ConfigView:    buildConfigView(resolved),
		ToolsEnabled:  resolved.Tools.Enabled,
		ToolsDisabled: resolved.Tools.Disabled,
		// Tool face: credential-ish argv values redacted (defense in
		// depth — the REST /api/mcp face keeps verbatim argv).
		MCPServers: redactMCPServersView(buildMCPServersView(resolved)),
	}
}
