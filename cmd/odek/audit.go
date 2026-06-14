package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/session"
)

// recordTurnAudit summarises a single agent turn into the audit log:
// which tools were called, which resources they touched, whether any
// untrusted content was ingested, and whether the resources referenced
// by tool calls diverge from those mentioned in the user message.
//
// "Divergence" is a heuristic: a turn is flagged as suspicious when
// the agent ingested untrusted content AND the agent's actions or final
// response reference resources that either (a) did not appear in the
// user's message, or (b) were introduced by the untrusted content itself.
// This is exactly the footprint of a successful prompt injection that
// steered the agent toward an attacker-chosen resource.
func recordTurnAudit(store *session.AuditStore, sessionID string, turn int, userText string, newMsgs []llm.Message) {
	if store == nil {
		return
	}

	var toolCalls []string
	var actionText strings.Builder  // agent actions: tool calls + final response
	var untrustedBodies strings.Builder
	var untrustedSources []string
	ingestedUntrusted := false
	lastAssistantContent := ""

	for _, m := range newMsgs {
		for _, tc := range m.ToolCalls {
			toolCalls = append(toolCalls, tc.Function.Name)
			actionText.WriteString(tc.Function.Arguments)
			actionText.WriteByte(' ')
		}
		if m.Role == "tool" {
			if hasUntrustedWrapper(m.Content) {
				ingestedUntrusted = true
				untrustedBodies.WriteString(unwrapUntrusted(m.Content))
				untrustedBodies.WriteByte(' ')
				if src := untrustedSource(m.Content); src != "" {
					untrustedSources = append(untrustedSources, src)
				}
			}
		}
		if m.Role == "assistant" && m.Content != "" {
			// Track the final assistant response; it can also be used for
			// exfiltration ("response-only" injection).
			lastAssistantContent = m.Content
		}
	}
	if lastAssistantContent != "" {
		actionText.WriteString(lastAssistantContent)
		actionText.WriteByte(' ')
	}

	// Resources referenced by the agent's actions that the user did not
	// mention. We intentionally do not scan raw tool results here; a
	// resource that merely appears in a fetched page is not itself a
	// divergence unless the agent acts on it.
	novel := session.NovelResources(userText, actionText.String())

	// Resources introduced by untrusted content itself. Even if the user
	// mentioned the same resource earlier, acting on it after it appears in
	// untrusted content is the footprint of a "reused-resource" injection.
	// We exclude resources that match the source of the untrusted content
	// (e.g. a fetched page mentioning its own URL) to avoid false positives
	// for legitimate user-requested fetches.
	isSource := func(r string) bool {
		lr := strings.ToLower(r)
		for _, s := range untrustedSources {
			ls := strings.ToLower(s)
			if lr == ls || strings.HasPrefix(lr, ls) || strings.HasPrefix(ls, lr) {
				return true
			}
		}
		return false
	}
	untrustedResSet := make(map[string]bool)
	for _, r := range session.ResourcesIn(untrustedBodies.String()) {
		if !isSource(r) {
			untrustedResSet[strings.ToLower(r)] = true
		}
	}
	var untrustedResources []string
	seen := make(map[string]bool)
	for _, r := range session.ResourcesIn(actionText.String()) {
		lr := strings.ToLower(r)
		if untrustedResSet[lr] && !seen[lr] {
			seen[lr] = true
			untrustedResources = append(untrustedResources, r)
		}
	}

	// We do not flag divergence on untainted turns — a trusted internal
	// search legitimately surfaces resources the user did not name.
	suspicious := ingestedUntrusted && (len(novel) > 0 || len(untrustedResources) > 0)

	at := session.AuditTurn{
		Turn:                 turn,
		UserMessage:          userText,
		ToolCalls:            toolCalls,
		NovelResources:       novel,
		UntrustedResources:   untrustedResources,
		IngestedUntrusted:    ingestedUntrusted,
		SuspiciousDivergence: suspicious,
	}
	_ = store.RecordTurn(sessionID, at)
}

// auditCmd handles `odek audit <session-id>` and `odek audit --list`.
// Read-only: it never modifies the audit log. Output is JSON to stdout
// so the caller can pipe through jq / their tool of choice.
func auditCmd(args []string) error {
	if len(args) == 0 {
		printAuditUsage()
		return fmt.Errorf("audit: argument required")
	}
	store, err := session.NewStore()
	if err != nil {
		return fmt.Errorf("audit: session store: %w", err)
	}
	auditStore := session.NewAuditStore(store.Dir())

	switch args[0] {
	case "--help", "-h", "help":
		printAuditUsage()
		return nil
	case "--list":
		return auditList(store, auditStore)
	default:
		log, err := auditStore.Load(args[0])
		if err != nil {
			return fmt.Errorf("audit: load: %w", err)
		}
		out, err := json.MarshalIndent(log, "", "  ")
		if err != nil {
			return fmt.Errorf("audit: marshal: %w", err)
		}
		fmt.Println(string(out))
		return nil
	}
}

func auditList(store *session.Store, auditStore *session.AuditStore) error {
	sessions, err := store.List(0)
	if err != nil {
		return fmt.Errorf("audit: list sessions: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Session                Ingests  Turns  Suspicious  First-Ingest-Source\n")
	for _, s := range sessions {
		log, err := auditStore.Load(s.ID)
		if err != nil || len(log.Ingests) == 0 {
			continue
		}
		suspicious := 0
		for _, t := range log.Turns {
			if t.SuspiciousDivergence {
				suspicious++
			}
		}
		firstSource := log.Ingests[0].Source
		if len(firstSource) > 40 {
			firstSource = firstSource[:37] + "..."
		}
		fmt.Printf("%-22s %7d %6d %11d  %s\n",
			s.ID, len(log.Ingests), len(log.Turns), suspicious, firstSource)
	}
	return nil
}

func printAuditUsage() {
	fmt.Println(`Usage: odek audit <session-id>
       odek audit --list

Prints the prompt-injection audit log for a session.

The log records every time the agent ingested externally-sourced
content (a fetched page, a file outside the working directory, an MCP
tool response, audio transcript, etc.) along with a per-turn
divergence assessment — turns where the agent referenced resources
the user did not mention AND the session ingested untrusted content
are flagged as 'suspicious'.

Output is JSON to stdout.`)
}
